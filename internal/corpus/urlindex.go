package corpus

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/usestring/gqlcrawl/internal/model"
)

const (
	waybackEndpoint         = "https://web.archive.org/cdx/search/cdx"
	commonCrawlCollections  = "https://index.commoncrawl.org/collinfo.json"
	commonCrawlMaxPages     = 200
	defaultURLIndexPattern  = "graphql"
	urlIndexMaxLineBytes    = 1024 * 1024
	commonCrawlIndexBaseURL = "https://index.commoncrawl.org/"
)

var urlIndexMatchTypes = map[string]struct{}{
	"exact":  {},
	"prefix": {},
	"host":   {},
	"domain": {},
}

func urlIndexMatchType(request Request) (string, error) {
	value := strings.ToLower(request.Option("matchtype", "domain"))
	if _, ok := urlIndexMatchTypes[value]; !ok {
		return "", fmt.Errorf("unsupported matchtype %q; use exact, prefix, host, or domain", value)
	}
	return value, nil
}

// Both indexes harvest URLs out of JavaScript, so unexpanded template literals such as
// https://example.com/${region}/graphql arrive as ordinary rows. They describe a path shape
// rather than an address, and probing one would request a literal dollar-brace path.
func hasTemplatePlaceholder(raw string) bool {
	lowered := strings.ToLower(raw)
	return strings.Contains(lowered, "${") || strings.Contains(lowered, "%24%7b")
}

func getWithStatus(ctx context.Context, request Request, target string, accept string) ([]byte, int, error) {
	if request.Fetcher == nil {
		return nil, 0, fmt.Errorf("no fetcher configured")
	}
	limit := request.MaxDownloadBytes
	if limit <= 0 {
		limit = DefaultMaxDownloadBytes
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	httpRequest.Header.Set("User-Agent", request.UserAgent)
	if accept != "" {
		httpRequest.Header.Set("Accept", accept)
	}

	response, err := request.Fetcher.Do(httpRequest)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch: %w", err)
	}
	defer response.Body.Close()

	body, err := readBounded(response.Body, limit)
	if err != nil {
		return nil, response.StatusCode, err
	}
	return body, response.StatusCode, nil
}

type waybackAdapter struct{}

func (waybackAdapter) Name() string { return "wayback" }

func (waybackAdapter) Summary() string {
	return "Archived URLs matching a path pattern via the Internet Archive CDX server (unranked, no credentials)"
}

func (waybackAdapter) ScopeUsage() string { return "one or more domains" }

func (waybackAdapter) Requirement() Requirement {
	return Requirement{
		Notes: "Signals throttling with HTTP 503 and no rate headers, so keep to one request per second. Rows are historical captures: a URL may have moved or stopped resolving since it was archived.",
	}
}

func (a waybackAdapter) Fetch(ctx context.Context, request Request) ([]model.Seed, error) {
	if len(request.Scope) == 0 {
		return nil, fmt.Errorf("wayback requires at least one domain")
	}
	matchType, err := urlIndexMatchType(request)
	if err != nil {
		return nil, err
	}
	pattern := request.Option("pattern", defaultURLIndexPattern)

	seeds := make([]model.Seed, 0, request.Limit)
	for _, rawDomain := range request.Scope {
		domain, err := NormalizeHost(rawDomain)
		if err != nil {
			return seeds, err
		}

		query := url.Values{}
		query.Set("url", domain)
		query.Set("matchType", matchType)
		query.Set("output", "json")
		query.Set("fl", "original,timestamp,statuscode")
		query.Set("collapse", "urlkey")
		query.Set("limit", fmt.Sprint(request.Limit))
		query.Set("filter", "urlkey:.*"+regexp.QuoteMeta(pattern)+".*")
		if from := request.Option("from", ""); from != "" {
			query.Set("from", from)
		}
		if to := request.Option("to", ""); to != "" {
			query.Set("to", to)
		}
		if status := request.Option("status", ""); status != "" {
			query.Add("filter", "statuscode:"+status)
		}

		body, err := Get(ctx, request, waybackEndpoint+"?"+query.Encode(), "application/json")
		if err != nil {
			return seeds, fmt.Errorf("%s: %w", domain, err)
		}

		var rows [][]string
		if err := json.Unmarshal(body, &rows); err != nil {
			return seeds, fmt.Errorf("%s: decode captures: %w", domain, err)
		}
		if len(rows) == 0 {
			continue
		}

		columns := map[string]int{}
		for index, name := range rows[0] {
			columns[name] = index
		}
		originalColumn, ok := columns["original"]
		if !ok {
			return seeds, fmt.Errorf("%s: response has no original column", domain)
		}
		timestampColumn, hasTimestamp := columns["timestamp"]

		for _, row := range rows[1:] {
			if originalColumn >= len(row) {
				continue
			}
			captured := row[originalColumn]
			if hasTemplatePlaceholder(captured) {
				continue
			}
			evidence := domain
			if hasTimestamp && timestampColumn < len(row) {
				evidence = domain + " " + row[timestampColumn]
			}
			seeds = append(seeds, model.Seed{
				Value:    captured,
				Kind:     model.SeedURL,
				Adapter:  a.Name(),
				Evidence: evidence,
			})
			if len(seeds) >= request.Limit {
				return seeds, nil
			}
		}
	}
	return seeds, nil
}

