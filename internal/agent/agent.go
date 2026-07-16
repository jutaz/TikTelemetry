// Package agent wires the MikroTik collection layer to the composable export
// layer and drives the periodic scrape/push loop. It acts as a hub: it scrapes
// every configured router in parallel, stamps each router's telemetry with a
// "target" label, and fans the merged result out to every configured sink.
package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/export"
	"github.com/jutaz/tiktelemetry/internal/mikrotik"
	"github.com/jutaz/tiktelemetry/internal/model"
)

// target is one router the hub scrapes, with its own client, log cursor, and
// self-metrics.
type target struct {
	name       string
	client     *mikrotik.Client
	collectors []mikrotik.Collector
	logColl    *mikrotik.LogCollector
	self       *selfMetrics
}

// Agent scrapes a set of routers and pushes their telemetry to the sinks.
type Agent struct {
	cfg    config.Config
	logger *slog.Logger

	targets []*target
	sinks   *export.MultiSink

	// wantMetrics / wantLogs cache whether any sink consumes each signal, so we
	// skip the corresponding router queries entirely when nothing wants them.
	wantMetrics bool
	wantLogs    bool
}

// New constructs an Agent from resolved configuration and the built sinks. Each
// router's client is created lazily and connects on first scrape.
func New(cfg config.Config, logger *slog.Logger, sinks *export.MultiSink) *Agent {
	collectors, unknown := mikrotik.SelectCollectors(cfg.Collectors)
	if len(unknown) > 0 {
		logger.Warn("ignoring unknown collectors in COLLECTORS", "unknown", unknown)
	}

	a := &Agent{
		cfg:         cfg,
		logger:      logger,
		sinks:       sinks,
		wantMetrics: sinks.WantsMetrics(),
		wantLogs:    sinks.WantsLogs(),
	}

	for _, rc := range cfg.Routers {
		t := &target{
			name:       rc.Name,
			client:     mikrotik.New(rc, logger.With("target", rc.Name)),
			collectors: collectors,
			self:       newSelfMetrics(),
		}
		if a.wantLogs {
			t.logColl = mikrotik.NewLogCollector()
		}
		a.targets = append(a.targets, t)
	}
	return a
}

// Run blocks running the scrape loop until ctx is cancelled. It performs an
// immediate first scrape, then ticks at cfg.PollInterval.
func (a *Agent) Run(ctx context.Context) error {
	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()

	a.logger.Info("agent started",
		"routers", len(a.targets),
		"interval", a.cfg.PollInterval,
		"concurrency", a.cfg.ScrapeConcurrency,
		"metrics", a.wantMetrics,
		"logs", a.wantLogs,
	)

	// Run one scrape immediately so we don't wait a full interval for data.
	a.scrapeAll(ctx)

	for {
		select {
		case <-ctx.Done():
			a.logger.Info("agent stopping")
			a.closeAll()
			return nil
		case <-ticker.C:
			a.scrapeAll(ctx)
		}
	}
}

// ScrapeOnce runs a single scrape/push cycle across all routers and returns. It
// is used for one-shot execution and by tests; Run calls the same underlying
// path on every tick.
func (a *Agent) ScrapeOnce(ctx context.Context) {
	a.scrapeAll(ctx)
}

// closeAll closes every router client.
func (a *Agent) closeAll() {
	for _, t := range a.targets {
		_ = t.client.Close()
	}
}

