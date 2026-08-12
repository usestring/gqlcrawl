package corpus

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/usestring/gqlcrawl/internal/model"
)

const (
	cruxBaseURL        = "https://raw.githubusercontent.com/zakird/crux-top-lists/main/data/"
	radarBaseURL       = "https://api.cloudflare.com/client/v4/radar/"
	radarTokenVariable = "CLOUDFLARE_API_TOKEN"
	radarDefaultLimit  = 100
)

// Radar publishes bucket datasets only at these sizes, and asking for any other
// alias answers 404 rather than the nearest bucket.
var radarBuckets = []int{200, 500, 1000, 2000, 5000, 10000, 20000, 50000, 100000, 200000, 500000, 1000000}

func getBearer(ctx context.Context, request Request, target string, accept string, token string) ([]byte, error) {
	if request.Fetcher == nil {
		return nil, fmt.Errorf("no fetcher configured")
	}
	limit := request.MaxDownloadBytes
	if limit <= 0 {
		limit = DefaultMaxDownloadBytes
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpRequest.Header.Set("User-Agent", request.UserAgent)
	if accept != "" {
		httpRequest.Header.Set("Accept", accept)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+token)

	response, err := request.Fetcher.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", response.StatusCode)
	}
	return readBounded(response.Body, limit)
}

type cruxAdapter struct{}

func (cruxAdapter) Name() string { return "crux" }

func (cruxAdapter) Summary() string {
	return "Chrome User Experience Report origins by popularity bucket (bucket, no credentials)"
}

func (cruxAdapter) ScopeUsage() string { return "" }

func (cruxAdapter) Requirement() Requirement {
	return Requirement{
		Notes: "Read from the zakird/crux-top-lists mirror, which publishes no license file; the underlying Google data is CC BY-SA 4.0, so share-alike follows any redistribution. Ranks are magnitude buckets, not positions.",
	}
}

func (a cruxAdapter) endpoint(request Request) (string, string, error) {
	month := request.Option("month", "")
	country := strings.ToLower(request.Option("country", ""))

	if country == "" {
		if month == "" {
			return cruxBaseURL + "global/current.csv.gz", "global current", nil
		}
		return cruxBaseURL + "global/" + month + ".csv.gz", "global " + month, nil
	}
	// The mirror publishes a rolling current.csv.gz for the global list only.
	if month == "" {
		return "", "", fmt.Errorf("crux country lists are published per month; set --option month=YYYYMM")
	}
	return cruxBaseURL + "country/" + country + "/" + month + ".csv.gz", country + " " + month, nil
}

func (a cruxAdapter) Fetch(ctx context.Context, request Request) ([]model.Seed, error) {
	endpoint, evidence, err := a.endpoint(request)
	if err != nil {
		return nil, err
	}

	payload, err := Get(ctx, request, endpoint, "application/gzip")
	if err != nil {
		return nil, err
	}
	rows, err := readGzipCSV(payload, request.Limit)
	if err != nil {
		return nil, err
	}

	seeds := make([]model.Seed, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		bucket, err := strconv.Atoi(strings.TrimSpace(row[1]))
		if err != nil || bucket <= 0 {
			continue
		}
		seeds = append(seeds, model.Seed{
			Value:    row[0],
			Kind:     model.SeedHost,
			Adapter:  a.Name(),
			Rank:     bucket,
			RankKind: model.RankBucket,
			Evidence: evidence,
		})
	}
	return seeds, nil
}

func readGzipCSV(payload []byte, limit int) ([][]string, error) {
	decompressor, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer decompressor.Close()

	reader := csv.NewReader(decompressor)
	reader.FieldsPerRecord = -1

	rows := make([][]string, 0, limit)
	for len(rows) < limit {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			if len(rows) > 0 {
				break
			}
			return nil, fmt.Errorf("parse CSV: %w", err)
		}
		if len(record) == 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(record[0]), "origin") {
			continue
		}
		rows = append(rows, append([]string(nil), record...))
	}
	return rows, nil
}

type radarAdapter struct{}

func (radarAdapter) Name() string { return "radar" }

func (radarAdapter) Summary() string {
	return "Cloudflare Radar most popular domains (ordinal, or set membership for a bucket)"
}

func (radarAdapter) ScopeUsage() string { return "" }

func (radarAdapter) Requirement() Requirement {
	return Requirement{
		EnvVars: []string{radarTokenVariable},
		Notes:   "A free Cloudflare account is enough; the token needs Account - Radar - Read. The data is CC BY-NC 4.0, so commercial use of the results is restricted regardless of this tool's license.",
	}
}

