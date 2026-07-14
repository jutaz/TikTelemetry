package prometheus

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/klauspost/compress/snappy"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/model"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mustLoadConfig loads config with t.Setenv already set.  Panics on error.
func mustLoadConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error: %v", err)
	}
	return cfg
}

// newPromSink builds a prometheus sink from a config with ROUTER_PASS and
// PROMETHEUS_ENDPOINT already set via t.Setenv.  Additional env vars can be set
// before calling this.
func newPromSink(t *testing.T, ctx context.Context) *prometheusSink {
	t.Helper()
	cfg := mustLoadConfig(t)
	sink, err := New(ctx, cfg, quietLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return sink.(*prometheusSink)
}

// ---------------------------------------------------------------------------
// Minimal protobuf reader for the remote_write wire format
// ---------------------------------------------------------------------------

type protoBuf struct {
	data []byte
	off  int
}

func (p *protoBuf) done() bool { return p.off >= len(p.data) }

func (p *protoBuf) varint() (uint64, error) {
	var v uint64
	shift := 0
	for {
		if p.off >= len(p.data) {
			return 0, errors.New("proto: unexpected EOF in varint")
		}
		b := p.data[p.off]
		p.off++
		v |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return v, nil
		}
		shift += 7
	}
}

func (p *protoBuf) fixed64() (uint64, error) {
	if p.off+8 > len(p.data) {
		return 0, errors.New("proto: unexpected EOF in fixed64")
	}
	v := binary.LittleEndian.Uint64(p.data[p.off:])
	p.off += 8
	return v, nil
}

func (p *protoBuf) bytes() ([]byte, error) {
	n, err := p.varint()
	if err != nil {
		return nil, err
	}
	end := p.off + int(n)
	if end > len(p.data) {
		return nil, fmt.Errorf("proto: bytes length %d exceeds data (%d)", n, len(p.data))
	}
	b := make([]byte, n)
	copy(b, p.data[p.off:end])
	p.off = end
	return b, nil
}

func (p *protoBuf) tag() (field int, wireType int, err error) {
	if p.done() {
		return 0, 0, io.EOF
	}
	v, err := p.varint()
	if err != nil {
		return 0, 0, err
	}
	return int(v >> 3), int(v & 7), nil
}

func (p *protoBuf) skip(wt int) error {
	switch wt {
	case 0:
		_, err := p.varint()
		return err
	case 1:
		_, err := p.fixed64()
		return err
	case 2:
		_, err := p.bytes()
		return err
	default:
		return fmt.Errorf("proto: unknown wire type %d", wt)
	}
}

type decodedLabel struct {
	Name, Value string
}

type decodedSample struct {
	Value       float64
	TimestampMs int64
}

type decodedTimeSeries struct {
	Labels  []decodedLabel
	Samples []decodedSample
}

func decodeLabel(data []byte) (decodedLabel, error) {
	p := &protoBuf{data: data}
	var l decodedLabel
	for {
		f, wt, err := p.tag()
		if err == io.EOF {
			break
		}
		if err != nil {
			return l, err
		}
		switch {
		case f == 1 && wt == 2: // Label.name
			b, err := p.bytes()
			if err != nil {
				return l, err
			}
			l.Name = string(b)
		case f == 2 && wt == 2: // Label.value
			b, err := p.bytes()
			if err != nil {
				return l, err
			}
			l.Value = string(b)
		default:
			if err := p.skip(wt); err != nil {
				return l, err
			}
		}
	}
	return l, nil
}

func decodeSample(data []byte) (decodedSample, error) {
	p := &protoBuf{data: data}
	var s decodedSample
	for {
		f, wt, err := p.tag()
		if err == io.EOF {
			break
		}
		if err != nil {
			return s, err
		}
		switch {
		case f == 1 && wt == 1: // Sample.value (double, fixed64)
			v, err := p.fixed64()
			if err != nil {
				return s, err
			}
			s.Value = math.Float64frombits(v)
		case f == 2 && wt == 0: // Sample.timestamp (int64, varint)
			v, err := p.varint()
			if err != nil {
				return s, err
			}
			s.TimestampMs = int64(v)
		default:
			if err := p.skip(wt); err != nil {
				return s, err
			}
		}
	}
	return s, nil
}

