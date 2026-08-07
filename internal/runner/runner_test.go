package runner

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/usestring/gqlcrawl/internal/model"
	"github.com/usestring/gqlcrawl/internal/source"
)

type recordingProber struct {
	mu    sync.Mutex
	calls []string
}

func (p *recordingProber) Probe(_ context.Context, raw string) model.ProbeOutcome {
	if raw == "https://slow.test/graphql" {
		time.Sleep(20 * time.Millisecond)
	}
	p.mu.Lock()
	p.calls = append(p.calls, raw)
	p.mu.Unlock()
	return model.ProbeOutcome{
		Endpoint:      source.SanitizeURL(raw),
		GraphQL:       model.GraphQLConfirmed,
		Introspection: model.IntrospectionEnabled,
		QueryType:     "Query",
		Reason:        model.ReasonIntrospectionEnabled,
	}
}

func TestRunDeduplicatesProbesAndPreservesInputOrder(t *testing.T) {
	candidates := []source.Candidate{
		{Raw: "https://slow.test/graphql", Source: model.Source{Kind: "argument", Input: "first"}},
		{Raw: "https://FAST.test:443/graphql", Source: model.Source{Kind: "argument", Input: "second"}},
		{Raw: "https://fast.test/graphql", Source: model.Source{Kind: "stdin", Input: "third"}},
	}
	prober := &recordingProber{}
	checkedAt := time.Date(2026, 8, 7, 7, 0, 0, 0, time.UTC)

	results, err := Run(context.Background(), candidates, 3, func() time.Time {
		return checkedAt
	}, prober)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 3 {
		t.Fatalf("results = %d", len(results))
	}
	if results[0].Source.Input != "first" ||
		results[1].Source.Input != "second" ||
		results[2].Source.Input != "third" {
		t.Fatalf("result order changed: %#v", results)
	}
	for _, result := range results {
		if result.CheckedAt != "2026-08-07T07:00:00Z" {
			t.Fatalf("checked_at = %q", result.CheckedAt)
		}
	}

	prober.mu.Lock()
	defer prober.mu.Unlock()
	if len(prober.calls) != 2 {
		t.Fatalf("probe calls = %d, want 2: %v", len(prober.calls), prober.calls)
	}
}

func TestRunCapsUniqueCandidatesPerHost(t *testing.T) {
	var candidates []source.Candidate
	for index := range 6 {
		raw := fmt.Sprintf("https://example.com/graphql/%d", index)
		candidates = append(candidates, source.Candidate{
			Raw:    raw,
			Source: model.Source{Kind: "argument", Input: raw},
		})
	}
	prober := &recordingProber{}

	results, err := Run(context.Background(), candidates, 6, time.Now, prober)
	if err != nil {
		t.Fatal(err)
	}
	prober.mu.Lock()
	callCount := len(prober.calls)
	prober.mu.Unlock()
	if callCount != 5 {
		t.Fatalf("probe calls = %d, want 5", callCount)
	}
	if results[5].Reason != model.ReasonPolicyRejected ||
		results[5].Introspection != model.IntrospectionIndeterminate {
		t.Fatalf("sixth result = %#v", results[5])
	}
}

func TestRunHonorsPrecomputedSkipReason(t *testing.T) {
	prober := &recordingProber{}
	results, err := Run(context.Background(), []source.Candidate{{
		Raw:        "https://example.test/graphql",
		Source:     model.Source{Kind: "crawl", Input: "https://example.test/"},
		SkipReason: model.ReasonRobotsDisallowed,
	}}, 1, time.Now, prober)
	if err != nil {
		t.Fatal(err)
	}

	prober.mu.Lock()
	callCount := len(prober.calls)
	prober.mu.Unlock()
	if callCount != 0 {
		t.Fatalf("probe calls = %d, want 0", callCount)
	}
	if len(results) != 1 ||
		results[0].Reason != model.ReasonRobotsDisallowed ||
		results[0].GraphQL != model.GraphQLIndeterminate ||
		results[0].Introspection != model.IntrospectionIndeterminate {
		t.Fatalf("result = %#v", results)
	}
}

func TestRunKeepsSkipReasonsCandidateSpecific(t *testing.T) {
	endpoint := "https://example.test/graphql"
	tests := []struct {
		name       string
		candidates []source.Candidate
	}{
		{
			name: "permitted first",
			candidates: []source.Candidate{
				{Raw: endpoint},
				{Raw: endpoint, SkipReason: model.ReasonRobotsDisallowed},
			},
		},
		{
			name: "skipped first",
			candidates: []source.Candidate{
				{Raw: endpoint, SkipReason: model.ReasonRobotsDisallowed},
				{Raw: endpoint},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prober := &recordingProber{}
			results, err := Run(context.Background(), test.candidates, 1, time.Now, prober)
			if err != nil {
				t.Fatal(err)
			}
			prober.mu.Lock()
			callCount := len(prober.calls)
			prober.mu.Unlock()
			if callCount != 1 {
				t.Fatalf("probe calls = %d, want 1", callCount)
			}
			if len(results) != 2 {
				t.Fatalf("results = %d, want 2", len(results))
			}
			for index, candidate := range test.candidates {
				expected := model.ReasonIntrospectionEnabled
				if candidate.SkipReason != "" {
					expected = candidate.SkipReason
				}
				if results[index].Reason != expected {
					t.Fatalf("result %d reason = %q, want %q", index, results[index].Reason, expected)
				}
			}
		})
	}
}
func TestRunRejectsInvalidConfiguration(t *testing.T) {
	_, err := Run(context.Background(), nil, 0, time.Now, &recordingProber{})
	if err == nil {
		t.Fatal("expected worker validation error")
	}
}
