# Contributing to TikTelemetry

Thanks for your interest in improving TikTelemetry. This document covers the
development workflow, project layout, and how to add new collectors or
exporters.

## Prerequisites

- Go 1.25 or newer.
- Docker (only for the end-to-end tests).
- Optional: [`golangci-lint`](https://golangci-lint.run) for linting.

## Common tasks

Everything is wired through the `Makefile`:

```sh
make build        # build the host binary
make test         # unit tests
make test-race    # unit tests with the race detector
make cover        # per-package coverage
make lint         # golangci-lint (needs golangci-lint installed)
make vet          # go vet, including e2e-tagged files
make fmt          # gofmt all sources
make check        # fmt + vet + lint + test
make test-e2e     # end-to-end tests against a real RouterOS (needs Docker)
```

Before opening a pull request, run `make check`. CI runs the same checks plus
`govulncheck`, a cross-compile matrix, and the e2e suite.

## Project layout

```
cmd/tiktelemetry/         Entry point: config load, logging, wiring, run loop.
internal/config/          Env-var configuration and per-exporter accessors.
internal/model/           Backend-agnostic Sample and LogEntry types.
internal/mikrotik/        RouterOS client + collectors (metrics and logs).
internal/export/          Sink interface, registry, MultiSink fan-out.
internal/export/otlp/     OTLP metrics + logs adapter.
internal/export/prometheus/  Prometheus remote_write adapter.
internal/export/loki/     Loki push adapter.
internal/exporters/       Composition root: registers adapters + aliases.
internal/agent/           Scrape loop tying collectors to sinks.
internal/preflight/       Read-only diagnostics for the `--check` mode.
test/e2e/                 End-to-end tests (build tag `e2e`).
scripts/                  RouterOS setup and container deployment scripts.
```

## Adding a metric collector

1. Implement the `mikrotik.Collector` interface in `internal/mikrotik/`
   (a good template is `collectors_extra.go`):

   ```go
   type myCollector struct{}

   func (c *myCollector) Name() string { return "mycollector" }

   func (c *myCollector) Collect(ctx context.Context, r Runner) ([]model.Sample, error) {
       reply, err := r.Run(ctx, "/some/routeros/path/print")
       if err != nil {
           return nil, err
       }
       // ... map reply.Re rows to model.Sample values ...
       return samples, nil
   }
   ```

2. Register it in `DefaultCollectors()` and add a compile-time interface check.

3. **Be resilient.** If the endpoint may not exist on all hardware (e.g.
   wireless on a CHR), catch the "no such command" error via
   `isUnavailableCommand` and return `nil, nil` instead of failing the scrape.

4. Add unit tests with the in-package `fakeRunner`, and an e2e assertion in
   `test/e2e/extra_collectors_test.go` (at minimum: the collector must not
   error against a live CHR).

5. Document the new metrics in the README `Metrics` section.

## Adding an exporter

1. Create `internal/export/<name>/` implementing `export.Sink`
   (`Name`, `Capabilities`, `ConsumeMetrics`, `ConsumeLogs`, `Shutdown`).

2. Register it in that package's `init()`:

   ```go
   func init() { export.Register("<name>", New) }
   ```

3. Add a blank import to `internal/exporters/exporters.go` so the registration
   runs. If it belongs in a convenience bundle, extend an alias there.

4. Read configuration through `config.Config`'s `Env*` helpers using a unique
   prefix (e.g. `MYSINK_ENDPOINT`).

5. Add tests (an `httptest` server is the usual approach for HTTP push sinks)
   and document the config variables in the README.

## Metric naming conventions

- Use dotted, OpenTelemetry-style names: `mikrotik.<area>.<thing>`.
- Prefer `model.KindCounter` for monotonic cumulative values (byte/packet
  totals, uptime) and `model.KindGauge` for point-in-time values.
- Use UCUM units: `By` (bytes), `%`, `Cel`, `V`, `A`, `W`, `s`, `1`.
- Put dimensional data in `Attributes`, not in the metric name.

## Commit style

Keep commits focused and scoped. Write imperative subject lines
("Add health collector", not "Added ..."), and explain the *why* in the body
when it is not obvious.
