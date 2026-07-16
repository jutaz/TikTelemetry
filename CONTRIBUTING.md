# Contributing to TikTelemetry

Thanks for your interest in improving TikTelemetry. This document covers the
development workflow, project layout, and how to add new collectors or
exporters.

## Prerequisites

- Go 1.25 or newer.
- Docker (only for the end-to-end tests).
- Node/`npx` (for the doc-reference check in `make docs` / `make check`).
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

1. Implement the [`Collector`](internal/mikrotik/collector.go#sym:type:Collector) interface in `internal/mikrotik/`
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

2. Register it in [`DefaultCollectors`](internal/mikrotik/collector.go#sym:fn:DefaultCollectors) and add a compile-time interface check.

3. **Be resilient.** If the endpoint may not exist on all hardware (e.g.
   wireless on a CHR), catch the "no such command" error via
   [`isUnavailableCommand`](internal/mikrotik/collectors_extra.go#sym:fn:isUnavailableCommand) and return `nil, nil` instead of failing the scrape.

4. Add unit tests with the in-package `fakeRunner`, and an e2e assertion in
   `test/e2e/extra_collectors_test.go` (at minimum: the collector must not
   error against a live CHR).

5. Document the new metrics in the README `Metrics` section.

## Adding an exporter

1. Create `internal/export/<name>/` implementing the [`Sink`](internal/export/sink.go#sym:type:Sink)
   interface (`Name`, `Capabilities`, `ConsumeMetrics`, `ConsumeLogs`, `Shutdown`).

2. Register it in that package's `init()` with [`Register`](internal/export/sink.go#sym:fn:Register):

   ```go
   func init() { export.Register("<name>", New) }
   ```

3. Add a blank import to `internal/exporters/exporters.go` so the registration
   runs. If it belongs in a convenience bundle, extend an alias with
   [`RegisterAlias`](internal/export/sink.go#sym:fn:RegisterAlias).

4. Read configuration through [`Config`](internal/config/config.go#sym:type:Config)'s
   [`Env`](internal/config/config.go#sym:fn:Env) helpers using a unique
   prefix (e.g. `MYSINK_ENDPOINT`).

5. Add tests (an `httptest` server is the usual approach for HTTP push sinks)
   and document the config variables in the README.

## Metric naming conventions

- Use dotted, OpenTelemetry-style names: `mikrotik.<area>.<thing>`.
- Prefer [`KindCounter`](internal/model/model.go#sym:const:KindCounter) for monotonic cumulative values (byte/packet
  totals, uptime) and [`KindGauge`](internal/model/model.go#sym:const:KindGauge) for point-in-time values.
- Use UCUM units: `By` (bytes), `%`, `Cel`, `V`, `A`, `W`, `s`, `1`.
- Put dimensional data in `Attributes`, not in the metric name.

## Documentation references

The docs link to code symbols with [`#sym:` references](https://symtether.dev/spec)
(e.g. `[Sink](internal/export/sink.go#sym:type:Sink)`). These render as normal
links on GitHub and are verified against the actual code in CI, so they never
rot silently.

If you rename or move a symbol referenced from the docs, update the refs:

```sh
npx symtether check   # list broken references
npx symtether fix     # propose repairs (add --write to apply)
```

### Staleness (`symtether.sum`)

A committed `symtether.sum` holds a content hash for each referenced symbol
(like `go.sum`, it stores derived checksums, not decisions). CI runs
`check --strict`, which flags a reference as **stale** when its target's
implementation changed — a prompt to re-read the surrounding prose and confirm
it is still accurate. Formatting and renames do not trigger this; only a change
to the symbol's actual content does.

When you intentionally change a referenced symbol, review the docs that point at
it, then re-stamp:

```sh
make docs          # check --strict + verify the sum is current
make docs-update   # re-generate symtether.sum, then commit it
```

When documenting code, prefer a `#sym:` link over pasting a snippet or citing a
line number.

## Commit style

Keep commits focused and scoped. Write imperative subject lines
("Add health collector", not "Added ..."), and explain the *why* in the body
when it is not obvious.

## Releasing

The git tag is the version — there is no `VERSION` file. At runtime the binary
reports its version from the module build info
([`runtime/debug.ReadBuildInfo`](https://pkg.go.dev/runtime/debug#ReadBuildInfo)):
the tag for a released or `go install`ed build, or a `dev-<revision>` string for
a local build. The release workflow also stamps the tag in explicitly via
`-ldflags -X main.version`.

Releases are driven by **GitHub Releases**. To cut one:

1. Update `CHANGELOG.md` (move the `Unreleased` entries under the new version)
   and merge it.
2. Publish a GitHub Release for the new tag — in the UI (**Releases → Draft a
   new release → choose a tag `vX.Y.Z` → Generate release notes → Publish**),
   or with `gh release create vX.Y.Z --generate-notes`. Creating the Release
   creates the git tag.

Publishing the Release fires the **Release** workflow, which builds and pushes
the multi-arch image to GHCR (tagging `latest` for non-prereleases) and attaches
the per-architecture offline tarballs to that same Release. A manual
`workflow_dispatch` on the Release workflow can re-build the artifacts for an
existing tag if a build needs retrying.
