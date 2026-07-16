package preflight

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jutaz/tiktelemetry/internal/config"
)

// loadCfg builds a config.Config from env for tests. ROUTER_PASS is required by
// config.Load, so it is always set.
func loadCfg(t *testing.T, env map[string]string) config.Config {
	t.Helper()
	t.Setenv("ROUTER_PASS", "secret")
	for k, v := range env {
		t.Setenv(k, v)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func TestExpandExporters(t *testing.T) {
	got := expandExporters([]string{"otlp", "grafanacloud", "loki"})
	want := []string{"otlp", "prometheus", "loki", "loki"}
	if len(got) != len(want) {
		t.Fatalf("expandExporters = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestExporterEndpoints(t *testing.T) {
	cfg := loadCfg(t, map[string]string{
		"EXPORTERS":           "grafanacloud",
		"PROMETHEUS_ENDPOINT": "https://prom.example/api/prom/push",
		"LOKI_ENDPOINT":       "https://loki.example",
	})
	eps := exporterEndpoints(cfg)
	if len(eps) != 2 {
		t.Fatalf("got %d endpoints, want 2: %+v", len(eps), eps)
	}
	byExporter := map[string]string{}
	for _, e := range eps {
		byExporter[e.exporter] = e.rawURL
	}
	if byExporter["prometheus"] != "https://prom.example/api/prom/push" {
		t.Errorf("prometheus endpoint = %q", byExporter["prometheus"])
	}
	if byExporter["loki"] != "https://loki.example" {
		t.Errorf("loki endpoint = %q", byExporter["loki"])
	}
}

func TestExporterEndpointsSkipsUnset(t *testing.T) {
	// otlp enabled but no OTLP_ENDPOINT -> no endpoint probe (Build reports the
	// missing config separately; preflight simply can't reach what isn't set).
	cfg := loadCfg(t, map[string]string{"EXPORTERS": "otlp"})
	if eps := exporterEndpoints(cfg); len(eps) != 0 {
		t.Errorf("expected no endpoints when OTLP_ENDPOINT unset, got %+v", eps)
	}
}

func TestCheckEndpointHTTPReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	detail, err := checkEndpoint(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("checkEndpoint(%s) failed: %v", srv.URL, err)
	}
	if !strings.Contains(detail, "reachable") {
		t.Errorf("detail = %q, want it to mention reachable", detail)
	}
}

func TestCheckEndpointTLSUntrustedFailsClearly(t *testing.T) {
	// httptest's TLS server presents a self-signed certificate that is not in
	// the system trust store. The preflight check must surface this as a TLS
	// verification failure with an actionable hint, which is exactly the signal
	// a user needs when the router's clock is wrong or a CA is missing.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := checkEndpoint(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected a TLS verification error for a self-signed server")
	}
	if !strings.Contains(err.Error(), "TLS handshake") {
		t.Errorf("error = %q, want it to mention TLS handshake", err)
	}
}

func TestCheckEndpointUnreachable(t *testing.T) {
	// Port 1 on localhost is not listening.
	_, err := checkEndpoint(context.Background(), "http://127.0.0.1:1")
	if err == nil {
		t.Fatal("expected an error connecting to a closed port")
	}
	if !strings.Contains(err.Error(), "TCP connect") {
		t.Errorf("error = %q, want it to mention TCP connect", err)
	}
}

func TestCheckEndpointBadURL(t *testing.T) {
	if _, err := checkEndpoint(context.Background(), "://not a url"); err == nil {
		t.Fatal("expected an error for a malformed URL")
	}
	if _, err := checkEndpoint(context.Background(), "https://"); err == nil {
		t.Fatal("expected an error for a URL with no host")
	}
}

func TestReportOK(t *testing.T) {
	r := Report{Results: []Result{{OK: true}, {OK: true}}}
	if !r.OK() {
		t.Error("Report.OK() = false, want true when all pass")
	}
	r.Results = append(r.Results, Result{OK: false})
	if r.OK() {
		t.Error("Report.OK() = true, want false when one fails")
	}
}

func TestRunReportsRouterAndEndpoints(t *testing.T) {
	// A reachable HTTP endpoint plus an unreachable router: Run should return
	// two results (router-api + exporter) with the router failing fast.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := loadCfg(t, map[string]string{
		"ROUTER_ADDRESS":      "127.0.0.1:1",
		"ROUTER_DIAL_TIMEOUT": "200ms",
		"EXPORTERS":           "otlp",
		"OTLP_ENDPOINT":       srv.URL,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report := Run(ctx, cfg)

	if len(report.Results) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(report.Results), report.Results)
	}
	if report.OK() {
		t.Error("expected overall failure (router unreachable)")
	}
	// The router check must be first and failing; exporter must pass.
	if !strings.HasPrefix(report.Results[0].Name, "router:") || report.Results[0].OK {
		t.Errorf("first result = %+v, want a failing router: check", report.Results[0])
	}
	if !strings.HasPrefix(report.Results[1].Name, "exporter:") || !report.Results[1].OK {
		t.Errorf("second result = %+v, want passing exporter", report.Results[1])
	}
}
