// Package export defines the composable telemetry sink abstraction. A Sink is
// one destination for metrics and/or logs (OTLP, Prometheus remote_write, Loki,
// ...). Multiple sinks can be enabled simultaneously; the agent collects data
// once per scrape and fans it out to every configured sink.
package export

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/model"
)

// Sink is a single telemetry destination. Implementations that only handle one
// signal (e.g. Prometheus for metrics, Loki for logs) should implement the
// other Consume* method as a no-op returning nil.
type Sink interface {
	// Name is a short, stable identifier for the sink (e.g. "otlp",
	// "prometheus", "loki"), used in logs and errors.
	Name() string

	// Capabilities reports which signals this sink accepts. The agent uses this
	// to decide whether to run the corresponding collectors at all.
	Capabilities() Capabilities

	// ConsumeMetrics ships a batch of samples. No-op (return nil) if the sink
	// does not handle metrics.
	ConsumeMetrics(ctx context.Context, samples []model.Sample) error

	// ConsumeLogs ships a batch of log entries. No-op (return nil) if the sink
	// does not handle logs.
	ConsumeLogs(ctx context.Context, entries []model.LogEntry) error

	// Shutdown flushes and releases resources.
	Shutdown(ctx context.Context) error
}

// Capabilities describes which signals a sink accepts.
type Capabilities struct {
	Metrics bool
	Logs    bool
}

// Factory builds a Sink from the resolved config. Factories are registered by
// name and invoked only for sinks the user enables via the EXPORTERS setting.
type Factory func(ctx context.Context, cfg config.Config, logger *slog.Logger) (Sink, error)

// registry holds all known sink factories keyed by name. Adapter packages
// register themselves via Register in their init().
var registry = map[string]Factory{}

// aliases maps convenience group names to a list of concrete sink names, e.g.
// "grafanacloud" -> ["prometheus", "loki"]. Registered by adapter packages or
// here for cross-cutting bundles.
var aliases = map[string][]string{}

// Register adds a sink factory under the given name. It panics on duplicate
// registration, which can only happen due to a programming error at init time.
func Register(name string, f Factory) {
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("export: sink %q already registered", name))
	}
	registry[name] = f
}

// RegisterAlias defines a convenience group that expands to several concrete
// sink names.
func RegisterAlias(name string, targets []string) {
	if _, exists := aliases[name]; exists {
		panic(fmt.Sprintf("export: alias %q already registered", name))
	}
	aliases[name] = targets
}

// Available returns the sorted list of registered concrete sink names, for
// diagnostics and error messages.
func Available() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// resolveNames expands aliases and de-duplicates the requested sink names,
// preserving first-seen order.
func resolveNames(requested []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}

	var add func(name string) error
	add = func(name string) error {
		if targets, ok := aliases[name]; ok {
			for _, t := range targets {
				if err := add(t); err != nil {
					return err
				}
			}
			return nil
		}
		if _, ok := registry[name]; !ok {
			return fmt.Errorf("unknown exporter %q (available: %v, aliases: %v)",
				name, Available(), aliasNames())
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
		return nil
	}

	for _, n := range requested {
		if err := add(n); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func aliasNames() []string {
	names := make([]string, 0, len(aliases))
	for n := range aliases {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Build constructs every sink named in cfg.Exporters (expanding aliases) and
// returns them wrapped in a MultiSink. If any sink fails to build, sinks
// already built are shut down and the error is returned.
func Build(ctx context.Context, cfg config.Config, logger *slog.Logger) (*MultiSink, error) {
	names, err := resolveNames(cfg.Exporters)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, errors.New("no exporters configured (set EXPORTERS)")
	}

	var sinks []Sink
	for _, name := range names {
		f := registry[name]
		s, err := f(ctx, cfg, logger.With("exporter", name))
		if err != nil {
			// Roll back any sinks already built.
			shutdownAll(ctx, sinks)
			return nil, fmt.Errorf("build exporter %q: %w", name, err)
		}
		sinks = append(sinks, s)
		logger.Info("exporter enabled",
			"exporter", name,
			"metrics", s.Capabilities().Metrics,
			"logs", s.Capabilities().Logs,
		)
	}

	return NewMultiSink(logger, sinks...), nil
}

func shutdownAll(ctx context.Context, sinks []Sink) {
	for _, s := range sinks {
		_ = s.Shutdown(ctx)
	}
}
