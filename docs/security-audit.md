# Red-team security audit

This is an adversarial review of TikTelemetry: what an attacker in each position
could do, what the code actually does today, and the residual risk. It is
evidence-based — every claim is tied to a specific code path — and is refreshed
when the attack surface changes.

Last reviewed against commit at the time of writing. Re-run the checks in
[Verification](#verification) after security-relevant changes.

## Assets and trust boundaries

TikTelemetry sits between three parties and holds credentials for two of them:

| Asset | Sensitivity |
|-------|-------------|
| RouterOS API credentials (`ROUTER_USER`/`ROUTER_PASS`) | High — router control-plane access |
| Backend push tokens (`*_PASS`, `*_BEARER_TOKEN`, `*_HEADERS`) | High — can write to your metrics/logs tenant |
| Router telemetry (metrics, log lines) | Low–Medium — may contain client IPs/MACs |

Trust boundaries:

1. **Agent ↔ RouterOS API.** The router is *semi-trusted*: it is the thing being
   monitored, and a compromised or hostile router is exactly the scenario
   monitoring must survive. All RouterOS API responses are attacker-controllable
   input.
2. **Agent ↔ telemetry backend.** Outbound HTTPS. The backend is trusted; the
   network path is not.
3. **Operator ↔ agent.** Configuration via environment variables. On RouterOS
   these live in the device config (plaintext) — see the deployment note below.

## Adversary positions considered

- **A1 — Malicious/compromised RouterOS device** feeding crafted API responses.
- **A2 — Network attacker** on the path to the router or the backend (MITM,
  DNS spoofing).
- **A3 — Local attacker / co-tenant** with read access to the process
  environment, container config, or logs.
- **A4 — Supply-chain attacker** via a dependency or the build/release pipeline.
- **A5 — Malicious operator input** (a hostile or mistaken value in an env var).

---

## Findings

Severity reflects likelihood × impact in a realistic edge deployment. Status is
one of: **OK** (defended), **Accepted** (documented residual risk), **Fix**
(actionable hardening applied or recommended).

### F1 — Backend/router credentials never logged or printed — OK

**A3.** Passwords and tokens are the crown jewels. Verified they do not leak
into agent output:

- The config struct is never dumped with `%+v`/`%v`. Logging uses explicit
  structured fields; the only router field logged is `address`
  ([`Client.dial`](../internal/mikrotik/client.go#sym:fn:dial)).
- The preflight report prints the router *address* and *username* on failure,
  never the password ([`checkRouter`](../internal/preflight/preflight.go#sym:fn:checkRouter)).
- Auth headers are constructed in the exporter factories and stored in a private
  `headers` map that is never logged.
- `grep` for any `Sprintf`/log of `Password`/`*_PASS` finds only assignment and
  the empty-check in [`validate`](../internal/config/config.go#sym:fn:validate),
  no output.

**Residual:** backend error bodies are echoed in returned errors (see F7); those
are backend-controlled, not credential-bearing.

### F2 — Untrusted RouterOS responses cannot inject into metric/log queries — OK

**A1.** A hostile router controls every field it returns (`message`, `topics`,
`.id`, interface names, comments, remote addresses, …). Where do these flow?

- **Prometheus label *names*** are sanitised to `[a-zA-Z_][a-zA-Z0-9_]*` via
  [`SanitizeLabelName`](../internal/export/exporthelp/exporthelp.go#sym:fn:SanitizeLabelName)
  before becoming labels, so a crafted attribute key cannot forge a new label
  structure or break the protobuf.
- **Label/attribute *values*** are sent verbatim, which is correct: Prometheus
  and Loki treat label and log-line values as opaque UTF-8. There is no query
  language into which a value is interpolated, so there is no "PromQL/LogQL
  injection" primitive here. Empty values are dropped (protobuf strings must be
  non-empty).
- **Log `message`** becomes a Loki log-line *value* and an OTLP log *body* — never
  a label — so it cannot forge stream labels
  ([`buildPayload`](../internal/export/loki/loki.go#sym:fn:buildPayload)).
- **`topics`** become a structured-metadata value / OTLP attribute value, not a
  label name.

**Verdict:** no injection into the query plane. The worst a hostile router can do
is emit misleading *content*, which is inherent to monitoring an untrusted
device.

### F3 — Resource exhaustion from a hostile router — Fix (defended)

**A1.** A malicious router could return an enormous `/log/print` or
`/interface/print` reply to exhaust agent memory. Mitigations:

- Each scrape is time-bounded by the poll interval
  ([`Agent.scrape`](../internal/agent/agent.go#sym:fn:scrape) derives a context
  timeout), so a slow/hung response cannot stall the loop indefinitely.
- `GOMEMLIMIT` is set to `24MiB` in the image, so the Go runtime GCs aggressively
  and the process is far more likely to slow than to OOM-kill the router.
- **Reply-row cap:** every RouterOS reply is truncated to `ROUTER_MAX_REPLY_ROWS`
  (default 10 000) at the single client choke point
  ([`capRows`](../internal/mikrotik/client.go#sym:fn:capRows) in `Client.Run`),
  which bounds the downstream amplification into `model.Sample` slices
  independent of `GOMEMLIMIT` and logs a warning when it trips. `0` disables the
  cap for operators who genuinely have very large tables.

**Residual:** the library still reads the full reply into memory before we
truncate; capping the wire read itself would require patching go-routeros. The
larger amplification (sample generation) is now bounded, and the attacker already
owns the monitored router, so this residual is minor.

### F4 — TLS verification is on by default; opt-out is explicit and scoped — OK

**A2.** MITM resistance depends on TLS verification:

- **Backend push** (Prometheus/Loki/OTLP) uses the default `http.Client`, which
  verifies certificates against the system trust store. Verification is only
  disabled when the operator explicitly sets `*_INSECURE_SKIP_VERIFY=true`, and
  that flag creates a *dedicated* client so it can never bleed into another
  exporter ([`New`](../internal/export/prometheus/prometheus.go#sym:fn:New)).
- **Router API TLS** likewise verifies unless `ROUTER_TLS_INSECURE=true`.
- The scratch image ships `ca-certificates.crt`, so verification works with no
  extra setup.

**Residual:** the insecure flags exist because RouterOS API-SSL commonly uses a
self-signed certificate on a trusted local link. This is a deliberate,
documented trade-off (README security note), and defaults are safe.

### F5 — Preflight `--check` is not an SSRF amplifier — OK

**A5.** `checkEndpoint` dials arbitrary operator-provided URLs, which in a
different design could be an SSRF gadget. Here it is not exploitable in a
meaningful way because:

- The URLs come only from the operator's own `*_ENDPOINT` env vars — the same
  values the agent already connects to in normal operation. `--check` grants no
  capability the running agent does not already have.
- It performs *only* DNS + TCP + TLS handshake and reads **no** response body
  ([`checkEndpoint`](../internal/preflight/preflight.go#sym:fn:checkEndpoint)),
  so it cannot exfiltrate internal service responses.
- Every probe is time-bounded (8s) and read-only.

### F6 — Secrets at rest on RouterOS are plaintext — Accepted (documented)

**A3.** RouterOS stores container environment variables — including
`ROUTER_PASS` and backend tokens — in plaintext in the device config export.
There is no RouterOS secret vault. This is a platform limitation, not a code
defect. It is called out prominently in the README "Running on RouterOS" section
and the `container-setup.rsc` script, with the mitigation of a least-privilege,
read-only API user (`policy=api,read,test`) so a leaked router credential cannot
change router configuration.

### F7 — Error messages echo backend response bodies (bounded) — OK

**A2/A1.** On a non-2xx push, the exporter includes the response body in the
returned error. This body is read through `io.LimitReader(resp.Body, 1024)`
([Prometheus](../internal/export/prometheus/prometheus.go#sym:fn:ConsumeMetrics),
Loki, OTLP), so a hostile endpoint cannot force an unbounded read into an error
string. The body is backend-controlled, not credential-bearing.

### F8 — Supply chain: production binary has a minimal dependency set — OK

**A4.** The large `go.mod` (docker, containerd, envoy, …) is alarming at a
glance, but those are pulled **only** by the `e2e`-tagged tests via
testcontainers. The production binary links just:

- `github.com/go-routeros/routeros/v3`
- `github.com/klauspost/compress` (snappy)
- the OpenTelemetry SDK + Go stdlib.

Verified with `go list -deps ./cmd/tiktelemetry` (no `testcontainers`, `docker`,
or `containerd`). `govulncheck` over the production code paths reports **no known
vulnerabilities**. CI runs `govulncheck` on every push, and Dependabot (grouped
OTel updates) keeps dependencies current. The release image is `scratch`, so
there are no OS packages to carry CVEs.

### F9 — CGO disabled, static scratch image — OK

**A4.** Builds are `CGO_ENABLED=0` and run `FROM scratch`. No dynamic linker, no
shell, no package manager, no setuid binaries. The container has only the agent
and CA certificates, which drastically shrinks the runtime attack surface and
means `/container/shell` cannot be used against it.

### F10 — Least-privilege router account is the documented default — OK

**A1/A3.** The setup script provisions `policy=api,read,test` — no write, no
policy that can alter the router. Even a fully compromised agent (or leaked
router credential) is confined to reading state. This is the correct blast-radius
control for a monitoring agent and is the documented path.

---

## Summary

| ID | Area | Severity | Status |
|----|------|----------|--------|
| F1 | Credential logging | High | OK |
| F2 | Query injection via router data | High | OK |
| F3 | Router-driven memory exhaustion | Medium | Fix (defended) |
| F4 | Backend/router TLS verification | High | OK |
| F5 | Preflight SSRF | Medium | OK |
| F6 | Plaintext secrets on RouterOS | Medium | Accepted (documented) |
| F7 | Error body echo | Low | OK |
| F8 | Supply chain | Medium | OK |
| F9 | Runtime hardening | Medium | OK |
| F10 | Router account privilege | High | OK |

No High-severity issue is unmitigated. The one remaining Accepted item (F6
plaintext secrets) is a documented platform limitation with a clear mitigation
(least-privilege router account). F3 was hardened during this audit with an
env-tunable reply-row cap.

## Hardening recommendations (non-blocking)

1. Consider redacting `Authorization`-style keys if `*_HEADERS` values are ever
   surfaced in future diagnostics (they are not today).
2. Keep the `e2e` dependency tree out of any future non-test import path so the
   production dependency set stays minimal (guarded by the F8 verification).

## Verification

Re-run after security-relevant changes:

```sh
# Production binary must not pull test-only heavy deps:
go list -deps ./cmd/tiktelemetry | grep -E 'testcontainers|docker/docker|containerd' && echo FAIL || echo OK

# No known vulnerabilities in reachable code:
go run golang.org/x/vuln/cmd/govulncheck@latest ./cmd/... ./internal/...

# No credential material in output paths (manual review of any new logging):
grep -rn 'Password\|_PASS\|Token\|Authorization' internal/ cmd/ | grep -iv test
```
