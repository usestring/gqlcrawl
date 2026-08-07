package runner

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/usestring/gqlcrawl/internal/model"
	"github.com/usestring/gqlcrawl/internal/source"
)

const maxCandidatesPerHost = 5

type Prober interface {
	Probe(context.Context, string) model.ProbeOutcome
}

type Clock func() time.Time

type job struct {
	key string
	raw string
}

type completedJob struct {
	key     string
	outcome model.ProbeOutcome
}

func Run(ctx context.Context, candidates []source.Candidate, workers int, clock Clock, prober Prober) ([]model.Result, error) {
	if workers <= 0 || workers > 64 {
		return nil, fmt.Errorf("workers must be between 1 and 64")
	}
	if clock == nil {
		return nil, fmt.Errorf("clock is required")
	}
	if prober == nil {
		return nil, fmt.Errorf("prober is required")
	}

	jobs, outcomes := buildJobs(candidates)
	if len(jobs) > 0 {
		jobChannel := make(chan job)
		completed := make(chan completedJob, len(jobs))
		workerCount := min(workers, len(jobs))

		var group sync.WaitGroup
		group.Add(workerCount)
		for range workerCount {
			go func() {
				defer group.Done()
				for next := range jobChannel {
					completed <- completedJob{
						key:     next.key,
						outcome: prober.Probe(ctx, next.raw),
					}
				}
			}()
		}

		go func() {
			defer close(jobChannel)
			for _, next := range jobs {
				select {
				case jobChannel <- next:
				case <-ctx.Done():
					return
				}
			}
		}()

		go func() {
			group.Wait()
			close(completed)
		}()

		for result := range completed {
			outcomes[result.key] = result.outcome
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	checkedAt := clock().UTC().Format(time.RFC3339)
	results := make([]model.Result, 0, len(candidates))
	for _, candidate := range candidates {
		outcome := outcomes[candidateKey(candidate.Raw)]
		results = append(results, model.Result{
			SchemaVersion: "1",
			Endpoint:      source.SanitizeURL(candidate.Raw),
			Source:        candidate.Source,
			CheckedAt:     checkedAt,
			HTTP:          outcome.HTTP,
			GraphQL:       outcome.GraphQL,
			Introspection: outcome.Introspection,
			QueryType:     outcome.QueryType,
			Reason:        outcome.Reason,
		})
	}
	return results, nil
}

func buildJobs(candidates []source.Candidate) ([]job, map[string]model.ProbeOutcome) {
	seen := make(map[string]struct{}, len(candidates))
	hostCounts := make(map[string]int)
	jobs := make([]job, 0, len(candidates))
	outcomes := make(map[string]model.ProbeOutcome, len(candidates))

	for _, candidate := range candidates {
		key := candidateKey(candidate.Raw)
		if _, found := seen[key]; found {
			continue
		}
		seen[key] = struct{}{}

		host := candidateHost(key)
		if host != "" && hostCounts[host] >= maxCandidatesPerHost {
			outcomes[key] = model.ProbeOutcome{
				Endpoint:      source.SanitizeURL(candidate.Raw),
				GraphQL:       model.GraphQLIndeterminate,
				Introspection: model.IntrospectionIndeterminate,
				Reason:        model.ReasonPolicyRejected,
			}
			continue
		}
		if host != "" {
			hostCounts[host]++
		}
		jobs = append(jobs, job{key: key, raw: candidate.Raw})
	}
	return jobs, outcomes
}

func candidateKey(raw string) string {
	normalized, err := source.NormalizeURL(raw)
	if err != nil {
		return "invalid:" + raw
	}
	return normalized
}

func candidateHost(key string) string {
	parsed, err := url.Parse(key)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}
