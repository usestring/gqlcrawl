package corpus

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/usestring/gqlcrawl/internal/model"
)

const (
	appleChartMaxLimit = 100
	appleLookupBatch   = 100
)

var appleFeeds = map[string]bool{"top-free": true, "top-paid": true}

type appleChartsAdapter struct{}

func (appleChartsAdapter) Name() string { return "applecharts" }

func (appleChartsAdapter) Summary() string {
	return "Apple App Store top charts resolved to publisher domains (ordinal, no credentials)"
}

func (appleChartsAdapter) ScopeUsage() string { return "" }

func (appleChartsAdapter) Requirement() Requirement {
	return Requirement{
		Notes: "Apple documents about twenty lookup calls per minute, so lower --per-host-rps for wide sweeps. Charts cap at 100 apps per storefront; use --option country=us,gb,de to widen. Seeds are publisher sites, not app backends.",
	}
}

type appleChartEntry struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ArtistName string `json:"artistName"`
}

type appleChartFeed struct {
	Feed struct {
		Results []appleChartEntry `json:"results"`
	} `json:"feed"`
}

type appleLookupResponse struct {
	Results []struct {
		TrackID    int64  `json:"trackId"`
		TrackName  string `json:"trackName"`
		SellerURL  string `json:"sellerUrl"`
		SellerName string `json:"sellerName"`
	} `json:"results"`
}

func validateStorefront(code string) (string, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if len(code) != 2 {
		return "", fmt.Errorf("storefront %q must be a two-letter code", code)
	}
	for _, character := range code {
		if character < 'a' || character > 'z' {
			return "", fmt.Errorf("storefront %q must be alphabetic", code)
		}
	}
	return code, nil
}

func (a appleChartsAdapter) Fetch(ctx context.Context, request Request) ([]model.Seed, error) {
	feed := strings.ToLower(strings.TrimSpace(request.Option("feed", "top-free")))
	if !appleFeeds[feed] {
		return nil, fmt.Errorf("unsupported feed %q; use top-free or top-paid", feed)
	}

	chartLimit := request.Limit
	if chartLimit > appleChartMaxLimit {
		chartLimit = appleChartMaxLimit
	}

	ordered := make([]appleChartEntry, 0, request.Limit)
	seen := make(map[string]struct{})
	for _, rawCountry := range strings.Split(request.Option("country", "us"), ",") {
		if strings.TrimSpace(rawCountry) == "" {
			continue
		}
		country, err := validateStorefront(rawCountry)
		if err != nil {
			return nil, err
		}

		endpoint := fmt.Sprintf(
			"https://rss.marketingtools.apple.com/api/v2/%s/apps/%s/%d/apps.json",
			country, feed, chartLimit,
		)
		var chart appleChartFeed
		if err := GetJSON(ctx, request, endpoint, &chart); err != nil {
			return nil, fmt.Errorf("%s chart: %w", country, err)
		}
		for _, entry := range chart.Feed.Results {
			if entry.ID == "" {
				continue
			}
			if _, exists := seen[entry.ID]; exists {
				continue
			}
			seen[entry.ID] = struct{}{}
			ordered = append(ordered, entry)
		}
	}
	if len(ordered) == 0 {
		return nil, fmt.Errorf("chart returned no apps")
	}

	sellers, err := a.resolveSellers(ctx, request, ordered)
	if err != nil {
		return nil, err
	}

	seeds := make([]model.Seed, 0, len(ordered))
	for index, entry := range ordered {
		seller, ok := sellers[entry.ID]
		if !ok || seller == "" {
			continue
		}
		seeds = append(seeds, model.Seed{
			Value:    seller,
			Kind:     model.SeedHost,
			Adapter:  a.Name(),
			Rank:     index + 1,
			RankKind: model.RankOrdinal,
			Evidence: strings.TrimSpace(entry.Name + " (" + entry.ArtistName + ")"),
		})
	}
	return seeds, nil
}

func (a appleChartsAdapter) resolveSellers(ctx context.Context, request Request, entries []appleChartEntry) (map[string]string, error) {
	sellers := make(map[string]string, len(entries))

	for start := 0; start < len(entries); start += appleLookupBatch {
		end := start + appleLookupBatch
		if end > len(entries) {
			end = len(entries)
		}

		identifiers := make([]string, 0, end-start)
		for _, entry := range entries[start:end] {
			identifiers = append(identifiers, entry.ID)
		}
		endpoint := "https://itunes.apple.com/lookup?id=" + url.QueryEscape(strings.Join(identifiers, ","))

		var lookup appleLookupResponse
		if err := GetJSON(ctx, request, endpoint, &lookup); err != nil {
			return nil, fmt.Errorf("lookup: %w", err)
		}
		for _, result := range lookup.Results {
			if result.SellerURL == "" {
				continue
			}
			host, err := NormalizeHost(result.SellerURL)
			if err != nil {
				continue
			}
			sellers[fmt.Sprintf("%d", result.TrackID)] = host
		}
	}
	return sellers, nil
}
