package source

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/usestring/gqlcrawl/internal/model"
)

const maxInputLineBytes = 1024 * 1024

type Candidate struct {
	Raw        string
	Source     model.Source
	SkipReason model.Reason
}

func Load(arguments []string, inputPath string, stdin io.Reader) ([]Candidate, error) {
	candidates := make([]Candidate, 0, len(arguments))
	for _, argument := range arguments {
		raw := strings.TrimSpace(argument)
		if raw == "" {
			continue
		}
		candidates = append(candidates, Candidate{
			Raw: raw,
			Source: model.Source{
				Kind:  "argument",
				Input: SanitizeURL(raw),
			},
		})
	}

	if inputPath == "" {
		if len(candidates) > 0 {
			return candidates, nil
		}
		inputPath = "-"
	}

	if inputPath == "-" {
		return appendLines(candidates, stdin, "stdin", "-")
	}

	file, err := os.Open(inputPath)
	if err != nil {
		return nil, fmt.Errorf("open input: %w", err)
	}
	defer file.Close()

	return appendLines(candidates, file, "file", inputPath)
}

func appendLines(candidates []Candidate, reader io.Reader, kind string, input string) ([]Candidate, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxInputLineBytes)

	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		candidates = append(candidates, Candidate{
			Raw: raw,
			Source: model.Source{
				Kind:  kind,
				Input: SanitizeURL(raw),
			},
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s input: %w", input, err)
	}

	return candidates, nil
}

func NormalizeURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse URL")
	}
	if parsed.Scheme == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("URL must include a scheme and host")
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	port := parsed.Port()
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		parsed.Host = "[" + host + "]"
	} else {
		parsed.Host = host
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	parsed.Fragment = ""
	parsed.RawQuery = parsed.Query().Encode()

	return parsed.String(), nil
}

func SanitizeURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return "<invalid-url>"
	}

	parsed.User = nil
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		query[key] = []string{"REDACTED"}
	}
	parsed.RawQuery = query.Encode()

	return parsed.String()
}
