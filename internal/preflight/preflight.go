// Package preflight implements the agent's `--check` mode: a set of read-only
// diagnostics that verify the router API and every configured exporter endpoint
// are reachable, then print a clear pass/fail report and exit.
//
// This exists because debugging inside a RouterOS container is painful: the
// image is `scratch` (no shell, no tools), so the only signal is the log. A
// deterministic preflight that says exactly which step failed — and why — turns
// a frustrating black box into a two-minute fix.
package preflight

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/mikrotik"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// Result is the outcome of a single check.
type Result struct {
	Name    string        // short check name, e.g. "router-api"
	OK      bool          // whether the check passed
	Detail  string        // human-readable success/failure detail
	Elapsed time.Duration // how long the check took
}

// Report is the full set of check results.
type Report struct {
	Results []Result
}

// OK reports whether every check passed.
func (r Report) OK() bool {
	for _, res := range r.Results {
		if !res.OK {
			return false
		}
	}
	return true
}

// checkFunc runs one check and returns (detail, error). A nil error means pass.
type checkFunc func(ctx context.Context) (string, error)

// Run executes all preflight checks against the given configuration and returns
// a Report. It never mutates the router or any backend — every probe is a
// read-only connect or a harmless RouterOS print command.
func Run(ctx context.Context, cfg config.Config) Report {
	var report Report

	add := func(name string, fn checkFunc) {
		start := time.Now()
		detail, err := fn(ctx)
		res := Result{Name: name, Elapsed: time.Since(start)}
		if err != nil {
			res.OK = false
			res.Detail = err.Error()
		} else {
			res.OK = true
			res.Detail = detail
		}
		report.Results = append(report.Results, res)
	}

	// 1. Router API: connect, authenticate, and run a read-only probe for every
	// configured router.
	for _, rc := range cfg.Routers {
		rc := rc
		add("router:"+rc.Name, func(ctx context.Context) (string, error) {
			return checkRouter(ctx, rc)
		})
	}

	// 2. Each exporter's endpoint: DNS + TCP (+ TLS) reachability.
	for _, ep := range exporterEndpoints(cfg) {
		ep := ep
		add("exporter:"+ep.exporter, func(ctx context.Context) (string, error) {
			return checkEndpoint(ctx, ep.rawURL)
		})
	}

	return report
}

// checkRouter connects to one router's RouterOS API and runs
// /system/resource/print, returning the board name and version on success.
func checkRouter(ctx context.Context, rc config.RouterConfig) (string, error) {
	// Use a discard logger; preflight prints its own report.
	client := mikrotik.New(rc, discardLogger())
	defer func() { _ = client.Close() }()

	dialCtx := ctx
	if rc.DialTimeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, rc.DialTimeout+2*time.Second)
		defer cancel()
	}

	reply, err := client.Run(dialCtx, "/system/resource/print")
	if err != nil {
		return "", fmt.Errorf("cannot reach RouterOS API at %s as user %q: %w (is the API service enabled and the address allow-list correct?)",
			rc.Address, rc.Username, err)
	}
	if len(reply.Re) == 0 {
		return fmt.Sprintf("connected to %s (no resource data returned)", rc.Address), nil
	}
	m := reply.Re[0].Map
	board := m["board-name"]
	version := m["version"]
	arch := m["architecture-name"]
	return fmt.Sprintf("connected to %s — board %q, RouterOS %q, arch %q",
		rc.Address, board, version, arch), nil
}

// endpoint pairs an exporter name with the URL to probe.
type endpoint struct {
	exporter string
	rawURL   string
}

// exporterEndpoints returns the endpoint URLs the configured exporters will push
// to, so each can be reachability-tested. Unknown/misconfigured exporters yield
// no endpoint (the build step reports those separately).
func exporterEndpoints(cfg config.Config) []endpoint {
	var eps []endpoint
	seen := map[string]bool{}
	for _, name := range expandExporters(cfg.Exporters) {
		if seen[name] {
			continue
		}
		seen[name] = true
		switch name {
		case "otlp":
			if v := cfg.Env("OTLP_ENDPOINT"); v != "" {
				eps = append(eps, endpoint{name, v})
			}
		case "prometheus":
			if v := cfg.Env("PROMETHEUS_ENDPOINT"); v != "" {
				eps = append(eps, endpoint{name, v})
			}
		case "loki":
			if v := cfg.Env("LOKI_ENDPOINT"); v != "" {
				eps = append(eps, endpoint{name, v})
			}
		}
	}
	return eps
}

// expandExporters expands the known "grafanacloud" alias so preflight probes the
// concrete endpoints. Keeping this local avoids a dependency on the export
// registry (and its global registration side effects).
func expandExporters(names []string) []string {
	var out []string
	for _, n := range names {
		if n == "grafanacloud" {
			out = append(out, "prometheus", "loki")
			continue
		}
		out = append(out, n)
	}
	return out
}

// checkEndpoint verifies DNS resolution and TCP (and TLS, for https) reachability
// of an exporter endpoint. It deliberately does NOT send telemetry — a
// successful handshake is enough to prove the network path, DNS, and (for HTTPS)
// the certificate chain are working, which are the usual on-router failures.
func checkEndpoint(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid endpoint URL %q: %w", rawURL, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("endpoint URL %q has no host", rawURL)
	}

	scheme := strings.ToLower(u.Scheme)
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	address := net.JoinHostPort(host, port)

	dialCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	// DNS resolution (surfaced separately because "no DNS" is the single most
	// common on-router failure for the outbound push).
	if net.ParseIP(host) == nil {
		if _, err := net.DefaultResolver.LookupHost(dialCtx, host); err != nil {
			return "", fmt.Errorf("DNS lookup for %q failed: %w (set /ip/dns servers on the router, or pass dns= to the container)", host, err)
		}
	}

	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(dialCtx, "tcp", address)
	if err != nil {
		return "", fmt.Errorf("TCP connect to %s failed: %w", address, err)
	}
	defer func() { _ = conn.Close() }()

	if scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: host})
		defer func() { _ = tlsConn.Close() }()
		if err := tlsConn.HandshakeContext(dialCtx); err != nil {
			return "", fmt.Errorf("TLS handshake with %s failed: %w (is the CA trusted and the clock correct?)", address, err)
		}
		return fmt.Sprintf("reachable — TLS handshake OK to %s", address), nil
	}
	return fmt.Sprintf("reachable — TCP connect OK to %s", address), nil
}
