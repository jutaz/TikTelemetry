package config

import (
	"strings"
	"testing"
	"time"
)

func TestMaxReplyRows(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("ROUTER_PASS", "secret")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Routers[0].MaxReplyRows != 10000 {
			t.Errorf("default MaxReplyRows = %d, want 10000", cfg.Routers[0].MaxReplyRows)
		}
	})

	t.Run("custom", func(t *testing.T) {
		t.Setenv("ROUTER_PASS", "secret")
		t.Setenv("ROUTER_MAX_REPLY_ROWS", "500")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Routers[0].MaxReplyRows != 500 {
			t.Errorf("MaxReplyRows = %d, want 500", cfg.Routers[0].MaxReplyRows)
		}
	})

	t.Run("zero disables cap", func(t *testing.T) {
		t.Setenv("ROUTER_PASS", "secret")
		t.Setenv("ROUTER_MAX_REPLY_ROWS", "0")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Routers[0].MaxReplyRows != 0 {
			t.Errorf("MaxReplyRows = %d, want 0 (unlimited)", cfg.Routers[0].MaxReplyRows)
		}
	})

	t.Run("invalid and negative fall back to default", func(t *testing.T) {
		for _, v := range []string{"notanumber", "-5"} {
			t.Setenv("ROUTER_PASS", "secret")
			t.Setenv("ROUTER_MAX_REPLY_ROWS", v)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Routers[0].MaxReplyRows != 10000 {
				t.Errorf("MaxReplyRows for %q = %d, want default 10000", v, cfg.Routers[0].MaxReplyRows)
			}
		}
	})
}

func TestPollInterval_Invalid(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("POLL_INTERVAL", "notaduration")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.PollInterval != 15*time.Second {
		t.Errorf("PollInterval = %s, want 15s", cfg.PollInterval)
	}
}

func TestPollInterval_Valid(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("POLL_INTERVAL", "30s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %s, want 30s", cfg.PollInterval)
	}
}

func TestPollInterval_Negative(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("POLL_INTERVAL", "-5s")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for negative POLL_INTERVAL")
	}
}

func TestPollInterval_BelowMinimum(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	for _, v := range []string{"1ns", "100ms", "999ms"} {
		t.Setenv("POLL_INTERVAL", v)
		if _, err := Load(); err == nil {
			t.Errorf("expected error for sub-1s POLL_INTERVAL %q", v)
		}
	}

	// Exactly 1s is allowed.
	t.Setenv("POLL_INTERVAL", "1s")
	if _, err := Load(); err != nil {
		t.Errorf("1s POLL_INTERVAL should be valid, got %v", err)
	}
}

func TestRouterTLS_BoolParsing(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")

	t.Run("ROUTER_TLS=true", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Routers[0].UseTLS {
			t.Error("UseTLS = false, want true")
		}
	})

	t.Run("ROUTER_TLS=1", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "1")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Routers[0].UseTLS {
			t.Error("UseTLS = false, want true")
		}
	})

	t.Run("ROUTER_TLS=yes", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "yes")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Routers[0].UseTLS {
			t.Error("UseTLS = false, want true")
		}
	})

	t.Run("ROUTER_TLS=on", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "on")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Routers[0].UseTLS {
			t.Error("UseTLS = false, want true")
		}
	})

	t.Run("ROUTER_TLS=false", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "false")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Routers[0].UseTLS {
			t.Error("UseTLS = true, want false")
		}
	})

	t.Run("ROUTER_TLS=0", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "0")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Routers[0].UseTLS {
			t.Error("UseTLS = true, want false")
		}
	})

	t.Run("ROUTER_TLS=no", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "no")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Routers[0].UseTLS {
			t.Error("UseTLS = true, want false")
		}
	})

	t.Run("ROUTER_TLS=off", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "off")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Routers[0].UseTLS {
			t.Error("UseTLS = true, want false")
		}
	})

	t.Run("ROUTER_TLS=garbage falls back to false", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "garbage")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Routers[0].UseTLS {
			t.Error("UseTLS = true, want false (fallback)")
		}
	})
}

