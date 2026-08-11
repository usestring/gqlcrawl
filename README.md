# gqlcrawl

Find public GraphQL endpoints that expose introspection, without turning discovery into exploitation.

`gqlcrawl` is an early-stage Go CLI from [String](https://www.usestring.ai/). Use `probe` for exact public endpoint URLs or `crawl` to find literal GraphQL endpoint evidence on supplied public sites before running the same read-only probe. Use `seeds` to turn a public corpus into a candidate list without contacting any of it. Explicit full-schema export is planned as a separately reviewed addition.

## Safety first

Only crawl or probe targets you are authorized to contact. You are responsible for applicable law, terms, and organizational policy.

The CLI intentionally has no flags for authentication headers, cookies, mutations, field guessing, or application-data queries. It also does not publish a hosted endpoint catalog. `seeds` widens where candidates can come from without changing what the probe sends: it contacts only the corpus provider and emits its results for you to review. Every endpoint probe uses:

```graphql
query IntrospectionAvailability {
  __schema {
    queryType {
      name
    }
  }
}
```

The network client enforces these defaults:

- HTTPS only unless `--allow-http` is explicit; TLS certificates are verified.
- DNS answers must all be public, and the validated address is pinned for the connection.
- Probe redirects are revalidated and rate-gated per hop, limited to two, and rejected if they change the request method.
- One in-flight request and one request per second per host.
- Sixteen global probe workers, a 10-second timeout, a 64 KiB response cap, and no retries.
- An identifiable project user agent with optional `--contact` text.
- A bundled project opt-out list plus an optional local `--denylist`.

`crawl` adds stricter discovery boundaries:

- `robots.txt` is honored by default for pages, scripts, and discovered endpoint paths. An inaccessible or oversized robots file fails closed.
- Discovery fetches reject redirects so every fetched path is checked directly against its origin's robots rules.
- Page traversal stays on the seed origin, stops at depth two, and fetches at most 25 pages per host.
- Only scripts explicitly referenced by fetched HTML are read, capped at 50 total and 10 per host.
- Candidates must appear literally in fetched evidence. There is no common-path guessing.
- Relative candidates must stay on the seed origin. A cross-origin candidate must be an absolute URL in the evidence.
- Discovered URL query strings are removed rather than replayed. The run keeps at most 250 candidates total and five per host.

A block, timeout, authentication response, rate limit, redirect rejection, or transport error is always `indeterminate`. The CLI reports `disabled` only when a GraphQL-shaped response explicitly says introspection is rejected.

## Build and verify

Go 1.24.5 or newer is required.

Install the latest tagged version directly from source:

```sh
go install github.com/usestring/gqlcrawl/cmd/gqlcrawl@latest
```

Tagged releases also publish Linux, macOS, and Windows binaries with SHA-256 checksums on the [GitHub releases page](https://github.com/usestring/gqlcrawl/releases).

Maintainers publish from an existing annotated `vMAJOR.MINOR.PATCH` tag on `main`. If its automatic release run fails, recover the same immutable tag from **Actions → Release → Run workflow** by entering the existing tag name. The workflow uses its current recovery code to resolve and validate the remote tag, then checks out that exact release commit before testing, building, or publishing. It rejects lightweight tags and commits outside `main`. Never replace a published tag to recover a release.

```sh
go build ./cmd/gqlcrawl
go test -race ./...
go vet ./...
```

These commands are offline. The test suite uses injected DNS and local fixtures and does not probe the public internet.

## Use

The examples use reserved `.example` names, so they cannot contact a real endpoint.

Probe exact URL arguments:

```sh
./gqlcrawl probe https://your-approved-host.example/graphql
```

Read exact candidates from stdin or a file:

```sh
printf '%s\n' 'https://your-approved-host.example/graphql' |
  ./gqlcrawl probe --input -
./gqlcrawl probe --input approved-endpoints.txt
```

Crawl supplied domains or starting URLs, then probe only the discovered candidates:

```sh
./gqlcrawl crawl your-approved-host.example
./gqlcrawl crawl https://your-approved-host.example/docs
printf '%s\n' 'your-approved-host.example' |
  ./gqlcrawl crawl --input -
```

The crawler recognizes literal `/graphql` and `/gql` path segments plus nearby GraphiQL, Playground, Apollo, Relay, urql, `graphql-ws`, `__schema`, and GraphQL client evidence. These signatures create candidates; only the benign probe confirms GraphQL and introspection status.

Common options may appear before or after inputs:

```text
--input FILE|-
--workers 16
--per-host-rps 1
--timeout 10s
--max-response-bytes 65536
--denylist FILE
--contact VALUE
--allow-http=false
--format jsonl
```

`crawl` also accepts:

```text
--max-pages-per-host 25
--max-depth 2
--respect-robots=true
```

Disabling robots handling requires the explicit `--respect-robots=false` flag and does not replace target authorization.

Exit code `0` means the command completed and emitted its available ordered JSONL records; it does not mean introspection was enabled. A crawl that cannot read a permitted page, referenced script, or robots file emits any completed candidate results and exits `1` as incomplete. Exit `2` means the CLI configuration was invalid.

## Corpus seeds

`seeds` reads a public corpus and writes candidate hostnames or URLs to stdout. It contacts only the corpus provider, never the seeds it emits, and it never probes. Piping its output into `crawl` or `probe` is a separate, deliberate step that you own.

```sh
./gqlcrawl seeds --list-sources
./gqlcrawl seeds --source SOURCE --limit 500
./gqlcrawl seeds --source SOURCE --limit 500 | ./gqlcrawl crawl --input -
```

Adapters that need a scope take it as positional arguments or through `--input`:

```sh
./gqlcrawl seeds --source SOURCE your-approved-host.example
```

Seeds are normalized, deduplicated, and truncated to `--limit` before they are written. Host seeds are lowercased with any wildcard label and trailing dot removed; URL seeds keep the probe pipeline's sanitization, so userinfo is dropped and query values become `REDACTED`. Emitting a seed is not authorization to contact it.

App store sources:

| Source | Ranks | Credentials | Notes |
| --- | --- | --- | --- |
| `applecharts` | ordinal | none | App Store top charts joined to each app's publisher site. `--option feed=top-free\|top-paid`, `--option country=us,gb,de`. |

`applecharts` emits the publisher's own site, which is not the app's backend API host. Recovering backend hosts would require analyzing the app binary; the supported path is to feed these domains to `crawl`, which already looks for GraphQL evidence on them. Apple documents roughly twenty lookup calls per minute, so lower `--per-host-rps` for wide sweeps. Google Play has no official charts API and the endpoint its scrapers use is disallowed by that site's `robots.txt`, so no Play source is provided.

Sources that require credentials read them from the environment and fail closed when they are unset. `--list-sources` reports each adapter's required variables and whether it consumes paid credits or query spend. Credentials are never written to output.

```text
--source NAME
--list-sources
--limit 1000
--option KEY=VALUE
--input FILE|-
--format lines|jsonl
--max-download-bytes 67108864
--per-host-rps 1
--timeout 30s
--denylist FILE
--contact VALUE
--allow-http=false
```

Exit code `0` means the corpus was read and the available seeds were written. A source that fails or returns partial data still writes what it collected and exits `1`. Exit `2` means the configuration was invalid.

## JSONL

`probe` keeps one source record per input. `crawl` emits one record per unique discovered endpoint, with the sanitized seed in `source.input` and the page or script that supplied the evidence in `source.evidence_url`. Duplicate normalized endpoints are probed once.

```json
{"schema_version":"1","endpoint":"https://api.example.invalid/graphql","source":{"kind":"crawl","input":"https://www.example.invalid/","evidence_url":"https://www.example.invalid/app.js"},"checked_at":"2026-08-07T07:00:00Z","http":{"status":200,"content_type":"application/json","bytes":52},"graphql":"confirmed","introspection":"enabled","query_type":"Query","reason":"introspection_enabled"}
```

`seeds --format jsonl` emits one record per seed with its originating adapter and, where the corpus supplies one, a rank and an evidence string.

```json
{"schema_version":"1","value":"www.example.invalid","kind":"host","adapter":"example","rank":42,"rank_kind":"ordinal"}
```

Corpora do not agree on what a rank means, so `rank_kind` states which one applies. `ordinal` is a meaningful 1..N position. `bucket` means the value is a magnitude ceiling and membership within it is unordered. `member` means the corpus supplies set membership only, and `rank` is omitted. A seed with no rank at all omits both fields.

URL userinfo is removed and displayed query values are replaced with `REDACTED`. Discovered candidate query strings are dropped before probing. Headers, response bodies, and schema details never enter output.

Stable reasons currently include:

- `introspection_enabled`, `introspection_rejected`
- `policy_rejected`, `dns_non_public`, `redirect_rejected`, `robots_disallowed`
- `timeout`, `response_too_large`, `http_error`
- `non_graphql_response`, `malformed_graphql_response`

## Denylist and opt-out

A local denylist contains one hostname per line. Use a leading dot or wildcard to match subdomains:

```text
example.com
.example.org
*.example.net
```

See [docs/OPT_OUT.md](docs/OPT_OUT.md) for project-level opt-out requests. Operators should also honor direct requests immediately with `--denylist` rather than waiting for a release.

## Development

Keep tests offline and deterministic. Changes that broaden discovery sources, traffic volume, query scope, stored data, or schema output require their own review.

Contributions are welcome through issues and pull requests. Security reports follow [SECURITY.md](SECURITY.md).

## License

MIT
