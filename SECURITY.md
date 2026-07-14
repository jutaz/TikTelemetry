# Security Policy

## Reporting a vulnerability

If you believe you have found a security vulnerability in TikTelemetry, please
report it privately rather than opening a public issue. Use GitHub's
[private vulnerability reporting](https://github.com/jutaz/tiktelemetry/security/advisories/new)
for this repository. Include:

- a description of the issue and its impact,
- steps to reproduce (a proof of concept if possible),
- affected version(s) or commit.

You will receive an acknowledgement, and a fix or mitigation will be coordinated
before public disclosure.

## Threat model and audit

TikTelemetry handles router credentials and backend push tokens and reads data
from a semi-trusted RouterOS device. The design's trust boundaries, the
adversary positions considered, and the current defenses are documented in the
red-team security audit:

- [docs/security-audit.md](docs/security-audit.md)

## Security posture at a glance

- **Least privilege:** the documented setup provisions a read-only RouterOS API
  user (`policy=api,read,test`) — a compromised agent cannot change the router.
- **TLS on by default:** backend and router connections verify certificates
  unless an operator explicitly opts out per connection.
- **No secret leakage:** credentials are never logged or printed.
- **Minimal runtime:** static `scratch` image, `CGO_ENABLED=0`, no shell or OS
  packages; the production binary links only `go-routeros`, `klauspost/compress`,
  and the OpenTelemetry SDK.
- **Automated checks:** CI runs `govulncheck` on every push, and Dependabot keeps
  dependencies current.

## Handling secrets

Configuration (including `ROUTER_PASS` and backend tokens) is supplied via
environment variables. On RouterOS these are stored in plaintext in the device
configuration; treat the router config as sensitive and prefer a least-privilege
API user. See the "Running on RouterOS" section of the README.
