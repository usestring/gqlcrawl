package corpus

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/usestring/gqlcrawl/internal/model"
)

func gzipped(t *testing.T, payload string) string {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte(payload)); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	return buffer.String()
}

func TestCruxEmitsBucketRanks(t *testing.T) {
	payload := gzipped(t, "origin,rank\nhttps://playhop.com,1000\nhttps://missav.live,1000\nhttps://shop.example.com,5000\n")
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"crux-top-lists": newResponse(http.StatusOK, payload),
	}}

	seeds, err := (cruxAdapter{}).Fetch(context.Background(), baseRequest(fetcher, 10))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 3 {
		t.Fatalf("want 3 seeds, got %d", len(seeds))
	}
	if seeds[0].RankKind != model.RankBucket {
		t.Fatalf("crux ranks are magnitude buckets, got %q", seeds[0].RankKind)
	}
	if seeds[0].Rank != 1000 || seeds[2].Rank != 5000 {
		t.Fatalf("unexpected buckets %d and %d", seeds[0].Rank, seeds[2].Rank)
	}
	if seeds[0].Evidence != "global current" {
		t.Fatalf("unexpected evidence %q", seeds[0].Evidence)
	}
}

func TestCruxNormalizesOriginsToHosts(t *testing.T) {
	payload := gzipped(t, "origin,rank\nhttps://www.example.com,1000\n")
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"crux-top-lists": newResponse(http.StatusOK, payload),
	}}

	seeds, err := (cruxAdapter{}).Fetch(context.Background(), baseRequest(fetcher, 10))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	collected := Collect(seeds, 10)
	if len(collected) != 1 || collected[0].Value != "www.example.com" {
		t.Fatalf("unexpected collected seeds %+v", collected)
	}
	if collected[0].Rank != 1000 || collected[0].RankKind != model.RankBucket {
		t.Fatalf("normalization dropped the bucket: %+v", collected[0])
	}
}

func TestCruxSelectsAMonthlyArchive(t *testing.T) {
	payload := gzipped(t, "origin,rank\nhttps://example.com,1000\n")
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"crux-top-lists": newResponse(http.StatusOK, payload),
	}}
	request := baseRequest(fetcher, 10)
	request.Options = map[string]string{"country": "DE", "month": "202607"}

	seeds, err := (cruxAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if seeds[0].Evidence != "de 202607" {
		t.Fatalf("unexpected evidence %q", seeds[0].Evidence)
	}
	if got := fetcher.requested[0].URL.String(); !strings.HasSuffix(got, "/data/country/de/202607.csv.gz") {
		t.Fatalf("unexpected endpoint %q", got)
	}
}

func TestCruxRejectsACountryWithoutAMonth(t *testing.T) {
	request := baseRequest(&routedFetcher{}, 10)
	request.Options = map[string]string{"country": "de"}

	if _, err := (cruxAdapter{}).Fetch(context.Background(), request); err == nil {
		t.Fatal("want an error because country lists have no rolling current file")
	}
}

func TestCruxStopsAtTheLimit(t *testing.T) {
	payload := gzipped(t, "origin,rank\nhttps://a.example.com,1000\nhttps://b.example.com,1000\nhttps://c.example.com,1000\n")
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"crux-top-lists": newResponse(http.StatusOK, payload),
	}}

	seeds, err := (cruxAdapter{}).Fetch(context.Background(), baseRequest(fetcher, 2))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("want the limit honoured, got %d", len(seeds))
	}
}

func TestRadarRequiresAToken(t *testing.T) {
	request := baseRequest(&routedFetcher{}, 10)
	request.Lookup = func(string) string { return "" }

	if _, err := (radarAdapter{}).Fetch(context.Background(), request); err == nil {
		t.Fatal("want an error when the token is unset")
	}
}

func TestRadarReadsTheSeriesKeyWhateverItIsCalled(t *testing.T) {
	body := `{"success":true,"result":{"meta":{"lastUpdated":"2026-08-11T00:00:00Z"},
"top_0":[{"domain":"google.com","rank":1},{"domain":"cloudflare.com","rank":2}]}}`
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"ranking/top": newResponse(http.StatusOK, body),
	}}
	request := baseRequest(fetcher, 10)
	request.Lookup = func(string) string { return "radar-token" }

	seeds, err := (radarAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("want 2 seeds, got %d", len(seeds))
	}
	if seeds[0].RankKind != model.RankOrdinal || seeds[0].Rank != 1 {
		t.Fatalf("unexpected rank %+v", seeds[0])
	}
	if got := fetcher.requested[0].Header.Get("Authorization"); got != "Bearer radar-token" {
		t.Fatalf("unexpected authorization header %q", got)
	}
}

func TestRadarCapsTheRankingRequestAtTheServerMaximum(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"ranking/top": newResponse(http.StatusOK, `{"success":true,"result":{"top_0":[]}}`),
	}}
	request := baseRequest(fetcher, 5000)
	request.Lookup = func(string) string { return "radar-token" }

	if _, err := (radarAdapter{}).Fetch(context.Background(), request); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := fetcher.requested[0].URL.Query().Get("limit"); got != "100" {
		t.Fatalf("unexpected limit %q", got)
	}
}

func TestRadarReportsAFailedEnvelope(t *testing.T) {
	body := `{"success":false,"errors":[{"code":9106,"message":"Missing X-Auth-Key, X-Auth-Email or Authorization headers"}]}`
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"ranking/top": newResponse(http.StatusOK, body),
	}}
	request := baseRequest(fetcher, 10)
	request.Lookup = func(string) string { return "radar-token" }

	_, err := (radarAdapter{}).Fetch(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "Missing X-Auth-Key") {
		t.Fatalf("want the reported message surfaced, got %v", err)
	}
}

func TestRadarBucketsCarryMembershipOnly(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"ranking_top_1000": newResponse(http.StatusOK, "domain\n1rx.io\n2mdn.net\n"),
	}}
	request := baseRequest(fetcher, 10)
	request.Lookup = func(string) string { return "radar-token" }
	request.Options = map[string]string{"bucket": "1000"}

	seeds, err := (radarAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("want 2 seeds, got %d", len(seeds))
	}
	for _, seed := range seeds {
		if seed.RankKind != model.RankMember {
			t.Fatalf("bucket entries are unordered, got %q", seed.RankKind)
		}
		if seed.Rank != 0 {
			t.Fatalf("a bucket has no rank column, got %d", seed.Rank)
		}
	}
}

func TestRadarRejectsAnUnpublishedBucket(t *testing.T) {
	request := baseRequest(&routedFetcher{}, 10)
	request.Lookup = func(string) string { return "radar-token" }
	request.Options = map[string]string{"bucket": "750"}

	_, err := (radarAdapter{}).Fetch(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "buckets of") {
		t.Fatalf("want the published bucket sizes listed, got %v", err)
	}
}
