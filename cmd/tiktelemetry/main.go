// Command tiktelemetry is a hyper-lightweight agent that pulls metrics and
// logs from a MikroTik RouterOS device via its binary API and pushes them
// directly to one or more telemetry backends (Grafana Cloud via OTLP,
// Prometheus remote_write, and/or Loki; any OTLP/HTTP collector; ...) with no
// intermediary agent required.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jutaz/tiktelemetry/internal/agent"
	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/export"
	"github.com/jutaz/tiktelemetry/internal/preflight"

	// Registers all built-in export adapters and aliases via their init().
	_ "github.com/jutaz/tiktelemetry/internal/exporters"
)

// version is overridable at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("tiktelemetry", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		checkMode   = fs.Bool("check", false, "run read-only preflight checks (router API + exporter endpoints) and exit")
		showVersion = fs.Bool("version", false, "print version and exit")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "tiktelemetry — push MikroTik RouterOS metrics and logs to OTLP/Prometheus/Loki backends.\n\n")
		fmt.Fprintf(os.Stderr, "Usage: tiktelemetry [flags]\n\n")
		fmt.Fprintf(os.Stderr, "All configuration is via environment variables (see the README).\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Println("tiktelemetry", version)
		return 0
	}

	cfg, err := config.Load()
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).
			Error("configuration error", "error", err)
		return 1
	}

	if *checkMode {
		return runCheck(cfg)
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)
	logger.Info("starting tiktelemetry", "version", version)

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

// runCheck executes the preflight diagnostics and prints a human-readable
// report to stdout. It returns 0 when every check passes, 1 otherwise.
func runCheck(cfg config.Config) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Printf("tiktelemetry %s — preflight check\n\n", version)
	report := preflight.Run(ctx, cfg)

	for _, res := range report.Results {
		mark := "PASS"
		if !res.OK {
			mark = "FAIL"
		}
		fmt.Printf("[%s] %-22s %s (%s)\n", mark, res.Name, res.Detail, res.Elapsed.Round(time.Millisecond))
	}

	fmt.Println()
	if report.OK() {
		fmt.Println("All checks passed. The agent should run cleanly with this configuration.")
		return 0
	}
	fmt.Println("One or more checks failed. Fix the items marked FAIL above, then re-run --check.")
	return 1
}

// newLogger builds a JSON slog logger at the configured level. JSON output is
// chosen so the host (Docker/RouterOS/systemd) can parse the agent's own
// diagnostics.
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
