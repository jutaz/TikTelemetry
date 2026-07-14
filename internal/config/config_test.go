package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Router.Address != "192.168.88.1:8728" {
		t.Errorf("default router address = %q", cfg.Router.Address)
	}
	if cfg.PollInterval != 15*time.Second {
		t.Errorf("default poll interval = %s", cfg.PollInterval)
	}
	if len(cfg.Exporters) != 1 || cfg.Exporters[0] != "otlp" {
		t.Errorf("default exporters = %v, want [otlp]", cfg.Exporters)
	}
	if cfg.InstanceID != cfg.Router.Address {
		t.Errorf("InstanceID should default to router address, got %q", cfg.InstanceID)
	}
}

func TestLoadRequiresRouterPass(t *testing.T) {
	// ROUTER_PASS intentionally unset.
	if _, err := Load(); err == nil {
		t.Fatal("expected error when ROUTER_PASS missing")
	}
}

func TestExportersList(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("EXPORTERS", "prometheus, loki , otlp")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	want := []string{"prometheus", "loki", "otlp"}
	if len(cfg.Exporters) != len(want) {
		t.Fatalf("exporters = %v, want %v", cfg.Exporters, want)
	}
	for i := range want {
		if cfg.Exporters[i] != want[i] {
			t.Errorf("exporters[%d] = %q, want %q", i, cfg.Exporters[i], want[i])
		}
	}
}

func TestEnvHeaders(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("OTLP_HEADERS", "X-Scope-OrgID=tenant1, X-Extra=v2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	h, err := cfg.EnvHeaders("OTLP_HEADERS")
	if err != nil {
		t.Fatalf("EnvHeaders error: %v", err)
	}
	if h["X-Scope-OrgID"] != "tenant1" {
		t.Errorf("X-Scope-OrgID = %q", h["X-Scope-OrgID"])
	}
	if h["X-Extra"] != "v2" {
		t.Errorf("X-Extra = %q", h["X-Extra"])
	}
}

func TestEnvHelpers(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("SOME_BOOL", "true")
	t.Setenv("SOME_DUR", "42s")
	t.Setenv("SOME_STR", "hello")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.EnvBool("SOME_BOOL", false) {
		t.Error("EnvBool(SOME_BOOL) = false, want true")
	}
	if cfg.EnvDuration("SOME_DUR", time.Second) != 42*time.Second {
		t.Error("EnvDuration(SOME_DUR) wrong")
	}
	if cfg.EnvOr("SOME_STR", "fallback") != "hello" {
		t.Error("EnvOr(SOME_STR) wrong")
	}
	if cfg.EnvOr("MISSING", "fallback") != "fallback" {
		t.Error("EnvOr(MISSING) should return fallback")
	}
}
