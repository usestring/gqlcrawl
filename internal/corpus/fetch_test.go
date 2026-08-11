package corpus

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type stubFetcher struct {
	response *http.Response
	err      error
	request  *http.Request
}

func (f *stubFetcher) Do(request *http.Request) (*http.Response, error) {
	f.request = request
	if f.err != nil {
		return nil, f.err
	}
	return f.response, nil
}

func newResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

func TestGetSendsUserAgentAndAccept(t *testing.T) {
	fetcher := &stubFetcher{response: newResponse(http.StatusOK, "payload")}
	request := Request{Fetcher: fetcher, UserAgent: "gqlcrawl/test", MaxDownloadBytes: 1024}

	body, err := Get(context.Background(), request, "https://corpus.example/list", "text/csv")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if string(body) != "payload" {
		t.Fatalf("body = %q, want \"payload\"", body)
	}
	if got := fetcher.request.Header.Get("User-Agent"); got != "gqlcrawl/test" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := fetcher.request.Header.Get("Accept"); got != "text/csv" {
		t.Fatalf("Accept = %q", got)
	}
}

func TestGetRejectsOversizedBody(t *testing.T) {
	fetcher := &stubFetcher{response: newResponse(http.StatusOK, strings.Repeat("a", 100))}
	request := Request{Fetcher: fetcher, UserAgent: "gqlcrawl/test", MaxDownloadBytes: 10}

	if _, err := Get(context.Background(), request, "https://corpus.example/list", ""); err == nil {
		t.Fatal("Get accepted a body over the download limit")
	}
}

func TestGetRejectsNonOKStatus(t *testing.T) {
	fetcher := &stubFetcher{response: newResponse(http.StatusTooManyRequests, "slow down")}
	request := Request{Fetcher: fetcher, UserAgent: "gqlcrawl/test", MaxDownloadBytes: 1024}

	_, err := Get(context.Background(), request, "https://corpus.example/list", "")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("Get error = %v, want a 429 status error", err)
	}
}

func TestGetRequiresFetcher(t *testing.T) {
	if _, err := Get(context.Background(), Request{}, "https://corpus.example/list", ""); err == nil {
		t.Fatal("Get succeeded without a fetcher")
	}
}

func TestGetPropagatesTransportError(t *testing.T) {
	fetcher := &stubFetcher{err: fmt.Errorf("dial refused")}
	request := Request{Fetcher: fetcher, UserAgent: "gqlcrawl/test", MaxDownloadBytes: 1024}

	if _, err := Get(context.Background(), request, "https://corpus.example/list", ""); err == nil {
		t.Fatal("Get hid a transport error")
	}
}

func TestGetRangeRequestsBoundedPrefix(t *testing.T) {
	fetcher := &stubFetcher{response: newResponse(http.StatusPartialContent, "first rows")}
	request := Request{Fetcher: fetcher, UserAgent: "gqlcrawl/test"}

	body, err := GetRange(context.Background(), request, "https://corpus.example/big.csv", "text/csv", 64)
	if err != nil {
		t.Fatalf("GetRange returned error: %v", err)
	}
	if string(body) != "first rows" {
		t.Fatalf("body = %q", body)
	}
	if got := fetcher.request.Header.Get("Range"); got != "bytes=0-63" {
		t.Fatalf("Range = %q, want \"bytes=0-63\"", got)
	}
}

func TestGetRangeTruncatesRatherThanFailing(t *testing.T) {
	fetcher := &stubFetcher{response: newResponse(http.StatusOK, strings.Repeat("a", 100))}
	request := Request{Fetcher: fetcher, UserAgent: "gqlcrawl/test"}

	body, err := GetRange(context.Background(), request, "https://corpus.example/big.csv", "", 10)
	if err != nil {
		t.Fatalf("GetRange returned error: %v", err)
	}
	if len(body) != 10 {
		t.Fatalf("body length = %d, want 10", len(body))
	}
}

func TestGetRangeRejectsInvalidLimitAndStatus(t *testing.T) {
	request := Request{Fetcher: &stubFetcher{response: newResponse(http.StatusOK, "x")}, UserAgent: "gqlcrawl/test"}
	if _, err := GetRange(context.Background(), request, "https://corpus.example/big.csv", "", 0); err == nil {
		t.Fatal("GetRange accepted a non-positive limit")
	}

	failing := Request{Fetcher: &stubFetcher{response: newResponse(http.StatusForbidden, "denied")}, UserAgent: "gqlcrawl/test"}
	if _, err := GetRange(context.Background(), failing, "https://corpus.example/big.csv", "", 10); err == nil {
		t.Fatal("GetRange accepted a 403 response")
	}
}

func TestGetJSONDecodesIntoDestination(t *testing.T) {
	fetcher := &stubFetcher{response: newResponse(http.StatusOK, `{"name":"corpus"}`)}
	request := Request{Fetcher: fetcher, UserAgent: "gqlcrawl/test", MaxDownloadBytes: 1024}

	var decoded struct {
		Name string `json:"name"`
	}
	if err := GetJSON(context.Background(), request, "https://corpus.example/list.json", &decoded); err != nil {
		t.Fatalf("GetJSON returned error: %v", err)
	}
	if decoded.Name != "corpus" {
		t.Fatalf("decoded.Name = %q, want \"corpus\"", decoded.Name)
	}
}

func TestGetJSONRejectsMalformedPayload(t *testing.T) {
	fetcher := &stubFetcher{response: newResponse(http.StatusOK, "not json")}
	request := Request{Fetcher: fetcher, UserAgent: "gqlcrawl/test", MaxDownloadBytes: 1024}

	var decoded map[string]string
	if err := GetJSON(context.Background(), request, "https://corpus.example/list.json", &decoded); err == nil {
		t.Fatal("GetJSON accepted a malformed payload")
	}
}