func decodeTimeSeries(data []byte) (decodedTimeSeries, error) {
	p := &protoBuf{data: data}
	var ts decodedTimeSeries
	for {
		f, wt, err := p.tag()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ts, err
		}
		switch {
		case f == 1 && wt == 2: // TimeSeries.labels (repeated Label)
			b, err := p.bytes()
			if err != nil {
				return ts, err
			}
			l, err := decodeLabel(b)
			if err != nil {
				return ts, err
			}
			ts.Labels = append(ts.Labels, l)
		case f == 2 && wt == 2: // TimeSeries.samples (repeated Sample)
			b, err := p.bytes()
			if err != nil {
				return ts, err
			}
			sm, err := decodeSample(b)
			if err != nil {
				return ts, err
			}
			ts.Samples = append(ts.Samples, sm)
		default:
			if err := p.skip(wt); err != nil {
				return ts, err
			}
		}
	}
	return ts, nil
}

func decodeWriteRequest(data []byte) ([]decodedTimeSeries, error) {
	p := &protoBuf{data: data}
	var all []decodedTimeSeries
	for {
		f, wt, err := p.tag()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch {
		case f == 1 && wt == 2: // WriteRequest.timeseries (repeated TimeSeries)
			b, err := p.bytes()
			if err != nil {
				return nil, err
			}
			ts, err := decodeTimeSeries(b)
			if err != nil {
				return nil, err
			}
			all = append(all, ts)
		default:
			if err := p.skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return all, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestPrometheusName(t *testing.T) {
	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("PROMETHEUS_ENDPOINT", "http://localhost:1")
	sink := newPromSink(t, context.Background())
	if sink.Name() != "prometheus" {
		t.Errorf("Name() = %q, want %q", sink.Name(), "prometheus")
	}
}

func TestPrometheusCapabilities(t *testing.T) {
	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("PROMETHEUS_ENDPOINT", "http://localhost:1")
	sink := newPromSink(t, context.Background())
	caps := sink.Capabilities()
	if !caps.Metrics {
		t.Error("Capabilities.Metrics should be true")
	}
	if caps.Logs {
		t.Error("Capabilities.Logs should be false")
	}
}

func TestPrometheusNewMissingEndpoint(t *testing.T) {
	t.Setenv("ROUTER_PASS", "x")
	// No PROMETHEUS_ENDPOINT
	cfg := mustLoadConfig(t)
	_, err := New(context.Background(), cfg, quietLogger())
	if err == nil {
		t.Fatal("expected error when PROMETHEUS_ENDPOINT is empty")
	}
	if !strings.Contains(err.Error(), "PROMETHEUS_ENDPOINT") {
		t.Errorf("error should mention PROMETHEUS_ENDPOINT, got: %v", err)
	}
}

func TestPrometheusShutdown(t *testing.T) {
	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("PROMETHEUS_ENDPOINT", "http://localhost:1")
	sink := newPromSink(t, context.Background())
	if err := sink.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}
}

func TestPrometheusConsumeLogsNoop(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(200)
	}))
	defer server.Close()

	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("PROMETHEUS_ENDPOINT", server.URL)
	sink := newPromSink(t, context.Background())

	if err := sink.ConsumeLogs(context.Background(), nil); err != nil {
		t.Fatalf("ConsumeLogs(nil) error: %v", err)
	}
	if err := sink.ConsumeLogs(context.Background(), []model.LogEntry{
		{Message: "test"},
	}); err != nil {
		t.Fatalf("ConsumeLogs(entries) error: %v", err)
	}
	if requests != 0 {
		t.Errorf("expected 0 requests, got %d", requests)
	}
}

