//go:build e2e

package e2e

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/mikrotik"
	"github.com/jutaz/tiktelemetry/internal/model"
)

// newClient builds a mikrotik.Client pointed at the E2E router.
func newClient(t *testing.T, r router) *mikrotik.Client {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return mikrotik.New(config.RouterConfig{
		Address:     r.addr,
		Username:    r.user,
		Password:    r.pass,
		DialTimeout: 5 * time.Second,
	}, logger)
}

// TestSystemResourceCollector verifies the system collector returns real
// gauges/counters from a live RouterOS instance.
func TestSystemResourceCollector(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	r := sharedRouter
	client := newClient(t, r)
	defer client.Close()

	collectors := mikrotik.DefaultCollectors()
	var sys mikrotik.Collector
	for _, c := range collectors {
		if c.Name() == "system" {
			sys = c
		}
	}
	if sys == nil {
		t.Fatal("system collector not found in DefaultCollectors")
	}

	samples, err := sys.Collect(ctx, client)
	if err != nil {
		t.Fatalf("system Collect: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("system collector returned no samples")
	}

	byName := indexByName(samples)

	// CPU load must be present and within a sane percentage range.
	cpu, ok := byName["mikrotik.system.cpu.load"]
	if !ok {
		t.Error("missing mikrotik.system.cpu.load")
	} else {
		if cpu.Kind != model.KindGauge {
			t.Errorf("cpu.load kind = %v, want gauge", cpu.Kind)
		}
		if cpu.Value < 0 || cpu.Value > 100 {
			t.Errorf("cpu.load = %d, want 0..100", cpu.Value)
		}
	}

	// Free memory must be present and positive.
	if mem, ok := byName["mikrotik.system.memory.free"]; !ok {
		t.Error("missing mikrotik.system.memory.free")
	} else if mem.Value <= 0 {
		t.Errorf("memory.free = %d, want > 0", mem.Value)
	}

	// Uptime is a counter and should be > 0 once the box has booted.
	if up, ok := byName["mikrotik.system.uptime"]; !ok {
		t.Error("missing mikrotik.system.uptime")
	} else {
		if up.Kind != model.KindCounter {
			t.Errorf("uptime kind = %v, want counter", up.Kind)
		}
		if up.Value <= 0 {
			t.Errorf("uptime = %d, want > 0", up.Value)
		}
	}
}

// TestInterfaceCollector verifies the interface collector returns per-interface
// series from a live RouterOS instance. CHR always has at least one interface.
func TestInterfaceCollector(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	r := sharedRouter
	client := newClient(t, r)
	defer client.Close()

	var iface mikrotik.Collector
	for _, c := range mikrotik.DefaultCollectors() {
		if c.Name() == "interface" {
			iface = c
		}
	}
	if iface == nil {
		t.Fatal("interface collector not found")
	}

	samples, err := iface.Collect(ctx, client)
	if err != nil {
		t.Fatalf("interface Collect: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("interface collector returned no samples (expected at least one interface)")
	}

	// Every sample must carry an interface attribute, and we must see at least
	// one "up" gauge (emitted for every interface).
	sawUp := false
	sawInterfaceAttr := false
	for _, s := range samples {
		if s.Attributes["interface"] == "" {
			t.Errorf("sample %q missing interface attribute", s.Name)
		} else {
			sawInterfaceAttr = true
		}
		if s.Name == "mikrotik.interface.up" {
			sawUp = true
			if s.Value != 0 && s.Value != 1 {
				t.Errorf("interface.up = %d, want 0 or 1", s.Value)
			}
		}
	}
	if !sawUp {
		t.Error("no mikrotik.interface.up gauge emitted")
	}
	if !sawInterfaceAttr {
		t.Error("no sample carried an interface attribute")
	}
}

// TestLogCollector verifies the log collector's de-duplication against a live
// instance: the first poll primes the cursor and returns nothing, and after
// generating a log entry a subsequent poll returns it.
func TestLogCollector(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	r := sharedRouter
	client := newClient(t, r)
	defer client.Close()

	lc := mikrotik.NewLogCollector()

	// First poll primes the cursor and returns nothing (avoids dumping the
	// historical buffer on startup).
	first, err := lc.Collect(ctx, client)
	if err != nil {
		t.Fatalf("first log Collect: %v", err)
	}
	if len(first) != 0 {
		t.Errorf("first poll returned %d entries, want 0 (cursor priming)", len(first))
	}

	// Generate a fresh log entry on the router.
	writeLog(t, r, "tiktelemetry-e2e-marker")

	// Poll again; the new entry should surface. Retry a few times since log
	// write and read are eventually consistent.
	var found bool
	var markerTime time.Time
	for i := 0; i < 10 && !found; i++ {
		entries, err := lc.Collect(ctx, client)
		if err != nil {
			t.Fatalf("log Collect: %v", err)
		}
		for _, e := range entries {
			if strings.Contains(e.Message, "tiktelemetry-e2e-marker") {
				found = true
				markerTime = e.Time
			}
		}
		if !found {
			time.Sleep(2 * time.Second)
		}
	}
	if !found {
		t.Fatal("did not observe the marker log entry after generating it")
	}

	// The timestamp must be resolved from the router clock (not epoch, not
	// wildly off). The marker was written moments ago, so its time should be
	// within a few minutes of the local wall clock.
	if markerTime.IsZero() {
		t.Error("marker entry has a zero timestamp")
	}
	if d := time.Since(markerTime); d < -5*time.Minute || d > 5*time.Minute {
		t.Errorf("marker timestamp %s is %s from now; expected within a few minutes", markerTime, d)
	}
}

// writeLog issues a :log command on the router to generate a known log line.
func writeLog(t *testing.T, r router, msg string) {
	t.Helper()
	client := r.dial(t)
	defer client.Close()
	if _, err := client.Run("/log/info", "=message="+msg); err != nil {
		t.Fatalf("write log: %v", err)
	}
}

func indexByName(samples []model.Sample) map[string]model.Sample {
	m := make(map[string]model.Sample, len(samples))
	for _, s := range samples {
		m[s.Name] = s
	}
	return m
}
