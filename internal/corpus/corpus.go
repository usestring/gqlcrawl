package corpus

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/usestring/gqlcrawl/internal/model"
	"github.com/usestring/gqlcrawl/internal/source"
)

const (
	DefaultLimit            = 1000
	MaxLimit                = 1000000
	DefaultMaxDownloadBytes = 64 * 1024 * 1024
	MaxDownloadBytes        = 512 * 1024 * 1024
)

type Fetcher interface {
	Do(*http.Request) (*http.Response, error)
}

type Requirement struct {
	EnvVars []string
	Metered bool
	Notes   string
}

type Request struct {
	Limit            int
	Scope            []string
	Fetcher          Fetcher
	UserAgent        string
	MaxDownloadBytes int64
	Lookup           func(string) string
}

type Adapter interface {
	Name() string
	Summary() string
	ScopeUsage() string
	Requirement() Requirement
	Fetch(ctx context.Context, request Request) ([]model.Seed, error)
}

func All() []Adapter {
	adapters := []Adapter{}
	sort.Slice(adapters, func(first int, second int) bool {
		return adapters[first].Name() < adapters[second].Name()
	})
	return adapters
}

func Names() []string {
	adapters := All()
	names := make([]string, 0, len(adapters))
	for _, adapter := range adapters {
		names = append(names, adapter.Name())
	}
	return names
}

func Lookup(name string) (Adapter, error) {
	wanted := strings.ToLower(strings.TrimSpace(name))
	if wanted == "" {
		return nil, fmt.Errorf("--source is required; available sources: %s", strings.Join(Names(), ", "))
	}
	for _, adapter := range All() {
		if adapter.Name() == wanted {
			return adapter, nil
		}
	}
	return nil, fmt.Errorf("unknown source %q; available sources: %s", name, strings.Join(Names(), ", "))
}

func (r Request) Credential(name string) (string, error) {
	lookup := r.Lookup
	if lookup == nil {
		return "", fmt.Errorf("no credential lookup configured")
	}
	value := strings.TrimSpace(lookup(name))
	if value == "" {
		return "", fmt.Errorf("%s is required for this source but is not set", name)
	}
	return value, nil
}

func NormalizeHost(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", fmt.Errorf("empty host")
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Hostname() == "" {
			return "", fmt.Errorf("parse host from URL")
		}
		value = parsed.Hostname()
	}
	value = strings.TrimPrefix(value, "*.")
	value = strings.TrimSuffix(value, ".")
	if value == "" {
		return "", fmt.Errorf("empty host")
	}
	if strings.ContainsAny(value, " \t\r\n/\\?#@:") {
		return "", fmt.Errorf("host contains unsupported characters")
	}
	if !strings.Contains(value, ".") {
		return "", fmt.Errorf("host is not fully qualified")
	}
	return value, nil
}

func Normalize(seed model.Seed) (model.Seed, error) {
	seed.Value = strings.TrimSpace(seed.Value)
	if seed.Value == "" {
		return model.Seed{}, fmt.Errorf("empty seed value")
	}

	switch seed.Kind {
	case model.SeedHost:
		host, err := NormalizeHost(seed.Value)
		if err != nil {
			return model.Seed{}, err
		}
		seed.Value = host
	case model.SeedURL:
		normalized, err := source.NormalizeURL(seed.Value)
		if err != nil {
			return model.Seed{}, err
		}
		seed.Value = source.SanitizeURL(normalized)
	default:
		return model.Seed{}, fmt.Errorf("unsupported seed kind %q", seed.Kind)
	}

	seed.SchemaVersion = "1"
	if seed.Rank < 0 {
		seed.Rank = 0
	}
	return seed, nil
}

func Collect(seeds []model.Seed, limit int) []model.Seed {
	if limit <= 0 {
		limit = DefaultLimit
	}

	collected := make([]model.Seed, 0, limit)
	seen := make(map[string]struct{}, len(seeds))
	for _, seed := range seeds {
		normalized, err := Normalize(seed)
		if err != nil {
			continue
		}
		key := string(normalized.Kind) + "\x00" + normalized.Value
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		collected = append(collected, normalized)
		if len(collected) >= limit {
			break
		}
	}
	return collected
}