func (a radarAdapter) Fetch(ctx context.Context, request Request) ([]model.Seed, error) {
	token, err := request.Credential(radarTokenVariable)
	if err != nil {
		return nil, err
	}
	if bucket := request.Option("bucket", ""); bucket != "" {
		return a.fetchBucket(ctx, request, token, bucket)
	}
	return a.fetchRanking(ctx, request, token)
}

func (a radarAdapter) fetchRanking(ctx context.Context, request Request, token string) ([]model.Seed, error) {
	limit := request.Limit
	if limit > radarDefaultLimit {
		limit = radarDefaultLimit
	}
	endpoint := fmt.Sprintf("%sranking/top?format=JSON&limit=%d", radarBaseURL, limit)
	if location := request.Option("location", ""); location != "" {
		endpoint += "&location=" + location
	}
	if date := request.Option("date", ""); date != "" {
		endpoint += "&date=" + date
	}

	body, err := getBearer(ctx, request, endpoint, "application/json", token)
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode ranking: %w", err)
	}
	if !envelope.Success {
		if len(envelope.Errors) > 0 {
			return nil, fmt.Errorf("radar rejected the request: %s", envelope.Errors[0].Message)
		}
		return nil, fmt.Errorf("radar rejected the request")
	}

	entries, err := radarSeries(envelope.Result)
	if err != nil {
		return nil, err
	}

	seeds := make([]model.Seed, 0, len(entries))
	for _, entry := range entries {
		if entry.Rank <= 0 {
			continue
		}
		seeds = append(seeds, model.Seed{
			Value:    entry.Domain,
			Kind:     model.SeedHost,
			Adapter:  a.Name(),
			Rank:     entry.Rank,
			RankKind: model.RankOrdinal,
			Evidence: "radar ranking top",
		})
	}
	return seeds, nil
}

type radarEntry struct {
	Domain string `json:"domain"`
	Rank   int    `json:"rank"`
}

// The ranking series is keyed by the requested name rather than a fixed field, so a
// single unnamed request comes back as top_0 and a named one as its own name.
func radarSeries(result map[string]json.RawMessage) ([]radarEntry, error) {
	for key, raw := range result {
		if key == "meta" {
			continue
		}
		var entries []radarEntry
		if err := json.Unmarshal(raw, &entries); err != nil {
			continue
		}
		return entries, nil
	}
	return nil, fmt.Errorf("response carries no ranking series")
}

func (a radarAdapter) fetchBucket(ctx context.Context, request Request, token string, bucket string) ([]model.Seed, error) {
	size, err := strconv.Atoi(bucket)
	if err != nil {
		return nil, fmt.Errorf("bucket must be a number: %w", err)
	}
	if !isRadarBucket(size) {
		return nil, fmt.Errorf("radar publishes buckets of %s only", formatRadarBuckets())
	}

	endpoint := fmt.Sprintf("%sdatasets/ranking_top_%d", radarBaseURL, size)
	body, err := getBearer(ctx, request, endpoint, "text/csv", token)
	if err != nil {
		return nil, err
	}

	reader := csv.NewReader(bytes.NewReader(body))
	reader.FieldsPerRecord = -1

	evidence := fmt.Sprintf("radar ranking_top_%d", size)
	seeds := make([]model.Seed, 0, request.Limit)
	for len(seeds) < request.Limit {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			if len(seeds) > 0 {
				break
			}
			return nil, fmt.Errorf("parse CSV: %w", err)
		}
		if len(record) == 0 {
			continue
		}
		domain := strings.TrimSpace(record[0])
		if domain == "" || strings.EqualFold(domain, "domain") {
			continue
		}
		// A bucket carries no rank column: membership is all the corpus asserts.
		seeds = append(seeds, model.Seed{
			Value:    domain,
			Kind:     model.SeedHost,
			Adapter:  a.Name(),
			RankKind: model.RankMember,
			Evidence: evidence,
		})
	}
	return seeds, nil
}

func isRadarBucket(size int) bool {
	for _, bucket := range radarBuckets {
		if bucket == size {
			return true
		}
	}
	return false
}

func formatRadarBuckets() string {
	formatted := make([]string, 0, len(radarBuckets))
	for _, bucket := range radarBuckets {
		formatted = append(formatted, strconv.Itoa(bucket))
	}
	return strings.Join(formatted, ", ")
}
