package loki

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/model"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustLoadConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error: %v", err)
	}
	return cfg
}

func newLokiSink(t *testing.T, ctx context.Context) *lokiSink {
	t.Helper()
	cfg := mustLoadConfig(t)
	sink, err := New(ctx, cfg, quietLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return sink.(*lokiSink)
}

// keysOf returns the keys of a string->stream map for diagnostics.
func keysOf(m map[string]stream) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestLokiName(t *testing.T) {
	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("LOKI_ENDPOINT", "http://localhost:1")
	sink := newLokiSink(t, context.Background())
	if sink.Name() != "loki" {
		t.Errorf("Name() = %q, want %q", sink.Name(), "loki")
	}
}

func TestLokiCapabilities(t *testing.T) {
	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("LOKI_ENDPOINT", "http://localhost:1")
	sink := newLokiSink(t, context.Background())
	caps := sink.Capabilities()
	if caps.Metrics {
		t.Error("Capabilities.Metrics should be false")
	}
	if !caps.Logs {
		t.Error("Capabilities.Logs should be true")
	}
}

func TestLokiNewMissingEndpoint(t *testing.T) {
	t.Setenv("ROUTER_PASS", "x")
	// No LOKI_ENDPOINT
	cfg := mustLoadConfig(t)
	_, err := New(context.Background(), cfg, quietLogger())
	if err == nil {
		t.Fatal("expected error when LOKI_ENDPOINT is empty")
	}
	if !strings.Contains(err.Error(), "LOKI_ENDPOINT") {
		t.Errorf("error should mention LOKI_ENDPOINT, got: %v", err)
	}
}

func TestLokiShutdown(t *testing.T) {
	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("LOKI_ENDPOINT", "http://localhost:1")
	sink := newLokiSink(t, context.Background())
	if err := sink.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}
}

func TestLokiConsumeMetricsNoop(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(200)
	}))
	defer server.Close()

	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("LOKI_ENDPOINT", server.URL)
	sink := newLokiSink(t, context.Background())

	if err := sink.ConsumeMetrics(context.Background(), nil); err != nil {
		t.Fatalf("ConsumeMetrics(nil) error: %v", err)
	}
	if err := sink.ConsumeMetrics(context.Background(), []model.Sample{
		{Name: "test", Value: 1},
	}); err != nil {
		t.Fatalf("ConsumeMetrics(samples) error: %v", err)
	}
	if requests != 0 {
		t.Errorf("expected 0 HTTP requests, got %d", requests)
	}
}

func TestLokiEndpointPathAppend(t *testing.T) {
	t.Run("plain_url_appends_push_path", func(t *testing.T) {
		var mu sync.Mutex
		var gotPath string

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			gotPath = r.URL.Path
			mu.Unlock()
			w.WriteHeader(200)
		}))
		defer server.Close()

		t.Setenv("ROUTER_PASS", "x")
		t.Setenv("LOKI_ENDPOINT", server.URL) // no path suffix
		t.Setenv("LOKI_USER", "user")
		t.Setenv("LOKI_PASS", "pass")
		sink := newLokiSink(t, context.Background())

		_ = sink.ConsumeLogs(context.Background(), []model.LogEntry{
			{Time: time.Now(), Message: "test"},
		})

		mu.Lock()
		if gotPath != "/loki/api/v1/push" {
			t.Errorf("path = %q, want /loki/api/v1/push", gotPath)
		}
		mu.Unlock()
	})

	t.Run("already_has_push_path_not_double_appended", func(t *testing.T) {
		var mu sync.Mutex
		var gotPath string

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			gotPath = r.URL.Path
			mu.Unlock()
			w.WriteHeader(200)
		}))
		defer server.Close()

		t.Setenv("ROUTER_PASS", "x")
		t.Setenv("LOKI_ENDPOINT", server.URL+"/loki/api/v1/push")
		t.Setenv("LOKI_USER", "user")
		t.Setenv("LOKI_PASS", "pass")
		sink := newLokiSink(t, context.Background())

		_ = sink.ConsumeLogs(context.Background(), []model.LogEntry{
			{Time: time.Now(), Message: "test"},
		})

		mu.Lock()
		if gotPath != "/loki/api/v1/push" {
			t.Errorf("path = %q, want /loki/api/v1/push", gotPath)
		}
		mu.Unlock()
	})
}

