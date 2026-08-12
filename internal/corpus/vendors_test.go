package corpus

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/usestring/gqlcrawl/internal/model"
)

type failingFetcher struct {
	err error
}

func (f *failingFetcher) Do(*http.Request) (*http.Response, error) {
	return nil, f.err
}

type cyclingFetcher struct {
	bodies    []string
	requested []*http.Request
}

func (f *cyclingFetcher) Do(request *http.Request) (*http.Response, error) {
	f.requested = append(f.requested, request)
	index := len(f.requested) - 1
	if index >= len(f.bodies) {
		index = len(f.bodies) - 1
	}
	return newResponse(http.StatusOK, f.bodies[index]), nil
}

func keyedRequest(fetcher Fetcher, limit int) Request {
	request := baseRequest(fetcher, limit)
	request.Lookup = func(string) string { return "vendor-key" }
	return request
}

func TestShodanRequiresAKey(t *testing.T) {
	request := baseRequest(&routedFetcher{}, 10)
	request.Lookup = func(string) string { return "" }

	if _, err := (shodanAdapter{}).Fetch(context.Background(), request); err == nil {
		t.Fatal("want an error when the key is unset")
	}
}

func TestShodanEmitsBannerHostnames(t *testing.T) {
	body := `{"total":2,"matches":[
{"ip_str":"203.0.113.10","hostnames":["api.example.com","www.example.com"]},
{"ip_str":"203.0.113.11","hostnames":["shop.example.net"]}]}`
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"api.shodan.io": newResponse(http.StatusOK, body),
	}}

	seeds, err := (shodanAdapter{}).Fetch(context.Background(), keyedRequest(fetcher, 10))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 3 {
		t.Fatalf("want 3 seeds, got %d", len(seeds))
	}
	if seeds[0].Kind != model.SeedHost || seeds[0].RankKind != model.RankNone {
		t.Fatalf("shodan results are unranked hosts, got %+v", seeds[0])
	}
	if seeds[0].Evidence != "http.component:GraphQL" {
		t.Fatalf("unexpected evidence %q", seeds[0].Evidence)
	}
}

func TestShodanSkipsMatchesWithoutHostnames(t *testing.T) {
	body := `{"total":1,"matches":[{"ip_str":"203.0.113.10","hostnames":[]}]}`
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"api.shodan.io": newResponse(http.StatusOK, body),
	}}

	seeds, err := (shodanAdapter{}).Fetch(context.Background(), keyedRequest(fetcher, 10))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 0 {
		t.Fatalf("an address without a name is not a seed, got %+v", seeds)
	}
}

func TestShodanUsesAConfiguredComponentAndQuery(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"api.shodan.io": newResponse(http.StatusOK, `{"matches":[]}`),
	}}
	request := keyedRequest(fetcher, 10)
	request.Options = map[string]string{"component": "Apollo"}

	if _, err := (shodanAdapter{}).Fetch(context.Background(), request); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := fetcher.requested[0].URL.Query().Get("query"); got != "http.component:Apollo" {
		t.Fatalf("unexpected query %q", got)
	}

	request.Options = map[string]string{"component": "Apollo", "query": `http.html:"__schema"`}
	if _, err := (shodanAdapter{}).Fetch(context.Background(), request); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := fetcher.requested[1].URL.Query().Get("query"); got != `http.html:"__schema"` {
		t.Fatalf("an explicit query must win, got %q", got)
	}
}

func TestShodanRequestsOnlyThePagesTheLimitNeeds(t *testing.T) {
	body := `{"matches":[{"hostnames":["a.example.com"]}]}`
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"api.shodan.io": newResponse(http.StatusOK, body),
	}}

	if _, err := (shodanAdapter{}).Fetch(context.Background(), keyedRequest(fetcher, 50)); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(fetcher.requested) != 1 {
		t.Fatalf("a sub-page limit must cost one request, got %d", len(fetcher.requested))
	}
	if got := fetcher.requested[0].URL.Query().Get("page"); got != "1" {
		t.Fatalf("shodan pages are one-based, got %q", got)
	}
}

