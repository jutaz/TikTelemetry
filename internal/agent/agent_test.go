package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/export"
	"github.com/jutaz/tiktelemetry/internal/mikrotik"
	"github.com/jutaz/tiktelemetry/internal/model"
)

// fakeSink implements export.Sink for testing without a live router.
type fakeSink struct {
	name string
	cap  export.Capabilities
}

func (f *fakeSink) Name() string                                         { return f.name }
func (f *fakeSink) Capabilities() export.Capabilities                    { return f.cap }
func (f *fakeSink) ConsumeMetrics(context.Context, []model.Sample) error { return nil }
func (f *fakeSink) ConsumeLogs(context.Context, []model.LogEntry) error  { return nil }
func (f *fakeSink) Shutdown(context.Context) error                       { return nil }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestAgent_CollectorSelection verifies that the COLLECTORS env var restricts
// which collectors the agent runs, and that log capability drives the log
// collector's presence.
func TestAgent_CollectorSelection(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("COLLECTORS", "system,interface")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	// Metrics + logs capable sink.
	multi := export.NewMultiSink(quietLogger(),
		&fakeSink{name: "s", cap: export.Capabilities{Metrics: true, Logs: true}})
	a := New(cfg, quietLogger(), multi)

	if len(a.targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(a.targets))
	}
	tgt := a.targets[0]
	if len(tgt.collectors) != 2 {
		t.Errorf("selected %d collectors, want 2", len(tgt.collectors))
	}
	names := map[string]bool{}
	for _, c := range tgt.collectors {
		names[c.Name()] = true
	}
	if !names["system"] || !names["interface"] {
		t.Errorf("expected system and interface collectors, got %v", names)
	}
	if tgt.logColl == nil {
		t.Error("expected a log collector when a logs-capable sink is present")
	}
}

// TestAgent_NoLogCollectorWithoutLogSink verifies the log collector is omitted
// when no sink consumes logs.
func TestAgent_NoLogCollectorWithoutLogSink(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	multi := export.NewMultiSink(quietLogger(),
		&fakeSink{name: "s", cap: export.Capabilities{Metrics: true, Logs: false}})
	a := New(cfg, quietLogger(), multi)

	if !a.wantMetrics {
		t.Error("wantMetrics should be true")
	}
	if a.wantLogs {
		t.Error("wantLogs should be false")
	}
	tgt := a.targets[0]
	if tgt.logColl != nil {
		t.Error("log collector should be nil when no sink consumes logs")
	}
	// With no COLLECTORS restriction, all default collectors run.
	if len(tgt.collectors) == 0 {
		t.Error("expected default collectors")
	}
}

// TestAgent_RunContextCancelled verifies that Run returns promptly when the
// context is already cancelled, without panicking or hanging.
func TestAgent_RunContextCancelled(t *testing.T) {
	// Build a minimal config with a very short dial timeout and an unroutable
	// address so the first scrape fails fast.
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("ROUTER_ADDRESS", "127.0.0.1:1")
	t.Setenv("ROUTER_DIAL_TIMEOUT", "10ms")
	t.Setenv("POLL_INTERVAL", "1s")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error: %v", err)
	}

	sink := &fakeSink{name: "test", cap: export.Capabilities{Metrics: true, Logs: true}}
	multi := export.NewMultiSink(quietLogger(), sink)

	agent := New(cfg, quietLogger(), multi)

	// Create an already-cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Run should return without blocking.
	done := make(chan struct{})
	go func() {
		err := agent.Run(ctx)
		// The client.Close() on a nil internal client returns nil;
		// we only care that it returns promptly and without panicking.
		t.Logf("Run returned: %v", err)
		close(done)
	}()

	select {
	case <-done:
		// Success.
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5 seconds on cancelled context")
	}
}

// TestAgent_RunUnreachableRouter verifies that when the router is unreachable,
// the agent does not panic and returns nil (context cancellation). It runs a
// full cycle: one scrape attempt that will fail to dial, then context
// cancellation causes Run to return.
// stubCollector is a Collector whose Collect returns a fixed result, so
// scrapeMetrics can be driven without a live router.
type stubCollector struct {
	name    string
	samples []model.Sample
	err     error
}

func (c *stubCollector) Name() string { return c.name }
func (c *stubCollector) Collect(context.Context, mikrotik.Runner) ([]model.Sample, error) {
	return c.samples, c.err
}

// TestScrapeMetrics_FeatureAbsentIsQuiet verifies that a collector returning a
// "feature absent" error (RouterOS !trap "no such command prefix", e.g. health
// on a board with no sensors) is skipped quietly: no collector-error metric is
// recorded, the router still counts as reached, and a real error is still
// recorded.
func TestScrapeMetrics_FeatureAbsentIsQuiet(t *testing.T) {
	okSample := model.Sample{Name: "mikrotik.system.cpu", Value: 1}
	tgt := &target{
		name: "ap-basement",
		collectors: []mikrotik.Collector{
			&stubCollector{name: "system", samples: []model.Sample{okSample}},
			&stubCollector{name: "health", err: errors.New("from RouterOS device: no such command prefix")},
			&stubCollector{name: "firewall", err: errors.New("connection reset by peer")},
		},
		self: newSelfMetrics(),
	}

	a := &Agent{logger: quietLogger()}
	samples := a.scrapeMetrics(context.Background(), tgt)

	// The feature-absent collector must NOT be recorded as an error.
	if n := tgt.self.collectorErrorTotal["health"]; n != 0 {
		t.Errorf("health recorded %d collector errors, want 0 (feature absent is not a failure)", n)
	}
	// A genuine error still must be recorded.
	if n := tgt.self.collectorErrorTotal["firewall"]; n != 1 {
		t.Errorf("firewall recorded %d collector errors, want 1", n)
	}
	// The router answered (system succeeded, health merely lacks the command).
	if tgt.self.routerUp != 1 {
		t.Errorf("routerUp = %d, want 1 (the router was reached)", tgt.self.routerUp)
	}
	// The successful collector's sample must still flow through (plus self-metrics).
	var sawOK bool
	for _, s := range samples {
		if s.Name == okSample.Name {
			sawOK = true
		}
	}
	if !sawOK {
		t.Errorf("expected the system sample to be present in the scrape output")
	}
}

func TestAgent_RunUnreachableRouter(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("ROUTER_ADDRESS", "127.0.0.1:1")
	t.Setenv("ROUTER_DIAL_TIMEOUT", "10ms")
	t.Setenv("POLL_INTERVAL", "1s")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error: %v", err)
	}

	sink := &fakeSink{name: "test", cap: export.Capabilities{Metrics: true, Logs: false}}
	multi := export.NewMultiSink(quietLogger(), sink)

	agent := New(cfg, quietLogger(), multi)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = agent.Run(ctx)
	// Run returns a.client.Close() error (nil for never-connected client) or nil.
	// The important thing is it returns without panic.
	if err != nil {
		t.Logf("Run returned error (acceptable): %v", err)
	}
}
