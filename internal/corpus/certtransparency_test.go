package corpus

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestCertSpotterRequestsExpandedNames(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"api.certspotter.com": newResponse(http.StatusOK, `[]`),
	}}

	request := baseRequest(fetcher, 10)
	request.Scope = []string{"example.com"}
	if _, err := (certSpotterAdapter{}).Fetch(context.Background(), request); err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	query := fetcher.requested[0].URL.String()
	if !strings.Contains(query, "expand=dns_names") {
		t.Fatalf("dns_names were not expanded, so the response would carry no hostnames: %q", query)
	}
	if !strings.Contains(query, "include_subdomains=true") {
		t.Fatalf("subdomains were not requested: %q", query)
	}
}

func TestCertSpotterFiltersToRequestedDomain(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"api.certspotter.com": newResponse(http.StatusOK, `[
			{"id":"1","dns_names":["example.com","*.api.example.com","notexample.com","example.com.evil.test"]}
		]`),
	}}

	request := baseRequest(fetcher, 10)
	request.Scope = []string{"example.com"}

	seeds, err := (certSpotterAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	distinct := map[string]bool{}
	for _, seed := range seeds {
		distinct[seed.Value] = true
	}
	if len(distinct) != 2 || !distinct["example.com"] || !distinct["api.example.com"] {
		t.Fatalf("unrelated hostnames leaked through: %v", distinct)
	}
}

func TestCertSpotterStopsOnEmptyPageNotShortPage(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"api.certspotter.com": newResponse(http.StatusOK, `[{"id":"7","dns_names":["one.example.com"]}]`),
	}}

	request := baseRequest(fetcher, 3)
	request.Scope = []string{"example.com"}

	if _, err := (certSpotterAdapter{}).Fetch(context.Background(), request); err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(fetcher.requested) < 2 {
		t.Fatal("paging stopped on a short page rather than an empty one")
	}
	if !strings.Contains(fetcher.requested[1].URL.String(), "after=7") {
		t.Fatalf("cursor was not carried forward: %q", fetcher.requested[1].URL.String())
	}
	if len(fetcher.requested) != 2 {
		t.Fatalf("a repeated cursor did not terminate paging: %d requests", len(fetcher.requested))
	}
}

func TestCertSpotterSendsBearerTokenOnlyWhenSet(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"api.certspotter.com": newResponse(http.StatusOK, `[]`),
	}}
	request := baseRequest(fetcher, 5)
	request.Scope = []string{"example.com"}

	if _, err := (certSpotterAdapter{}).Fetch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if got := fetcher.requested[0].Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization sent without a key: %q", got)
	}

	keyed := &routedFetcher{responses: map[string]*http.Response{
		"api.certspotter.com": newResponse(http.StatusOK, `[]`),
	}}
	request = baseRequest(keyed, 5)
	request.Scope = []string{"example.com"}
	request.Lookup = func(name string) string {
		if name == "CERTSPOTTER_API_KEY" {
			return "secret-token"
		}
		return ""
	}

	if _, err := (certSpotterAdapter{}).Fetch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if got := keyed.requested[0].Header.Get("Authorization"); got != "Bearer secret-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestCertSpotterRequiresScope(t *testing.T) {
	if _, err := (certSpotterAdapter{}).Fetch(context.Background(), baseRequest(&routedFetcher{}, 5)); err == nil {
		t.Fatal("Fetch accepted an empty scope")
	}
}

func TestCrtShSplitsNewlineSeparatedNames(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"crt.sh": newResponse(http.StatusOK, `[
			{"name_value":"*.example.com\nexample.com"},
			{"name_value":"portal.example.com"},
			{"name_value":"myexample.community"}
		]`),
	}}

	request := baseRequest(fetcher, 10)
	request.Scope = []string{"example.com"}

	seeds, err := (crtShAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(seeds) != 3 {
		t.Fatalf("newline-packed names were not split, or a substring match leaked: %+v", seeds)
	}
	for _, seed := range seeds {
		if !coversDomain(seed.Value, "example.com") {
			t.Fatalf("unrelated host %q survived the substring filter", seed.Value)
		}
	}
}

func TestCrtShHonorsLimit(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"crt.sh": newResponse(http.StatusOK, `[{"name_value":"a.example.com\nb.example.com\nc.example.com"}]`),
	}}

	request := baseRequest(fetcher, 2)
	request.Scope = []string{"example.com"}

	seeds, err := (crtShAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("limit not honored: %+v", seeds)
	}
}

func TestCrtShSurfacesOutageRatherThanEmptyResult(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusNotFound} {
		fetcher := &routedFetcher{responses: map[string]*http.Response{
			"crt.sh": newResponse(status, "unavailable"),
		}}
		request := baseRequest(fetcher, 5)
		request.Scope = []string{"example.com"}

		if _, err := (crtShAdapter{}).Fetch(context.Background(), request); err == nil {
			t.Fatalf("status %d was reported as a successful empty result", status)
		}
	}
}

func TestCoversDomainRejectsSuffixLookalikes(t *testing.T) {
	if coversDomain("notexample.com", "example.com") {
		t.Fatal("a suffix lookalike was treated as in scope")
	}
	if coversDomain("example.com.evil.test", "example.com") {
		t.Fatal("a prefixed lookalike was treated as in scope")
	}
	if !coversDomain("example.com", "example.com") || !coversDomain("api.example.com", "example.com") {
		t.Fatal("legitimate in-scope hosts were rejected")
	}
}
