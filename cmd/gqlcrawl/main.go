package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/usestring/gqlcrawl/internal/network"
	"github.com/usestring/gqlcrawl/internal/output"
	"github.com/usestring/gqlcrawl/internal/probe"
	"github.com/usestring/gqlcrawl/internal/runner"
	"github.com/usestring/gqlcrawl/internal/source"
)

const version = "dev"

var errHelp = errors.New("help requested")

type probeConfig struct {
	inputPath       string
	format          string
	workers         int
	perHostRPS      float64
	timeout         time.Duration
	maxResponseSize int64
	denylistPath    string
	contact         string
	allowHTTP       bool
}

func defaultProbeConfig() probeConfig {
	return probeConfig{
		format:          "jsonl",
		workers:         16,
		perHostRPS:      1,
		timeout:         10 * time.Second,
		maxResponseSize: 64 * 1024,
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, arguments []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	if len(arguments) == 0 {
		writeRootHelp(stderr)
		return 2
	}

	switch arguments[0] {
	case "help", "--help", "-h":
		writeRootHelp(stdout)
		return 0
	case "version", "--version":
		fmt.Fprintln(stdout, version)
		return 0
	case "probe":
		return runProbe(ctx, arguments[1:], stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", arguments[0])
		writeRootHelp(stderr)
		return 2
	}
}

func runProbe(ctx context.Context, arguments []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	config, endpointArguments, err := parseProbeArguments(arguments)
	if errors.Is(err, errHelp) {
		writeProbeHelp(stdout)
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	denylist, err := network.LoadDenylist(config.denylistPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	policy := network.NewPolicy(config.allowHTTP, denylist, nil)
	client, err := network.NewClient(network.ClientConfig{
		Policy:       policy,
		Timeout:      config.timeout,
		MaxRedirects: 2,
		PerHostRPS:   config.perHostRPS,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	userAgent, err := makeUserAgent(config.contact)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	introspectionProber, err := probe.New(client, config.maxResponseSize, userAgent)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	candidates, err := source.Load(endpointArguments, config.inputPath, stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	results, err := runner.Run(ctx, candidates, config.workers, time.Now, introspectionProber)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := output.WriteJSONL(stdout, results); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func parseProbeArguments(arguments []string) (probeConfig, []string, error) {
	config := defaultProbeConfig()
	var endpoints []string

	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			endpoints = append(endpoints, arguments[index+1:]...)
			break
		}
		if !strings.HasPrefix(argument, "-") {
			endpoints = append(endpoints, argument)
			continue
		}

		name, value, hasValue := strings.Cut(strings.TrimPrefix(argument, "--"), "=")
		if argument == "-h" || name == "help" {
			return config, nil, errHelp
		}
		if name == "allow-http" && !hasValue {
			config.allowHTTP = true
			continue
		}
		if !hasValue {
			index++
			if index >= len(arguments) {
				return config, nil, fmt.Errorf("--%s requires a value", name)
			}
			value = arguments[index]
		}

		var err error
		switch name {
		case "input":
			config.inputPath = value
		case "format":
			config.format = value
		case "workers":
			config.workers, err = strconv.Atoi(value)
		case "per-host-rps":
			config.perHostRPS, err = strconv.ParseFloat(value, 64)
		case "timeout":
			config.timeout, err = time.ParseDuration(value)
		case "max-response-bytes":
			config.maxResponseSize, err = strconv.ParseInt(value, 10, 64)
		case "denylist":
			config.denylistPath = value
		case "contact":
			config.contact = value
		case "allow-http":
			config.allowHTTP, err = strconv.ParseBool(value)
		default:
			return config, nil, fmt.Errorf("unknown option --%s", name)
		}
		if err != nil {
			return config, nil, fmt.Errorf("invalid --%s value: %w", name, err)
		}
	}

	if config.format != "jsonl" {
		return config, nil, fmt.Errorf("--format must be jsonl")
	}
	if config.workers <= 0 || config.workers > 64 {
		return config, nil, fmt.Errorf("--workers must be between 1 and 64")
	}
	if config.perHostRPS <= 0 || config.perHostRPS > 10 {
		return config, nil, fmt.Errorf("--per-host-rps must be greater than 0 and at most 10")
	}
	if config.timeout <= 0 || config.timeout > time.Minute {
		return config, nil, fmt.Errorf("--timeout must be greater than 0 and at most 1m")
	}
	if config.maxResponseSize <= 0 || config.maxResponseSize > 1024*1024 {
		return config, nil, fmt.Errorf("--max-response-bytes must be between 1 and 1048576")
	}

	return config, endpoints, nil
}

func makeUserAgent(contact string) (string, error) {
	contact = strings.TrimSpace(contact)
	if strings.ContainsAny(contact, "\r\n") {
		return "", fmt.Errorf("--contact cannot contain line breaks")
	}
	if len(contact) > 200 {
		return "", fmt.Errorf("--contact cannot exceed 200 bytes")
	}
	userAgent := "gqlcrawl/" + version + " (+https://github.com/usestring/gqlcrawl)"
	if contact != "" {
		userAgent += " contact=" + contact
	}
	return userAgent, nil
}

func writeRootHelp(writer io.Writer) {
	fmt.Fprintln(writer, "gqlcrawl finds public GraphQL endpoints that expose introspection.")
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Usage:")
	fmt.Fprintln(writer, "  gqlcrawl probe [options] [URL...]")
	fmt.Fprintln(writer, "  gqlcrawl version")
}

func writeProbeHelp(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: gqlcrawl probe [options] [URL...]")
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Options:")
	fmt.Fprintln(writer, "  --input FILE|-              Read additional URLs from a file or stdin")
	fmt.Fprintln(writer, "  --workers N                 Global workers (default 16, max 64)")
	fmt.Fprintln(writer, "  --per-host-rps N            Per-host requests per second (default 1, max 10)")
	fmt.Fprintln(writer, "  --timeout DURATION          Overall request timeout (default 10s, max 1m)")
	fmt.Fprintln(writer, "  --max-response-bytes N      Response body limit (default 65536, max 1048576)")
	fmt.Fprintln(writer, "  --denylist FILE             Local host denylist")
	fmt.Fprintln(writer, "  --contact VALUE             Contact appended to the user agent")
	fmt.Fprintln(writer, "  --allow-http[=true|false]    Permit cleartext HTTP (default false)")
	fmt.Fprintln(writer, "  --format jsonl              Output format (default jsonl)")
}
