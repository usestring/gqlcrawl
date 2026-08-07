package source

import (
	"strings"
	"testing"
)

func TestLoadSanitizesInputs(t *testing.T) {
	candidates, err := Load(
		[]string{"https://user:secret@example.com/graphql?token=secret"},
		"-",
		strings.NewReader("# comment\nhttps://example.org/graphql?key=value\n"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %d", len(candidates))
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate.Source.Input, "secret") ||
			strings.Contains(candidate.Source.Input, "value") ||
			strings.Contains(candidate.Source.Input, "user") {
			t.Fatalf("source input leaked sensitive URL material: %q", candidate.Source.Input)
		}
	}
	if candidates[0].Source.Kind != "argument" || candidates[1].Source.Kind != "stdin" {
		t.Fatalf("source kinds = %q, %q", candidates[0].Source.Kind, candidates[1].Source.Kind)
	}
}

func TestNormalizeURLCanonicalizesForDeduplication(t *testing.T) {
	first, err := NormalizeURL("HTTPS://Example.COM.:443/graphql?b=2&a=1#fragment")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NormalizeURL("https://example.com/graphql?a=1&b=2")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("normalized URLs differ: %q != %q", first, second)
	}
}

func TestLoadRejectsUnreadableInput(t *testing.T) {
	_, err := Load(nil, "/path/that/does/not/exist", strings.NewReader(""))
	if err == nil {
		t.Fatal("expected input error")
	}
}
