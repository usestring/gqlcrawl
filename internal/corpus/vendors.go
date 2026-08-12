package corpus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/usestring/gqlcrawl/internal/model"
)

const (
	shodanSearchURL       = "https://api.shodan.io/shodan/host/search"
	shodanKeyVariable     = "SHODAN_API_KEY"
	shodanResultsPerPage  = 100
	shodanMaxPages        = 20
	builtWithListsURL     = "https://api.builtwith.com/lists12/api.json"
	builtWithKeyVariable  = "BUILTWITH_API_KEY"
	builtWithMaxPages     = 100
	builtWithEndOfResults = "END"
)

// Neither vendor documents header authentication for the endpoint used here, so the key
// travels as a query parameter and a transport error would otherwise print the whole URL.
func getKeyed(ctx context.Context, request Request, target string, accept string, secret string) ([]byte, error) {
	body, err := Get(ctx, request, target, accept)
	if err != nil && secret != "" {
		return nil, errors.New(strings.ReplaceAll(err.Error(), secret, "REDACTED"))
	}
	return body, err
}

type shodanAdapter struct{}

func (shodanAdapter) Name() string { return "shodan" }

func (shodanAdapter) Summary() string {
	return "Hosts whose banner matches a Shodan web technology query (unranked, paid)"
}

func (shodanAdapter) ScopeUsage() string { return "" }

func (shodanAdapter) Requirement() Requirement {
	return Requirement{
		EnvVars: []string{shodanKeyVariable},
		Metered: true,
		Notes:   "Any filtered query costs one query credit, and each page past the first costs another. Shodan publishes no list of legal http.component values, so confirm the technology name on its facet analysis page before trusting a narrow result.",
	}
}

func (a shodanAdapter) query(request Request) string {
	if query := request.Option("query", ""); query != "" {
		return query
	}
	return "http.component:" + request.Option("component", "GraphQL")
}

func (a shodanAdapter) Fetch(ctx context.Context, request Request) ([]model.Seed, error) {
	key, err := request.Credential(shodanKeyVariable)
	if err != nil {
		return nil, err
	}
	query := a.query(request)

	pages := (request.Limit + shodanResultsPerPage - 1) / shodanResultsPerPage
	if pages > shodanMaxPages {
		pages = shodanMaxPages
	}

	seeds := make([]model.Seed, 0, request.Limit)
	for page := 1; page <= pages && len(seeds) < request.Limit; page++ {
		parameters := url.Values{}
		parameters.Set("key", key)
		parameters.Set("query", query)
		parameters.Set("page", fmt.Sprint(page))

		body, err := getKeyed(ctx, request, shodanSearchURL+"?"+parameters.Encode(), "application/json", key)
		if err != nil {
			return seeds, err
		}

		var results struct {
			Error   string `json:"error"`
			Matches []struct {
				Hostnames []string `json:"hostnames"`
			} `json:"matches"`
		}
		if err := json.Unmarshal(body, &results); err != nil {
			return seeds, fmt.Errorf("decode matches: %w", err)
		}
		if results.Error != "" {
			return seeds, fmt.Errorf("shodan rejected the request: %s", results.Error)
		}
		if len(results.Matches) == 0 {
			break
		}

		for _, match := range results.Matches {
			// A banner without a hostname leaves only an address, which is not a
			// name the probe can present to a virtual host.
			for _, hostname := range match.Hostnames {
				seeds = append(seeds, model.Seed{
					Value:    hostname,
					Kind:     model.SeedHost,
					Adapter:  a.Name(),
					Evidence: query,
				})
				if len(seeds) >= request.Limit {
					return seeds, nil
				}
			}
		}
	}
	return seeds, nil
}

type builtWithAdapter struct{}

func (builtWithAdapter) Name() string { return "builtwith" }

func (builtWithAdapter) Summary() string {
	return "Domains BuiltWith reports as using a named web technology (unranked, paid)"
}

func (builtWithAdapter) ScopeUsage() string { return "" }

func (builtWithAdapter) Requirement() Requirement {
	return Requirement{
		EnvVars: []string{builtWithKeyVariable},
		Metered: true,
		Notes:   "The Lists API is gated on a BuiltWith subscription rather than API credits. BuiltWith publishes no bare GraphQL technology, so the default asks for Apollo-GraphQL; set --option tech= for another name.",
	}
}

func (a builtWithAdapter) Fetch(ctx context.Context, request Request) ([]model.Seed, error) {
	key, err := request.Credential(builtWithKeyVariable)
	if err != nil {
		return nil, err
	}
	technology := request.Option("tech", "Apollo-GraphQL")

	seeds := make([]model.Seed, 0, request.Limit)
	offset := ""
	for page := 0; page < builtWithMaxPages && len(seeds) < request.Limit; page++ {
		parameters := url.Values{}
		parameters.Set("KEY", key)
		parameters.Set("TECH", technology)
		if others := request.Option("othertechs", ""); others != "" {
			parameters.Set("OTHERTECHS", others)
		}
		if country := request.Option("country", ""); country != "" {
			parameters.Set("COUNTRY", country)
		}
		if since := request.Option("since", ""); since != "" {
			parameters.Set("SINCE", since)
		}
		if offset != "" {
			parameters.Set("OFFSET", offset)
		}

		body, err := getKeyed(ctx, request, builtWithListsURL+"?"+parameters.Encode(), "application/json", key)
		if err != nil {
			return seeds, err
		}

		var list struct {
			NextOffset string `json:"NextOffset"`
			Results    []struct {
				Domain string `json:"D"`
			} `json:"Results"`
			Errors []struct {
				Message string `json:"Message"`
			} `json:"Errors"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return seeds, fmt.Errorf("decode list: %w", err)
		}
		if len(list.Errors) > 0 {
			return seeds, fmt.Errorf("builtwith rejected the request: %s", list.Errors[0].Message)
		}

		for _, result := range list.Results {
			if result.Domain == "" {
				continue
			}
			seeds = append(seeds, model.Seed{
				Value:    result.Domain,
				Kind:     model.SeedHost,
				Adapter:  a.Name(),
				Evidence: technology,
			})
			if len(seeds) >= request.Limit {
				return seeds, nil
			}
		}

		// NextOffset is an opaque continuation token, and the page size is undocumented,
		// so an empty page is only the end when the token says so.
		if strings.EqualFold(list.NextOffset, builtWithEndOfResults) || list.NextOffset == "" {
			break
		}
		offset = list.NextOffset
	}
	return seeds, nil
}
