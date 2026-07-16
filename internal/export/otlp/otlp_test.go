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

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"

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

// capturedRequest is one request the test OTLP server received.
type capturedRequest struct {
	Path string
	Auth string
	Body []byte
}

// otlpCapture is a thread-safe collector of OTLP requests.
type otlpCapture struct {
	mu   sync.Mutex
	reqs []capturedRequest
}

func (c *otlpCapture) add(r capturedRequest) {
	c.mu.Lock()
	c.reqs = append(c.reqs, r)
	c.mu.Unlock()
}

func (c *otlpCapture) snapshot() []capturedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]capturedRequest, len(c.reqs))
	copy(out, c.reqs)
	return out
}

// TestEndToEndPush starts an httptest server, builds a real OTLP sink pointing
// at it, sends one metric sample and one log entry, then shuts down (which
// flushes both pipelines). It decodes the OTLP protobuf payloads and asserts
// the exact metric name/value/attribute and log body/severity/attributes that
// were pushed — not merely that a request arrived.
//
// The default OTLP/HTTP compression is "none", so request bodies are raw
// protobuf and can be unmarshalled directly with the OTLP proto types (already
// a dependency of the exporter, so no new imports are pulled into production).
func TestEndToEndPush(t *testing.T) {
	cap := &otlpCapture{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		cap.add(capturedRequest{
			Path: r.URL.Path,
			Auth: r.Header.Get("Authorization"),
			Body: body,
		})
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := requireCfg(t, func() {
		t.Setenv("OTLP_ENDPOINT", srv.URL)
		t.Setenv("OTLP_USER", "testuser")
		t.Setenv("OTLP_PASS", "testpass")
		t.Setenv("POLL_INTERVAL", "1s")
	})

	sink, err := New(context.Background(), cfg, quietLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if err := sink.ConsumeMetrics(context.Background(), []model.Sample{{
		Name:       "test.metric",
		Unit:       "1",
		Kind:       model.KindGauge,
		Value:      42,
		Attributes: map[string]string{"iface": "ether1"},
	}}); err != nil {
		t.Fatalf("ConsumeMetrics error: %v", err)
	}

	if err := sink.ConsumeLogs(context.Background(), []model.LogEntry{{
		Message: "test log message",
		Topics:  []string{"system", "error"},
		Target:  "router-x",
		Time:    time.Now(),
	}}); err != nil {
		t.Fatalf("ConsumeLogs error: %v", err)
	}

	// Shutdown flushes and closes both pipelines. It returns only after the
	// final export completes, so assertions after it are deterministic.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sink.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown error: %v", err)
	}

	reqs := cap.snapshot()
	if len(reqs) == 0 {
		t.Fatal("server received no OTLP requests")
	}

	metricReq := findRequest(t, reqs, "/v1/metrics")
	logReq := findRequest(t, reqs, "/v1/logs")

	// Auth must be Basic on every request.
	for _, r := range []capturedRequest{metricReq, logReq} {
		if !strings.HasPrefix(r.Auth, "Basic ") {
			t.Errorf("request to %s: Authorization = %q, want Basic ...", r.Path, r.Auth)
		}
	}

	assertMetricPayload(t, metricReq.Body)
	assertLogPayload(t, logReq.Body)
}

// findRequest returns the first captured request whose path ends with suffix,
// failing the test if none exists.
func findRequest(t *testing.T, reqs []capturedRequest, suffix string) capturedRequest {
	t.Helper()
	for _, r := range reqs {
		if strings.HasSuffix(r.Path, suffix) {
			return r
		}
	}
	t.Fatalf("no OTLP request to a path ending in %q (paths: %s)", suffix, pathsOf(reqs))
	return capturedRequest{}
}

func pathsOf(reqs []capturedRequest) string {
	var ps []string
	for _, r := range reqs {
		ps = append(ps, r.Path)
	}
	return strings.Join(ps, ", ")
}

// assertMetricPayload decodes an OTLP ExportMetricsServiceRequest and verifies
// the exact metric name, gauge value, unit, and attribute we pushed.
func assertMetricPayload(t *testing.T, body []byte) {
	t.Helper()
	var req colmetricspb.ExportMetricsServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal metrics payload: %v", err)
	}

	metric := findMetric(t, &req, "test.metric")
	if metric.GetUnit() != "1" {
		t.Errorf("metric unit = %q, want %q", metric.GetUnit(), "1")
	}

	gauge := metric.GetGauge()
	if gauge == nil {
		t.Fatalf("metric %q is not a gauge (got %T)", metric.GetName(), metric.GetData())
	}
	points := gauge.GetDataPoints()
	if len(points) != 1 {
		t.Fatalf("gauge has %d data points, want 1", len(points))
	}
	dp := points[0]

	if got := dp.GetAsInt(); got != 42 {
		t.Errorf("gauge value = %d, want 42", got)
	}
	if v := attrString(dp.GetAttributes(), "iface"); v != "ether1" {
		t.Errorf("attribute iface = %q, want ether1", v)
	}
}

// findMetric locates a metric by name across all resource/scope metrics.
func findMetric(t *testing.T, req *colmetricspb.ExportMetricsServiceRequest, name string) *metricspb.Metric {
	t.Helper()
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				if m.GetName() == name {
					return m
				}
			}
		}
	}
	t.Fatalf("metric %q not found in payload", name)
	return nil
}

// assertLogPayload decodes an OTLP ExportLogsServiceRequest and verifies the
// exact log body, severity, and attributes we emitted.
func assertLogPayload(t *testing.T, body []byte) {
	t.Helper()
	var req collogspb.ExportLogsServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal logs payload: %v", err)
	}

	var found bool
	for _, rl := range req.GetResourceLogs() {
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				if lr.GetBody().GetStringValue() != "test log message" {
					continue
				}
				found = true

				// Severity: topics {system, error} -> Error (see model.Severity).
				if lr.GetSeverityText() != "ERROR" {
					t.Errorf("log severity text = %q, want ERROR", lr.GetSeverityText())
				}
				if tgt := attrString(lr.GetAttributes(), "target"); tgt != "router-x" {
					t.Errorf("log attribute target = %q, want router-x", tgt)
				}
				if src := attrString(lr.GetAttributes(), "source"); src != "mikrotik" {
					t.Errorf("log attribute source = %q, want mikrotik", src)
				}
				if topics := attrString(lr.GetAttributes(), "topics"); topics != "system,error" {
					t.Errorf("log attribute topics = %q, want system,error", topics)
				}
			}
		}
	}
	if !found {
		t.Error("log record with body \"test log message\" not found in payload")
	}
}

// attrString returns the string value of the attribute with the given key, or
// "" if absent / not a string.
func attrString(attrs []*commonpb.KeyValue, key string) string {
	for _, kv := range attrs {
		if kv.GetKey() == key {
			return kv.GetValue().GetStringValue()
		}
	}
	return ""
}
