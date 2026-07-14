package export

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/jutaz/tiktelemetry/internal/model"
)

// fakeSink implements Sink for testing. It records calls and payloads so
// tests can verify fan-out, skip behaviour, and error isolation.
type fakeSink struct {
	mu sync.Mutex

	name        string
	cap         Capabilities
	returnErr   error // returned by ConsumeMetrics / ConsumeLogs
	shutdownErr error // returned by Shutdown

	metricsCalled  int
	logsCalled     int
	shutdownCalled int

	lastSamples []model.Sample
	lastEntries []model.LogEntry
}

func newFakeSink(name string, cap Capabilities) *fakeSink {
	return &fakeSink{name: name, cap: cap}
}

func (f *fakeSink) Name() string { return f.name }

func (f *fakeSink) Capabilities() Capabilities { return f.cap }

func (f *fakeSink) ConsumeMetrics(_ context.Context, samples []model.Sample) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metricsCalled++
	f.lastSamples = append([]model.Sample(nil), samples...)
	return f.returnErr
}

func (f *fakeSink) ConsumeLogs(_ context.Context, entries []model.LogEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logsCalled++
	f.lastEntries = append([]model.LogEntry(nil), entries...)
	return f.returnErr
}

func (f *fakeSink) Shutdown(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdownCalled++
	return f.shutdownErr
}

