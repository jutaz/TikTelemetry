// Package config loads and validates the agent's runtime configuration from
// environment variables. Every knob is exposed as an env var so the agent can
// be driven purely by container configuration with no config files.
//
// Exporters are composable: the EXPORTERS variable selects one or more sinks
// (e.g. "otlp", "prometheus", "loki", or the "grafanacloud" bundle) and each
// adapter reads its own prefixed variables via Config.Env.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Config is the fully-resolved runtime configuration for the agent.
type Config struct {
	// Router holds MikroTik RouterOS API connection settings.
	Router RouterConfig

	// Exporters is the ordered, de-duplicated list of exporter names to enable
	// (after alias expansion happens in the export package). Populated from the
	// EXPORTERS env var.
	Exporters []string

	// PollInterval is how often metrics are collected from the router and
	// flushed to the backend(s).
	PollInterval time.Duration

	// ServiceName / ServiceVersion identify this agent in emitted telemetry.
	ServiceName    string
	ServiceVersion string

	// InstanceID identifies the specific router/agent pair. Defaults to the
	// router address when unset. Exported as a label/attribute on all telemetry.
	InstanceID string

	// LogLevel controls the agent's own structured logging.
	LogLevel string

	// env is the captured process environment, used by adapters to read their
	// own prefixed variables without touching the global os environment
	// directly (which keeps them testable).
	env map[string]string
}

// RouterConfig describes how to reach the RouterOS API.
type RouterConfig struct {
	// Address is host:port for the RouterOS API (e.g. 192.168.88.1:8728 for
	// plaintext, :8729 for API-SSL).
	Address string

	Username string
	Password string

	// UseTLS switches to the API-SSL transport (DialTLS).
	UseTLS bool

	// InsecureSkipVerify disables TLS certificate verification, useful with the
	// self-signed certificates commonly used on RouterOS.
	InsecureSkipVerify bool

	// DialTimeout bounds the initial connection + login handshake.
	DialTimeout time.Duration
}

// Load reads configuration from the environment, applies defaults, and
// validates required fields. Per-exporter settings are validated by each
// adapter at build time, not here.
func Load() (Config, error) {
	env := currentEnv()

	cfg := Config{
		Router: RouterConfig{
			Address:            getEnv(env, "ROUTER_ADDRESS", "192.168.88.1:8728"),
			Username:           getEnv(env, "ROUTER_USER", "admin"),
			Password:           env["ROUTER_PASS"],
			UseTLS:             getBool(env, "ROUTER_TLS", false),
			InsecureSkipVerify: getBool(env, "ROUTER_TLS_INSECURE", false),
			DialTimeout:        getDuration(env, "ROUTER_DIAL_TIMEOUT", 5*time.Second),
		},
		PollInterval:   getDuration(env, "POLL_INTERVAL", 15*time.Second),
		ServiceName:    getEnv(env, "SERVICE_NAME", "tiktelemetry"),
		ServiceVersion: getEnv(env, "SERVICE_VERSION", "dev"),
		InstanceID:     env["INSTANCE_ID"],
		LogLevel:       getEnv(env, "LOG_LEVEL", "info"),
		env:            env,
	}

	// EXPORTERS is a comma-separated list. Default to "otlp" to preserve the
	// original single-OTLP behaviour when unset.
	cfg.Exporters = splitList(getEnv(env, "EXPORTERS", "otlp"))

	if cfg.InstanceID == "" {
		cfg.InstanceID = cfg.Router.Address
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if len(c.Exporters) == 0 {
		return fmt.Errorf("EXPORTERS must name at least one exporter")
	}
	if c.Router.Password == "" {
		return fmt.Errorf("ROUTER_PASS is required")
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("POLL_INTERVAL must be positive, got %s", c.PollInterval)
	}
	return nil
}

// Env returns the value of an environment variable captured at Load time, or
// the empty string. Adapters use this to read their own prefixed settings.
func (c Config) Env(key string) string {
	return c.env[key]
}

// EnvOr returns the environment value for key or fallback when unset/empty.
func (c Config) EnvOr(key, fallback string) string {
	return getEnv(c.env, key, fallback)
}

// EnvBool parses a boolean environment value, or returns fallback.
func (c Config) EnvBool(key string, fallback bool) bool {
	return getBool(c.env, key, fallback)
}

// EnvDuration parses a duration environment value, or returns fallback.
func (c Config) EnvDuration(key string, fallback time.Duration) time.Duration {
	return getDuration(c.env, key, fallback)
}

// EnvHeaders parses a comma-separated "key=value" list into a header map.
// Returns an error on a malformed entry.
func (c Config) EnvHeaders(key string) (map[string]string, error) {
	raw := c.env[key]
	out := map[string]string{}
	if raw == "" {
		return out, nil
	}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("invalid %s entry %q (want key=value)", key, pair)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

// currentEnv snapshots the process environment into a map.
func currentEnv() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			env[k] = v
		}
	}
	return env
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func getEnv(env map[string]string, key, fallback string) string {
	if v, ok := env[key]; ok && v != "" {
		return v
	}
	return fallback
}

func getBool(env map[string]string, key string, fallback bool) bool {
	v, ok := env[key]
	if !ok || v == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func getDuration(env map[string]string, key string, fallback time.Duration) time.Duration {
	v, ok := env[key]
	if !ok || v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
