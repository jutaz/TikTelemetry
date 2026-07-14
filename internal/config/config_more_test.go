package config

import (
	"testing"
	"time"
)

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

func TestRouterTLS_BoolParsing(t *testing.T) {
	t.Setenv("ROUTER_PASS", "secret")

	t.Run("ROUTER_TLS=true", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Router.UseTLS {
			t.Error("UseTLS = false, want true")
		}
	})

	t.Run("ROUTER_TLS=1", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "1")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Router.UseTLS {
			t.Error("UseTLS = false, want true")
		}
	})

	t.Run("ROUTER_TLS=yes", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "yes")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Router.UseTLS {
			t.Error("UseTLS = false, want true")
		}
	})

	t.Run("ROUTER_TLS=on", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "on")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Router.UseTLS {
			t.Error("UseTLS = false, want true")
		}
	})

	t.Run("ROUTER_TLS=false", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "false")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Router.UseTLS {
			t.Error("UseTLS = true, want false")
		}
	})

	t.Run("ROUTER_TLS=0", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "0")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Router.UseTLS {
			t.Error("UseTLS = true, want false")
		}
	})

	t.Run("ROUTER_TLS=no", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "no")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Router.UseTLS {
			t.Error("UseTLS = true, want false")
		}
	})

	t.Run("ROUTER_TLS=off", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "off")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Router.UseTLS {
			t.Error("UseTLS = true, want false")
		}
	})

	t.Run("ROUTER_TLS=garbage falls back to false", func(t *testing.T) {
		t.Setenv("ROUTER_TLS", "garbage")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Router.UseTLS {
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
		if !cfg.Router.InsecureSkipVerify {
			t.Error("InsecureSkipVerify = false, want true")
		}
	})

	t.Run("false value", func(t *testing.T) {
		t.Setenv("ROUTER_TLS_INSECURE", "false")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Router.InsecureSkipVerify {
			t.Error("InsecureSkipVerify = true, want false")
		}
	})

	t.Run("garbage falls back to false", func(t *testing.T) {
		t.Setenv("ROUTER_TLS_INSECURE", "maybe")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Router.InsecureSkipVerify {
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
	if cfg.InstanceID != "my-router-1" {
		t.Errorf("InstanceID = %q, want %q", cfg.InstanceID, "my-router-1")
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
