# Security policy

## Reporting a vulnerability

Do not open a public issue for a vulnerability that could expose a target or enable abuse.

Use the repository's **Security** tab and **Report a vulnerability** flow to contact the maintainers privately. If that flow is unavailable, open a non-sensitive issue asking the maintainers to establish private contact; do not include vulnerability details in the issue. Include the affected version or commit, reproduction steps that use a local fixture where possible, impact, and a proposed mitigation in the private report.

## Scope

Security-sensitive behavior includes public-address validation, DNS pinning, redirect handling, TLS verification, robots enforcement, evidence classification, crawl and traffic bounds, output redaction, query scope, and denylist enforcement.

The maintainers do not authorize testing against third-party systems. Use an offline fixture or a target you control.
