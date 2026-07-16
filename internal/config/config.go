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
	"strconv"
	"strings"
	"time"
)

// Config is the fully-resolved runtime configuration for the agent.
type Config struct {
	// Routers is the set of MikroTik devices to scrape. The agent acts as a hub:
	// it polls every router in parallel and stamps each router's telemetry with
	// a "target" label (the router Name) so a single backend can distinguish
	// them. Always contains at least one entry.
	Routers []RouterConfig

	// ScrapeConcurrency bounds how many routers are scraped in parallel.
	ScrapeConcurrency int

	// Exporters is the ordered, de-duplicated list of exporter names to enable
	// (after alias expansion happens in the export package). Populated from the
	// EXPORTERS env var.
	Exporters []string

	// Collectors optionally restricts which metric collectors run. When empty,
	// all registered collectors are used. Populated from the COLLECTORS env var
	// (comma-separated collector names, e.g. "system,interface,health").
	Collectors []string

	// PollInterval is how often metrics are collected from the routers and
	// flushed to the backend(s).
	PollInterval time.Duration

	// ServiceName / ServiceVersion identify this agent (the hub) in emitted
	// telemetry. Individual routers are identified by their per-sample "target"
	// label, not by ServiceName.
	ServiceName    string
	ServiceVersion string

	// LogLevel controls the agent's own structured logging.
	LogLevel string

	// env is the captured process environment, used by adapters to read their
	// own prefixed variables without touching the global os environment
	// directly (which keeps them testable).
	env map[string]string
}

// RouterConfig describes how to reach one RouterOS device.
type RouterConfig struct {
	// Name is the target identifier, emitted as the "target" label on every
	// sample/log from this router so multiple routers are distinguishable in a
	// shared backend.
	Name string

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

	// MaxReplyRows caps how many rows from a single RouterOS reply the client
	// will process. It bounds the memory a hostile or malfunctioning router can
	// force the agent to allocate per scrape, independent of GOMEMLIMIT. Zero
	// means unlimited.
	MaxReplyRows int
}

// Load reads configuration from the environment, applies defaults, and
// validates required fields. Per-exporter settings are validated by each
// adapter at build time, not here.
func Load() (Config, error) {
	env := currentEnv()

	cfg := Config{
		ScrapeConcurrency: getInt(env, "ROUTER_SCRAPE_CONCURRENCY", 4),
		PollInterval:      getDuration(env, "POLL_INTERVAL", 15*time.Second),
		ServiceName:       getEnv(env, "SERVICE_NAME", "tiktelemetry"),
		ServiceVersion:    getEnv(env, "SERVICE_VERSION", "dev"),
		LogLevel:          getEnv(env, "LOG_LEVEL", "info"),
		env:               env,
	}

	// EXPORTERS is a comma-separated list. Default to "otlp".
	cfg.Exporters = splitList(getEnv(env, "EXPORTERS", "otlp"))

	// COLLECTORS optionally restricts the active metric collectors. Empty means
	// "all registered collectors".
	cfg.Collectors = splitList(env["COLLECTORS"])

	routers, err := parseRouters(env)
	if err != nil {
		return Config{}, err
	}
	cfg.Routers = routers

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// parseRouters builds the list of routers to scrape. It accepts either:
//
//	ROUTERS=core@192.168.88.1:8728,cap-office@192.168.88.2:8728
//
// (a comma-separated list of name@address entries), or, when ROUTERS is unset,
// a single router from ROUTER_ADDRESS. ROUTER_USER / ROUTER_PASS / ROUTER_TLS /
// ROUTER_TLS_INSECURE provide shared defaults; any of them can be overridden
// per router with ROUTER_<NAME>_<FIELD> (NAME upper-cased, non-alphanumerics
// replaced with '_'), e.g. ROUTER_CAP_OFFICE_PASS.
func parseRouters(env map[string]string) ([]RouterConfig, error) {
	defUser := getEnv(env, "ROUTER_USER", "admin")
	defPass := env["ROUTER_PASS"]
	defTLS := getBool(env, "ROUTER_TLS", false)
	defInsecure := getBool(env, "ROUTER_TLS_INSECURE", false)
	dialTimeout := getDuration(env, "ROUTER_DIAL_TIMEOUT", 5*time.Second)
	maxRows := getInt(env, "ROUTER_MAX_REPLY_ROWS", 10000)

	type spec struct{ name, address string }
	var specs []spec

	if raw := strings.TrimSpace(env["ROUTERS"]); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, addr, ok := strings.Cut(part, "@")
			if !ok {
				return nil, fmt.Errorf("invalid ROUTERS entry %q (want name@host:port)", part)
			}
			name = strings.TrimSpace(name)
			addr = strings.TrimSpace(addr)
			if name == "" || addr == "" {
				return nil, fmt.Errorf("invalid ROUTERS entry %q (empty name or address)", part)
			}
			specs = append(specs, spec{name, addr})
		}
	} else {
		// Single-router shorthand.
		addr := getEnv(env, "ROUTER_ADDRESS", "192.168.88.1:8728")
		name := getEnv(env, "INSTANCE_ID", addr)
		specs = append(specs, spec{name, addr})
	}

	seen := map[string]bool{}
	out := make([]RouterConfig, 0, len(specs))
	for _, s := range specs {
		if seen[s.name] {
			return nil, fmt.Errorf("duplicate router name %q in ROUTERS", s.name)
		}
		seen[s.name] = true

		prefix := "ROUTER_" + envKeySegment(s.name) + "_"
		out = append(out, RouterConfig{
			Name:               s.name,
			Address:            s.address,
			Username:           getEnv(env, prefix+"USER", defUser),
			Password:           firstNonEmpty(env[prefix+"PASS"], defPass),
			UseTLS:             getBool(env, prefix+"TLS", defTLS),
			InsecureSkipVerify: getBool(env, prefix+"TLS_INSECURE", defInsecure),
			DialTimeout:        dialTimeout,
			MaxReplyRows:       maxRows,
		})
	}
	return out, nil
}

// envKeySegment converts a router name into the upper-cased, underscore-safe
// segment used in per-router override env vars (e.g. "cap-office" -> "CAP_OFFICE").
func envKeySegment(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (c Config) validate() error {
	if len(c.Exporters) == 0 {
		return fmt.Errorf("EXPORTERS must name at least one exporter")
	}
	if len(c.Routers) == 0 {
		return fmt.Errorf("at least one router must be configured (ROUTERS or ROUTER_ADDRESS)")
	}
	for _, r := range c.Routers {
		if r.Password == "" {
			return fmt.Errorf("router %q has no password (set ROUTER_PASS or ROUTER_%s_PASS)",
				r.Name, envKeySegment(r.Name))
		}
	}
	if c.ScrapeConcurrency < 1 {
		return fmt.Errorf("ROUTER_SCRAPE_CONCURRENCY must be at least 1, got %d", c.ScrapeConcurrency)
	}
	// Enforce a floor so a tiny interval cannot hammer the routers (each scrape
	// issues a dozen API commands per router) or spin the export pipeline.
	if c.PollInterval < time.Second {
		return fmt.Errorf("POLL_INTERVAL must be at least 1s, got %s", c.PollInterval)
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

func getInt(env map[string]string, key string, fallback int) int {
	v, ok := env[key]
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return fallback
	}
	return n
}
