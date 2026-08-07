package crawl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/usestring/gqlcrawl/internal/source"
)

const (
	robotsProductToken = "gqlcrawl"
	maxRobotsLineBytes = 64 * 1024
)

type robotsCache struct {
	client           HTTPDoer
	maxResponseBytes int64
	userAgent        string
	respect          bool
	mu               sync.Mutex
	entries          map[string]robotsEntry
}

type robotsEntry struct {
	rules robotsRules
	err   error
}

type robotsRules struct {
	disallowAll bool
	groups      []robotsGroup
}

type robotsGroup struct {
	agents []string
	rules  []robotsRule
}

type robotsRule struct {
	allow   bool
	pattern string
}

func newRobotsCache(client HTTPDoer, maxResponseBytes int64, userAgent string, respect bool) *robotsCache {
	return &robotsCache{
		client:           client,
		maxResponseBytes: maxResponseBytes,
		userAgent:        userAgent,
		respect:          respect,
		entries:          make(map[string]robotsEntry),
	}
}

func (c *robotsCache) allowed(ctx context.Context, rawURL string) (bool, error) {
	if !c.respect {
		return true, nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false, fmt.Errorf("parse target URL: %w", err)
	}
	origin := strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)

	c.mu.Lock()
	entry, found := c.entries[origin]
	c.mu.Unlock()
	if !found {
		entry.rules, entry.err = c.fetch(ctx, origin+"/robots.txt")
		c.mu.Lock()
		c.entries[origin] = entry
		c.mu.Unlock()
	}
	if entry.err != nil {
		return false, entry.err
	}

	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	return entry.rules.allowed(path), nil
}

func (c *robotsCache) fetch(ctx context.Context, robotsURL string) (robotsRules, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return robotsRules{disallowAll: true}, fmt.Errorf("build robots request: %w", err)
	}
	request.Header.Set("Accept", "text/plain, */*;q=0.1")
	request.Header.Set("User-Agent", c.userAgent)

	response, err := c.client.Do(request)
	if err != nil {
		return robotsRules{disallowAll: true}, fmt.Errorf("fetch %s: %w", source.SanitizeURL(robotsURL), safeHTTPError(err))
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusNotFound, http.StatusGone:
		return robotsRules{}, nil
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return robotsRules{disallowAll: true}, fmt.Errorf("fetch %s: HTTP %d", source.SanitizeURL(robotsURL), response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
	if err != nil {
		return robotsRules{disallowAll: true}, fmt.Errorf("read %s: %w", source.SanitizeURL(robotsURL), err)
	}
	if int64(len(body)) > c.maxResponseBytes {
		return robotsRules{disallowAll: true}, fmt.Errorf("read %s: response exceeds %d bytes", source.SanitizeURL(robotsURL), c.maxResponseBytes)
	}
	return parseRobots(body), nil
}

func parseRobots(body []byte) robotsRules {
	var groups []robotsGroup
	var current *robotsGroup
	hasRules := false
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	scanner.Buffer(make([]byte, 4096), maxRobotsLineBytes)

	for scanner.Scan() {
		line := strings.TrimPrefix(scanner.Text(), "\ufeff")
		if strings.TrimSpace(line) == "" {
			current = nil
			hasRules = false
			continue
		}
		if comment := strings.IndexByte(line, '#'); comment >= 0 {
			line = line[:comment]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		field = strings.ToLower(strings.TrimSpace(field))
		value = strings.TrimSpace(value)

		switch field {
		case "user-agent":
			if current == nil || hasRules {
				groups = append(groups, robotsGroup{})
				current = &groups[len(groups)-1]
				hasRules = false
			}
			if value != "" {
				current.agents = append(current.agents, strings.ToLower(value))
			}
		case "allow", "disallow":
			if current == nil || value == "" {
				continue
			}
			current.rules = append(current.rules, robotsRule{
				allow:   field == "allow",
				pattern: value,
			})
			hasRules = true
		}
	}
	if scanner.Err() != nil {
		return robotsRules{disallowAll: true}
	}
	return robotsRules{groups: groups}
}

func (r robotsRules) allowed(path string) bool {
	if r.disallowAll {
		return false
	}
	path = normalizeRobotsOctets(path)

	bestAgentScore := -1
	var selectedRules []robotsRule
	for _, group := range r.groups {
		score := groupAgentScore(group)
		if score < 0 || score < bestAgentScore {
			continue
		}
		if score > bestAgentScore {
			bestAgentScore = score
			selectedRules = nil
		}
		selectedRules = append(selectedRules, group.rules...)
	}
	if bestAgentScore < 0 {
		return true
	}

	bestRuleLength := -1
	allowed := true
	for _, rule := range selectedRules {
		matched, length := matchesRobotsPattern(path, rule.pattern)
		if !matched || length < bestRuleLength {
			continue
		}
		if length > bestRuleLength || rule.allow {
			bestRuleLength = length
			allowed = rule.allow
		}
	}
	return allowed
}

func groupAgentScore(group robotsGroup) int {
	best := -1
	for _, agent := range group.agents {
		switch {
		case agent == "*" && best < 0:
			best = 0
		case agent != "" && strings.HasPrefix(robotsProductToken, agent) && len(agent) > best:
			best = len(agent)
		}
	}
	return best
}

func matchesRobotsPattern(path string, pattern string) (bool, int) {
	pattern = normalizeRobotsOctets(pattern)
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = strings.TrimSuffix(pattern, "$")
	}
	quoted := regexp.QuoteMeta(pattern)
	quoted = strings.ReplaceAll(quoted, `\*`, `.*`)
	expression := "^" + quoted
	if anchored {
		expression += "$"
	}
	matched, err := regexp.MatchString(expression, path)
	if err != nil {
		return false, 0
	}
	return matched, len(strings.ReplaceAll(pattern, "*", ""))
}

func normalizeRobotsOctets(value string) string {
	var normalized strings.Builder
	normalized.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '%' || index+2 >= len(value) {
			if value[index] >= 0x80 {
				normalized.WriteByte('%')
				normalized.WriteByte("0123456789ABCDEF"[value[index]>>4])
				normalized.WriteByte("0123456789ABCDEF"[value[index]&0x0f])
				continue
			}
			normalized.WriteByte(value[index])
			continue
		}
		high, highOK := hexValue(value[index+1])
		low, lowOK := hexValue(value[index+2])
		if !highOK || !lowOK {
			normalized.WriteByte(value[index])
			continue
		}
		decoded := high<<4 | low
		if isUnreserved(decoded) {
			normalized.WriteByte(decoded)
		} else {
			normalized.WriteByte('%')
			normalized.WriteByte("0123456789ABCDEF"[high])
			normalized.WriteByte("0123456789ABCDEF"[low])
		}
		index += 2
	}
	return normalized.String()
}

func hexValue(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

func isUnreserved(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == '-' || value == '.' || value == '_' || value == '~'
}
