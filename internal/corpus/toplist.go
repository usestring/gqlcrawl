package corpus

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/usestring/gqlcrawl/internal/model"
)

const majesticBytesPerRow = 220

type rankedRow struct {
	rank   int
	domain string
}

func parseRankedCSV(payload []byte, rankColumn int, domainColumn int, hasHeader bool, limit int) ([]rankedRow, error) {
	reader := csv.NewReader(bytes.NewReader(payload))
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = true

	rows := make([]rankedRow, 0, limit)
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
		if rankColumn >= len(record) || domainColumn >= len(record) {
			continue
		}

		rank, err := strconv.Atoi(strings.TrimSpace(record[rankColumn]))
		if err != nil {
			if hasHeader {
				hasHeader = false
				continue
			}
			continue
		}
		domain := strings.TrimSpace(record[domainColumn])
		if domain == "" {
			continue
		}
		rows = append(rows, rankedRow{rank: rank, domain: domain})
	}
	return rows, nil
}

func rankedSeeds(rows []rankedRow, adapter string, evidence string) []model.Seed {
	seeds := make([]model.Seed, 0, len(rows))
	for _, row := range rows {
		seeds = append(seeds, model.Seed{
			Value:    row.domain,
			Kind:     model.SeedHost,
			Adapter:  adapter,
			Rank:     row.rank,
			RankKind: model.RankOrdinal,
			Evidence: evidence,
		})
	}
	return seeds
}

func readZipMember(payload []byte, suffix string) ([]byte, error) {
	archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	for _, file := range archive.File {
		if suffix != "" && !strings.HasSuffix(file.Name, suffix) {
			continue
		}
		opened, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", file.Name, err)
		}
		defer opened.Close()
		return io.ReadAll(opened)
	}
	return nil, fmt.Errorf("archive has no %s member", suffix)
}

type trancoAdapter struct{}

func (trancoAdapter) Name() string { return "tranco" }

func (trancoAdapter) Summary() string {
	return "Tranco research-oriented top sites ranking (ordinal, no credentials)"
}

func (trancoAdapter) ScopeUsage() string { return "" }

func (trancoAdapter) Requirement() Requirement {
	return Requirement{
		Notes: "Derived from providers under non-commercial and share-alike terms; fetched at run time and never redistributed. Cite the list id in published work.",
	}
}

func (a trancoAdapter) Fetch(ctx context.Context, request Request) ([]model.Seed, error) {
	listEndpoint := "https://tranco-list.eu/api/lists/date/latest"
	if date := request.Option("date", ""); date != "" {
		listEndpoint = "https://tranco-list.eu/api/lists/date/" + date
	}
	if request.Option("subdomains", "") == "true" {
		listEndpoint += "?subdomains=true"
	}

	var metadata struct {
		ListID    string `json:"list_id"`
		Available bool   `json:"available"`
		Failed    bool   `json:"failed"`
		Download  string `json:"download"`
		CreatedOn string `json:"created_on"`
	}
	if err := GetJSON(ctx, request, listEndpoint, &metadata); err != nil {
		return nil, err
	}
	if metadata.Failed {
		return nil, fmt.Errorf("tranco reported a failed list")
	}
	if !metadata.Available || metadata.ListID == "" {
		return nil, fmt.Errorf("tranco list is not available yet")
	}

	download := fmt.Sprintf("https://tranco-list.eu/download/%s/%d", metadata.ListID, request.Limit)
	payload, err := Get(ctx, request, download, "text/csv")
	if err != nil {
		return nil, err
	}

	rows, err := parseRankedCSV(payload, 0, 1, false, request.Limit)
	if err != nil {
		return nil, err
	}
	evidence := metadata.ListID
	if metadata.CreatedOn != "" {
		evidence = metadata.ListID + " " + metadata.CreatedOn
	}
	return rankedSeeds(rows, a.Name(), evidence), nil
}

type umbrellaAdapter struct{}

func (umbrellaAdapter) Name() string { return "umbrella" }

func (umbrellaAdapter) Summary() string {
	return "Cisco Umbrella top 1M by DNS resolution volume (ordinal, no credentials)"
}

func (umbrellaAdapter) ScopeUsage() string { return "" }

func (umbrellaAdapter) Requirement() Requirement {
	return Requirement{
		Notes: "Cisco publishes no redistribution grant, so the list is fetched at run time only. Entries include subdomains rather than registrable domains.",
	}
}

func (a umbrellaAdapter) Fetch(ctx context.Context, request Request) ([]model.Seed, error) {
	name := "top-1m.csv.zip"
	if date := request.Option("date", ""); date != "" {
		name = "top-1m-" + date + ".csv.zip"
	}
	download := "https://s3-us-west-1.amazonaws.com/umbrella-static/" + name

	payload, err := Get(ctx, request, download, "application/zip")
	if err != nil {
		return nil, err
	}
	member, err := readZipMember(payload, ".csv")
	if err != nil {
		return nil, err
	}
	rows, err := parseRankedCSV(member, 0, 1, false, request.Limit)
	if err != nil {
		return nil, err
	}
	return rankedSeeds(rows, a.Name(), name), nil
}

type majesticAdapter struct{}

func (majesticAdapter) Name() string { return "majestic" }

func (majesticAdapter) Summary() string {
	return "Majestic Million ranked by referring subnets (ordinal, no credentials)"
}

func (majesticAdapter) ScopeUsage() string { return "" }

func (majesticAdapter) Requirement() Requirement {
	return Requirement{
		Notes: "Published under CC BY 3.0. Ranks link-graph authority rather than traffic, so it is not interchangeable with the traffic lists.",
	}
}

func (a majesticAdapter) Fetch(ctx context.Context, request Request) ([]model.Seed, error) {
	download := "https://downloads.majestic.com/majestic_million.csv"

	wanted := int64(request.Limit+1) * majesticBytesPerRow
	if wanted > request.MaxDownloadBytes && request.MaxDownloadBytes > 0 {
		wanted = request.MaxDownloadBytes
	}
	payload, err := GetRange(ctx, request, download, "text/csv", wanted)
	if err != nil {
		return nil, err
	}

	rows, err := parseRankedCSV(payload, 0, 2, true, request.Limit)
	if err != nil {
		return nil, err
	}
	return rankedSeeds(rows, a.Name(), "majestic_million.csv"), nil
}
