# Endpoint opt-out

Website and API operators can request that a hostname be added to `gqlcrawl`'s bundled denylist.

1. Open an issue titled `Opt out: example.com` without including credentials or private endpoint details.
2. State the hostname or subdomain boundary and your relationship to it.
3. Be prepared to verify control through an agreed public method such as a DNS record.
4. Maintainers add verified requests to `internal/network/denylist.txt` and publish them in the next release.

A leading-dot entry, such as `.example.com`, covers subdomains but not the parent hostname. Exact parent and wildcard rules may both be added when requested.

Operators running the tool should honor a direct request immediately by adding the hostname to a local file and passing `--denylist FILE`. The bundled list is a minimum, not a substitute for local policy.
