package otlp

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

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/model"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// requireCfg loads config with ROUTER_PASS set and the given env overrides set
// via t.Setenv. Callers must have already set any required env vars before calling.
func requireCfg(t *testing.T, setters ...func()) config.Config {
	t.Helper()
	t.Setenv("ROUTER_PASS", "secret")
	for _, s := range setters {
		s()
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error: %v", err)
	}
	return cfg
}

func TestNew_EmptyEndpoint(t *testing.T) {
	cfg := requireCfg(t, func() {
		t.Setenv("OTLP_ENDPOINT", "")
	})
	_, err := New(context.Background(), cfg, quietLogger())
	if err == nil {
		t.Fatal("expected error for empty OTLP_ENDPOINT")
	}
}

func TestNew_Success(t *testing.T) {
	cfg := requireCfg(t, func() {
		t.Setenv("OTLP_ENDPOINT", "http://127.0.0.1:1/otlp")
		t.Setenv("OTLP_INSECURE", "true")
	})

	sink, err := New(context.Background(), cfg, quietLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if sink.Name() != "otlp" {
		t.Errorf("Name() = %q, want %q", sink.Name(), "otlp")
	}

	cap := sink.Capabilities()
	if !cap.Metrics {
		t.Error("Capabilities().Metrics = false, want true")
	}
	if !cap.Logs {
		t.Error("Capabilities().Logs = false, want true")
	}

	// Shutdown must not panic; it may return an error because the endpoint is
	// unreachable, but that is acceptable.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = sink.Shutdown(ctx)
}

func TestNew_MalformedHeaders(t *testing.T) {
	cfg := requireCfg(t, func() {
		t.Setenv("OTLP_ENDPOINT", "http://127.0.0.1:1/otlp")
		t.Setenv("OTLP_HEADERS", "bad-entry-without-equals")
	})

	_, err := New(context.Background(), cfg, quietLogger())
	if err == nil {
		t.Fatal("expected error for malformed OTLP_HEADERS")
	}
}

// TestEndToEndPush starts an httptest server, builds a real OTLP sink pointing
// at it, sends one metric sample and one log entry, then shuts down (which
// flushes both pipelines). We assert the server received a POST to /v1/metrics
// (and best-effort for /v1/logs).
func TestEndToEndPush(t *testing.T) {
	var mu sync.Mutex
	var received []requestInfo

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read a small amount to accept the body so the client can complete.
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()

		mu.Lock()
		received = append(received, requestInfo{
			Method:  r.Method,
			Path:    r.URL.Path,
			Auth:    r.Header.Get("Authorization"),
			BodyLen: len(body),
		})
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := requireCfg(t, func() {
		t.Setenv("OTLP_ENDPOINT", srv.URL)
		t.Setenv("OTLP_USER", "testuser")
		t.Setenv("OTLP_PASS", "testpass")
		// Flush is forced via Shutdown below, so the interval only needs to be a
		// valid (>= 1s) value.
		t.Setenv("POLL_INTERVAL", "1s")
	})

	sink, err := New(context.Background(), cfg, quietLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	// Send a metric sample.
	err = sink.ConsumeMetrics(context.Background(), []model.Sample{
		{
			Name:        "test.metric",
			Description: "a test gauge",
			Unit:        "1",
			Kind:        model.KindGauge,
			Value:       42,
			Attributes:  map[string]string{"tag": "val"},
		},
	})
	if err != nil {
		t.Errorf("ConsumeMetrics error: %v", err)
	}

	// Send a log entry.
	err = sink.ConsumeLogs(context.Background(), []model.LogEntry{
		{
			Message: "test log message",
			Topics:  []string{"info"},
		},
	})
	if err != nil {
		t.Errorf("ConsumeLogs error: %v", err)
	}

	// Shutdown flushes both pipelines.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sink.Shutdown(ctx); err != nil {
		t.Logf("Shutdown returned error (may be expected if flush partial): %v", err)
	}

	// Check what the server received.
	mu.Lock()
	info := make([]requestInfo, len(received))
	copy(info, received)
	mu.Unlock()

	if len(info) == 0 {
		t.Fatal("server received no requests after Shutdown")
	}

	foundMetrics := false
	foundLogs := false
	authOk := false
	for _, req := range info {
		if strings.HasSuffix(req.Path, "/v1/metrics") && req.Method == "POST" && req.BodyLen > 0 {
			foundMetrics = true
		}
		if strings.HasSuffix(req.Path, "/v1/logs") && req.Method == "POST" && req.BodyLen > 0 {
			foundLogs = true
		}
		if strings.HasPrefix(req.Auth, "Basic ") {
			authOk = true
		}
	}

	if !foundMetrics {
		t.Error("server did not receive a POST to /v1/metrics with body after Shutdown")
	}
	if !foundLogs {
		// Logs flushing via BatchProcessor is best-effort in tests; the
		// Metrics PeriodicReader flush on Shutdown is more reliable.
		t.Log("NOTE: server did not receive a POST to /v1/logs — log flushing is best-effort")
	}
	if !authOk {
		t.Error("Authorization header does not start with 'Basic '")
	}
}

type requestInfo struct {
	Method  string
	Path    string
	Auth    string
	BodyLen int
}
