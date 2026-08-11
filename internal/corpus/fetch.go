package corpus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

var errTruncated = fmt.Errorf("response exceeded the download limit")

func Get(ctx context.Context, request Request, target string, accept string) ([]byte, error) {
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

	response, err := request.Fetcher.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", response.StatusCode)
	}

	body, err := readBounded(response.Body, limit)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func GetRange(ctx context.Context, request Request, target string, accept string, maxBytes int64) ([]byte, error) {
	if request.Fetcher == nil {
		return nil, fmt.Errorf("no fetcher configured")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("range limit must be positive")
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpRequest.Header.Set("User-Agent", request.UserAgent)
	if accept != "" {
		httpRequest.Header.Set("Accept", accept)
	}
	httpRequest.Header.Set("Range", fmt.Sprintf("bytes=0-%d", maxBytes-1))

	response, err := request.Fetcher.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("unexpected status %d", response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return body, nil
}

func GetJSON(ctx context.Context, request Request, target string, destination any) error {
	body, err := Get(ctx, request, target, "application/json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, destination); err != nil {
		return fmt.Errorf("decode JSON response: %w", err)
	}
	return nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, errTruncated
	}
	return body, nil
}
