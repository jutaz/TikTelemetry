package otlp

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/jutaz/tiktelemetry/internal/model"
)

// logsPipeline owns the OTel LoggerProvider and a derived Logger.
type logsPipeline struct {
	lp     *sdklog.LoggerProvider
	logger log.Logger
}

// newLogsPipeline constructs the log pipeline: exporter, BatchProcessor, and
// LoggerProvider.
func newLogsPipeline(ctx context.Context, endpoint string, headers map[string]string, insecure bool, res *resource.Resource) (*logsPipeline, error) {
	expOpts := []otlploghttp.Option{
		otlploghttp.WithEndpointURL(endpoint),
	}
	if len(headers) > 0 {
		expOpts = append(expOpts, otlploghttp.WithHeaders(headers))
	}
	if insecure && !hasScheme(endpoint) {
		expOpts = append(expOpts, otlploghttp.WithInsecure())
	}

	exp, err := otlploghttp.New(ctx, expOpts...)
	if err != nil {
		return nil, fmt.Errorf("create OTLP logs exporter: %w", err)
	}

	proc := sdklog.NewBatchProcessor(exp)
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(proc),
	)

	return &logsPipeline{
		lp:     lp,
		logger: lp.Logger("github.com/jutaz/tiktelemetry"),
	}, nil
}

// consumeLogs sends a batch of log entries to the OTLP backend.
func (l *logsPipeline) consumeLogs(ctx context.Context, entries []model.LogEntry) error {
	for _, entry := range entries {
		var r log.Record
		r.SetTimestamp(entry.Time)
		sev := levelToSeverity(entry.Severity())
		r.SetSeverity(sev)
		r.SetSeverityText(severityText(entry.Severity()))
		r.SetBody(log.StringValue(entry.Message))
		r.AddAttributes(
			log.String("topics", strings.Join(entry.Topics, ",")),
			log.String("source", "mikrotik"),
		)
		l.logger.Emit(ctx, r)
	}
	return nil
}

// shutdownLogs gracefully drains and closes the log pipeline.
func (l *logsPipeline) shutdownLogs(ctx context.Context) error {
	return l.lp.Shutdown(ctx)
}

// levelToSeverity maps the model's Level to an OTel log Severity.
func levelToSeverity(l model.Level) log.Severity {
	switch l {
	case model.LevelDebug:
		return log.SeverityDebug
	case model.LevelInfo:
		return log.SeverityInfo
	case model.LevelWarn:
		return log.SeverityWarn
	case model.LevelError:
		return log.SeverityError
	case model.LevelFatal:
		return log.SeverityFatal
	default:
		return log.SeverityInfo
	}
}

// severityText returns an uppercase string representation of the level.
func severityText(l model.Level) string {
	switch l {
	case model.LevelDebug:
		return "DEBUG"
	case model.LevelInfo:
		return "INFO"
	case model.LevelWarn:
		return "WARN"
	case model.LevelError:
		return "ERROR"
	case model.LevelFatal:
		return "FATAL"
	default:
		return "INFO"
	}
}
