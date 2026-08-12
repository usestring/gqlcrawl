package corpus

import (
	"context"
	"strings"
	"testing"

	"github.com/usestring/gqlcrawl/internal/model"
)

func exportRequest(limit int, lines ...string) Request {
	request := baseRequest(&routedFetcher{}, limit)
	request.Scope = lines
	return request
}

func TestHTTPArchiveReadsRankAsABucket(t *testing.T) {
	request := exportRequest(10,
		"page,rank",
		"https://autoparts.example.com/,100000",
		"https://shop.example.net/,1000000",
	)

	seeds, err := (httpArchiveAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("want 2 seeds, got %d", len(seeds))
	}
	if seeds[0].RankKind != model.RankBucket || seeds[0].Rank != 100000 {
		t.Fatalf("http archive carries the CrUX bucket, got %+v", seeds[0])
	}
	if seeds[0].Kind != model.SeedHost {
		t.Fatalf("want host seeds by default, got %q", seeds[0].Kind)
	}
	collected := Collect(seeds, 10)
	if collected[0].Value != "autoparts.example.com" {
		t.Fatalf("unexpected normalized value %q", collected[0].Value)
	}
}

func TestHTTPArchiveFindsTheValueColumnByName(t *testing.T) {
	request := exportRequest(10,
		"rank,client,root_page",
		"5000,mobile,https://example.com/",
	)

	seeds, err := (httpArchiveAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 1 || seeds[0].Value != "https://example.com/" {
		t.Fatalf("unexpected seeds %+v", seeds)
	}
	if seeds[0].Rank != 5000 {
		t.Fatalf("unexpected rank %d", seeds[0].Rank)
	}
}

func TestHTTPArchiveReadsAHeaderlessExport(t *testing.T) {
	request := exportRequest(10, "https://example.com/", "https://shop.example.net/")

	seeds, err := (httpArchiveAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("want both rows kept, got %d", len(seeds))
	}
	if seeds[0].RankKind != model.RankNone {
		t.Fatalf("an unlabelled second column must not become a rank, got %q", seeds[0].RankKind)
	}
}

func TestHTTPArchiveSkipsAnEchoedStatement(t *testing.T) {
	request := exportRequest(10,
		"SELECT DISTINCT root_page AS page, rank",
		"FROM `httparchive.crawl.pages`",
		"WHERE date = latest_crawl",
		"page,rank",
		"https://example.com/,1000",
	)

	seeds, err := (httpArchiveAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 1 || seeds[0].Value != "https://example.com/" {
		t.Fatalf("unexpected seeds %+v", seeds)
	}
	if seeds[0].Rank != 1000 {
		t.Fatalf("unexpected rank %d", seeds[0].Rank)
	}
}

func TestHTTPArchiveKeepsURLsOnRequest(t *testing.T) {
	request := exportRequest(10, "page", "https://example.com/docs/api")
	request.Options = map[string]string{"kind": "url", "label": "2026-07-01"}

	seeds, err := (httpArchiveAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if seeds[0].Kind != model.SeedURL || seeds[0].Value != "https://example.com/docs/api" {
		t.Fatalf("unexpected seed %+v", seeds[0])
	}
	if seeds[0].Evidence != "2026-07-01" {
		t.Fatalf("unexpected evidence %q", seeds[0].Evidence)
	}
}

func TestHTTPArchiveRejectsAnUnknownKind(t *testing.T) {
	request := exportRequest(10, "page", "https://example.com/")
	request.Options = map[string]string{"kind": "everything"}

	if _, err := (httpArchiveAdapter{}).Fetch(context.Background(), request); err == nil {
		t.Fatal("want an error for an unsupported kind")
	}
}

func TestHTTPArchiveExplainsAMissingExport(t *testing.T) {
	_, err := (httpArchiveAdapter{}).Fetch(context.Background(), exportRequest(10))
	if err == nil || !strings.Contains(err.Error(), "--input") {
		t.Fatalf("want the error to name the input flag, got %v", err)
	}
}

func TestHTTPArchiveStopsAtTheLimit(t *testing.T) {
	request := exportRequest(2, "page", "https://a.example.com/", "https://b.example.com/", "https://c.example.com/")

	seeds, err := (httpArchiveAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("want the limit honoured, got %d", len(seeds))
	}
}

func TestHTTPArchiveHandlesQuotedFields(t *testing.T) {
	request := exportRequest(10, `page,title`, `"https://example.com/","A site, with a comma"`)

	seeds, err := (httpArchiveAdapter{}).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(seeds) != 1 || seeds[0].Value != "https://example.com/" {
		t.Fatalf("unexpected seeds %+v", seeds)
	}
}
