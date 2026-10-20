# Security Policy

## Supported versions

| Version | Supported |
|---|---|
| 0.1.x | Yes |
| Unreleased `main` | Best effort |

## Reporting a vulnerability

Please do not open a public issue. Use GitHub private vulnerability reporting:
the "Report a vulnerability" button on the Security tab of
https://github.com/thnhdatle/triage.

We aim to acknowledge a report within 72 hours.

## Scope notes

Triage masks only detectable PII formats, and the optional NER detector is
English only. Undetected personal data reaching a cloud target is a documented
limit (see the README), not a vulnerability. A bypass of a documented guarantee
(for example unmasked detected PII reaching a cloud target in `mask` or
`local_only` mode) is in scope.

## Our practices

- gitleaks on every commit in CI and in `make lint`.
- `govulncheck` in CI; findings that affect called code fail the build.
- `trivy` on built images: 0 critical and 0 high findings with a fix available.
- Containers use a distroless base and run as a non-root user.