func TestRouterTLSInsecure_BoolParsing(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")

	t.Run("true value", func(t *testing.T) {
		t.Setenv("ROUTER_TLS_INSECURE", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Routers[0].InsecureSkipVerify {
			t.Error("InsecureSkipVerify = false, want true")
		}
	})

	t.Run("false value", func(t *testing.T) {
		t.Setenv("ROUTER_TLS_INSECURE", "false")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Routers[0].InsecureSkipVerify {
			t.Error("InsecureSkipVerify = true, want false")
		}
	})

	t.Run("garbage falls back to false", func(t *testing.T) {
		t.Setenv("ROUTER_TLS_INSECURE", "maybe")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Routers[0].InsecureSkipVerify {
			t.Error("InsecureSkipVerify = true, want false (fallback)")
		}
	})
}

func TestInstanceID_Explicit(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("INSTANCE_ID", "my-router-1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Routers) != 1 {
		t.Fatalf("expected 1 router, got %d", len(cfg.Routers))
	}
	if cfg.Routers[0].Name != "my-router-1" {
		t.Errorf("router Name = %q, want %q", cfg.Routers[0].Name, "my-router-1")
	}
}

func TestServiceName_ServiceVersion(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("SERVICE_NAME", "my-agent")
	t.Setenv("SERVICE_VERSION", "1.2.3")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.ServiceName != "my-agent" {
		t.Errorf("ServiceName = %q, want %q", cfg.ServiceName, "my-agent")
	}
	if cfg.ServiceVersion != "1.2.3" {
		t.Errorf("ServiceVersion = %q, want %q", cfg.ServiceVersion, "1.2.3")
	}
}

func TestEnvHeaders_Malformed(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("MY_HEADERS", "noequalshere")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	_, err = cfg.EnvHeaders("MY_HEADERS")
	if err == nil {
		t.Fatal("expected error for malformed header entry")
	}
}

func TestEnvHeaders_Empty(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	// OTLP_HEADERS not set or empty.
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	h, err := cfg.EnvHeaders("OTLP_HEADERS")
	if err != nil {
		t.Fatalf("EnvHeaders error: %v", err)
	}
	if len(h) != 0 {
		t.Errorf("expected empty headers map, got %v", h)
	}
}

func TestExporters_EmptyDefaults(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("EXPORTERS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Exporters) != 1 || cfg.Exporters[0] != "otlp" {
		t.Errorf("Exporters = %v, want [otlp]", cfg.Exporters)
	}
}

func TestEnvBool_TrueValues(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("BOOL_TRUE", "true")
	t.Setenv("BOOL_ONE", "1")
	t.Setenv("BOOL_YES", "yes")
	t.Setenv("BOOL_ON", "on")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnvBool("BOOL_TRUE", false) {
		t.Error("BOOL_TRUE should be true")
	}
	if !cfg.EnvBool("BOOL_ONE", false) {
		t.Error("BOOL_ONE should be true")
	}
	if !cfg.EnvBool("BOOL_YES", false) {
		t.Error("BOOL_YES should be true")
	}
	if !cfg.EnvBool("BOOL_ON", false) {
		t.Error("BOOL_ON should be true")
	}
}

func TestEnvBool_FalseValues(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("BOOL_FALSE", "false")
	t.Setenv("BOOL_ZERO", "0")
	t.Setenv("BOOL_NO", "no")
	t.Setenv("BOOL_OFF", "off")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnvBool("BOOL_FALSE", true) {
		t.Error("BOOL_FALSE should be false")
	}
	if cfg.EnvBool("BOOL_ZERO", true) {
		t.Error("BOOL_ZERO should be false")
	}
	if cfg.EnvBool("BOOL_NO", true) {
		t.Error("BOOL_NO should be false")
	}
	if cfg.EnvBool("BOOL_OFF", true) {
		t.Error("BOOL_OFF should be false")
	}
}

func TestEnvBool_GarbageFallsBack(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("BOOL_GARBAGE", "notabool")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnvBool("BOOL_GARBAGE", true) != true {
		t.Error("garbage should fall back to true")
	}
	if cfg.EnvBool("BOOL_GARBAGE", false) != false {
		t.Error("garbage should fall back to false")
	}
}

// ---------------------------------------------------------------------------
// NEW: Multi-router tests
// ---------------------------------------------------------------------------

func TestMultiRouter_BasicParsing(t *testing.T) {
	t.Setenv("ROUTERS", "core@10.0.0.1:8728,edge@10.0.0.2:8728")
	t.Setenv("ROUTER_PASS", "shared")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Routers) != 2 {
		t.Fatalf("expected 2 routers, got %d", len(cfg.Routers))
	}

	// First router: core
	r0 := cfg.Routers[0]
	if r0.Name != "core" {
		t.Errorf("Routers[0].Name = %q, want %q", r0.Name, "core")
	}
	if r0.Address != "10.0.0.1:8728" {
		t.Errorf("Routers[0].Address = %q, want %q", r0.Address, "10.0.0.1:8728")
	}
	if r0.Username != "admin" {
		t.Errorf("Routers[0].Username = %q, want %q", r0.Username, "admin")
	}
	if r0.Password != "shared" {
		t.Errorf("Routers[0].Password = %q, want %q", r0.Password, "shared")
	}

	// Second router: edge
	r1 := cfg.Routers[1]
	if r1.Name != "edge" {
		t.Errorf("Routers[1].Name = %q, want %q", r1.Name, "edge")
	}
	if r1.Address != "10.0.0.2:8728" {
		t.Errorf("Routers[1].Address = %q, want %q", r1.Address, "10.0.0.2:8728")
	}
	if r1.Username != "admin" {
		t.Errorf("Routers[1].Username = %q, want %q", r1.Username, "admin")
	}
	if r1.Password != "shared" {
		t.Errorf("Routers[1].Password = %q, want %q", r1.Password, "shared")
	}
}