// scrapeAll scrapes every router in parallel (bounded by ScrapeConcurrency),
// collects the merged samples/logs, and pushes them once. One slow or dead
// router cannot block the others: each target's collection is independently
// bounded by a per-scrape timeout.
func (a *Agent) scrapeAll(ctx context.Context) {
	budget := a.cfg.PollInterval
	if budget < 5*time.Second {
		budget = 5 * time.Second
	}

	type result struct {
		samples []model.Sample
		entries []model.LogEntry
	}

	results := make([]result, len(a.targets))
	sem := make(chan struct{}, a.cfg.ScrapeConcurrency)
	var wg sync.WaitGroup

	for i, t := range a.targets {
		wg.Add(1)
		go func(i int, t *target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Each target gets its own bounded context so a hung router only
			// delays itself.
			tctx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()

			var r result
			if a.wantMetrics {
				r.samples = a.scrapeMetrics(tctx, t)
			}
			if a.wantLogs && t.logColl != nil {
				r.entries = a.scrapeLogs(tctx, t)
			}
			results[i] = r
		}(i, t)
	}
	wg.Wait()

	// Merge and push once so a single batch fans out to all sinks.
	var allSamples []model.Sample
	var allEntries []model.LogEntry
	for _, r := range results {
		allSamples = append(allSamples, r.samples...)
		allEntries = append(allEntries, r.entries...)
	}

	if len(allSamples) > 0 {
		if err := a.sinks.ConsumeMetrics(ctx, allSamples); err != nil {
			for _, t := range a.targets {
				t.self.recordMetricExportError()
			}
			a.logger.Warn("one or more exporters rejected metrics", "error", err)
		}
	}
	if len(allEntries) > 0 {
		if err := a.sinks.ConsumeLogs(ctx, allEntries); err != nil {
			for _, t := range a.targets {
				t.self.recordLogExportError()
			}
			a.logger.Warn("one or more exporters rejected logs", "error", err)
		}
	}
}

// scrapeMetrics collects one router's metrics, stamps them with its target
// label, and appends the router's own self-metrics. It returns the samples
// rather than pushing, so scrapeAll can merge across routers and push once.
func (a *Agent) scrapeMetrics(ctx context.Context, t *target) []model.Sample {
	start := time.Now()

	var all []model.Sample
	routerReached := false
	for _, coll := range t.collectors {
		samples, err := coll.Collect(ctx, t.client)
		if err != nil {
			// A command the board simply does not have (e.g. `health` on a
			// device with no sensors, wireless on a wired router) is not a
			// failure: the router answered, it just lacks that feature. Skip it
			// quietly so it does not spam a warning and an error metric every
			// scrape. The router WAS reached.
			if mikrotik.IsFeatureAbsent(err) {
				routerReached = true
				a.logger.Debug("collector skipped (feature absent)",
					"target", t.name, "collector", coll.Name())
				continue
			}
			a.logger.Warn("collector failed",
				"target", t.name, "collector", coll.Name(), "error", err)
			t.self.recordCollectorError(coll.Name())
			continue
		}
		routerReached = true
		all = append(all, samples...)
	}

	// Self-metrics are emitted even when the router is unreachable so
	// tiktelemetry.router.up reports 0 rather than the series disappearing.
	t.self.recordScrape(time.Since(start).Milliseconds(), int64(len(all)), routerReached)
	all = append(all, t.self.samples()...)

	stampTarget(all, t.name)
	a.logger.Debug("metrics collected", "target", t.name, "samples", len(all))
	return all
}

// scrapeLogs collects one router's new log entries and stamps each with its
// target name.
func (a *Agent) scrapeLogs(ctx context.Context, t *target) []model.LogEntry {
	entries, err := t.logColl.Collect(ctx, t.client)
	if err != nil {
		t.self.recordLogCollectError()
		a.logger.Warn("log collection failed", "target", t.name, "error", err)
		return nil
	}
	for i := range entries {
		entries[i].Target = t.name
	}
	if len(entries) > 0 {
		a.logger.Debug("logs collected", "target", t.name, "entries", len(entries))
	}
	return entries
}

// stampTarget adds the target label to every sample, so a shared backend can
// distinguish routers. It does not overwrite a target already set by a
// collector (none do today).
func stampTarget(samples []model.Sample, target string) {
	for i := range samples {
		if samples[i].Attributes == nil {
			samples[i].Attributes = map[string]string{"target": target}
			continue
		}
		if _, ok := samples[i].Attributes["target"]; !ok {
			samples[i].Attributes["target"] = target
		}
	}
}
