package corpus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/usestring/gqlcrawl/internal/model"
)

const certSpotterMaxPages = 50

func getAuthorized(ctx context.Context, request Request, target string, token string) ([]byte, error) {
	if request.Fetcher == nil {
		return nil, fmt.Errorf("no fetcher configured")
	}
	limit := request.MaxDownloadBytes
	if limit <= 0 {
		limit = DefaultMaxDownloadBytes
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpRequest.Header.Set("User-Agent", request.UserAgent)
	httpRequest.Header.Set("Accept", "application/json")
	if token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := request.Fetcher.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", response.StatusCode)
	}
	return readBounded(response.Body, limit)
}

func coversDomain(name string, domain string) bool {
	return name == domain || strings.HasSuffix(name, "."+domain)
}

type certSpotterAdapter struct{}

func (certSpotterAdapter) Name() string { return "certspotter" }

func (certSpotterAdapter) Summary() string {
	return "Certificate Transparency hostnames via SSLMate Cert Spotter (unranked, key optional)"
}

func (certSpotterAdapter) ScopeUsage() string { return "one or more domains" }

func (certSpotterAdapter) Requirement() Requirement {
	return Requirement{
		Notes: "Unauthenticated use is evaluation-grade and capped near ten full-domain queries per hour. Set CERTSPOTTER_API_KEY for production volume.",
	}
}

func (a certSpotterAdapter) Fetch(ctx context.Context, request Request) ([]model.Seed, error) {
	if len(request.Scope) == 0 {
		return nil, fmt.Errorf("certspotter requires at least one domain")
	}

	token := ""
	if request.Lookup != nil {
		token = strings.TrimSpace(request.Lookup("CERTSPOTTER_API_KEY"))
	}

	seeds := make([]model.Seed, 0, request.Limit)
	for _, rawDomain := range request.Scope {
		domain, err := NormalizeHost(rawDomain)
		if err != nil {
			return seeds, err
		}

		after := ""
		for page := 0; page < certSpotterMaxPages && len(seeds) < request.Limit; page++ {
			endpoint := "https://api.certspotter.com/v1/issuances?include_subdomains=true&expand=dns_names&domain=" + url.QueryEscape(domain)
			if after != "" {
				endpoint += "&after=" + url.QueryEscape(after)
			}

			body, err := getAuthorized(ctx, request, endpoint, token)
			if err != nil {
				return seeds, fmt.Errorf("%s: %w", domain, err)
			}

			var issuances []struct {
				ID       string   `json:"id"`
				DNSNames []string `json:"dns_names"`
			}
			if err := json.Unmarshal(body, &issuances); err != nil {
				return seeds, fmt.Errorf("%s: decode issuances: %w", domain, err)
			}
			if len(issuances) == 0 {
				break
			}

			previous := after
			for _, issuance := range issuances {
				if issuance.ID != "" {
					after = issuance.ID
				}
				for _, name := range issuance.DNSNames {
					host, err := NormalizeHost(name)
					if err != nil || !coversDomain(host, domain) {
						continue
					}
					seeds = append(seeds, model.Seed{
						Value:    host,
						Kind:     model.SeedHost,
						Adapter:  a.Name(),
						Evidence: domain,
					})
				}
			}
			if after == previous {
				break
			}
		}
	}
	return seeds, nil
}

type crtShAdapter struct{}

func (crtShAdapter) Name() string { return "crtsh" }

func (crtShAdapter) Summary() string {
	return "Certificate Transparency hostnames via crt.sh (unranked, no credentials)"
}

func (crtShAdapter) ScopeUsage() string { return "one or more domains" }

func (crtShAdapter) Requirement() Requirement {
	return Requirement{
		Notes: "Rate limited near five requests per minute and frequently unavailable. It can also answer 200 with a silently truncated result, so treat a run as a lower bound and prefer certspotter.",
	}
}

func (a crtShAdapter) Fetch(ctx context.Context, request Request) ([]model.Seed, error) {
	if len(request.Scope) == 0 {
		return nil, fmt.Errorf("crtsh requires at least one domain")
	}

	seeds := make([]model.Seed, 0, request.Limit)
	for _, rawDomain := range request.Scope {
		domain, err := NormalizeHost(rawDomain)
		if err != nil {
			return seeds, err
		}

		endpoint := "https://crt.sh/?output=json&q=" + url.QueryEscape(domain)
		body, err := Get(ctx, request, endpoint, "application/json")
		if err != nil {
			return seeds, fmt.Errorf("%s: %w", domain, err)
		}

		var entries []struct {
			NameValue string `json:"name_value"`
		}
		if err := json.Unmarshal(body, &entries); err != nil {
			return seeds, fmt.Errorf("%s: decode certificates: %w", domain, err)
		}

		for _, entry := range entries {
			for _, name := range strings.Split(entry.NameValue, "\n") {
				host, err := NormalizeHost(name)
				if err != nil || !coversDomain(host, domain) {
					continue
				}
				seeds = append(seeds, model.Seed{
					Value:    host,
					Kind:     model.SeedHost,
					Adapter:  a.Name(),
					Evidence: domain,
				})
				if len(seeds) >= request.Limit {
					return seeds, nil
				}
			}
		}
	}
	return seeds, nil
}
