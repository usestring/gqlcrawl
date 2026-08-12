package corpus

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/usestring/gqlcrawl/internal/model"
)

func TestAppleChartsResolvesSellerDomains(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"rss.marketingtools.apple.com": newResponse(http.StatusOK, `{"feed":{"results":[
			{"id":"284815942","name":"Google","artistName":"Google"},
			{"id":"6448311069","name":"ChatGPT","artistName":"OpenAI"}
		]}}`),
		"itunes.apple.com/lookup": newResponse(http.StatusOK, `{"resultCount":2,"results":[
			{"trackId":6448311069,"trackName":"ChatGPT","sellerUrl":"https://openai.com/chatgpt"},
			{"trackId":284815942,"trackName":"Google","sellerUrl":"http://www.google.com/"}
		]}`),
	}}

	seeds, err := appleChartsAdapter{}.Fetch(context.Background(), baseRequest(fetcher, 10))
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("seeds = %+v", seeds)
	}
	if seeds[0].Value != "www.google.com" || seeds[1].Value != "openai.com" {
		t.Fatalf("lookup results were matched by position rather than track id: %+v", seeds)
	}
	if seeds[0].Rank != 1 || seeds[0].RankKind != model.RankOrdinal {
		t.Fatalf("rank = %+v", seeds[0])
	}
	if !strings.Contains(seeds[0].Evidence, "Google") {
		t.Fatalf("evidence = %q", seeds[0].Evidence)
	}
}

func TestAppleChartsSkipsAppsWithoutSellerURL(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"rss.marketingtools.apple.com": newResponse(http.StatusOK, `{"feed":{"results":[
			{"id":"1","name":"Weather","artistName":"Apple"},
			{"id":"2","name":"Spotify","artistName":"Spotify"}
		]}}`),
		"itunes.apple.com/lookup": newResponse(http.StatusOK, `{"resultCount":2,"results":[
			{"trackId":1,"trackName":"Weather"},
			{"trackId":2,"trackName":"Spotify","sellerUrl":"https://www.spotify.com/"}
		]}`),
	}}

	seeds, err := appleChartsAdapter{}.Fetch(context.Background(), baseRequest(fetcher, 10))
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(seeds) != 1 || seeds[0].Value != "www.spotify.com" {
		t.Fatalf("seeds = %+v", seeds)
	}
}

func TestAppleChartsRejectsUnsupportedFeed(t *testing.T) {
	request := baseRequest(&routedFetcher{}, 10)
	request.Options = map[string]string{"feed": "top-grossing"}

	_, err := appleChartsAdapter{}.Fetch(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "top-free") {
		t.Fatalf("error = %v, want a rejection naming the supported feeds", err)
	}
}

func TestAppleChartsRejectsMalformedStorefront(t *testing.T) {
	for _, country := range []string{"usa", "u", "u1", "12"} {
		request := baseRequest(&routedFetcher{}, 10)
		request.Options = map[string]string{"country": country}

		if _, err := (appleChartsAdapter{}).Fetch(context.Background(), request); err == nil {
			t.Fatalf("storefront %q was accepted", country)
		}
	}
}

func TestAppleChartsCapsChartRequestAtHundred(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"rss.marketingtools.apple.com": newResponse(http.StatusOK, `{"feed":{"results":[{"id":"1","name":"One","artistName":"One"}]}}`),
		"itunes.apple.com/lookup":      newResponse(http.StatusOK, `{"results":[{"trackId":1,"sellerUrl":"https://one.example"}]}`),
	}}

	request := baseRequest(fetcher, 5000)
	if _, err := (appleChartsAdapter{}).Fetch(context.Background(), request); err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if !strings.Contains(fetcher.requested[0].URL.String(), "/top-free/100/apps.json") {
		t.Fatalf("chart URL exceeded the documented cap: %q", fetcher.requested[0].URL.String())
	}
}

func TestAppleChartsDeduplicatesAcrossStorefronts(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"rss.marketingtools.apple.com": newResponse(http.StatusOK, `{"feed":{"results":[{"id":"1","name":"One","artistName":"One"}]}}`),
		"itunes.apple.com/lookup":      newResponse(http.StatusOK, `{"results":[{"trackId":1,"sellerUrl":"https://one.example"}]}`),
	}}

	request := baseRequest(fetcher, 10)
	request.Options = map[string]string{"country": "us,gb,de"}

	seeds, err := appleChartsAdapter{}.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(seeds) != 1 {
		t.Fatalf("the same app was emitted once per storefront: %+v", seeds)
	}
}

func TestAppleChartsFailsWhenChartIsEmpty(t *testing.T) {
	fetcher := &routedFetcher{responses: map[string]*http.Response{
		"rss.marketingtools.apple.com": newResponse(http.StatusOK, `{"feed":{"results":[]}}`),
	}}

	if _, err := (appleChartsAdapter{}).Fetch(context.Background(), baseRequest(fetcher, 10)); err == nil {
		t.Fatal("Fetch accepted an empty chart")
	}
}
