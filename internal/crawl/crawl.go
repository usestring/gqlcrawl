package crawl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	pathpkg "path"
	"regexp"
	"strings"

	"github.com/usestring/gqlcrawl/internal/model"
	"github.com/usestring/gqlcrawl/internal/source"
)

const (
	maxScriptsTotal      = 50
	maxScriptsPerHost    = 10
	maxCandidatesTotal   = 250
	maxCandidatesPerHost = 5
	markerWindowBytes    = 256
)

var (
	anchorPattern       = regexp.MustCompile(`(?is)<a\b[^>]*>`)
	scriptPattern       = regexp.MustCompile(`(?is)<script\b[^>]*>`)
	hrefPattern         = regexp.MustCompile(`(?is)\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>]+))`)
	srcPattern          = regexp.MustCompile(`(?is)\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>]+))`)
	referencePattern    = regexp.MustCompile(`(?i)https?://[^\s"'<>\\]+|(?:\.\.?/|/)[A-Za-z0-9_~!$&()*+,;=:@%./?\-]+`)
	graphqlPathSegment  = regexp.MustCompile(`(?i)(?:^|/)(?:graphql|gql)(?:/|$)`)
	graphqlMarkers      = []string{"graphiql", "graphql playground", "graphqlplayground", "apollo sandbox", "apollosandbox", "apollo server", "apolloserver", "must provide query string", "__schema", "apolloclient", "createhttplink", "graphqlclient", "urql", "relayenvironment", "graphql-ws"}
	endpointHints       = []string{`"uri"`, "'uri'", "uri:", "uri =", "endpoint", "fetch(", "graphqlclient", "createhttplink", "subscriptionclient"}
	selfEndpointMarkers = []string{"graphiql", "graphql playground", "graphqlplayground", "apollo sandbox", "apollosandbox", "apollo server", "apolloserver", "must provide query string"}
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Config struct {
	Client           HTTPDoer
	MaxResponseBytes int64
	MaxPagesPerHost  int
	MaxDepth         int
	RespectRobots    bool
	UserAgent        string
}

type Crawler struct {
	client           HTTPDoer
	maxResponseBytes int64
	maxPagesPerHost  int
	maxDepth         int
	userAgent        string
	robots           *robotsCache
}

type pageTask struct {
	url        string
	depth      int
	rootOrigin string
	source     model.Source
}

type fetchedDocument struct {
	body        []byte
	contentType string
	url         string
}

func New(config Config) (*Crawler, error) {
	if config.Client == nil {
		return nil, fmt.Errorf("HTTP client is required")
	}
	if config.MaxResponseBytes <= 0 || config.MaxResponseBytes > 1024*1024 {
		return nil, fmt.Errorf("maximum response bytes must be between 1 and 1048576")
	}
	if config.MaxPagesPerHost <= 0 || config.MaxPagesPerHost > 100 {
		return nil, fmt.Errorf("maximum pages per host must be between 1 and 100")
	}
	if config.MaxDepth < 0 || config.MaxDepth > 4 {
		return nil, fmt.Errorf("maximum depth must be between 0 and 4")
	}
	if strings.TrimSpace(config.UserAgent) == "" {
		return nil, fmt.Errorf("user agent is required")
	}

	return &Crawler{
		client:           config.Client,
		maxResponseBytes: config.MaxResponseBytes,
		maxPagesPerHost:  config.MaxPagesPerHost,
		maxDepth:         config.MaxDepth,
		userAgent:        config.UserAgent,
		robots: newRobotsCache(
			config.Client,
			config.MaxResponseBytes,
			config.UserAgent,
			config.RespectRobots,
		),
	}, nil
}

func NormalizeSeed(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("seed is empty")
	}
	if !strings.Contains(trimmed, "://") {
		if strings.ContainsAny(trimmed, "/?#@") {
			return "", fmt.Errorf("a seed without a scheme must be a domain")
		}
		trimmed = "https://" + trimmed
	}

	normalized, err := source.NormalizeURL(trimmed)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", fmt.Errorf("parse normalized seed: %w", err)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("seed URL userinfo is not allowed")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("seed scheme %q is not supported", parsed.Scheme)
	}
	return normalized, nil
}