func TestPrometheusConsumeMetricsFull(t *testing.T) {
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

	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("PROMETHEUS_ENDPOINT", server.URL)
	t.Setenv("PROMETHEUS_USER", "user123")
	t.Setenv("PROMETHEUS_PASS", "token456")
	sink := newPromSink(t, context.Background())

	samples := []model.Sample{
		{
			Name:  "mikrotik.system.cpu.load",
			Value: 5,
			Kind:  model.KindGauge,
		},
		{
			Name:  "mikrotik.interface.rx.bytes",
			Value: 1000,
			Kind:  model.KindCounter,
			Attributes: map[string]string{
				"interface": "ether1",
			},
		},
	}

	if err := sink.ConsumeMetrics(context.Background(), samples); err != nil {
		t.Fatalf("ConsumeMetrics() error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// HTTP basics
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/" {
		t.Errorf("path = %q, want /", gotPath)
	}

	// Required headers
	if gotHeaders.Get("Content-Encoding") != "snappy" {
		t.Errorf("Content-Encoding = %q", gotHeaders.Get("Content-Encoding"))
	}
	if gotHeaders.Get("Content-Type") != "application/x-protobuf" {
		t.Errorf("Content-Type = %q", gotHeaders.Get("Content-Type"))
	}
	if gotHeaders.Get("X-Prometheus-Remote-Write-Version") != "0.1.0" {
		t.Errorf("X-Prometheus-Remote-Write-Version = %q", gotHeaders.Get("X-Prometheus-Remote-Write-Version"))
	}
	if gotHeaders.Get("User-Agent") != "tiktelemetry/dev" {
		t.Errorf("User-Agent = %q", gotHeaders.Get("User-Agent"))
	}

	// Authorization
	auth := gotHeaders.Get("Authorization")
	if !strings.HasPrefix(auth, "Basic ") {
		t.Fatalf("Authorization = %q, expected Basic prefix", auth)
	}
	decoded, err := base64.StdEncoding.DecodeString(auth[len("Basic "):])
	if err != nil {
		t.Fatalf("base64 decode Authorization: %v", err)
	}
	if string(decoded) != "user123:token456" {
		t.Errorf("Authorization credentials = %q, want %q", string(decoded), "user123:token456")
	}

	// Body: snappy decompress then protobuf decode
	raw, err := snappy.Decode(nil, gotBody)
	if err != nil {
		t.Fatalf("snappy.Decode error: %v", err)
	}

	series, err := decodeWriteRequest(raw)
	if err != nil {
		t.Fatalf("decodeWriteRequest error: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("expected 2 time series, got %d", len(series))
	}

	// Helper: build label map for a series
	labelMap := func(ts decodedTimeSeries) map[string]string {
		m := make(map[string]string, len(ts.Labels))
		for _, l := range ts.Labels {
			m[l.Name] = l.Value
		}
		return m
	}

	// --- First series: mikrotik.system.cpu.load (no attributes) ---
	m0 := labelMap(series[0])
	if m0["__name__"] != "mikrotik_system_cpu_load" {
		t.Errorf("series 0 __name__ = %q", m0["__name__"])
	}
	if m0["service"] == "" {
		t.Error("series 0 missing service label")
	}
	if m0["instance"] == "" {
		t.Error("series 0 missing instance label")
	}
	if len(series[0].Samples) != 1 {
		t.Fatalf("series 0: expected 1 sample, got %d", len(series[0].Samples))
	}
	if series[0].Samples[0].Value != 5.0 {
		t.Errorf("series 0 value = %f, want 5.0", series[0].Samples[0].Value)
	}
	if series[0].Samples[0].TimestampMs <= 0 {
		t.Errorf("series 0 timestamp should be positive, got %d", series[0].Samples[0].TimestampMs)
	}

	// --- Second series: mikrotik.interface.rx.bytes (attrs + service + instance) ---
	m1 := labelMap(series[1])
	if m1["__name__"] != "mikrotik_interface_rx_bytes" {
		t.Errorf("series 1 __name__ = %q", m1["__name__"])
	}
	if m1["interface"] != "ether1" {
		t.Errorf("series 1 interface = %q", m1["interface"])
	}
	if m1["service"] == "" {
		t.Error("series 1 missing service label")
	}
	if m1["instance"] == "" {
		t.Error("series 1 missing instance label")
	}
	if len(series[1].Samples) != 1 {
		t.Fatalf("series 1: expected 1 sample, got %d", len(series[1].Samples))
	}
	if series[1].Samples[0].Value != 1000.0 {
		t.Errorf("series 1 value = %f, want 1000.0", series[1].Samples[0].Value)
	}
	if series[1].Samples[0].TimestampMs <= 0 {
		t.Errorf("series 1 timestamp should be positive, got %d", series[1].Samples[0].TimestampMs)
	}

	// Verify lexicographic label sort within each series
	for i, ts := range series {
		for j := 1; j < len(ts.Labels); j++ {
			if ts.Labels[j-1].Name >= ts.Labels[j].Name {
				t.Errorf("series %d: labels not sorted at index %d: %q >= %q",
					i, j, ts.Labels[j-1].Name, ts.Labels[j].Name)
			}
		}
	}
}

func TestPrometheusServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer server.Close()

	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("PROMETHEUS_ENDPOINT", server.URL)
	sink := newPromSink(t, context.Background())

	err := sink.ConsumeMetrics(context.Background(), []model.Sample{
		{Name: "test", Value: 1},
	})
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500, got: %v", err)
	}
}