// helpers to inspect calls safely.
func (f *fakeSink) callCounts() (metrics, logs, shutdown int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.metricsCalled, f.logsCalled, f.shutdownCalled
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMultiSink_WantsMetrics_WantsLogs(t *testing.T) {
	t.Run("both false when no sinks", func(t *testing.T) {
		m := NewMultiSink(quietLogger())
		if m.WantsMetrics() {
			t.Error("WantsMetrics = true, want false")
		}
		if m.WantsLogs() {
			t.Error("WantsLogs = true, want false")
		}
	})

	t.Run("metrics-only sink", func(t *testing.T) {
		s := newFakeSink("m", Capabilities{Metrics: true})
		m := NewMultiSink(quietLogger(), s)
		if !m.WantsMetrics() {
			t.Error("WantsMetrics = false, want true")
		}
		if m.WantsLogs() {
			t.Error("WantsLogs = true, want false")
		}
	})

	t.Run("logs-only sink", func(t *testing.T) {
		s := newFakeSink("l", Capabilities{Logs: true})
		m := NewMultiSink(quietLogger(), s)
		if m.WantsMetrics() {
			t.Error("WantsMetrics = true, want false")
		}
		if !m.WantsLogs() {
			t.Error("WantsLogs = false, want true")
		}
	})

	t.Run("both capabilities with two sinks", func(t *testing.T) {
		ms := newFakeSink("m", Capabilities{Metrics: true})
		ls := newFakeSink("l", Capabilities{Logs: true})
		m := NewMultiSink(quietLogger(), ms, ls)
		if !m.WantsMetrics() {
			t.Error("WantsMetrics = false, want true")
		}
		if !m.WantsLogs() {
			t.Error("WantsLogs = false, want true")
		}
	})

	t.Run("both capabilities in one sink", func(t *testing.T) {
		s := newFakeSink("both", Capabilities{Metrics: true, Logs: true})
		m := NewMultiSink(quietLogger(), s)
		if !m.WantsMetrics() {
			t.Error("WantsMetrics = false, want true")
		}
		if !m.WantsLogs() {
			t.Error("WantsLogs = false, want true")
		}
	})
}

func TestMultiSink_ConsumeMetrics_SkipsLogsOnly(t *testing.T) {
	metricsSink := newFakeSink("m", Capabilities{Metrics: true})
	logsSink := newFakeSink("l", Capabilities{Logs: true})

	m := NewMultiSink(quietLogger(), metricsSink, logsSink)
	samples := []model.Sample{{Name: "test"}}
	_ = m.ConsumeMetrics(context.Background(), samples)

	mc, lc, _ := metricsSink.callCounts()
	if mc != 1 {
		t.Errorf("metrics-capable sink ConsumeMetrics called %d times, want 1", mc)
	}
	// logsSink should NOT have received metrics
	lc2, _, _ := logsSink.callCounts()
	if lc2 != 0 {
		t.Errorf("logs-only sink ConsumeMetrics called %d times, want 0", lc2)
	}
	_ = lc // silence unused
}

func TestMultiSink_ConsumeLogs_SkipsMetricsOnly(t *testing.T) {
	metricsSink := newFakeSink("m", Capabilities{Metrics: true})
	logsSink := newFakeSink("l", Capabilities{Logs: true})

	m := NewMultiSink(quietLogger(), metricsSink, logsSink)
	entries := []model.LogEntry{{Message: "hello"}}
	_ = m.ConsumeLogs(context.Background(), entries)

	_, ll, _ := logsSink.callCounts()
	if ll != 1 {
		t.Errorf("logs-capable sink ConsumeLogs called %d times, want 1", ll)
	}
	// metricsSink should NOT have received logs
	_, ml, _ := metricsSink.callCounts()
	if ml != 0 {
		t.Errorf("metrics-only sink ConsumeLogs called %d times, want 0", ml)
	}
}

func TestMultiSink_ConsumeMetrics_FanOut(t *testing.T) {
	s1 := newFakeSink("s1", Capabilities{Metrics: true})
	s2 := newFakeSink("s2", Capabilities{Metrics: true})

	m := NewMultiSink(quietLogger(), s1, s2)
	samples := []model.Sample{{Name: "a"}, {Name: "b"}}
	_ = m.ConsumeMetrics(context.Background(), samples)

	for _, s := range []*fakeSink{s1, s2} {
		mc, _, _ := s.callCounts()
		if mc != 1 {
			t.Errorf("sink %s ConsumeMetrics called %d times, want 1", s.name, mc)
		}
	}
}

func TestMultiSink_ConsumeMetrics_ErrorIsolation(t *testing.T) {
	s1 := newFakeSink("s1", Capabilities{Metrics: true})
	s1.returnErr = errors.New("s1 failed")
	s2 := newFakeSink("s2", Capabilities{Metrics: true})

	m := NewMultiSink(quietLogger(), s1, s2)
	samples := []model.Sample{{Name: "test"}}
	err := m.ConsumeMetrics(context.Background(), samples)

	// Both sinks should have been called.
	mc1, _, _ := s1.callCounts()
	if mc1 != 1 {
		t.Errorf("s1 ConsumeMetrics called %d times, want 1", mc1)
	}
	mc2, _, _ := s2.callCounts()
	if mc2 != 1 {
		t.Errorf("s2 ConsumeMetrics called %d times, want 1", mc2)
	}

	// Error should be non-nil and include s1's error.
	if err == nil {
		t.Fatal("expected non-nil joined error")
	}
	if !errors.Is(err, s1.returnErr) {
		t.Errorf("joined error should contain s1 error: %v", err)
	}
}

func TestMultiSink_Shutdown_CallsAllAndJoinsErrors(t *testing.T) {
	s1 := newFakeSink("s1", Capabilities{Metrics: true})
	s1.shutdownErr = errors.New("shutdown s1 failed")
	s2 := newFakeSink("s2", Capabilities{Metrics: true})
	s2.shutdownErr = errors.New("shutdown s2 failed")
	s3 := newFakeSink("s3", Capabilities{Logs: true}) // different capability, should still be shut down

	m := NewMultiSink(quietLogger(), s1, s2, s3)
	ctx := context.Background()
	err := m.Shutdown(ctx)

	// Every sink must have been called.
	for _, s := range []*fakeSink{s1, s2, s3} {
		_, _, sc := s.callCounts()
		if sc != 1 {
			t.Errorf("sink %s Shutdown called %d times, want 1", s.name, sc)
		}
	}

	if err == nil {
		t.Fatal("expected non-nil joined error")
	}
	if !errors.Is(err, s1.shutdownErr) {
		t.Errorf("joined error should contain s1 error: %v", err)
	}
	if !errors.Is(err, s2.shutdownErr) {
		t.Errorf("joined error should contain s2 error: %v", err)
	}
}

func TestMultiSink_NilLogger(t *testing.T) {
	// Passing nil logger should not panic.
	s := newFakeSink("s", Capabilities{Metrics: true, Logs: true})
	m := NewMultiSink(nil, s)
	_ = m.ConsumeMetrics(context.Background(), []model.Sample{{Name: "t"}})
	_ = m.ConsumeLogs(context.Background(), []model.LogEntry{{Message: "t"}})
}