func (c *Crawler) Discover(ctx context.Context, seeds []source.Candidate) ([]source.Candidate, error) {
	var candidates []source.Candidate
	var discoveryErrors []error
	seenCandidates := make(map[string]struct{})
	seenPages := make(map[string]struct{})
	seenScriptEvidence := make(map[string]struct{})
	scriptAttempts := make(map[string]struct{})
	scriptCache := make(map[string]fetchedDocument)
	pageCounts := make(map[string]int)
	scriptCounts := make(map[string]int)
	candidateCounts := make(map[string]int)
	totalCandidates := 0
	totalScripts := 0

	for _, seed := range seeds {
		if totalCandidates >= maxCandidatesTotal {
			break
		}
		normalized, err := NormalizeSeed(seed.Raw)
		if err != nil {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("invalid seed %s: %w", seed.Source.Input, err))
			continue
		}
		seed.Source.Input = source.SanitizeURL(normalized)
		rootOrigin := urlOrigin(normalized)
		queue := []pageTask{{url: normalized, rootOrigin: rootOrigin, source: seed.Source}}

		for len(queue) > 0 {
			task := queue[0]
			queue = queue[1:]
			if _, found := seenPages[task.url]; found {
				continue
			}

			host := urlHost(task.url)
			if host == "" || pageCounts[host] >= c.maxPagesPerHost {
				continue
			}
			seenPages[task.url] = struct{}{}
			pageCounts[host]++

			allowed, err := c.robots.allowed(ctx, task.url)
			if err != nil {
				discoveryErrors = append(discoveryErrors, fmt.Errorf("check robots for %s: %w", source.SanitizeURL(task.url), err))
				continue
			}
			if !allowed {
				continue
			}

			document, err := c.fetch(ctx, task.url)
			if err != nil {
				discoveryErrors = append(discoveryErrors, err)
				continue
			}
			c.appendCandidates(
				ctx,
				&candidates,
				seenCandidates,
				candidateCounts,
				&totalCandidates,
				&discoveryErrors,
				document.body,
				document.url,
				document.url,
				task.rootOrigin,
				task.source,
				true,
			)
			if totalCandidates >= maxCandidatesTotal {
				break
			}

			if !isHTML(document.contentType) {
				continue
			}

			for _, scriptReference := range scriptReferences(document.body) {
				scriptURL, ok := resolveHTTPReference(document.url, scriptReference)
				if !ok {
					continue
				}
				evidenceKey := scriptURL + "\x00" + document.url
				if _, found := seenScriptEvidence[evidenceKey]; found {
					continue
				}
				seenScriptEvidence[evidenceKey] = struct{}{}

				script, found := scriptCache[scriptURL]
				if !found {
					if _, attempted := scriptAttempts[scriptURL]; attempted {
						continue
					}
					if totalScripts >= maxScriptsTotal {
						continue
					}
					scriptHost := urlHost(scriptURL)
					if scriptHost == "" || scriptCounts[scriptHost] >= maxScriptsPerHost {
						continue
					}
					scriptAttempts[scriptURL] = struct{}{}
					scriptCounts[scriptHost]++
					totalScripts++

					allowed, err := c.robots.allowed(ctx, scriptURL)
					if err != nil {
						discoveryErrors = append(discoveryErrors, fmt.Errorf("check robots for %s: %w", source.SanitizeURL(scriptURL), err))
						continue
					}
					if !allowed {
						continue
					}
					script, err = c.fetch(ctx, scriptURL)
					if err != nil {
						discoveryErrors = append(discoveryErrors, err)
						continue
					}
					scriptCache[scriptURL] = script
				}
				c.appendCandidates(
					ctx,
					&candidates,
					seenCandidates,
					candidateCounts,
					&totalCandidates,
					&discoveryErrors,
					script.body,
					script.url,
					document.url,
					task.rootOrigin,
					task.source,
					false,
				)
			}
			if totalCandidates >= maxCandidatesTotal {
				break
			}

			if task.depth >= c.maxDepth {
				continue
			}
			for _, linkReference := range linkReferences(document.body) {
				linkURL, ok := resolveHTTPReference(document.url, linkReference)
				if !ok || urlOrigin(linkURL) != task.rootOrigin || isGraphQLPathURL(linkURL) {
					continue
				}
				if _, found := seenCandidates[urlWithoutQuery(linkURL)]; found {
					continue
				}
				queue = append(queue, pageTask{
					url:        linkURL,
					depth:      task.depth + 1,
					rootOrigin: task.rootOrigin,
					source:     task.source,
				})
			}
		}
	}

	return candidates, errors.Join(discoveryErrors...)
}

