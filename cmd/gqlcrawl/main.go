package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/usestring/gqlcrawl/internal/corpus"
	"github.com/usestring/gqlcrawl/internal/crawl"
	"github.com/usestring/gqlcrawl/internal/network"
	"github.com/usestring/gqlcrawl/internal/output"
	"github.com/usestring/gqlcrawl/internal/probe"
	"github.com/usestring/gqlcrawl/internal/runner"
	"github.com/usestring/gqlcrawl/internal/source"
)

var version = "dev"

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

type crawlConfig struct {
	probeConfig
	maxPagesPerHost int
	maxDepth        int
	respectRobots   bool
}

type seedsConfig struct {
	source           string
	inputPath        string
	format           string
	limit            int
	perHostRPS       float64
	timeout          time.Duration
	maxDownloadBytes int64
	denylistPath     string
	contact          string
	allowHTTP        bool
	listSources      bool
}

type probeRuntime struct {
	client    *network.Client
	prober    *probe.Prober
	userAgent string
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

func defaultSeedsConfig() seedsConfig {
	return seedsConfig{
		format:           "lines",
		limit:            corpus.DefaultLimit,
		perHostRPS:       1,
		timeout:          30 * time.Second,
		maxDownloadBytes: corpus.DefaultMaxDownloadBytes,
	}
}

func defaultCrawlConfig() crawlConfig {
	return crawlConfig{
		probeConfig:     defaultProbeConfig(),
		maxPagesPerHost: 25,
		maxDepth:        2,
		respectRobots:   true,
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
		fmt.Fprintln(stdout, buildVersion())
		return 0
	case "probe":
		return runProbe(ctx, arguments[1:], stdin, stdout, stderr)
	case "crawl":
		return runCrawl(ctx, arguments[1:], stdin, stdout, stderr)
	case "seeds":
		return runSeeds(ctx, arguments[1:], stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", arguments[0])
		writeRootHelp(stderr)
		return 2
	}
}

func buildVersion() string {
	if version != "dev" {
		return version
	}
	buildInfo, ok := debug.ReadBuildInfo()
	if ok && buildInfo.Main.Version != "" && buildInfo.Main.Version != "(devel)" {
		return buildInfo.Main.Version
	}
	return version
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

	runtime, err := buildProbeRuntime(config)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	candidates, err := source.Load(endpointArguments, config.inputPath, stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	results, err := runner.Run(ctx, candidates, config.workers, time.Now, runtime.prober)
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

func runCrawl(ctx context.Context, arguments []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	config, seedArguments, err := parseCrawlArguments(arguments)
	if errors.Is(err, errHelp) {
		writeCrawlHelp(stdout)
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	runtime, err := buildProbeRuntime(config.probeConfig)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	seeds, err := source.Load(seedArguments, config.inputPath, stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	discoveryCrawler, err := crawl.New(crawl.Config{
		Client:           runtime.client.WithoutRedirects(),
		MaxResponseBytes: config.maxResponseSize,
		MaxPagesPerHost:  config.maxPagesPerHost,
		MaxDepth:         config.maxDepth,
		RespectRobots:    config.respectRobots,
		UserAgent:        runtime.userAgent,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	candidates, discoveryErr := discoveryCrawler.Discover(ctx, seeds)
	results, err := runner.Run(ctx, candidates, config.workers, time.Now, runtime.prober)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := output.WriteJSONL(stdout, results); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if discoveryErr != nil {
		fmt.Fprintf(stderr, "crawl incomplete: %v\n", discoveryErr)
		return 1
	}
	return 0
}

func runSeeds(ctx context.Context, arguments []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	config, scopeArguments, err := parseSeedsArguments(arguments)
	if errors.Is(err, errHelp) {
		writeSeedsHelp(stdout)
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if config.listSources {
		writeSourceList(stdout)
		return 0
	}

	adapter, err := corpus.Lookup(config.source)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	userAgent, err := makeUserAgent(config.contact)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	client, err := buildNetworkClient(probeConfig{
		timeout:      config.timeout,
		perHostRPS:   config.perHostRPS,
		denylistPath: config.denylistPath,
		allowHTTP:    config.allowHTTP,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	scope, err := loadScope(scopeArguments, config.inputPath, stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	seeds, err := adapter.Fetch(ctx, corpus.Request{
		Limit:            config.limit,
		Scope:            scope,
		Fetcher:          client,
		UserAgent:        userAgent,
		MaxDownloadBytes: config.maxDownloadBytes,
		Lookup:           os.Getenv,
	})
	collected := corpus.Collect(seeds, config.limit)

	writeSeeds := output.WriteSeedLines
	if config.format == "jsonl" {
		writeSeeds = output.WriteSeedJSONL
	}
	if writeErr := writeSeeds(stdout, collected); writeErr != nil {
		fmt.Fprintln(stderr, writeErr)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "seeds incomplete: %v\n", err)
		return 1
	}
	return 0
}

func writeSourceList(writer io.Writer) {
	for _, adapter := range corpus.All() {
		fmt.Fprintf(writer, "%s\t%s\n", adapter.Name(), adapter.Summary())
		requirement := adapter.Requirement()
		if len(requirement.EnvVars) > 0 {
			fmt.Fprintf(writer, "\trequires: %s\n", strings.Join(requirement.EnvVars, ", "))
		}
		if requirement.Metered {
			fmt.Fprintln(writer, "\tmetered: this source consumes paid credits or query spend")
		}
		if requirement.Notes != "" {
			fmt.Fprintf(writer, "\tnotes: %s\n", requirement.Notes)
		}
	}
}

func loadScope(arguments []string, inputPath string, stdin io.Reader) ([]string, error) {
	scope := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		value := strings.TrimSpace(argument)
		if value == "" {
			continue
		}
		scope = append(scope, value)
	}

	if inputPath == "" {
		return scope, nil
	}

	reader := stdin
	if inputPath != "-" {
		file, err := os.Open(inputPath)
		if err != nil {
			return nil, fmt.Errorf("open input: %w", err)
		}
		defer file.Close()
		reader = file
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		value := strings.TrimSpace(scanner.Text())
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		scope = append(scope, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read scope input: %w", err)
	}
	return scope, nil
}

func buildProbeRuntime(config probeConfig) (probeRuntime, error) {
	client, err := buildNetworkClient(config)
	if err != nil {
		return probeRuntime{}, err
	}

	userAgent, err := makeUserAgent(config.contact)
	if err != nil {
		return probeRuntime{}, err
	}
	introspectionProber, err := probe.New(client, config.maxResponseSize, userAgent)
	if err != nil {
		return probeRuntime{}, err
	}
	return probeRuntime{
		client:    client,
		prober:    introspectionProber,
		userAgent: userAgent,
	}, nil
}

func buildNetworkClient(config probeConfig) (*network.Client, error) {
	denylist, err := network.LoadDenylist(config.denylistPath)
	if err != nil {
		return nil, err
	}
	policy := network.NewPolicy(config.allowHTTP, denylist, nil)
	client, err := network.NewClient(network.ClientConfig{
		Policy:       policy,
		Timeout:      config.timeout,
		MaxRedirects: 2,
		PerHostRPS:   config.perHostRPS,
	})
	if err != nil {
		return nil, err
	}
	return client, nil
}

func parseProbeArguments(arguments []string) (probeConfig, []string, error) {
	config, inputs, err := parseCommandArguments(arguments, false)
	return config.probeConfig, inputs, err
}

func parseCrawlArguments(arguments []string) (crawlConfig, []string, error) {
	return parseCommandArguments(arguments, true)
}

func parseSeedsArguments(arguments []string) (seedsConfig, []string, error) {
	config := defaultSeedsConfig()
	var scope []string

	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			scope = append(scope, arguments[index+1:]...)
			break
		}
		if !strings.HasPrefix(argument, "-") {
			scope = append(scope, argument)
			continue
		}

		name, value, hasValue := strings.Cut(strings.TrimPrefix(argument, "--"), "=")
		if argument == "-h" || name == "help" {
			return config, nil, errHelp
		}
		if name == "list-sources" && !hasValue {
			config.listSources = true
			continue
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
		case "source":
			config.source = value
		case "input":
			config.inputPath = value
		case "format":
			config.format = value
		case "limit":
			config.limit, err = strconv.Atoi(value)
		case "per-host-rps":
			config.perHostRPS, err = strconv.ParseFloat(value, 64)
		case "timeout":
			config.timeout, err = time.ParseDuration(value)
		case "max-download-bytes":
			config.maxDownloadBytes, err = strconv.ParseInt(value, 10, 64)
		case "denylist":
			config.denylistPath = value
		case "contact":
			config.contact = value
		case "allow-http":
			config.allowHTTP, err = strconv.ParseBool(value)
		case "list-sources":
			config.listSources, err = strconv.ParseBool(value)
		default:
			return config, nil, fmt.Errorf("unknown option --%s", name)
		}
		if err != nil {
			return config, nil, fmt.Errorf("invalid --%s value: %w", name, err)
		}
	}

	if config.listSources {
		return config, scope, nil
	}
	if config.format != "lines" && config.format != "jsonl" {
		return config, nil, fmt.Errorf("--format must be lines or jsonl")
	}
	if config.limit <= 0 || config.limit > corpus.MaxLimit {
		return config, nil, fmt.Errorf("--limit must be between 1 and %d", corpus.MaxLimit)
	}
	if math.IsNaN(config.perHostRPS) || math.IsInf(config.perHostRPS, 0) || config.perHostRPS <= 0 || config.perHostRPS > 10 {
		return config, nil, fmt.Errorf("--per-host-rps must be greater than 0 and at most 10")
	}
	if config.timeout <= 0 || config.timeout > time.Minute {
		return config, nil, fmt.Errorf("--timeout must be greater than 0 and at most 1m")
	}
	if config.maxDownloadBytes <= 0 || config.maxDownloadBytes > corpus.MaxDownloadBytes {
		return config, nil, fmt.Errorf("--max-download-bytes must be between 1 and %d", corpus.MaxDownloadBytes)
	}

	return config, scope, nil
}

func parseCommandArguments(arguments []string, allowCrawlOptions bool) (crawlConfig, []string, error) {
	config := defaultCrawlConfig()
	var inputs []string

	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			inputs = append(inputs, arguments[index+1:]...)
			break
		}
		if !strings.HasPrefix(argument, "-") {
			inputs = append(inputs, argument)
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
		if allowCrawlOptions && name == "respect-robots" && !hasValue {
			config.respectRobots = true
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
		case "max-pages-per-host":
			if !allowCrawlOptions {
				return config, nil, fmt.Errorf("unknown option --%s", name)
			}
			config.maxPagesPerHost, err = strconv.Atoi(value)
		case "max-depth":
			if !allowCrawlOptions {
				return config, nil, fmt.Errorf("unknown option --%s", name)
			}
			config.maxDepth, err = strconv.Atoi(value)
		case "respect-robots":
			if !allowCrawlOptions {
				return config, nil, fmt.Errorf("unknown option --%s", name)
			}
			config.respectRobots, err = strconv.ParseBool(value)
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
	if math.IsNaN(config.perHostRPS) || math.IsInf(config.perHostRPS, 0) || config.perHostRPS <= 0 || config.perHostRPS > 10 {
		return config, nil, fmt.Errorf("--per-host-rps must be greater than 0 and at most 10")
	}
	if config.timeout <= 0 || config.timeout > time.Minute {
		return config, nil, fmt.Errorf("--timeout must be greater than 0 and at most 1m")
	}
	if config.maxResponseSize <= 0 || config.maxResponseSize > 1024*1024 {
		return config, nil, fmt.Errorf("--max-response-bytes must be between 1 and 1048576")
	}
	if allowCrawlOptions {
		if config.maxPagesPerHost <= 0 || config.maxPagesPerHost > 100 {
			return config, nil, fmt.Errorf("--max-pages-per-host must be between 1 and 100")
		}
		if config.maxDepth < 0 || config.maxDepth > 4 {
			return config, nil, fmt.Errorf("--max-depth must be between 0 and 4")
		}
	}

	return config, inputs, nil
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
	fmt.Fprintln(writer, "  gqlcrawl crawl [options] [DOMAIN|URL...]")
	fmt.Fprintln(writer, "  gqlcrawl seeds --source NAME [options] [SCOPE...]")
	fmt.Fprintln(writer, "  gqlcrawl version")
}

func writeSeedsHelp(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: gqlcrawl seeds --source NAME [options] [SCOPE...]")
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Emit candidate seeds from a corpus to stdout. This command never contacts")
	fmt.Fprintln(writer, "the seeds themselves; pipe its output into probe or crawl to do that.")
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Options:")
	fmt.Fprintln(writer, "  --source NAME               Corpus adapter to read (required)")
	fmt.Fprintln(writer, "  --list-sources              List available corpus adapters and exit")
	fmt.Fprintln(writer, "  --limit N                   Maximum seeds to emit (default 1000)")
	fmt.Fprintln(writer, "  --input FILE|-              Read additional scope entries from a file or stdin")
	fmt.Fprintln(writer, "  --format lines|jsonl        Output format (default lines)")
	fmt.Fprintln(writer, "  --max-download-bytes N      Corpus response limit (default 67108864)")
	fmt.Fprintln(writer, "  --per-host-rps N            Per-host requests per second (default 1, max 10)")
	fmt.Fprintln(writer, "  --timeout DURATION          Overall request timeout (default 30s, max 1m)")
	fmt.Fprintln(writer, "  --denylist FILE             Local host denylist")
	fmt.Fprintln(writer, "  --contact VALUE             Contact appended to the user agent")
	fmt.Fprintln(writer, "  --allow-http[=true|false]   Permit cleartext HTTP (default false)")
}

func writeProbeHelp(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: gqlcrawl probe [options] [URL...]")
	fmt.Fprintln(writer)
	writeCommonHelp(writer)
}

func writeCrawlHelp(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: gqlcrawl crawl [options] [DOMAIN|URL...]")
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Discovery options:")
	fmt.Fprintln(writer, "  --max-pages-per-host N      Page limit per host (default 25, max 100)")
	fmt.Fprintln(writer, "  --max-depth N               Same-origin link depth (default 2, max 4)")
	fmt.Fprintln(writer, "  --respect-robots=true|false Honor robots.txt (default true)")
	fmt.Fprintln(writer)
	writeCommonHelp(writer)
}

func writeCommonHelp(writer io.Writer) {
	fmt.Fprintln(writer, "Options:")
	fmt.Fprintln(writer, "  --input FILE|-              Read additional inputs from a file or stdin")
	fmt.Fprintln(writer, "  --workers N                 Global probe workers (default 16, max 64)")
	fmt.Fprintln(writer, "  --per-host-rps N            Per-host requests per second (default 1, max 10)")
	fmt.Fprintln(writer, "  --timeout DURATION          Overall request timeout (default 10s, max 1m)")
	fmt.Fprintln(writer, "  --max-response-bytes N      Response body limit (default 65536, max 1048576)")
	fmt.Fprintln(writer, "  --denylist FILE             Local host denylist")
	fmt.Fprintln(writer, "  --contact VALUE             Contact appended to the user agent")
	fmt.Fprintln(writer, "  --allow-http[=true|false]    Permit cleartext HTTP (default false)")
	fmt.Fprintln(writer, "  --format jsonl              Output format (default jsonl)")
}
