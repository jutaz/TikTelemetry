package export

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/model"
)

// buildFakeSink is a Sink used to exercise Build and the registry.
type buildFakeSink struct {
	name         string
	caps         Capabilities
	shutdownErr  error
	shutdownSeen *bool
}

func (s buildFakeSink) Name() string                                         { return s.name }
func (s buildFakeSink) Capabilities() Capabilities                           { return s.caps }
func (s buildFakeSink) ConsumeMetrics(context.Context, []model.Sample) error { return nil }
func (s buildFakeSink) ConsumeLogs(context.Context, []model.LogEntry) error  { return nil }
func (s buildFakeSink) Shutdown(context.Context) error {
	if s.shutdownSeen != nil {
		*s.shutdownSeen = true
	}
	return s.shutdownErr
}

// withCleanRegistry swaps in fresh registries for a test and restores them.
func withCleanRegistry(t *testing.T) {
	t.Helper()
	origReg, origAlias := registry, aliases
	t.Cleanup(func() { registry, aliases = origReg, origAlias })
	registry = map[string]Factory{}
	aliases = map[string][]string{}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func baseConfig(t *testing.T, exporters ...string) config.Config {
	t.Helper()
	t.Setenv("ROUTER_PASS", "secret")
	if len(exporters) > 0 {
		list := exporters[0]
		for _, e := range exporters[1:] {
			list += "," + e
		}
		t.Setenv("EXPORTERS", list)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func TestRegisterAndBuild(t *testing.T) {
	withCleanRegistry(t)

	Register("metrics-only", func(context.Context, config.Config, *slog.Logger) (Sink, error) {
		return buildFakeSink{name: "metrics-only", caps: Capabilities{Metrics: true}}, nil
	})
	Register("logs-only", func(context.Context, config.Config, *slog.Logger) (Sink, error) {
		return buildFakeSink{name: "logs-only", caps: Capabilities{Logs: true}}, nil
	})
	RegisterAlias("both", []string{"metrics-only", "logs-only"})

	// Available lists the concrete names, sorted.
	got := Available()
	if len(got) != 2 || got[0] != "logs-only" || got[1] != "metrics-only" {
		t.Errorf("Available() = %v", got)
	}

	cfg := baseConfig(t, "both")
	ms, err := Build(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !ms.WantsMetrics() || !ms.WantsLogs() {
		t.Error("expected both metrics and logs capability from the 'both' alias")
	}
	if len(ms.sinks) != 2 {
		t.Errorf("built %d sinks, want 2", len(ms.sinks))
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	withCleanRegistry(t)
	Register("dup", func(context.Context, config.Config, *slog.Logger) (Sink, error) { return nil, nil })

	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate Register")
		}
	}()
	Register("dup", func(context.Context, config.Config, *slog.Logger) (Sink, error) { return nil, nil })
}

func TestRegisterAliasDuplicatePanics(t *testing.T) {
	withCleanRegistry(t)
	RegisterAlias("a", []string{"x"})

	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate RegisterAlias")
		}
	}()
	RegisterAlias("a", []string{"y"})
}

func TestBuildUnknownExporter(t *testing.T) {
	withCleanRegistry(t)
	Register("known", func(context.Context, config.Config, *slog.Logger) (Sink, error) {
		return buildFakeSink{name: "known", caps: Capabilities{Metrics: true}}, nil
	})

	cfg := baseConfig(t, "nope")
	if _, err := Build(context.Background(), cfg, discardLogger()); err == nil {
		t.Fatal("expected error building an unknown exporter")
	}
}

func TestBuildRollsBackOnFailure(t *testing.T) {
	withCleanRegistry(t)

	firstShutdown := false
	Register("first", func(context.Context, config.Config, *slog.Logger) (Sink, error) {
		return buildFakeSink{name: "first", caps: Capabilities{Metrics: true}, shutdownSeen: &firstShutdown}, nil
	})
	Register("second", func(context.Context, config.Config, *slog.Logger) (Sink, error) {
		return nil, errors.New("boom")
	})

	cfg := baseConfig(t, "first", "second")
	if _, err := Build(context.Background(), cfg, discardLogger()); err == nil {
		t.Fatal("expected Build to fail when the second exporter errors")
	}
	if !firstShutdown {
		t.Error("expected the first (already-built) sink to be shut down on rollback")
	}
}

func TestBuildEmptyExportersFails(t *testing.T) {
	withCleanRegistry(t)
	// A config whose Exporters resolve to nothing. We bypass config.Load's
	// default by constructing the empty list directly is not possible (private
	// field), so register nothing and request only an alias expanding to empty.
	RegisterAlias("empty", []string{})

	cfg := baseConfig(t, "empty")
	if _, err := Build(context.Background(), cfg, discardLogger()); err == nil {
		t.Fatal("expected error when no concrete exporters are resolved")
	}
}
