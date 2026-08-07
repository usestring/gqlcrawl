# gqlcrawl

Find public GraphQL endpoints that expose introspection, without turning discovery into exploitation.

`gqlcrawl` is an early-stage Go CLI from [String](https://www.usestring.ai/). The current version implements the safe seed-driven `probe` foundation: give it exact public endpoint URLs and it sends one standard read-only introspection-availability query. Evidence crawling, corpus adapters, and explicit full-schema export are planned as separately reviewed additions.

## Safety first

Only probe endpoints you are authorized to contact. You are responsible for applicable law, terms, and organizational policy.

The CLI intentionally has no flags for authentication headers, cookies, mutations, field guessing, or application-data queries. It also does not publish a hosted endpoint catalog. Every request uses:

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
- Redirects are revalidated, limited to two, and rejected if they change the POST method.
- One in-flight request and one request per second per host.
- Sixteen global workers, five unique candidates per host, a 10-second timeout, a 64 KiB response cap, and no retries.
- An identifiable project user agent with optional `--contact` text.
- A bundled project opt-out list plus an optional local `--denylist`.

A block, timeout, authentication response, rate limit, redirect rejection, or transport error is always `indeterminate`. The CLI reports `disabled` only when a GraphQL-shaped response explicitly says introspection is rejected.

## Build and verify

Go 1.24.5 or newer is required.

```sh
go build ./cmd/gqlcrawl
go test -race ./...
go vet ./...
```

These commands are offline. The test suite uses injected DNS and local fixtures and does not probe the public internet.

## Use

The examples use reserved `.example` names, so they cannot contact a real endpoint.

Probe URL arguments:

```sh
./gqlcrawl probe https://your-approved-host.example/graphql
```

Read exact candidates from stdin or a file:

```sh
printf '%s\n' 'https://your-approved-host.example/graphql' |
  ./gqlcrawl probe --input -
./gqlcrawl probe --input approved-endpoints.txt
```

Options may appear before or after URL arguments:

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

Exit code `0` means every input produced a JSONL record; it does not mean introspection was enabled. Exit `1` means execution was incomplete, and exit `2` means the CLI configuration was invalid.

## JSONL

Each input produces one ordered record. Duplicate normalized endpoints are probed once but retain their individual source records.

```json
{"schema_version":"1","endpoint":"https://api.example.invalid/graphql","source":{"kind":"argument","input":"https://api.example.invalid/graphql"},"checked_at":"2026-08-07T07:00:00Z","http":{"status":200,"content_type":"application/json","bytes":52},"graphql":"confirmed","introspection":"enabled","query_type":"Query","reason":"introspection_enabled"}
```

URL userinfo is removed and query values are replaced with `REDACTED`. Headers, response bodies, and schema details never enter probe output.

Stable reasons currently include:

- `introspection_enabled`, `introspection_rejected`
- `policy_rejected`, `dns_non_public`, `redirect_rejected`
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
