package corpus

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

type routedFetcher struct {
	responses map[string]*http.Response
	bodies    map[string][]byte
	requested []*http.Request
}

func (f *routedFetcher) Do(request *http.Request) (*http.Response, error) {
	f.requested = append(f.requested, request)
	for pattern, response := range f.responses {
		if !strings.Contains(request.URL.String(), pattern) {
			continue
		}
		if f.bodies == nil {
			f.bodies = map[string][]byte{}
		}
		body, cached := f.bodies[pattern]
		if !cached {
			read, err := io.ReadAll(response.Body)
			if err != nil {
				return nil, err
			}
			response.Body.Close()
			f.bodies[pattern] = read
			body = read
		}
		return &http.Response{
			StatusCode: response.StatusCode,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     http.Header{},
		}, nil
	}
	return newResponse(http.StatusNotFound, "no stub"), nil
}

func baseRequest(fetcher Fetcher, limit int) Request {
	return Request{
		Limit:            limit,
		Fetcher:          fetcher,
		UserAgent:        "gqlcrawl/test",
		MaxDownloadBytes: DefaultMaxDownloadBytes,
	}
}
