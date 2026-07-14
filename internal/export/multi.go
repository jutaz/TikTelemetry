package export

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jutaz/tiktelemetry/internal/model"
)

// MultiSink fans a single batch of telemetry out to several sinks. A failure in
// one sink is logged and joined into the returned error but never prevents the
// other sinks from receiving the data — one dead backend must not blind the
// others.
type MultiSink struct {
	sinks  []Sink
	logger *slog.Logger
}

// NewMultiSink wraps a fixed set of sinks. Build is the usual entry point for
// production use; this constructor exists for programmatic composition and
// testing. A nil logger is replaced with a no-op logger.
func NewMultiSink(logger *slog.Logger, sinks ...Sink) *MultiSink {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &MultiSink{sinks: sinks, logger: logger}
}

// WantsMetrics reports whether any underlying sink accepts metrics, so the
// agent can skip metric collection entirely when nothing consumes it.
func (m *MultiSink) WantsMetrics() bool {
	for _, s := range m.sinks {
		if s.Capabilities().Metrics {
			return true
		}
	}
	return false
}

// WantsLogs reports whether any underlying sink accepts logs.
func (m *MultiSink) WantsLogs() bool {
	for _, s := range m.sinks {
		if s.Capabilities().Logs {
			return true
		}
	}
	return false
}

// ConsumeMetrics delivers samples to every metrics-capable sink.
func (m *MultiSink) ConsumeMetrics(ctx context.Context, samples []model.Sample) error {
	var errs error
	for _, s := range m.sinks {
		if !s.Capabilities().Metrics {
			continue
		}
		if err := s.ConsumeMetrics(ctx, samples); err != nil {
			m.logger.Warn("sink rejected metrics", "exporter", s.Name(), "error", err)
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// ConsumeLogs delivers entries to every logs-capable sink.
func (m *MultiSink) ConsumeLogs(ctx context.Context, entries []model.LogEntry) error {
	var errs error
	for _, s := range m.sinks {
		if !s.Capabilities().Logs {
			continue
		}
		if err := s.ConsumeLogs(ctx, entries); err != nil {
			m.logger.Warn("sink rejected logs", "exporter", s.Name(), "error", err)
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// Shutdown shuts down every sink, joining errors.
func (m *MultiSink) Shutdown(ctx context.Context) error {
	var errs error
	for _, s := range m.sinks {
		if err := s.Shutdown(ctx); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}