func TestMultiRouter_PerRouterPasswordOverride(t *testing.T) {
	t.Setenv("ROUTERS", "core@10.0.0.1:8728,cap-office@10.0.0.2:8728")
	t.Setenv("ROUTER_PASS", "shared")
	t.Setenv("ROUTER_CAP_OFFICE_PASS", "special")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Routers) != 2 {
		t.Fatalf("expected 2 routers, got %d", len(cfg.Routers))
	}

	// cap-office gets the per-router override.
	if cfg.Routers[1].Name != "cap-office" {
		t.Fatalf("Routers[1].Name = %q, want %q", cfg.Routers[1].Name, "cap-office")
	}
	if cfg.Routers[1].Password != "special" {
		t.Errorf("Routers[1].Password = %q, want %q", cfg.Routers[1].Password, "special")
	}
	// core gets the shared default.
	if cfg.Routers[0].Password != "shared" {
		t.Errorf("Routers[0].Password = %q, want %q", cfg.Routers[0].Password, "shared")
	}
}

func TestMultiRouter_PerRouterTLSOverride(t *testing.T) {
	t.Setenv("ROUTERS", "secure@10.0.0.1:8728,plain@10.0.0.2:8728")
	t.Setenv("ROUTER_PASS", "secret")
	t.Setenv("ROUTER_TLS", "false")
	t.Setenv("ROUTER_SECURE_TLS", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Routers) != 2 {
		t.Fatalf("expected 2 routers, got %d", len(cfg.Routers))
	}

	if !cfg.Routers[0].UseTLS {
		t.Errorf("Routers[0] (secure) UseTLS = false, want true")
	}
	if cfg.Routers[1].UseTLS {
		t.Errorf("Routers[1] (plain) UseTLS = true, want false")
	}
}

