package corpus

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/usestring/gqlcrawl/internal/model"
)

var httpArchiveValueColumns = map[string]struct{}{
	"page":      {},
	"root_page": {},
	"origin":    {},
	"site":      {},
	"url":       {},
	"domain":    {},
	"host":      {},
}

type httpArchiveAdapter struct{}

func (httpArchiveAdapter) Name() string { return "httparchive" }

func (httpArchiveAdapter) Summary() string {
	return "Sites HTTP Archive detected as using GraphQL, from a CSV you export yourself (bucket when ranked, no credentials)"
}

func (httpArchiveAdapter) ScopeUsage() string { return "a CSV exported from BigQuery" }

func (httpArchiveAdapter) Requirement() Requirement {
	return Requirement{
		Notes: "Reads an export rather than querying BigQuery, so the query runs in your own project on your own billing. Its GraphQL label is inherited from Apollo and a handful of commerce platforms far more often than it is matched directly; see docs/httparchive.md.",
	}
}

func (a httpArchiveAdapter) Fetch(ctx context.Context, request Request) ([]model.Seed, error) {
	if len(request.Scope) == 0 {
		return nil, fmt.Errorf("httparchive reads an exported CSV; pass it with --input FILE or --input - (see docs/httparchive.md)")
	}

	rows, err := readHTTPArchiveCSV(request.Scope)
	if err != nil {
		return nil, err
	}

	kind := model.SeedHost
	switch strings.ToLower(request.Option("kind", "host")) {
	case "host":
	case "url":
		kind = model.SeedURL
	default:
		return nil, fmt.Errorf("unsupported kind; use host or url")
	}
	evidence := request.Option("label", "httparchive export")

	seeds := make([]model.Seed, 0, len(rows))
	for _, row := range rows {
		if row.value == "" {
			continue
		}
		seed := model.Seed{
			Value:    row.value,
			Kind:     kind,
			Adapter:  a.Name(),
			Evidence: evidence,
		}
		// HTTP Archive carries the CrUX rank verbatim, so it is a magnitude bucket.
		if row.rank > 0 {
			seed.Rank = row.rank
			seed.RankKind = model.RankBucket
		}
		seeds = append(seeds, seed)
		if len(seeds) >= request.Limit {
			break
		}
	}
	return seeds, nil
}

type httpArchiveRow struct {
	value string
	rank  int
}

func readHTTPArchiveCSV(lines []string) ([]httpArchiveRow, error) {
	reader := csv.NewReader(strings.NewReader(strings.Join(lines, "\n")))
	reader.FieldsPerRecord = -1

	valueColumn := 0
	rankColumn := -1
	headerSeen := false
	rows := make([]httpArchiveRow, 0, len(lines))

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse CSV: %w", err)
		}
		if len(record) == 0 {
			continue
		}
		// A bq script run echoes each statement above its results, so the header is not
		// necessarily the first line and everything above it is not data.
		if !headerSeen && isHTTPArchiveHeader(record) {
			headerSeen = true
			valueColumn, rankColumn = httpArchiveColumns(record)
			rows = rows[:0]
			continue
		}
		if valueColumn >= len(record) {
			continue
		}

		row := httpArchiveRow{value: strings.TrimSpace(record[valueColumn])}
		if rankColumn >= 0 && rankColumn < len(record) {
			if rank, err := strconv.Atoi(strings.TrimSpace(record[rankColumn])); err == nil && rank > 0 {
				row.rank = rank
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func isHTTPArchiveHeader(record []string) bool {
	for _, field := range record {
		if _, ok := httpArchiveValueColumns[strings.ToLower(strings.TrimSpace(field))]; ok {
			return true
		}
	}
	return false
}

func httpArchiveColumns(record []string) (int, int) {
	valueColumn := -1
	rankColumn := -1
	for index, field := range record {
		name := strings.ToLower(strings.TrimSpace(field))
		if _, ok := httpArchiveValueColumns[name]; ok && valueColumn < 0 {
			valueColumn = index
		}
		if name == "rank" {
			rankColumn = index
		}
	}
	return valueColumn, rankColumn
}
