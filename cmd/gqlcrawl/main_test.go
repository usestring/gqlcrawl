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
	}
	for _, arguments := range tests {
		if _, _, err := parseProbeArguments(arguments); err == nil {
			t.Fatalf("arguments %v were accepted", arguments)
		}
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
