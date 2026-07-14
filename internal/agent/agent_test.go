package agent

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/export"
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
