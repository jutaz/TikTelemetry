# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project aims
to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - Initial release

First public release of TikTelemetry — a lightweight Go agent that pulls metrics
and logs from MikroTik RouterOS devices and pushes them directly to a telemetry
backend, with no intermediary agent.

### Added

- **Multi-router hub.** One agent scrapes any number of routers in parallel
  (`ROUTERS=name@host:port,...`), stamping each router's telemetry with a
  `target` label. Shared credentials with optional per-router
  `ROUTER_<NAME>_*` overrides. Bounded concurrency via
  `ROUTER_SCRAPE_CONCURRENCY`.
- **Composable exporters.** OTLP/HTTP, Prometheus remote_write, and Loki push,
  selectable and combinable via `EXPORTERS` (plus the `grafanacloud` bundle).
- **Collectors.** system, interface (bytes/packets/errors/drops/link-downs/up),
  health sensors, DHCP leases, connection tracking, IP address/route counts,
  firewall filter counters, wireless clients, WireGuard peers, IPsec peers, PPP
  sessions, and simple-queue stats. Selectable via `COLLECTORS`.
- **Log tailing** with `.id`-based de-duplication and router-clock/timezone
  timestamp resolution.
- **Agent self-metrics** (`tiktelemetry.*`, per target) — scrape health, router
  reachability, and error counters — emitted through the same pipeline.
- **`--check` preflight** that verifies every router API and exporter endpoint
  is reachable, and prints a clear pass/fail report.
- **Static `scratch` container** (~14 MB, CGO-free) for `linux/amd64`,
  `linux/arm64`, and `linux/arm/v7`; runs off-router or inside RouterOS v7's
  container feature.
- **RouterOS scripts** for the least-privilege API user and on-router container
  deployment.
- **Grafana dashboard** (`dashboards/tiktelemetry.json`).
- **Docs & rigor:** README, security audit, `--check`, symtether-verified doc
  references, and CI (lint, race tests, vuln scan, cross-compile, and an
  end-to-end suite against a real Cloud Hosted Router).

[Unreleased]: https://github.com/jutaz/TikTelemetry/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/jutaz/TikTelemetry/releases/tag/v0.1.0
