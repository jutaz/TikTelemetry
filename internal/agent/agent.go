// Package agent wires the MikroTik collection layer to the composable export
// layer and drives the periodic scrape/push loop. It collects each signal once
// per tick and fans it out to every configured sink via the MultiSink.
package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/export"
	"github.com/jutaz/tiktelemetry/internal/mikrotik"
	"github.com/jutaz/tiktelemetry/internal/model"
)

// Agent owns the router client, collectors, export sinks and the run loop.
type Agent struct {
	cfg    config.Config
	logger *slog.Logger

	client     *mikrotik.Client
	collectors []mikrotik.Collector
	logColl    *mikrotik.LogCollector

	sinks *export.MultiSink

	// wantMetrics / wantLogs cache whether any sink consumes each signal, so we
	// skip the corresponding router queries entirely when nothing wants them.
	wantMetrics bool
	wantLogs    bool
}

// New constructs an Agent from resolved configuration and the built sinks. The
// router client is created lazily and connects on first scrape.
func New(cfg config.Config, logger *slog.Logger, sinks *export.MultiSink) *Agent {
	collectors, unknown := mikrotik.SelectCollectors(cfg.Collectors)
	if len(unknown) > 0 {
		logger.Warn("ignoring unknown collectors in COLLECTORS", "unknown", unknown)
	}

	a := &Agent{
		cfg:         cfg,
		logger:      logger,
		client:      mikrotik.New(cfg.Router, logger),
		collectors:  collectors,
		sinks:       sinks,
		wantMetrics: sinks.WantsMetrics(),
		wantLogs:    sinks.WantsLogs(),
	}
	if a.wantLogs {
		a.logColl = mikrotik.NewLogCollector()
	}
	return a
}

// Run blocks running the scrape loop until ctx is cancelled. It performs an
// immediate first scrape, then ticks at cfg.PollInterval. Errors from a single
// scrape are logged and swallowed so a transient failure does not tear down the
// agent.
func (a *Agent) Run(ctx context.Context) error {
	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()

	a.logger.Info("agent started",
		"router", a.cfg.Router.Address,
		"interval", a.cfg.PollInterval,
		"metrics", a.wantMetrics,
		"logs", a.wantLogs,
	)

	// Run one scrape immediately so we don't wait a full interval for the
	// first data point.
	a.scrape(ctx)

	for {
		select {
		case <-ctx.Done():
			a.logger.Info("agent stopping")
			return a.client.Close()
		case <-ticker.C:
			a.scrape(ctx)
		}
	}
}

// scrape performs a single collection + push cycle, bounded by a timeout so a
// hung router cannot stall the loop.
func (a *Agent) scrape(ctx context.Context) {
	budget := a.cfg.PollInterval
	if budget < 5*time.Second {
		budget = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	if a.wantMetrics {
		a.scrapeMetrics(ctx)
	}
	if a.wantLogs && a.logColl != nil {
		a.scrapeLogs(ctx)
	}
}

func (a *Agent) scrapeMetrics(ctx context.Context) {
	var all []model.Sample
	for _, coll := range a.collectors {
		samples, err := coll.Collect(ctx, a.client)
		if err != nil {
			a.logger.Warn("collector failed",
				"collector", coll.Name(), "error", err)
			continue
		}
		all = append(all, samples...)
	}

	if len(all) == 0 {
		return
	}

	// MultiSink logs per-sink failures itself; surface a joined error here at
	// warn level so a persistently failing synchronous exporter (Prometheus /
	// Loki) is visible in the agent's own log, not just the sink's.
	if err := a.sinks.ConsumeMetrics(ctx, all); err != nil {
		a.logger.Warn("one or more exporters rejected metrics", "error", err)
	}
	a.logger.Debug("metrics collected", "samples", len(all))
}

func (a *Agent) scrapeLogs(ctx context.Context) {
	entries, err := a.logColl.Collect(ctx, a.client)
	if err != nil {
		a.logger.Warn("log collection failed", "error", err)
		return
	}
	if len(entries) == 0 {
		return
	}
	if err := a.sinks.ConsumeLogs(ctx, entries); err != nil {
		a.logger.Warn("one or more exporters rejected logs", "error", err)
	}
	a.logger.Debug("logs collected", "entries", len(entries))
}
