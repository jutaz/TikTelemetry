//go:build e2e

package e2e

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/snappy"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/export"
	"github.com/jutaz/tiktelemetry/internal/mikrotik"
	"github.com/jutaz/tiktelemetry/internal/model"

	// Register the built-in exporters.
	_ "github.com/jutaz/tiktelemetry/internal/exporters"
)

// captureServer records every request body it receives, for assertions.
type captureServer struct {
	mu       sync.Mutex
	requests [][]byte
	paths    []string
	server   *httptest.Server
}

func newCaptureServer(t *testing.T) *captureServer {
	t.Helper()
	cs := &captureServer{}
	cs.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cs.mu.Lock()
		cs.requests = append(cs.requests, body)
		cs.paths = append(cs.paths, r.URL.Path)
		cs.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(cs.server.Close)
	return cs
}

func (cs *captureServer) count() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return len(cs.requests)
}

func (cs *captureServer) bodies() [][]byte {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	out := make([][]byte, len(cs.requests))
	copy(out, cs.requests)
	return out
}

// TestPipelinePrometheus runs the interface+system collectors against a live
// RouterOS and pushes the results through the real Prometheus remote_write sink
// to a local capture server, asserting a valid snappy+protobuf payload arrives
// carrying a known metric name.
func TestPipelinePrometheus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), apiReadyTimeout+2*time.Minute)
	defer cancel()

	r := startRouter(ctx, t)
	client := newClient(t, r)
	defer client.Close()

	cs := newCaptureServer(t)

	t.Setenv("ROUTER_PASS", "unused-but-required-for-load")
	t.Setenv("EXPORTERS", "prometheus")
	t.Setenv("PROMETHEUS_ENDPOINT", cs.server.URL+"/api/prom/push")
	t.Setenv("PROMETHEUS_USER", "12345")
	t.Setenv("PROMETHEUS_PASS", "token")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sinks, err := export.Build(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("export.Build: %v", err)
	}
	defer sinks.Shutdown(ctx)

	// Collect real samples and push them.
	samples := collectAll(ctx, t, client)
	if err := sinks.ConsumeMetrics(ctx, samples); err != nil {
		t.Fatalf("ConsumeMetrics: %v", err)
	}

	if cs.count() == 0 {
		t.Fatal("prometheus capture server received no requests")
	}

	// Decompress the first body and confirm it contains a mikrotik metric name.
	decoded, err := snappy.Decode(nil, cs.bodies()[0])
	if err != nil {
		t.Fatalf("snappy decode: %v", err)
	}
	if !strings.Contains(string(decoded), "mikrotik_") {
		t.Errorf("prometheus payload missing a mikrotik_ metric name; body=%q", truncate(decoded, 400))
	}
}

// TestPipelineLoki generates a log line on the router, tails it through the log
// collector, and pushes through the real Loki sink to a capture server,
// asserting a JSON stream carrying the marker arrives.
func TestPipelineLoki(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), apiReadyTimeout+2*time.Minute)
	defer cancel()

	r := startRouter(ctx, t)
	client := newClient(t, r)
	defer client.Close()

	cs := newCaptureServer(t)

	t.Setenv("ROUTER_PASS", "unused-but-required-for-load")
	t.Setenv("EXPORTERS", "loki")
	t.Setenv("LOKI_ENDPOINT", cs.server.URL)
	t.Setenv("LOKI_USER", "6789")
	t.Setenv("LOKI_PASS", "token")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sinks, err := export.Build(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("export.Build: %v", err)
	}
	defer sinks.Shutdown(ctx)

	lc := mikrotik.NewLogCollector()
	// Prime the cursor.
	if _, err := lc.Collect(ctx, client); err != nil {
		t.Fatalf("prime log collect: %v", err)
	}

	writeLog(t, r, "tiktelemetry-loki-e2e")

	// Poll, push, and check the capture server for the marker.
	found := false
	for i := 0; i < 12 && !found; i++ {
		entries, err := lc.Collect(ctx, client)
		if err != nil {
			t.Fatalf("log collect: %v", err)
		}
		if len(entries) > 0 {
			if err := sinks.ConsumeLogs(ctx, entries); err != nil {
				t.Fatalf("ConsumeLogs: %v", err)
			}
		}
		for _, body := range cs.bodies() {
			if strings.Contains(string(body), "tiktelemetry-loki-e2e") {
				found = true
			}
		}
		if !found {
			time.Sleep(2 * time.Second)
		}
	}
	if !found {
		t.Error("loki capture server never received the marker log line")
	}
	// Sanity: the Loki payload path must be the push endpoint.
	for _, p := range cs.paths {
		if p != "/loki/api/v1/push" {
			t.Errorf("unexpected loki path %q", p)
		}
	}
}

// collectAll runs the default metric collectors and returns all samples.
func collectAll(ctx context.Context, t *testing.T, client *mikrotik.Client) []model.Sample {
	t.Helper()
	var all []model.Sample
	for _, c := range mikrotik.DefaultCollectors() {
		s, err := c.Collect(ctx, client)
		if err != nil {
			t.Fatalf("collector %s: %v", c.Name(), err)
		}
		all = append(all, s...)
	}
	if len(all) == 0 {
		t.Fatal("no samples collected from live router")
	}
	return all
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
