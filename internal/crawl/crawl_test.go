package crawl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/usestring/gqlcrawl/internal/model"
	"github.com/usestring/gqlcrawl/internal/source"
)

type fixtureResponse struct {
	status      int
	contentType string
	body        string
}

type fixtureDoer struct {
	responses map[string]fixtureResponse
	calls     []string
}

func (d *fixtureDoer) Do(request *http.Request) (*http.Response, error) {
	d.calls = append(d.calls, request.URL.String())
	fixture, found := d.responses[request.URL.String()]
	if !found {
		return nil, fmt.Errorf("unexpected request %s", request.URL)
	}
	status := fixture.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type": []string{fixture.contentType},
		},
		Body:    io.NopCloser(strings.NewReader(fixture.body)),
		Request: request,
	}, nil
}

func TestDiscoverFindsLiteralEvidenceWithinBudgets(t *testing.T) {
	padding := strings.Repeat("x", markerWindowBytes+20)
	doer := &fixtureDoer{responses: map[string]fixtureResponse{
		"https://seed.test/robots.txt": {
			contentType: "text/plain",
			body:        "User-agent: *\nAllow: /\n",
		},
		"https://seed.test/": {
			contentType: "text/html; charset=utf-8",
			body: `<script src="https://cdn.test/app.js"></script>` + padding +
				`<script>const client = new ApolloClient({uri: "/api/graphql?token=secret"})</script>` + padding +
				`<a href="/next">next</a><a href="https://outside.test/page">outside</a>`,
		},
		"https://seed.test/next": {
			contentType: "text/html",
			body:        `<a href="/v1/graphql">GraphQL</a>`,
		},
		"https://cdn.test/robots.txt": {
			contentType: "text/plain",
			body:        "User-agent: *\nAllow: /\n",
		},
		"https://cdn.test/app.js": {
			contentType: "application/javascript",
			body:        `const client = new GraphQLClient("https://mobile.test/custom?api_key=secret"); const ignored = "/graphql";`,
		},
		"https://mobile.test/robots.txt": {
			contentType: "text/plain",
			body:        "User-agent: *\nAllow: /\n",
		},
	}}
	crawler := newFixtureCrawler(t, doer, 25, 2, true)

	candidates, err := crawler.Discover(context.Background(), []source.Candidate{{
		Raw: "seed.test",
		Source: model.Source{
			Kind:  "argument",
			Input: "<invalid-url>",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	expected := []string{
		"https://seed.test/api/graphql",
		"https://mobile.test/custom",
		"https://seed.test/graphql",
		"https://seed.test/v1/graphql",
	}
	if len(candidates) != len(expected) {
		t.Fatalf("candidates = %#v", candidates)
	}
	for index, candidate := range candidates {
		if candidate.Raw != expected[index] {
			t.Fatalf("candidate %d = %q, want %q", index, candidate.Raw, expected[index])
		}
		if candidate.Source.Kind != "crawl" || candidate.Source.Input != "https://seed.test/" {
			t.Fatalf("candidate source = %#v", candidate.Source)
		}
		if strings.Contains(candidate.Raw, "secret") || strings.Contains(candidate.Source.EvidenceURL, "secret") {
			t.Fatalf("candidate leaked query material: %#v", candidate)
		}
	}

	for _, forbidden := range []string{"https://outside.test/page", "https://cdn.test/graphql"} {
		for _, call := range doer.calls {
			if call == forbidden {
				t.Fatalf("unexpected cross-origin request %q; calls = %v", forbidden, doer.calls)
			}
		}
	}
}

func TestDiscoverReusesSharedScriptAcrossDocumentOrigins(t *testing.T) {
	doer := &fixtureDoer{responses: map[string]fixtureResponse{
		"https://one.test/": {
			contentType: "text/html",
			body:        `<script src="https://cdn.test/shared.js"></script>`,
		},
		"https://two.test/": {
			contentType: "text/html",
			body:        `<script src="https://cdn.test/shared.js"></script>`,
		},
		"https://cdn.test/shared.js": {
			contentType: "application/javascript",
			body:        `const endpoint = "/graphql";`,
		},
	}}
	crawler := newFixtureCrawler(t, doer, 25, 2, false)
	candidates, err := crawler.Discover(context.Background(), []source.Candidate{
		{Raw: "one.test", Source: model.Source{Kind: "argument"}},
		{Raw: "two.test", Source: model.Source{Kind: "argument"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"https://one.test/graphql", "https://two.test/graphql"}
	if len(candidates) != len(expected) {
		t.Fatalf("candidates = %#v", candidates)
	}
	for index, candidate := range candidates {
		if candidate.Raw != expected[index] {
			t.Fatalf("candidate %d = %q, want %q", index, candidate.Raw, expected[index])
		}
	}
	scriptFetches := 0
	for _, call := range doer.calls {
		if call == "https://cdn.test/shared.js" {
			scriptFetches++
		}
	}
	if scriptFetches != 1 {
		t.Fatalf("shared script fetches = %d, want 1; calls = %v", scriptFetches, doer.calls)
	}
}

func TestDiscoverTreatsSeedResponseSignatureAsCandidate(t *testing.T) {
	doer := &fixtureDoer{responses: map[string]fixtureResponse{
		"https://seed.test/custom?token=secret": {
			contentType: "text/plain",
			body:        "Must provide query string.",
		},
	}}
	crawler := newFixtureCrawler(t, doer, 25, 2, false)

	candidates, err := crawler.Discover(context.Background(), []source.Candidate{{
		Raw: "https://seed.test/custom?token=secret",
		Source: model.Source{
			Kind:  "argument",
			Input: "https://seed.test/custom?token=REDACTED",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Raw != "https://seed.test/custom" {
		t.Fatalf("candidates = %#v", candidates)
	}
	if strings.Contains(candidates[0].Source.Input, "secret") ||
		strings.Contains(candidates[0].Source.EvidenceURL, "secret") {
		t.Fatalf("source leaked query material: %#v", candidates[0].Source)
	}
}

type failingDoer struct {
	errorURL string
}

func (d failingDoer) Do(request *http.Request) (*http.Response, error) {
	errorURL := d.errorURL
	if errorURL == "" {
		errorURL = request.URL.String()
	}
	return nil, &url.Error{
		Op:  request.Method,
		URL: errorURL,
		Err: fmt.Errorf("fixture failure"),
	}
}

func TestDiscoverRedactsURLValuesFromErrors(t *testing.T) {
	crawler := newFixtureCrawler(t, failingDoer{}, 25, 2, false)
	_, err := crawler.Discover(context.Background(), []source.Candidate{{
		Raw: "https://seed.test/start?token=secret",
		Source: model.Source{
			Kind:  "argument",
			Input: "https://seed.test/start?token=REDACTED",
		},
	}})
	if err == nil {
		t.Fatal("expected discovery error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaked query material: %v", err)
	}
	if !strings.Contains(err.Error(), "fixture failure") {
		t.Fatalf("error lost safe failure context: %v", err)
	}
}

func TestDiscoverRedactsRobotsRedirectURLValuesFromErrors(t *testing.T) {
	crawler := newFixtureCrawler(t, failingDoer{
		errorURL: "https://seed.test/robots.txt?token=secret",
	}, 25, 2, true)
	_, err := crawler.Discover(context.Background(), []source.Candidate{{
		Raw: "https://seed.test/",
		Source: model.Source{
			Kind:  "argument",
			Input: "https://seed.test/",
		},
	}})
	if err == nil {
		t.Fatal("expected robots discovery error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("robots error leaked redirect query material: %v", err)
	}
}
func TestDiscoverReportsUnavailableRobotsAsIncomplete(t *testing.T) {
	doer := &fixtureDoer{responses: map[string]fixtureResponse{
		"https://seed.test/robots.txt": {status: http.StatusServiceUnavailable},
	}}
	crawler := newFixtureCrawler(t, doer, 25, 2, true)
	_, err := crawler.Discover(context.Background(), []source.Candidate{{
		Raw:    "https://seed.test/",
		Source: model.Source{Kind: "argument", Input: "https://seed.test/"},
	}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("error = %v", err)
	}
}

func TestDiscoverMarksRobotsDisallowedCandidateWithoutFetchingIt(t *testing.T) {
	doer := &fixtureDoer{responses: map[string]fixtureResponse{
		"https://seed.test/robots.txt": {
			contentType: "text/plain",
			body:        "User-agent: gqlcrawl\nAllow: /\nDisallow: /api/graphql\n",
		},
		"https://seed.test/": {
			contentType: "text/html",
			body:        `<script>const client = new ApolloClient({uri: "/api/graphql"})</script>`,
		},
	}}
	crawler := newFixtureCrawler(t, doer, 25, 2, true)

	candidates, err := crawler.Discover(context.Background(), []source.Candidate{{
		Raw:    "https://seed.test/",
		Source: model.Source{Kind: "argument", Input: "https://seed.test/"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].SkipReason != model.ReasonRobotsDisallowed {
		t.Fatalf("candidates = %#v", candidates)
	}
	for _, call := range doer.calls {
		if call == "https://seed.test/api/graphql" {
			t.Fatalf("robots-disallowed endpoint was fetched: %v", doer.calls)
		}
	}
}

func TestDiscoverCapsPagesPerHost(t *testing.T) {
	doer := &fixtureDoer{responses: map[string]fixtureResponse{
		"https://seed.test/": {
			contentType: "text/html",
			body:        `<a href="/a">a</a><a href="/b">b</a>`,
		},
		"https://seed.test/a": {contentType: "text/html"},
		"https://seed.test/b": {contentType: "text/html"},
	}}
	crawler := newFixtureCrawler(t, doer, 2, 2, false)

	_, err := crawler.Discover(context.Background(), []source.Candidate{{
		Raw:    "https://seed.test/",
		Source: model.Source{Kind: "argument", Input: "https://seed.test/"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(doer.calls, ",") != "https://seed.test/,https://seed.test/a" {
		t.Fatalf("calls = %v", doer.calls)
	}
}

func TestDiscoverCapsCandidatesPerHost(t *testing.T) {
	var references strings.Builder
	for index := range 6 {
		fmt.Fprintf(&references, `<a href="/graphql/%d">candidate</a>`, index)
	}
	doer := &fixtureDoer{responses: map[string]fixtureResponse{
		"https://seed.test/": {
			contentType: "text/html",
			body:        references.String(),
		},
	}}
	crawler := newFixtureCrawler(t, doer, 25, 2, false)

	candidates, err := crawler.Discover(context.Background(), []source.Candidate{{
		Raw:    "https://seed.test/",
		Source: model.Source{Kind: "argument", Input: "https://seed.test/"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != maxCandidatesPerHost {
		t.Fatalf("candidates = %d, want %d", len(candidates), maxCandidatesPerHost)
	}
	if len(doer.calls) != 1 || doer.calls[0] != "https://seed.test/" {
		t.Fatalf("endpoint candidates were fetched as pages: %v", doer.calls)
	}
}

func TestNormalizeSeedDefaultsDomainsToHTTPS(t *testing.T) {
	normalized, err := NormalizeSeed("Example.COM.")
	if err != nil {
		t.Fatal(err)
	}
	if normalized != "https://example.com/" {
		t.Fatalf("normalized seed = %q", normalized)
	}
	if _, err := NormalizeSeed("example.com/path"); err == nil {
		t.Fatal("expected schemeless path rejection")
	}
	if _, err := NormalizeSeed("https://user:secret@example.com/"); err == nil {
		t.Fatal("expected URL userinfo rejection")
	}
}

func newFixtureCrawler(t *testing.T, client HTTPDoer, maxPages int, maxDepth int, respectRobots bool) *Crawler {
	t.Helper()
	crawler, err := New(Config{
		Client:           client,
		MaxResponseBytes: 64 * 1024,
		MaxPagesPerHost:  maxPages,
		MaxDepth:         maxDepth,
		RespectRobots:    respectRobots,
		UserAgent:        "gqlcrawl/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return crawler
}