func TestLokiConsumeLogsFull(t *testing.T) {
	var mu sync.Mutex
	var gotMethod, gotPath string
	var gotHeaders http.Header
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer server.Close()

	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("LOKI_ENDPOINT", server.URL)
	t.Setenv("LOKI_USER", "lokiuser")
	t.Setenv("LOKI_PASS", "lokitoken")
	sink := newLokiSink(t, context.Background())

	now := time.Date(2026, 7, 14, 12, 0, 0, 123456789, time.UTC)
	earlier := now.Add(-10 * time.Minute)

	entries := []model.LogEntry{
		{
			Time:    now,
			Topics:  []string{"error", "dhcp"},
			Message: "dhcp lease failed",
			ID:      "e1",
		},
		{
			Time:    earlier,
			Topics:  []string{"info", "system"},
			Message: "system info message",
			ID:      "i1",
		},
		{
			Time:    now,
			Topics:  []string{"warning", "wireless"},
			Message: "signal low",
			ID:      "",
		},
	}

	if err := sink.ConsumeLogs(context.Background(), entries); err != nil {
		t.Fatalf("ConsumeLogs() error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// HTTP basics
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/loki/api/v1/push" {
		t.Errorf("path = %q, want /loki/api/v1/push", gotPath)
	}

	// Headers
	if gotHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", gotHeaders.Get("Content-Type"))
	}
	if gotHeaders.Get("User-Agent") != "tiktelemetry/tiktelemetry" {
		t.Errorf("User-Agent = %q", gotHeaders.Get("User-Agent"))
	}

	auth := gotHeaders.Get("Authorization")
	if !strings.HasPrefix(auth, "Basic ") {
		t.Fatalf("Authorization = %q, expected Basic prefix", auth)
	}
	decoded, err := base64.StdEncoding.DecodeString(auth[len("Basic "):])
	if err != nil {
		t.Fatalf("base64 decode Authorization: %v", err)
	}
	if string(decoded) != "lokiuser:lokitoken" {
		t.Errorf("Authorization credentials = %q, want %q", string(decoded), "lokiuser:lokitoken")
	}

	// Decode JSON body
	var pr pushRequest
	if err := json.Unmarshal(gotBody, &pr); err != nil {
		t.Fatalf("json.Unmarshal error: %v\nbody: %s", err, string(gotBody))
	}

	// Collect streams by level
	byLevel := make(map[string]stream)
	for _, s := range pr.Streams {
		lvl := s.Stream["level"]
		if lvl == "" {
			t.Errorf("stream missing level label: %v", s.Stream)
			continue
		}
		byLevel[lvl] = s
	}

	// We expect 3 streams: error, info, warn
	if len(byLevel) != 3 {
		t.Fatalf("expected 3 streams, got %d: %v", len(byLevel), keysOf(byLevel))
	}

	// Common label assertions for each stream
	for level, s := range byLevel {
		if s.Stream["service"] == "" {
			t.Errorf("stream %q: missing service label", level)
		}
		if s.Stream["source"] != "mikrotik" {
			t.Errorf("stream %q: source = %q, want mikrotik", level, s.Stream["source"])
		}
		if s.Stream["instance"] == "" {
			t.Errorf("stream %q: missing instance label", level)
		}
		if s.Stream["level"] != level {
			t.Errorf("stream %q: level label = %q", level, s.Stream["level"])
		}
	}

	// --- "error" stream ---
	es, ok := byLevel["error"]
	if !ok {
		t.Fatal("missing 'error' stream")
	}
	if len(es.Values) != 1 {
		t.Fatalf("error stream: expected 1 value, got %d", len(es.Values))
	}
	tsStr, ok := es.Values[0][0].(string)
	if !ok {
		t.Fatalf("error stream: timestamp is not string: %v", es.Values[0][0])
	}
	// Verify nanosecond timestamp
	if !strings.HasSuffix(tsStr, "123456789") {
		t.Errorf("error stream: timestamp = %q, should end with 123456789", tsStr)
	}
	msg, ok := es.Values[0][1].(string)
	if !ok || msg != "dhcp lease failed" {
		t.Errorf("error stream: message = %v, want 'dhcp lease failed'", es.Values[0][1])
	}
	// Structured metadata
	if len(es.Values[0]) != 3 {
		t.Fatalf("error stream: expected 3 elements (ts, msg, meta), got %d", len(es.Values[0]))
	}
	meta, ok := es.Values[0][2].(map[string]interface{})
	if !ok {
		t.Fatalf("error stream: metadata is %T", es.Values[0][2])
	}
	if meta["topics"] != "error,dhcp" {
		t.Errorf("error stream: topics = %v", meta["topics"])
	}
	if meta["id"] != "e1" {
		t.Errorf("error stream: id = %v", meta["id"])
	}

	// --- "info" stream ---
	is, ok := byLevel["info"]
	if !ok {
		t.Fatal("missing 'info' stream")
	}
	if len(is.Values) != 1 {
		t.Fatalf("info stream: expected 1 value, got %d", len(is.Values))
	}
	tsStr2, _ := is.Values[0][0].(string)

	// Verify the info message timestamp is before the error message timestamp (info is earlier)
	tsInfo, _ := strconv.ParseInt(tsStr2, 10, 64)
	tsErr, _ := strconv.ParseInt(tsStr, 10, 64)
	if tsInfo >= tsErr {
		t.Errorf("info timestamp (%d) should be before error timestamp (%d)", tsInfo, tsErr)
	}
	msg2, _ := is.Values[0][1].(string)
	if msg2 != "system info message" {
		t.Errorf("info stream: message = %v", msg2)
	}
	if len(is.Values[0]) != 3 {
		t.Fatalf("info stream: expected 3 elements, got %d", len(is.Values[0]))
	}
	meta2, _ := is.Values[0][2].(map[string]interface{})
	if meta2["topics"] != "info,system" {
		t.Errorf("info stream: topics = %v", meta2["topics"])
	}
	if meta2["id"] != "i1" {
		t.Errorf("info stream: id = %v", meta2["id"])
	}

	// --- "warn" stream ---
	ws, ok := byLevel["warn"]
	if !ok {
		t.Fatal("missing 'warn' stream")
	}
	if len(ws.Values) != 1 {
		t.Fatalf("warn stream: expected 1 value, got %d", len(ws.Values))
	}
	msg3, _ := ws.Values[0][1].(string)
	if msg3 != "signal low" {
		t.Errorf("warn stream: message = %v", msg3)
	}
	// warn entry has no ID, but has topics so metadata should still be present
	if len(ws.Values[0]) != 3 {
		t.Fatalf("warn stream: expected 3 elements, got %d", len(ws.Values[0]))
	}
	meta3, _ := ws.Values[0][2].(map[string]interface{})
	if meta3["topics"] != "warning,wireless" {
		t.Errorf("warn stream: topics = %v", meta3["topics"])
	}
	if _, exists := meta3["id"]; exists {
		t.Errorf("warn stream: id should not be present, got %v", meta3)
	}
}

func TestLokiServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer server.Close()

	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("LOKI_ENDPOINT", server.URL)
	sink := newLokiSink(t, context.Background())

	err := sink.ConsumeLogs(context.Background(), []model.LogEntry{
		{Time: time.Now(), Message: "test"},
	})
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500, got: %v", err)
	}
}

func TestLokiEmptyEntriesNoRequest(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(200)
	}))
	defer server.Close()

	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("LOKI_ENDPOINT", server.URL)
	sink := newLokiSink(t, context.Background())

	// ConsumeLogs with nil entries — short-circuits in sink.
	if err := sink.ConsumeLogs(context.Background(), nil); err != nil {
		t.Fatalf("ConsumeLogs(nil) error: %v", err)
	}
	// ConsumeLogs with empty slice.
	if err := sink.ConsumeLogs(context.Background(), []model.LogEntry{}); err != nil {
		t.Fatalf("ConsumeLogs(empty) error: %v", err)
	}
	if requests != 0 {
		t.Errorf("expected 0 HTTP requests, got %d", requests)
	}
}

func TestLokiExtraHeaders(t *testing.T) {
	var mu sync.Mutex
	var gotHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHeaders = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer server.Close()

	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("LOKI_ENDPOINT", server.URL)
	t.Setenv("LOKI_HEADERS", "X-Custom=custom-val, X-Scope-OrgID=myorg")
	t.Setenv("LOKI_USER", "u")
	t.Setenv("LOKI_PASS", "p")
	sink := newLokiSink(t, context.Background())

	if err := sink.ConsumeLogs(context.Background(), []model.LogEntry{
		{Time: time.Now(), Message: "test"},
	}); err != nil {
		t.Fatalf("ConsumeLogs: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if gotHeaders.Get("X-Custom") != "custom-val" {
		t.Errorf("X-Custom = %q", gotHeaders.Get("X-Custom"))
	}
	if gotHeaders.Get("X-Scope-OrgID") != "myorg" {
		t.Errorf("X-Scope-OrgID = %q", gotHeaders.Get("X-Scope-OrgID"))
	}
	// Authorization should also be set
	if !strings.HasPrefix(gotHeaders.Get("Authorization"), "Basic ") {
		t.Errorf("Authorization should be set, got %q", gotHeaders.Get("Authorization"))
	}
}
