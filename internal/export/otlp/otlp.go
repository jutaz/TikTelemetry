// Package otlp implements the export.Sink interface for OpenTelemetry Protocol
// (OTLP) exporters, supporting both metrics and logs via HTTP.
package otlp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/export"
	"github.com/jutaz/tiktelemetry/internal/export/exporthelp"
	"github.com/jutaz/tiktelemetry/internal/model"
)

func init() {
	export.Register("otlp", New)
}

// Sink is an export.Sink that pushes metrics and logs to an OTLP-compatible
// backend (e.g. Grafana Cloud, OpenTelemetry Collector).
type Sink struct {
	mp *metricsPipeline
	lp *logsPipeline
}

// Name returns the stable identifier "otlp".
func (s *Sink) Name() string { return "otlp" }

// Capabilities reports that this sink handles both metrics and logs.
func (s *Sink) Capabilities() export.Capabilities {
	return export.Capabilities{Metrics: true, Logs: true}
}

// ConsumeMetrics records a batch of metric samples.
func (s *Sink) ConsumeMetrics(ctx context.Context, samples []model.Sample) error {
	// model import is in metrics.go
	return s.mp.consumeMetrics(ctx, samples)
}

// ConsumeLogs sends a batch of log entries.
func (s *Sink) ConsumeLogs(ctx context.Context, entries []model.LogEntry) error {
	return s.lp.consumeLogs(ctx, entries)
}

// Shutdown flushes and shuts down both pipelines, joining any errors.
func (s *Sink) Shutdown(ctx context.Context) error {
	return errors.Join(
		s.mp.shutdownMetrics(ctx),
		s.lp.shutdownLogs(ctx),
	)
}

// buildHeaders assembles the HTTP headers map for both OTLP exporters from
// config environment variables.
func buildHeaders(cfg config.Config) (map[string]string, error) {
	h, err := cfg.EnvHeaders("OTLP_HEADERS")
	if err != nil {
		return nil, err
	}

	// Bearer token.
	if token := cfg.Env("OTLP_BEARER_TOKEN"); token != "" {
		h["Authorization"] = "Bearer " + token
	}

	// Basic auth (overrides bearer when either is set).
	user, pass := cfg.Env("OTLP_USER"), cfg.Env("OTLP_PASS")
	if user != "" || pass != "" {
		if ba := exporthelp.BasicAuth(user, pass); ba != "" {
			h["Authorization"] = ba
		}
	}

	return h, nil
}

// New constructs an OTLP sink. It reads endpoint and auth configuration from
// the environment (via cfg.Env*) and builds the metric and log pipelines.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (export.Sink, error) {
	endpoint := cfg.Env("OTLP_ENDPOINT")
	if endpoint == "" {
		return nil, errors.New("OTLP_ENDPOINT is required")
	}
	endpoint = strings.TrimRight(endpoint, "/")

	headers, err := buildHeaders(cfg)
	if err != nil {
		return nil, err
	}
	insecure := cfg.EnvBool("OTLP_INSECURE", false)

	res, err := newResource(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	// Build metrics pipeline.
	mp, err := newMetricsPipeline(
		ctx,
		endpoint+"/v1/metrics",
		headers,
		insecure,
		cfg.PollInterval,
		res,
	)
	if err != nil {
		return nil, fmt.Errorf("init metrics: %w", err)
	}

	// Build logs pipeline.
	lp, err := newLogsPipeline(
		ctx,
		endpoint+"/v1/logs",
		headers,
		insecure,
		res,
	)
	if err != nil {
		// Tear down metrics on failure.
		_ = mp.shutdownMetrics(ctx)
		return nil, fmt.Errorf("init logs: %w", err)
	}

	return &Sink{mp: mp, lp: lp}, nil
}