func TestShodanSurfacesAReportedError(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"api.shodan.io": newResponse(http.StatusOK, `{"error":"Requires membership or higher to access"}`),
	}}

	_, err := (shodanAdapter{}).Fetch(context.Background(), keyedRequest(fetcher, 10))
	if err == nil || !strings.Contains(err.Error(), "Requires membership") {
		t.Fatalf("want the reported error surfaced, got %v", err)
	}
}

func TestVendorTransportErrorsDoNotCarryTheKey(t *testing.T) {
	// A transport error wraps *url.Error, whose message is the whole request URL.
	failing := &failingFetcher{err: errors.New(`Get "https://api.shodan.io/shodan/host/search?key=vendor-key&page=1": dial tcp: lookup failed`)}

	for name, fetch := range map[string]func(context.Context, Request) ([]model.Seed, error){
		"shodan":    (shodanAdapter{}).Fetch,
		"builtwith": (builtWithAdapter{}).Fetch,
	} {
		_, err := fetch(context.Background(), keyedRequest(failing, 10))
		if err == nil {
			t.Fatalf("%s: want a transport error", name)
		}
		if strings.Contains(err.Error(), "vendor-key") {
			t.Fatalf("%s leaked the key: %v", name, err)
		}
	}
}

func TestBuiltWithRequiresAKey(t *testing.T) {
	request := baseRequest(&routedFetcher{}, 10)
	request.Lookup = func(string) string { return "" }

	if _, err := (builtWithAdapter{}).Fetch(context.Background(), request); err == nil {
		t.Fatal("want an error when the key is unset")
	}
}

func TestBuiltWithReadsTheShortDomainKey(t *testing.T) {
	body := `{"NextOffset":"END","Results":[{"D":"example.com","LD":1786072593},{"D":"shop.example.net"}]}`
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"builtwith.com": newResponse(http.StatusOK, body),
	}}

	seeds, err := (builtWithAdapter{}).Fetch(context.Background(), keyedRequest(fetcher, 10))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 2 || seeds[0].Value != "example.com" {
		t.Fatalf("unexpected seeds %+v", seeds)
	}
	if seeds[0].Evidence != "Apollo-GraphQL" {
		t.Fatalf("unexpected evidence %q", seeds[0].Evidence)
	}
	if got := fetcher.requested[0].URL.Query().Get("TECH"); got != "Apollo-GraphQL" {
		t.Fatalf("unexpected technology %q", got)
	}
}

func TestBuiltWithStopsOnTheEndToken(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"builtwith.com": newResponse(http.StatusOK, `{"NextOffset":"END","Results":[{"D":"example.com"}]}`),
	}}

	if _, err := (builtWithAdapter{}).Fetch(context.Background(), keyedRequest(fetcher, 500)); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(fetcher.requested) != 1 {
		t.Fatalf("END must end paging, got %d requests", len(fetcher.requested))
	}
}

func TestBuiltWithPassesTheOffsetTokenBackVerbatim(t *testing.T) {
	fetcher := &cyclingFetcher{bodies: []string{
		`{"NextOffset":"aWQ9MTIzNDU=","Results":[{"D":"first.example.com"}]}`,
		`{"NextOffset":"END","Results":[{"D":"second.example.com"}]}`,
	}}

	seeds, err := (builtWithAdapter{}).Fetch(context.Background(), keyedRequest(fetcher, 500))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("want both pages collected, got %d", len(seeds))
	}
	if got := fetcher.requested[1].URL.Query().Get("OFFSET"); got != "aWQ9MTIzNDU=" {
		t.Fatalf("the continuation token must round-trip unchanged, got %q", got)
	}
}

func TestBuiltWithSurfacesAReportedError(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"builtwith.com": newResponse(http.StatusOK, `{"Errors":[{"Message":"Invalid API Key"}]}`),
	}}

	_, err := (builtWithAdapter{}).Fetch(context.Background(), keyedRequest(fetcher, 10))
	if err == nil || !strings.Contains(err.Error(), "Invalid API Key") {
		t.Fatalf("want the reported error surfaced, got %v", err)
	}
}
