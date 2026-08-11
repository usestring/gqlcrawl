package corpus

import (
	"strings"
	"testing"

	"github.com/usestring/gqlcrawl/internal/model"
)

func TestNormalizeHostAcceptsCommonForms(t *testing.T) {
	cases := map[string]string{
		"Example.COM":              "example.com",
		"example.com.":             "example.com",
		"*.example.com":            "example.com",
		"https://www.example.com/": "www.example.com",
		"  api.example.com  ":      "api.example.com",
	}

	for input, want := range cases {
		got, err := NormalizeHost(input)
		if err != nil {
			t.Fatalf("NormalizeHost(%q) returned error: %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeHost(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeHostRejectsUnusableValues(t *testing.T) {
	for _, input := range []string{"", "   ", "localhost", "has space.com", "example.com/path", "user@example.com"} {
		if got, err := NormalizeHost(input); err == nil {
			t.Fatalf("NormalizeHost(%q) = %q, want an error", input, got)
		}
	}
}

func TestNormalizeRejectsUnknownKind(t *testing.T) {
	if _, err := Normalize(model.Seed{Value: "example.com", Kind: model.SeedKind("other")}); err == nil {
		t.Fatal("Normalize accepted an unsupported seed kind")
	}
}

func TestNormalizeStampsSchemaVersionAndRedactsQuery(t *testing.T) {
	seed, err := Normalize(model.Seed{
		Value: "https://example.com/graphql?token=secret",
		Kind:  model.SeedURL,
	})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if seed.SchemaVersion != "1" {
		t.Fatalf("SchemaVersion = %q, want \"1\"", seed.SchemaVersion)
	}
	if strings.Contains(seed.Value, "secret") {
		t.Fatalf("normalized URL leaked a query value: %q", seed.Value)
	}
}

func TestNormalizeSeparatesRankSemantics(t *testing.T) {
	ordinal, err := Normalize(model.Seed{Value: "one.example", Kind: model.SeedHost, Rank: 7, RankKind: model.RankOrdinal})
	if err != nil {
		t.Fatalf("ordinal rank rejected: %v", err)
	}
	if ordinal.Rank != 7 || ordinal.RankKind != model.RankOrdinal {
		t.Fatalf("ordinal seed = %+v", ordinal)
	}

	bucket, err := Normalize(model.Seed{Value: "two.example", Kind: model.SeedHost, Rank: 1000, RankKind: model.RankBucket})
	if err != nil {
		t.Fatalf("bucket rank rejected: %v", err)
	}
	if bucket.Rank != 1000 || bucket.RankKind != model.RankBucket {
		t.Fatalf("bucket seed = %+v", bucket)
	}

	member, err := Normalize(model.Seed{Value: "three.example", Kind: model.SeedHost, Rank: 42, RankKind: model.RankMember})
	if err != nil {
		t.Fatalf("member rank rejected: %v", err)
	}
	if member.Rank != 0 {
		t.Fatalf("set-membership seed kept a rank: %+v", member)
	}

	unranked, err := Normalize(model.Seed{Value: "four.example", Kind: model.SeedHost, Rank: 9})
	if err != nil {
		t.Fatalf("unranked seed rejected: %v", err)
	}
	if unranked.Rank != 0 {
		t.Fatalf("unranked seed kept a rank: %+v", unranked)
	}
}

func TestNormalizeRejectsRankedSeedWithoutValue(t *testing.T) {
	for _, kind := range []model.RankKind{model.RankOrdinal, model.RankBucket} {
		if _, err := Normalize(model.Seed{Value: "example.com", Kind: model.SeedHost, RankKind: kind}); err == nil {
			t.Fatalf("%s rank accepted a zero value", kind)
		}
	}
	if _, err := Normalize(model.Seed{Value: "example.com", Kind: model.SeedHost, RankKind: model.RankKind("made-up")}); err == nil {
		t.Fatal("Normalize accepted an unsupported rank kind")
	}
}

func TestCollectDeduplicatesAndCaps(t *testing.T) {
	seeds := []model.Seed{
		{Value: "Example.com", Kind: model.SeedHost, Adapter: "test"},
		{Value: "example.com.", Kind: model.SeedHost, Adapter: "test"},
		{Value: "second.example", Kind: model.SeedHost, Adapter: "test"},
		{Value: "not a host", Kind: model.SeedHost, Adapter: "test"},
		{Value: "third.example", Kind: model.SeedHost, Adapter: "test"},
	}

	collected := Collect(seeds, 2)
	if len(collected) != 2 {
		t.Fatalf("Collect returned %d seeds, want 2", len(collected))
	}
	if collected[0].Value != "example.com" || collected[1].Value != "second.example" {
		t.Fatalf("unexpected collected seeds: %+v", collected)
	}
}

func TestCollectDropsInvalidSeedsWithoutFailing(t *testing.T) {
	collected := Collect([]model.Seed{
		{Value: "bad host", Kind: model.SeedHost},
		{Value: "good.example", Kind: model.SeedHost},
	}, DefaultLimit)

	if len(collected) != 1 || collected[0].Value != "good.example" {
		t.Fatalf("unexpected collected seeds: %+v", collected)
	}
}

func TestLookupReportsAvailableSources(t *testing.T) {
	if _, err := Lookup("does-not-exist"); err == nil {
		t.Fatal("Lookup accepted an unknown source")
	}
	if _, err := Lookup(""); err == nil {
		t.Fatal("Lookup accepted an empty source")
	}
}

func TestRequestCredentialRequiresValue(t *testing.T) {
	request := Request{Lookup: func(string) string { return "" }}
	if _, err := request.Credential("EXAMPLE_KEY"); err == nil {
		t.Fatal("Credential accepted an unset variable")
	}

	request = Request{Lookup: func(name string) string {
		if name == "EXAMPLE_KEY" {
			return " value "
		}
		return ""
	}}
	value, err := request.Credential("EXAMPLE_KEY")
	if err != nil {
		t.Fatalf("Credential returned error: %v", err)
	}
	if value != "value" {
		t.Fatalf("Credential = %q, want \"value\"", value)
	}
}

func TestOptionFallsBackWhenUnsetOrBlank(t *testing.T) {
	request := Request{Options: map[string]string{"tech": " Apollo-GraphQL ", "blank": "   "}}

	if got := request.Option("tech", "GraphQL"); got != "Apollo-GraphQL" {
		t.Fatalf("Option(tech) = %q", got)
	}
	if got := request.Option("blank", "GraphQL"); got != "GraphQL" {
		t.Fatalf("Option(blank) = %q, want the fallback", got)
	}
	if got := request.Option("missing", "GraphQL"); got != "GraphQL" {
		t.Fatalf("Option(missing) = %q, want the fallback", got)
	}
	if got := (Request{}).Option("tech", "GraphQL"); got != "GraphQL" {
		t.Fatalf("Option on a nil map = %q, want the fallback", got)
	}
}

func TestCredentialWithoutLookupFails(t *testing.T) {
	if _, err := (Request{}).Credential("EXAMPLE_KEY"); err == nil {
		t.Fatal("Credential succeeded without a lookup function")
	}
}
