package corpus

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/usestring/gqlcrawl/internal/model"
)

func TestWaybackReadsColumnsFromHeaderRow(t *testing.T) {
	payload := `[["original","timestamp","statuscode"],
["https://api.example.com/graphql","20260101120000","405"],
["https://shop.example.com/api/graphql","20250704090000","200"]]`
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"web.archive.org": newResponse(http.StatusOK, payload),
	}}
	request := baseRequest(fetcher, 10)
	request.Scope = []string{"example.com"}

	seeds, err := (waybackAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("want 2 seeds, got %d", len(seeds))
	}
	if seeds[0].Value != "https://api.example.com/graphql" {
		t.Fatalf("unexpected first seed %q", seeds[0].Value)
	}
	if seeds[0].Kind != model.SeedURL {
		t.Fatalf("want url seeds, got %q", seeds[0].Kind)
	}
	if seeds[0].Evidence != "example.com 20260101120000" {
		t.Fatalf("unexpected evidence %q", seeds[0].Evidence)
	}
	if seeds[0].RankKind != model.RankNone {
		t.Fatalf("archived captures are unranked, got %q", seeds[0].RankKind)
	}
}

func TestWaybackToleratesReorderedColumns(t *testing.T) {
	payload := `[["timestamp","statuscode","original"],
["20260101120000","200","https://api.example.com/graphql"]]`
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"web.archive.org": newResponse(http.StatusOK, payload),
	}}
	request := baseRequest(fetcher, 10)
	request.Scope = []string{"example.com"}

	seeds, err := (waybackAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 1 || seeds[0].Value != "https://api.example.com/graphql" {
		t.Fatalf("unexpected seeds %+v", seeds)
	}
}

func TestWaybackFullMatchesTheEscapedPattern(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"web.archive.org": newResponse(http.StatusOK, `[["original"]]`),
	}}
	request := baseRequest(fetcher, 10)
	request.Scope = []string{"example.com"}
	request.Options = map[string]string{"pattern": "graph.ql"}

	if _, err := (waybackAdapter{}).Fetch(context.Background(), request); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	query := fetcher.requested[0].URL.Query()
	if got := query.Get("filter"); got != `urlkey:.*graph\.ql.*` {
		t.Fatalf("unexpected filter %q", got)
	}
	if got := query.Get("matchType"); got != "domain" {
		t.Fatalf("unexpected matchType %q", got)
	}
}

func TestWaybackSkipsTemplatePlaceholders(t *testing.T) {
	payload := `[["original"],
["https://example.com/${region}/graphql"],
["https://example.com/%24%7Bregion%7D/graphql"],
["https://example.com/api/graphql"]]`
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"web.archive.org": newResponse(http.StatusOK, payload),
	}}
	request := baseRequest(fetcher, 10)
	request.Scope = []string{"example.com"}

	seeds, err := (waybackAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 1 || seeds[0].Value != "https://example.com/api/graphql" {
		t.Fatalf("unexpected seeds %+v", seeds)
	}
}

func TestWaybackRejectsUnknownMatchType(t *testing.T) {
	request := baseRequest(&routedFetcher{}, 10)
	request.Scope = []string{"example.com"}
	request.Options = map[string]string{"matchtype": "everything"}

	if _, err := (waybackAdapter{}).Fetch(context.Background(), request); err == nil {
		t.Fatal("want an error for an unsupported matchtype")
	}
}

func TestWaybackRequiresScope(t *testing.T) {
	if _, err := (waybackAdapter{}).Fetch(context.Background(), baseRequest(&routedFetcher{}, 10)); err == nil {
		t.Fatal("want an error when no domain is supplied")
	}
}

