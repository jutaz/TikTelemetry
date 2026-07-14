package otlp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/jutaz/tiktelemetry/internal/model"
)

// newTestMetricsPipeline builds a metricsPipeline pointed at a throwaway OTLP
// endpoint. The endpoint need not respond for the delta/instrument logic to be
// exercised, since recording happens in-memory before the periodic flush.
func newTestMetricsPipeline(t *testing.T) *metricsPipeline {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	mp, err := newMetricsPipeline(
		context.Background(),
		srv.URL+"/v1/metrics",
		nil,
		false,
		time.Hour, // long interval: we never rely on the periodic flush here
		resource.Default(),
	)
	if err != nil {
		t.Fatalf("newMetricsPipeline: %v", err)
	}
	t.Cleanup(func() { _ = mp.shutdownMetrics(context.Background()) })
	return mp
}

func TestConsumeMetricsGaugeAndCounter(t *testing.T) {
	mp := newTestMetricsPipeline(t)
	ctx := context.Background()

	// Gauge and counter, first observation.
	err := mp.consumeMetrics(ctx, []model.Sample{
		{Name: "test.gauge", Kind: model.KindGauge, Value: 42, Unit: "1"},
		{Name: "test.counter", Kind: model.KindCounter, Value: 100, Unit: "By",
			Attributes: map[string]string{"iface": "ether1"}},
	})
	if err != nil {
		t.Fatalf("consumeMetrics: %v", err)
	}

	// Instruments should be cached now.
	if _, ok := mp.gauges["test.gauge"]; !ok {
		t.Error("gauge instrument not cached")
	}
	if _, ok := mp.counters["test.counter"]; !ok {
		t.Error("counter instrument not cached")
	}

	// The counter's last value should be recorded.
	key := counterKey("test.counter", map[string]string{"iface": "ether1"})
	if mp.lastValues[key] != 100 {
		t.Errorf("lastValues[%q] = %d, want 100", key, mp.lastValues[key])
	}
}

func TestConsumeMetricsCounterDelta(t *testing.T) {
	mp := newTestMetricsPipeline(t)
	ctx := context.Background()
	attrs := map[string]string{"iface": "ether1"}
	key := counterKey("c", attrs)

	// Absolute values 100 -> 150 -> 150 (no change) -> 40 (reset).
	steps := []struct {
		value    int64
		wantLast int64
	}{
		{100, 100}, // first observation
		{150, 150}, // delta 50
		{150, 150}, // delta 0 (no Add, but lastValues updated)
		{40, 40},   // reset: current < last, emits full 40
	}
	for i, step := range steps {
		if err := mp.consumeMetrics(ctx, []model.Sample{
			{Name: "c", Kind: model.KindCounter, Value: step.value, Attributes: attrs},
		}); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if mp.lastValues[key] != step.wantLast {
			t.Errorf("step %d: lastValues = %d, want %d", i, mp.lastValues[key], step.wantLast)
		}
	}
}

func TestCounterKeyStability(t *testing.T) {
	// Attribute order in the map must not change the key.
	a := counterKey("m", map[string]string{"x": "1", "y": "2"})
	b := counterKey("m", map[string]string{"y": "2", "x": "1"})
	if a != b {
		t.Errorf("counterKey not order-stable: %q vs %q", a, b)
	}

	// Different names or attribute values produce different keys.
	if counterKey("m", map[string]string{"x": "1"}) == counterKey("n", map[string]string{"x": "1"}) {
		t.Error("counterKey collided across metric names")
	}
	if counterKey("m", map[string]string{"x": "1"}) == counterKey("m", map[string]string{"x": "2"}) {
		t.Error("counterKey collided across attribute values")
	}
}

func TestSortedAttrs(t *testing.T) {
	out := sortedAttrs(map[string]string{"b": "2", "a": "1", "c": "3"})
	if len(out) != 3 {
		t.Fatalf("got %d attrs, want 3", len(out))
	}
	if string(out[0].Key) != "a" || string(out[1].Key) != "b" || string(out[2].Key) != "c" {
		t.Errorf("attrs not sorted: %v", out)
	}
	if sortedAttrs(nil) == nil {
		// nil map yields an empty (non-nil) slice; either is acceptable but must
		// not panic.
		t.Log("sortedAttrs(nil) returned nil slice")
	}
}

func TestLevelToSeverity(t *testing.T) {
	cases := []struct {
		in   model.Level
		want log.Severity
	}{
		{model.LevelDebug, log.SeverityDebug},
		{model.LevelInfo, log.SeverityInfo},
		{model.LevelWarn, log.SeverityWarn},
		{model.LevelError, log.SeverityError},
		{model.LevelFatal, log.SeverityFatal},
		{model.Level(999), log.SeverityInfo}, // unknown -> Info
	}
	for _, c := range cases {
		if got := levelToSeverity(c.in); got != c.want {
			t.Errorf("levelToSeverity(%d) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSeverityText(t *testing.T) {
	cases := map[model.Level]string{
		model.LevelDebug: "DEBUG",
		model.LevelInfo:  "INFO",
		model.LevelWarn:  "WARN",
		model.LevelError: "ERROR",
		model.LevelFatal: "FATAL",
		model.Level(999): "INFO",
	}
	for in, want := range cases {
		if got := severityText(in); got != want {
			t.Errorf("severityText(%d) = %q, want %q", in, got, want)
		}
	}
}