func TestMultiRouter_InvalidEntries(t *testing.T) {
	tests := []struct {
		name    string
		routers string
	}{
		{"no @ separator", "noatsign"},
		{"empty name", "@1.2.3.4:8728"},
		{"empty address", "name@"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ROUTERS", tt.routers)
			t.Setenv("ROUTER_PASS", "x")
			if _, err := Load(); err == nil {
				t.Errorf("expected error for ROUTERS=%q", tt.routers)
			}
		})
	}
}

func TestMultiRouter_DuplicateNames(t *testing.T) {
	t.Setenv("ROUTERS", "a@1.1.1.1:8728,a@2.2.2.2:8728")
	t.Setenv("ROUTER_PASS", "x")

	if _, err := Load(); err == nil {
		t.Fatal("expected error for duplicate router name")
	}
}

func TestMultiRouter_MissingPassword(t *testing.T) {
	// Two routers, shared ROUTER_PASS unset, only one with per-router PASS.
	t.Setenv("ROUTERS", "haspass@10.0.0.1:8728,nopass@10.0.0.2:8728")
	t.Setenv("ROUTER_HASPASS_PASS", "ok")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for router without password")
	}
	// The error message should mention the router name.
	if err != nil && !contains(err.Error(), "nopass") {
		t.Errorf("error does not mention router without password: %v", err)
	}
}

func TestScrapeConcurrency(t *testing.T) {
	t.Run("default is 4", func(t *testing.T) {
		t.Setenv("ROUTER_PASS", "secret")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ScrapeConcurrency != 4 {
			t.Errorf("ScrapeConcurrency = %d, want 4", cfg.ScrapeConcurrency)
		}
	})

	t.Run("custom valid", func(t *testing.T) {
		t.Setenv("ROUTER_PASS", "secret")
		t.Setenv("ROUTER_SCRAPE_CONCURRENCY", "8")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ScrapeConcurrency != 8 {
			t.Errorf("ScrapeConcurrency = %d, want 8", cfg.ScrapeConcurrency)
		}
	})

	t.Run("zero causes error", func(t *testing.T) {
		t.Setenv("ROUTER_PASS", "secret")
		t.Setenv("ROUTER_SCRAPE_CONCURRENCY", "0")
		if _, err := Load(); err == nil {
			t.Fatal("expected error for ScrapeConcurrency=0")
		}
	})

	t.Run("negative falls back to default 4", func(t *testing.T) {
		t.Setenv("ROUTER_PASS", "secret")
		t.Setenv("ROUTER_SCRAPE_CONCURRENCY", "-1")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ScrapeConcurrency != 4 {
			t.Errorf("ScrapeConcurrency = %d, want 4", cfg.ScrapeConcurrency)
		}
	})

	t.Run("garbage falls back to default 4", func(t *testing.T) {
		t.Setenv("ROUTER_PASS", "secret")
		t.Setenv("ROUTER_SCRAPE_CONCURRENCY", "abc")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ScrapeConcurrency != 4 {
			t.Errorf("ScrapeConcurrency = %d, want 4", cfg.ScrapeConcurrency)
		}
	})
}

func TestSingleRouterShim(t *testing.T) {
	t.Setenv("ROUTER_ADDRESS", "1.2.3.4:8728")
	t.Setenv("ROUTER_PASS", "x")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Routers) != 1 {
		t.Fatalf("expected 1 router, got %d", len(cfg.Routers))
	}
	if cfg.Routers[0].Address != "1.2.3.4:8728" {
		t.Errorf("Address = %q, want %q", cfg.Routers[0].Address, "1.2.3.4:8728")
	}
	// When INSTANCE_ID is unset, Name defaults to the address.
	if cfg.Routers[0].Name != "1.2.3.4:8728" {
		t.Errorf("Name = %q, want address %q", cfg.Routers[0].Name, "1.2.3.4:8728")
	}
}

// contains is a small helper for substring checks.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
