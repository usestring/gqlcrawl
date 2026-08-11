package corpus

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/usestring/gqlcrawl/internal/model"
)

type routedFetcher struct {
	responses map[string]*http.Response
	requested []*http.Request
}

func (f *routedFetcher) Do(request *http.Request) (*http.Response, error) {
	f.requested = append(f.requested, request)
	for pattern, response := range f.responses {
		if strings.Contains(request.URL.String(), pattern) {
			return response, nil
		}
	}
	return newResponse(http.StatusNotFound, "no stub"), nil
}

func baseRequest(fetcher Fetcher, limit int) Request {
	return Request{
		Limit:            limit,
		Fetcher:          fetcher,
		UserAgent:        "gqlcrawl/test",
		MaxDownloadBytes: DefaultMaxDownloadBytes,
	}
}

func TestParseRankedCSVHandlesCRLFAndHeaders(t *testing.T) {
	rows, err := parseRankedCSV([]byte("1,one.example\r\n2,two.example\r\n"), 0, 1, false, 10)
	if err != nil {
		t.Fatalf("parseRankedCSV returned error: %v", err)
	}
	if len(rows) != 2 || rows[0].domain != "one.example" || rows[1].rank != 2 {
		t.Fatalf("rows = %+v", rows)
	}

	headed, err := parseRankedCSV([]byte("GlobalRank,TldRank,Domain\n1,1,one.example\n2,2,two.example\n"), 0, 2, true, 10)
	if err != nil {
		t.Fatalf("parseRankedCSV returned error: %v", err)
	}
	if len(headed) != 2 || headed[0].domain != "one.example" {
		t.Fatalf("rows = %+v", headed)
	}
}

func TestParseRankedCSVStopsAtLimitAndToleratesTruncation(t *testing.T) {
	rows, err := parseRankedCSV([]byte("1,one.example\n2,two.example\n3,thr"), 0, 1, false, 2)
	if err != nil {
		t.Fatalf("parseRankedCSV returned error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("limit not honored: %+v", rows)
	}
}

func TestTrancoFetchUsesServerSideLimitAndReportsListID(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"/api/lists/date/latest": newResponse(http.StatusOK,
			`{"list_id":"Q2XX4","available":true,"failed":false,"download":"https://tranco-list.eu/download/Q2XX4/1000000","created_on":"2026-08-10T22:00:04"}`),
		"/download/Q2XX4/": newResponse(http.StatusOK, "1,one.example\r\n2,two.example\r\n"),
	}}

	seeds, err := trancoAdapter{}.Fetch(context.Background(), baseRequest(fetcher, 2))
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(seeds) != 2 || seeds[0].Value != "one.example" {
		t.Fatalf("seeds = %+v", seeds)
	}
	if seeds[0].RankKind != model.RankOrdinal || seeds[0].Rank != 1 {
		t.Fatalf("rank not ordinal: %+v", seeds[0])
	}
	if !strings.Contains(seeds[0].Evidence, "Q2XX4") {
		t.Fatalf("evidence lost the list id: %q", seeds[0].Evidence)
	}

	var downloadURL string
	for _, request := range fetcher.requested {
		if strings.Contains(request.URL.Path, "/download/") {
			downloadURL = request.URL.String()
		}
	}
	if !strings.HasSuffix(downloadURL, "/download/Q2XX4/2") {
		t.Fatalf("download did not carry the server-side row limit: %q", downloadURL)
	}
}

func TestTrancoFetchRejectsUnavailableList(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"/api/lists/date/latest": newResponse(http.StatusOK, `{"list_id":"Q2XX4","available":false,"failed":false}`),
	}}

	if _, err := (trancoAdapter{}).Fetch(context.Background(), baseRequest(fetcher, 5)); err == nil {
		t.Fatal("Fetch accepted an unavailable list")
	}
}

func TestTrancoFetchRejectsFailedList(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"/api/lists/date/latest": newResponse(http.StatusOK, `{"list_id":"Q2XX4","available":true,"failed":true}`),
	}}

	if _, err := (trancoAdapter{}).Fetch(context.Background(), baseRequest(fetcher, 5)); err == nil {
		t.Fatal("Fetch accepted a failed list")
	}
}

func TestUmbrellaFetchReadsZipMember(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	member, err := writer.Create("top-1m.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(member, "1,www.one.example\n2,two.example\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"umbrella-static": {
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(archive.Bytes())),
			Header:     http.Header{},
		},
	}}

	seeds, err := umbrellaAdapter{}.Fetch(context.Background(), baseRequest(fetcher, 10))
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(seeds) != 2 || seeds[0].Value != "www.one.example" {
		t.Fatalf("seeds = %+v", seeds)
	}
}

func TestUmbrellaFetchRejectsNonArchive(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"umbrella-static": newResponse(http.StatusOK, "not a zip"),
	}}

	if _, err := (umbrellaAdapter{}).Fetch(context.Background(), baseRequest(fetcher, 10)); err == nil {
		t.Fatal("Fetch accepted a non-archive payload")
	}
}

func TestMajesticFetchRequestsBoundedRange(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"majestic_million.csv": newResponse(http.StatusPartialContent,
			"GlobalRank,TldRank,Domain,TLD\n1,1,one.example,example\n2,2,two.example,example\n"),
	}}

	seeds, err := majesticAdapter{}.Fetch(context.Background(), baseRequest(fetcher, 2))
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(seeds) != 2 || seeds[0].Value != "one.example" {
		t.Fatalf("seeds = %+v", seeds)
	}
	if got := fetcher.requested[0].Header.Get("Range"); got == "" || !strings.HasPrefix(got, "bytes=0-") {
		t.Fatalf("Range header = %q", got)
	}
}

func TestAllAdaptersAreDiscoverableAndDescribed(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("no adapters registered")
	}
	for _, name := range names {
		adapter, err := Lookup(name)
		if err != nil {
			t.Fatalf("Lookup(%q) failed: %v", name, err)
		}
		if adapter.Summary() == "" {
			t.Fatalf("adapter %q has no summary", name)
		}
	}
	for index := 1; index < len(names); index++ {
		if names[index-1] >= names[index] {
			t.Fatalf("adapter names are not sorted: %v", names)
		}
	}
}
