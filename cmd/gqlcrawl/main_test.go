package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestParseProbeArgumentsAcceptsInterspersedOptions(t *testing.T) {
	config, endpoints, err := parseProbeArguments([]string{
		"https://one.test/graphql",
		"--workers", "4",
		"https://two.test/graphql",
		"--allow-http=false",
		"--timeout=2s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.workers != 4 || config.allowHTTP || config.timeout.String() != "2s" {
		t.Fatalf("config = %#v", config)
	}
	if len(endpoints) != 2 || endpoints[0] != "https://one.test/graphql" || endpoints[1] != "https://two.test/graphql" {
		t.Fatalf("endpoints = %v", endpoints)
	}
}

func TestParseProbeArgumentsEnforcesSafetyBounds(t *testing.T) {
	tests := [][]string{
		{"--workers=65"},
		{"--per-host-rps=10.1"},
		{"--timeout=61s"},
		{"--max-response-bytes=1048577"},
		{"--per-host-rps=NaN"},
		{"--per-host-rps=+Inf"},
		{"--per-host-rps=-Inf"},
	}
	for _, arguments := range tests {
		if _, _, err := parseProbeArguments(arguments); err == nil {
			t.Fatalf("arguments %v were accepted", arguments)
		}
	}
}

func TestParseCrawlArgumentsAcceptsBoundedDiscoveryOptions(t *testing.T) {
	config, seeds, err := parseCrawlArguments([]string{
		"example.test",
		"--max-pages-per-host=12",
		"--max-depth", "1",
		"--respect-robots=false",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.maxPagesPerHost != 12 || config.maxDepth != 1 || config.respectRobots {
		t.Fatalf("config = %#v", config)
	}
	if len(seeds) != 1 || seeds[0] != "example.test" {
		t.Fatalf("seeds = %v", seeds)
	}

	if _, _, err := parseCrawlArguments([]string{"--max-pages-per-host=101"}); err == nil {
		t.Fatal("expected crawl page-limit validation error")
	}
	if _, _, err := parseProbeArguments([]string{"--max-depth=1"}); err == nil {
		t.Fatal("probe unexpectedly accepted a crawl-only option")
	}
}

func TestMakeUserAgentRejectsHeaderInjection(t *testing.T) {
	if _, err := makeUserAgent("owner@example.com\nAuthorization: secret"); err == nil {
		t.Fatal("expected contact validation error")
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"probe", "--workers=0", "https://example.com/graphql"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if exitCode != 2 {
		t.Fatalf("exit code = %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "--workers must be between 1 and 64") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"probe", "--help"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "--denylist FILE") {
		t.Fatalf("help = %q", stdout.String())
	}
}

func TestRunCrawlHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"crawl", "--help"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "--respect-robots=true|false") ||
		!strings.Contains(stdout.String(), "--max-pages-per-host N") {
		t.Fatalf("help = %q", stdout.String())
	}
}

func TestRunVersionUsesInjectedVersion(t *testing.T) {
	originalVersion := version
	version = "v1.2.3"
	t.Cleanup(func() {
		version = originalVersion
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"version"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d", exitCode)
	}
	if stdout.String() != "v1.2.3\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
