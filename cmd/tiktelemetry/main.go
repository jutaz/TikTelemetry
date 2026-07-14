// Command tiktelemetry is a hyper-lightweight agent that pulls metrics and
// logs from a MikroTik RouterOS device via its binary API and pushes them
// directly to one or more telemetry backends (Grafana Cloud via OTLP,
// Prometheus remote_write, and/or Loki; any OTLP/HTTP collector; ...) with no
// intermediary agent required.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jutaz/tiktelemetry/internal/agent"
	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/export"

	// Registers all built-in export adapters and aliases via their init().
	_ "github.com/jutaz/tiktelemetry/internal/exporters"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load()
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).
			Error("configuration error", "error", err)
		return 1
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	// Root context cancelled on SIGINT/SIGTERM for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sinks, err := export.Build(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to build exporters", "error", err)
		return 1
	}
	defer func() {
		// Flush and close sinks with a bounded, shutdown-specific context so a
		// dead backend cannot hang exit indefinitely.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := sinks.Shutdown(shutdownCtx); err != nil {
			logger.Warn("exporter shutdown reported errors", "error", err)
		}
	}()

	a := agent.New(cfg, logger, sinks)
	if err := a.Run(ctx); err != nil {
		logger.Error("agent exited with error", "error", err)
		return 1
	}
	return 0
}

// newLogger builds a JSON slog logger at the configured level. JSON output is
// chosen so the host (Docker/systemd) can parse the agent's own diagnostics.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
