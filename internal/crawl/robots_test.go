package crawl

import (
	"strings"
	"testing"
)

func TestRobotsUsesMostSpecificAgentAndLongestRule(t *testing.T) {
	rules := parseRobots([]byte(`
User-agent: *
Disallow: /private

User-agent: gqlcrawl
Disallow: /api
Allow: /api/public
Disallow: /*.json$
Disallow: /%73ecret
Disallow: /café
`))

	tests := []struct {
		path    string
		allowed bool
	}{
		{path: "/private", allowed: true},
		{path: "/api/private", allowed: false},
		{path: "/%61pi/private", allowed: false},
		{path: "/secret", allowed: false},
		{path: "/caf%C3%A9", allowed: false},
		{path: "/api/public/schema", allowed: true},
		{path: "/schema.json", allowed: false},
		{path: "/schema.json?download=1", allowed: true},
	}
	for _, test := range tests {
		if actual := rules.allowed(test.path); actual != test.allowed {
			t.Fatalf("allowed(%q) = %t, want %t", test.path, actual, test.allowed)
		}
	}
}

func TestRobotsAllowWinsEqualLengthTie(t *testing.T) {
	rules := parseRobots([]byte("User-agent: *\nDisallow: /same\nAllow: /same\n"))
	if !rules.allowed("/same") {
		t.Fatal("allow rule should win an equal-length tie")
	}
}

func TestRobotsBlankLineSeparatesEmptyGroup(t *testing.T) {
	rules := parseRobots([]byte("User-agent: *\n\nUser-agent: other\nDisallow: /\n"))
	if !rules.allowed("/public") {
		t.Fatal("rules for another agent leaked into an empty wildcard group")
	}
}

func TestRobotsCommentDoesNotEndGroup(t *testing.T) {
	rules := parseRobots([]byte("User-agent: gqlcrawl\n# keep this group\nDisallow: /private\n"))
	if rules.allowed("/private") {
		t.Fatal("comment unexpectedly ended the active group")
	}
}

func TestRobotsAcceptsUTF8ByteOrderMark(t *testing.T) {
	rules := parseRobots([]byte("\ufeffUser-agent: gqlcrawl\nDisallow: /private\n"))
	if rules.allowed("/private") {
		t.Fatal("byte-order mark hid the matching user-agent group")
	}
}

func TestRobotsOversizedLineFailsClosed(t *testing.T) {
	body := "User-agent: gqlcrawl\nDisallow: /" + strings.Repeat("x", 70*1024) + "\n"
	rules := parseRobots([]byte(body))
	if rules.allowed("/anything") {
		t.Fatal("scanner failure did not fail closed")
	}
}
