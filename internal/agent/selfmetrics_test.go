package agent

import (
	"context"
	"testing"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/export"
	"github.com/jutaz/tiktelemetry/internal/model"
)

// findSelf returns the first sample with the given name and matching attrs
// subset, or nil.
func findSelf(samples []model.Sample, name string, attrs map[string]string) *model.Sample {
	for i := range samples {
		s := &samples[i]
		if s.Name != name {
			continue
		}
		ok := true
		for k, v := range attrs {
			if s.Attributes[k] != v {
				ok = false
				break
			}
		}
		if ok {
			return s
		}
	}
	return nil
}

func TestSelfMetrics_Samples(t *testing.T) {
	m := newSelfMetrics()

	// Simulate two scrapes and some errors.
	m.recordScrape(120, 42, true)
	m.recordScrape(90, 40, false) // second scrape: router unreachable
	m.recordCollectorError("wireless")
	m.recordCollectorError("wireless")
	m.recordCollectorError("ipsec")
	m.recordMetricExportError()
	m.recordLogExportError()
	m.recordLogCollectError()

	samples := m.samples()

	// scrapes.total is a cumulative counter = 2.
	if s := findSelf(samples, "tiktelemetry.scrapes.total", nil); s == nil {
		t.Error("missing tiktelemetry.scrapes.total")
	} else {
		if s.Value != 2 {
			t.Errorf("scrapes.total = %d, want 2", s.Value)
		}
		if s.Kind != model.KindCounter {
			t.Errorf("scrapes.total kind = %v, want counter", s.Kind)
		}
	}

	// Last-scrape gauges reflect the most recent scrape (the unreachable one).
	if s := findSelf(samples, "tiktelemetry.scrape.duration", nil); s == nil || s.Value != 90 || s.Kind != model.KindGauge {
		t.Errorf("scrape.duration = %+v, want value 90 gauge", s)
	}
	if s := findSelf(samples, "tiktelemetry.scrape.samples", nil); s == nil || s.Value != 40 {
		t.Errorf("scrape.samples = %+v, want value 40", s)
	}
	if s := findSelf(samples, "tiktelemetry.router.up", nil); s == nil || s.Value != 0 {
		t.Errorf("router.up = %+v, want 0 (last scrape unreachable)", s)
	}

	// Per-collector error counters, labelled by collector.
	if s := findSelf(samples, "tiktelemetry.collector.errors", map[string]string{"collector": "wireless"}); s == nil || s.Value != 2 {
		t.Errorf("collector.errors{wireless} = %+v, want 2", s)
	}
	if s := findSelf(samples, "tiktelemetry.collector.errors", map[string]string{"collector": "ipsec"}); s == nil || s.Value != 1 {
		t.Errorf("collector.errors{ipsec} = %+v, want 1", s)
	}

	// Export/collect error counters, split by signal where relevant.
	if s := findSelf(samples, "tiktelemetry.export.errors", map[string]string{"signal": "metrics"}); s == nil || s.Value != 1 {
		t.Errorf("export.errors{metrics} = %+v, want 1", s)
	}
	if s := findSelf(samples, "tiktelemetry.export.errors", map[string]string{"signal": "logs"}); s == nil || s.Value != 1 {
		t.Errorf("export.errors{logs} = %+v, want 1", s)
	}
	if s := findSelf(samples, "tiktelemetry.log.collect.errors", nil); s == nil || s.Value != 1 {
		t.Errorf("log.collect.errors = %+v, want 1", s)
	}
}

func TestSelfMetrics_RouterUpTransition(t *testing.T) {
	m := newSelfMetrics()

	m.recordScrape(10, 5, true)
	if s := findSelf(m.samples(), "tiktelemetry.router.up", nil); s.Value != 1 {
		t.Errorf("router.up after reachable scrape = %d, want 1", s.Value)
	}

	m.recordScrape(10, 0, false)
	if s := findSelf(m.samples(), "tiktelemetry.router.up", nil); s.Value != 0 {
		t.Errorf("router.up after unreachable scrape = %d, want 0", s.Value)
	}

	m.recordScrape(10, 5, true)
	if s := findSelf(m.samples(), "tiktelemetry.router.up", nil); s.Value != 1 {
		t.Errorf("router.up after recovery = %d, want 1", s.Value)
	}
}

func TestSelfMetrics_Empty(t *testing.T) {
	// Before any scrape, samples() still returns the base set with zero values,
	// so the series exist from the first push.
	m := newSelfMetrics()
	samples := m.samples()
	for _, name := range []string{
		"tiktelemetry.scrapes.total",
		"tiktelemetry.scrape.duration",
		"tiktelemetry.scrape.samples",
		"tiktelemetry.router.up",
	} {
		if findSelf(samples, name, nil) == nil {
			t.Errorf("missing base self-metric %s before first scrape", name)
		}
	}
	// No per-collector series exist until a collector error is recorded.
	if findSelf(samples, "tiktelemetry.collector.errors", nil) != nil {
		t.Error("collector.errors should not exist before any collector error")
	}
}

// capturingSink records the metric samples it receives, so we can assert what
// the agent injected.
type capturingSink struct {
	cap     export.Capabilities
	metrics [][]model.Sample
}

func (c *capturingSink) Name() string                      { return "capture" }
func (c *capturingSink) Capabilities() export.Capabilities { return c.cap }
func (c *capturingSink) ConsumeMetrics(_ context.Context, s []model.Sample) error {
	cp := make([]model.Sample, len(s))
	copy(cp, s)
	c.metrics = append(c.metrics, cp)
	return nil
}
func (c *capturingSink) ConsumeLogs(context.Context, []model.LogEntry) error { return nil }
func (c *capturingSink) Shutdown(context.Context) error                      { return nil }

// TestAgent_EmitsSelfMetricsWhenRouterUnreachable runs a single scrape against
// an unreachable router and verifies the agent still pushes its self-metrics
// (with router.up=0), proving observability survives a router outage.
func TestAgent_EmitsSelfMetricsWhenRouterUnreachable(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("ROUTER_ADDRESS", "127.0.0.1:1") // refuses connections
	t.Setenv("ROUTER_DIAL_TIMEOUT", "100ms")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	sink := &capturingSink{cap: export.Capabilities{Metrics: true}}
	multi := export.NewMultiSink(quietLogger(), sink)
	a := New(cfg, quietLogger(), multi)

	// One scrape cycle against the dead router.
	a.scrapeMetrics(context.Background())

	if len(sink.metrics) != 1 {
		t.Fatalf("sink received %d metric batches, want 1", len(sink.metrics))
	}
	batch := sink.metrics[0]

	// Self-metrics must be present even though every collector failed.
	up := findSelf(batch, "tiktelemetry.router.up", nil)
	if up == nil {
		t.Fatal("missing tiktelemetry.router.up in the batch")
	}
	if up.Value != 0 {
		t.Errorf("router.up = %d, want 0 (router unreachable)", up.Value)
	}

	// At least one collector error should be recorded (all collectors failed to
	// dial), surfaced as a per-collector counter.
	if findSelf(batch, "tiktelemetry.collector.errors", nil) == nil {
		t.Error("expected at least one tiktelemetry.collector.errors series after a failed scrape")
	}

	// scrapes.total should be 1 after one scrape.
	if s := findSelf(batch, "tiktelemetry.scrapes.total", nil); s == nil || s.Value != 1 {
		t.Errorf("scrapes.total = %+v, want 1", s)
	}
}