func (c *Crawler) appendCandidates(
	ctx context.Context,
	candidates *[]source.Candidate,
	seen map[string]struct{},
	hostCounts map[string]int,
	totalCandidates *int,
	errorsFound *[]error,
	body []byte,
	evidenceURL string,
	resolutionBaseURL string,
	rootOrigin string,
	seedSource model.Source,
	includeEvidenceCandidate bool,
) {
	for _, candidateURL := range extractCandidateURLs(body, evidenceURL, resolutionBaseURL, rootOrigin, includeEvidenceCandidate) {
		if *totalCandidates >= maxCandidatesTotal {
			return
		}
		if _, found := seen[candidateURL]; found {
			continue
		}
		host := urlHost(candidateURL)
		if host == "" || hostCounts[host] >= maxCandidatesPerHost {
			continue
		}
		seen[candidateURL] = struct{}{}
		hostCounts[host]++
		*totalCandidates++

		skipReason := model.Reason("")
		allowed, err := c.robots.allowed(ctx, candidateURL)
		if err != nil {
			*errorsFound = append(*errorsFound, fmt.Errorf("check robots for %s: %w", source.SanitizeURL(candidateURL), err))
			skipReason = model.ReasonRobotsDisallowed
		} else if !allowed {
			skipReason = model.ReasonRobotsDisallowed
		}

		*candidates = append(*candidates, source.Candidate{
			Raw: candidateURL,
			Source: model.Source{
				Kind:        "crawl",
				Input:       seedSource.Input,
				EvidenceURL: source.SanitizeURL(evidenceURL),
			},
			SkipReason: skipReason,
		})
	}
}