func TestPrometheusConsecutiveSameCounterValue(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer server.Close()

	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("PROMETHEUS_ENDPOINT", server.URL)
	sink := newPromSink(t, context.Background())

	samples := []model.Sample{
		{Name: "test.metric", Value: 1000, Kind: model.KindCounter},
	}

	// First push
	if err := sink.ConsumeMetrics(context.Background(), samples); err != nil {
		t.Fatalf("first ConsumeMetrics: %v", err)
	}
	// Second push with same value
	if err := sink.ConsumeMetrics(context.Background(), samples); err != nil {
		t.Fatalf("second ConsumeMetrics: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(bodies) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(bodies))
	}

	for i, body := range bodies {
		raw, err := snappy.Decode(nil, body)
		if err != nil {
			t.Fatalf("request %d: snappy.Decode error: %v", i, err)
		}
		series, err := decodeWriteRequest(raw)
		if err != nil {
			t.Fatalf("request %d: decodeWriteRequest error: %v", i, err)
		}
		if len(series) != 1 {
			t.Fatalf("request %d: expected 1 series, got %d", i, len(series))
		}
		if len(series[0].Samples) != 1 {
			t.Fatalf("request %d: expected 1 sample, got %d", i, len(series[0].Samples))
		}
		if series[0].Samples[0].Value != 1000.0 {
			t.Errorf("request %d: value = %f, want 1000.0", i, series[0].Samples[0].Value)
		}
	}
}

func TestPrometheusExtraHeaders(t *testing.T) {
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
	t.Setenv("PROMETHEUS_ENDPOINT", server.URL)
	t.Setenv("PROMETHEUS_HEADERS", "X-Custom=value123, X-Scope-OrgID=mytenant")
	sink := newPromSink(t, context.Background())

	if err := sink.ConsumeMetrics(context.Background(), []model.Sample{
		{Name: "test", Value: 1},
	}); err != nil {
		t.Fatalf("ConsumeMetrics: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if gotHeaders.Get("X-Custom") != "value123" {
		t.Errorf("X-Custom = %q", gotHeaders.Get("X-Custom"))
	}
	if gotHeaders.Get("X-Scope-OrgID") != "mytenant" {
		t.Errorf("X-Scope-OrgID = %q", gotHeaders.Get("X-Scope-OrgID"))
	}
}

func TestPrometheusEmptySamplesNoOp(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(200)
	}))
	defer server.Close()

	t.Setenv("ROUTER_PASS", "x")
	t.Setenv("PROMETHEUS_ENDPOINT", server.URL)
	sink := newPromSink(t, context.Background())

	if err := sink.ConsumeMetrics(context.Background(), nil); err != nil {
		t.Fatalf("ConsumeMetrics(nil) error: %v", err)
	}
	if requests != 0 {
		t.Errorf("expected 0 requests for nil samples, got %d", requests)
	}
}