func TestCommonCrawlUsesNewestCrawlAndWalksPages(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"collinfo.json": newResponse(http.StatusOK, `[{"id":"CC-MAIN-2026-30","cdx-api":"https://index.commoncrawl.org/CC-MAIN-2026-30-index"},
{"id":"CC-MAIN-2026-26","cdx-api":"https://index.commoncrawl.org/CC-MAIN-2026-26-index"}]`),
		"showNumPages": newResponse(http.StatusOK, `{"pages": 3, "pageSize": 5, "blocks": 12}`),
		"page=0":       newResponse(http.StatusNotFound, `{"message": "No Captures found for: example.com"}`),
		"page=1":       newResponse(http.StatusOK, `{"url":"https://api.example.com/graphql","timestamp":"20260720223604","status":"200"}`),
		"page=2":       newResponse(http.StatusOK, `{"url":"https://shop.example.com/graphql","timestamp":"20260720223700","status":"400"}`),
	}}

	request := baseRequest(fetcher, 10)
	request.Scope = []string{"example.com"}

	seeds, err := (commonCrawlAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("want 2 seeds across pages, got %d: %+v", len(seeds), seeds)
	}
	if seeds[0].Evidence != "CC-MAIN-2026-30 20260720223604" {
		t.Fatalf("unexpected evidence %q", seeds[0].Evidence)
	}
	if seeds[1].Value != "https://shop.example.com/graphql" {
		t.Fatalf("unexpected second seed %q", seeds[1].Value)
	}
}

func TestCommonCrawlFiltersWithoutRegexSyntax(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"collinfo.json": newResponse(http.StatusOK, `[{"id":"CC-MAIN-2026-30","cdx-api":"https://index.commoncrawl.org/CC-MAIN-2026-30-index"}]`),
		"showNumPages":  newResponse(http.StatusOK, `{"pages": 1}`),
		"page=0":        newResponse(http.StatusOK, ""),
	}}
	request := baseRequest(fetcher, 10)
	request.Scope = []string{"example.com"}
	request.Options = map[string]string{"pattern": "graph.ql"}

	if _, err := (commonCrawlAdapter{}).Fetch(context.Background(), request); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, sent := range fetcher.requested {
		if sent.URL.Query().Get("page") == "" {
			continue
		}
		if got := sent.URL.Query().Get("filter"); got != "urlkey:graph.ql" {
			t.Fatalf("unexpected filter %q", got)
		}
	}
}

func TestCommonCrawlHonoursTheCrawlOption(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"showNumPages": newResponse(http.StatusOK, `{"pages": 1}`),
		"page=0":       newResponse(http.StatusOK, `{"url":"https://api.example.com/graphql","timestamp":"20260101000000"}`),
	}}
	request := baseRequest(fetcher, 10)
	request.Scope = []string{"example.com"}
	request.Options = map[string]string{"crawl": "CC-MAIN-2026-26"}

	seeds, err := (commonCrawlAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 1 || seeds[0].Evidence != "CC-MAIN-2026-26 20260101000000" {
		t.Fatalf("unexpected seeds %+v", seeds)
	}
	for _, sent := range fetcher.requested {
		if strings.Contains(sent.URL.String(), "collinfo.json") {
			t.Fatal("an explicit crawl must not list the collections")
		}
		if !strings.Contains(sent.URL.String(), "CC-MAIN-2026-26-index") {
			t.Fatalf("unexpected index URL %q", sent.URL)
		}
	}
}

func TestCommonCrawlStopsAtTheLimit(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"collinfo.json": newResponse(http.StatusOK, `[{"id":"CC-MAIN-2026-30","cdx-api":"https://index.commoncrawl.org/CC-MAIN-2026-30-index"}]`),
		"showNumPages":  newResponse(http.StatusOK, `{"pages": 5}`),
		"page=0": newResponse(http.StatusOK, `{"url":"https://a.example.com/graphql"}
{"url":"https://b.example.com/graphql"}
{"url":"https://c.example.com/graphql"}`),
	}}
	request := baseRequest(fetcher, 2)
	request.Scope = []string{"example.com"}

	seeds, err := (commonCrawlAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("want the limit honoured, got %d", len(seeds))
	}
	for _, sent := range fetcher.requested {
		if sent.URL.Query().Get("page") == "1" {
			t.Fatal("reaching the limit must stop paging")
		}
	}
}

func TestCommonCrawlReportsAnEmptyCollectionList(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"collinfo.json": newResponse(http.StatusOK, `[]`),
	}}
	request := baseRequest(fetcher, 10)
	request.Scope = []string{"example.com"}

	if _, err := (commonCrawlAdapter{}).Fetch(context.Background(), request); err == nil {
		t.Fatal("want an error when no crawl index is published")
	}
}

func TestParseCommonCrawlRecordsSkipsBlankLines(t *testing.T) {
	body := []byte("\n{\"url\":\"https://api.example.com/graphql\"}\n\n")
	records, err := parseCommonCrawlRecords(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records) != 1 || records[0].URL != "https://api.example.com/graphql" {
		t.Fatalf("unexpected records %+v", records)
	}
}