func (c *Crawler) fetch(ctx context.Context, rawURL string) (fetchedDocument, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fetchedDocument{}, fmt.Errorf("build request for %s: %w", source.SanitizeURL(rawURL), safeHTTPError(err))
	}
	request.Header.Set("Accept", "text/html, application/xhtml+xml, application/javascript, text/javascript, */*;q=0.1")
	request.Header.Set("User-Agent", c.userAgent)

	response, err := c.client.Do(request)
	if err != nil {
		return fetchedDocument{}, fmt.Errorf("fetch %s: %w", source.SanitizeURL(rawURL), safeHTTPError(err))
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusBadRequest {
		return fetchedDocument{}, fmt.Errorf("fetch %s: HTTP %d", source.SanitizeURL(rawURL), response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
	if err != nil {
		return fetchedDocument{}, fmt.Errorf("read %s: %w", source.SanitizeURL(rawURL), err)
	}
	if int64(len(body)) > c.maxResponseBytes {
		return fetchedDocument{}, fmt.Errorf("read %s: response exceeds %d bytes", source.SanitizeURL(rawURL), c.maxResponseBytes)
	}

	finalURL := rawURL
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}
	normalizedFinalURL, err := source.NormalizeURL(finalURL)
	if err != nil {
		return fetchedDocument{}, fmt.Errorf("normalize response URL for %s: %w", source.SanitizeURL(rawURL), err)
	}
	return fetchedDocument{
		body:        body,
		contentType: mediaType(response.Header.Get("Content-Type")),
		url:         normalizedFinalURL,
	}, nil
}

func extractCandidateURLs(body []byte, evidenceURL string, resolutionBaseURL string, rootOrigin string, includeEvidenceCandidate bool) []string {
	lowerBody := bytes.ToLower(body)
	markerIndexes := termIndexes(lowerBody, graphqlMarkers)
	hintIndexes := termIndexes(lowerBody, endpointHints)

	matches := referencePattern.FindAllIndex(body, -1)
	results := make([]string, 0, len(matches)+1)
	seen := make(map[string]struct{}, len(matches)+1)
	if includeEvidenceCandidate && containsTerm(lowerBody, selfEndpointMarkers) {
		parsed, err := url.Parse(evidenceURL)
		if err == nil && !isStaticAsset(parsed.Path) {
			parsed.RawQuery = ""
			parsed.Fragment = ""
			candidateURL, normalizeErr := source.NormalizeURL(parsed.String())
			if normalizeErr == nil {
				seen[candidateURL] = struct{}{}
				results = append(results, candidateURL)
			}
		}
	}
	for _, match := range matches {
		if match[0] > 0 && body[match[0]-1] == '<' {
			continue
		}
		reference := strings.TrimRight(string(body[match[0]:match[1]]), ".,;:!?)\\]}")
		absolute := strings.HasPrefix(strings.ToLower(reference), "http://") || strings.HasPrefix(strings.ToLower(reference), "https://")
		candidateURL, ok := resolveHTTPReference(resolutionBaseURL, reference)
		if !ok {
			continue
		}
		if urlOrigin(candidateURL) != rootOrigin && !absolute {
			continue
		}

		parsed, err := url.Parse(candidateURL)
		if err != nil {
			continue
		}
		graphqlPath := isGraphQLPath(parsed.Path)
		if !graphqlPath && (!nearTerm(match, markerIndexes) || !nearTerm(match, hintIndexes)) {
			continue
		}
		if !graphqlPath && isStaticAsset(parsed.Path) {
			continue
		}
		parsed.RawQuery = ""
		parsed.Fragment = ""
		candidateURL, err = source.NormalizeURL(parsed.String())
		if err != nil {
			continue
		}
		if _, found := seen[candidateURL]; found {
			continue
		}
		seen[candidateURL] = struct{}{}
		results = append(results, candidateURL)
	}
	return results
}

func safeHTTPError(err error) error {
	var urlError *url.Error
	if errors.As(err, &urlError) {
		if urlError.Err != nil {
			return urlError.Err
		}
		return fmt.Errorf("request failed")
	}
	return err
}
func termIndexes(body []byte, terms []string) []int {
	var indexes []int
	for _, term := range terms {
		for offset := 0; ; {
			index := bytes.Index(body[offset:], []byte(term))
			if index < 0 {
				break
			}
			indexes = append(indexes, offset+index)
			offset += index + len(term)
		}
	}
	return indexes
}

func containsTerm(body []byte, terms []string) bool {
	for _, term := range terms {
		if bytes.Contains(body, []byte(term)) {
			return true
		}
	}
	return false
}

func nearTerm(match []int, indexes []int) bool {
	for _, marker := range indexes {
		if marker >= match[0]-markerWindowBytes && marker <= match[1]+markerWindowBytes {
			return true
		}
	}
	return false
}

func linkReferences(body []byte) []string {
	return tagAttributeValues(body, anchorPattern, hrefPattern)
}

func scriptReferences(body []byte) []string {
	return tagAttributeValues(body, scriptPattern, srcPattern)
}

func tagAttributeValues(body []byte, tagPattern *regexp.Regexp, attributePattern *regexp.Regexp) []string {
	var values []string
	for _, tag := range tagPattern.FindAll(body, -1) {
		match := attributePattern.FindSubmatch(tag)
		if len(match) < 2 {
			continue
		}
		for _, group := range match[1:] {
			if len(group) > 0 {
				values = append(values, html.UnescapeString(string(group)))
				break
			}
		}
	}
	return values
}

func resolveHTTPReference(baseRawURL string, reference string) (string, bool) {
	baseURL, err := url.Parse(baseRawURL)
	if err != nil {
		return "", false
	}
	referenceURL, err := url.Parse(strings.TrimSpace(reference))
	if err != nil {
		return "", false
	}
	resolved := baseURL.ResolveReference(referenceURL)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", false
	}
	if resolved.Hostname() == "" || resolved.User != nil {
		return "", false
	}
	normalized, err := source.NormalizeURL(resolved.String())
	return normalized, err == nil
}

func urlOrigin(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
}

func urlHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func isGraphQLPathURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return isGraphQLPath(parsed.Path)
}

func isGraphQLPath(path string) bool {
	return graphqlPathSegment.MatchString(strings.TrimSuffix(path, "/"))
}

func urlWithoutQuery(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	normalized, err := source.NormalizeURL(parsed.String())
	if err != nil {
		return rawURL
	}
	return normalized
}

func isStaticAsset(path string) bool {
	switch strings.ToLower(pathpkg.Ext(path)) {
	case ".css", ".gif", ".ico", ".jpeg", ".jpg", ".js", ".map", ".png", ".svg", ".webp", ".woff", ".woff2":
		return true
	default:
		return false
	}
}

func isHTML(contentType string) bool {
	return contentType == "text/html" || contentType == "application/xhtml+xml"
}

func mediaType(value string) string {
	parsed, _, err := mime.ParseMediaType(value)
	if err != nil {
		return strings.TrimSpace(strings.ToLower(value))
	}
	return strings.ToLower(parsed)
}