type commonCrawlAdapter struct{}

func (commonCrawlAdapter) Name() string { return "commoncrawl" }

func (commonCrawlAdapter) Summary() string {
	return "Crawled URLs matching a path pattern via the Common Crawl index (unranked, no credentials)"
}

func (commonCrawlAdapter) ScopeUsage() string { return "one or more domains" }

func (commonCrawlAdapter) Requirement() Requirement {
	return Requirement{
		Notes: "Common Crawl asks for single-threaded access below one request per second and answers 503 when throttling. A blocked address stays blocked for about a day, so back off rather than retrying.",
	}
}

func (a commonCrawlAdapter) resolveIndex(ctx context.Context, request Request) (string, string, error) {
	if crawl := request.Option("crawl", ""); crawl != "" {
		return commonCrawlIndexBaseURL + crawl + "-index", crawl, nil
	}

	var collections []struct {
		ID     string `json:"id"`
		CDXAPI string `json:"cdx-api"`
	}
	if err := GetJSON(ctx, request, commonCrawlCollections, &collections); err != nil {
		return "", "", fmt.Errorf("list crawls: %w", err)
	}
	if len(collections) == 0 || collections[0].CDXAPI == "" {
		return "", "", fmt.Errorf("no crawl index is available")
	}
	return collections[0].CDXAPI, collections[0].ID, nil
}

func (a commonCrawlAdapter) pageCount(ctx context.Context, request Request, index string, query url.Values) (int, error) {
	counting := url.Values{}
	for key, values := range query {
		counting[key] = values
	}
	counting.Set("showNumPages", "true")

	body, status, err := getWithStatus(ctx, request, index+"?"+counting.Encode(), "application/json")
	if err != nil {
		return 0, err
	}
	if status == http.StatusNotFound {
		return 0, nil
	}
	if status != http.StatusOK {
		return 0, fmt.Errorf("unexpected status %d", status)
	}

	var pages struct {
		Pages int `json:"pages"`
	}
	if err := json.Unmarshal(body, &pages); err != nil {
		return 0, fmt.Errorf("decode page count: %w", err)
	}
	if pages.Pages > commonCrawlMaxPages {
		return commonCrawlMaxPages, nil
	}
	return pages.Pages, nil
}

func (a commonCrawlAdapter) Fetch(ctx context.Context, request Request) ([]model.Seed, error) {
	if len(request.Scope) == 0 {
		return nil, fmt.Errorf("commoncrawl requires at least one domain")
	}
	matchType, err := urlIndexMatchType(request)
	if err != nil {
		return nil, err
	}
	pattern := request.Option("pattern", defaultURLIndexPattern)

	index, crawl, err := a.resolveIndex(ctx, request)
	if err != nil {
		return nil, err
	}

	seeds := make([]model.Seed, 0, request.Limit)
	for _, rawDomain := range request.Scope {
		domain, err := NormalizeHost(rawDomain)
		if err != nil {
			return seeds, err
		}

		query := url.Values{}
		query.Set("url", domain)
		query.Set("matchType", matchType)

		pages, err := a.pageCount(ctx, request, index, query)
		if err != nil {
			return seeds, fmt.Errorf("%s: %w", domain, err)
		}

		query.Set("output", "json")
		query.Set("fl", "url,timestamp,status")
		query.Set("collapse", "urlkey")
		query.Set("filter", "urlkey:"+pattern)

		// filter and limit are applied within a page, so an empty page is not end of data.
		for page := 0; page < pages && len(seeds) < request.Limit; page++ {
			query.Set("page", fmt.Sprint(page))

			body, status, err := getWithStatus(ctx, request, index+"?"+query.Encode(), "application/json")
			if err != nil {
				return seeds, fmt.Errorf("%s: %w", domain, err)
			}
			if status == http.StatusNotFound {
				continue
			}
			if status != http.StatusOK {
				return seeds, fmt.Errorf("%s: unexpected status %d", domain, status)
			}

			captures, err := parseCommonCrawlRecords(body)
			if err != nil {
				return seeds, fmt.Errorf("%s: %w", domain, err)
			}
			for _, capture := range captures {
				if capture.URL == "" || hasTemplatePlaceholder(capture.URL) {
					continue
				}
				evidence := crawl
				if capture.Timestamp != "" {
					evidence = crawl + " " + capture.Timestamp
				}
				seeds = append(seeds, model.Seed{
					Value:    capture.URL,
					Kind:     model.SeedURL,
					Adapter:  a.Name(),
					Evidence: evidence,
				})
				if len(seeds) >= request.Limit {
					return seeds, nil
				}
			}
		}
	}
	return seeds, nil
}

type commonCrawlRecord struct {
	URL       string `json:"url"`
	Timestamp string `json:"timestamp"`
	Status    string `json:"status"`
}

func parseCommonCrawlRecords(body []byte) ([]commonCrawlRecord, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), urlIndexMaxLineBytes)

	records := make([]commonCrawlRecord, 0, 64)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record commonCrawlRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return records, fmt.Errorf("decode capture: %w", err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return records, fmt.Errorf("read captures: %w", err)
	}
	return records, nil
}
