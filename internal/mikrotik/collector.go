package mikrotik

import (
	"context"
	"strconv"

	"github.com/go-routeros/routeros/v3"
	"github.com/jutaz/tiktelemetry/internal/model"
)

// Runner executes a RouterOS command and returns its reply. *Client satisfies
// it; tests use an in-memory fake. This seam lets collectors be exercised
// without a live router.
type Runner interface {
	Run(ctx context.Context, cmd ...string) (*routeros.Reply, error)
}

// Collector produces a slice of model.Sample from the RouterOS device.
type Collector interface {
	// Name returns a human-readable label for the collector (used in logging).
	Name() string

	// Collect executes one or more RouterOS commands via the Runner and
	// returns the resulting samples.
	Collect(ctx context.Context, r Runner) ([]model.Sample, error)
}

// ---------------------------------------------------------------------------
// systemResourceCollector
// ---------------------------------------------------------------------------

type systemResourceCollector struct{}

func (c *systemResourceCollector) Name() string { return "system" }

func (c *systemResourceCollector) Collect(ctx context.Context, r Runner) ([]model.Sample, error) {
	reply, err := r.Run(ctx, "/system/resource/print")
	if err != nil {
		return nil, err
	}
	if len(reply.Re) == 0 {
		return nil, nil
	}
	m := reply.Re[0].Map

	var samples []model.Sample

	if v, ok := parseInt(m, "cpu-load"); ok {
		samples = append(samples, model.Sample{
			Name:        "mikrotik.system.cpu.load",
			Description: "Current CPU load percentage",
			Unit:        "%",
			Kind:        model.KindGauge,
			Value:       v,
		})
	}
	if v, ok := parseInt(m, "free-memory"); ok {
		samples = append(samples, model.Sample{
			Name:        "mikrotik.system.memory.free",
			Description: "Free memory in bytes",
			Unit:        "By",
			Kind:        model.KindGauge,
			Value:       v,
		})
	}
	if v, ok := parseInt(m, "total-memory"); ok {
		samples = append(samples, model.Sample{
			Name:        "mikrotik.system.memory.total",
			Description: "Total memory in bytes",
			Unit:        "By",
			Kind:        model.KindGauge,
			Value:       v,
		})
	}
	if v, ok := parseInt(m, "free-hdd-space"); ok {
		samples = append(samples, model.Sample{
			Name:        "mikrotik.system.hdd.free",
			Description: "Free HDD space in bytes",
			Unit:        "By",
			Kind:        model.KindGauge,
			Value:       v,
		})
	}
	if uptimeStr, ok := m["uptime"]; ok {
		if seconds := parseUptime(uptimeStr); seconds > 0 {
			samples = append(samples, model.Sample{
				Name:        "mikrotik.system.uptime",
				Description: "System uptime in seconds",
				Unit:        "s",
				Kind:        model.KindCounter,
				Value:       seconds,
			})
		}
	}

	return samples, nil
}

// parseUptime decodes a RouterOS uptime string (e.g. "1w2d3h4m5s") into total
// seconds. Unrecognised suffixes are silently ignored.
func parseUptime(s string) int64 {
	var total int64
	var n int
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			n = n*10 + int(s[i]-'0')
		} else {
			switch s[i] {
			case 'w':
				total += int64(n) * 7 * 24 * 3600
			case 'd':
				total += int64(n) * 24 * 3600
			case 'h':
				total += int64(n) * 3600
			case 'm':
				total += int64(n) * 60
			case 's':
				total += int64(n)
			}
			n = 0
		}
	}
	return total
}

// ---------------------------------------------------------------------------
// interfaceCollector
// ---------------------------------------------------------------------------

type interfaceCollector struct{}

func (c *interfaceCollector) Name() string { return "interface" }

func (c *interfaceCollector) Collect(ctx context.Context, r Runner) ([]model.Sample, error) {
	reply, err := r.Run(ctx, "/interface/print")
	if err != nil {
		return nil, err
	}

	var samples []model.Sample
	for _, s := range reply.Re {
		name := s.Map["name"]
		if name == "" {
			continue
		}

		// Build the base attribute set for this interface.
		attrs := map[string]string{"interface": name}
		if ifType, ok := s.Map["type"]; ok {
			attrs["type"] = ifType
		}

		if v, ok := parseInt(s.Map, "rx-byte"); ok {
			samples = append(samples, sampleForInterface("mikrotik.interface.rx.bytes",
				"Bytes received on interface", "By", model.KindCounter, v, attrs))
		}
		if v, ok := parseInt(s.Map, "tx-byte"); ok {
			samples = append(samples, sampleForInterface("mikrotik.interface.tx.bytes",
				"Bytes transmitted on interface", "By", model.KindCounter, v, attrs))
		}
		if v, ok := parseInt(s.Map, "rx-packet"); ok {
			samples = append(samples, sampleForInterface("mikrotik.interface.rx.packets",
				"Packets received on interface", "1", model.KindCounter, v, attrs))
		}
		if v, ok := parseInt(s.Map, "tx-packet"); ok {
			samples = append(samples, sampleForInterface("mikrotik.interface.tx.packets",
				"Packets transmitted on interface", "1", model.KindCounter, v, attrs))
		}

		// Error and drop counters — key health indicators. Present on most
		// interface types; absent fields are simply skipped.
		if v, ok := parseInt(s.Map, "rx-error"); ok {
			samples = append(samples, sampleForInterface("mikrotik.interface.rx.errors",
				"Receive errors on interface", "1", model.KindCounter, v, attrs))
		}
		if v, ok := parseInt(s.Map, "tx-error"); ok {
			samples = append(samples, sampleForInterface("mikrotik.interface.tx.errors",
				"Transmit errors on interface", "1", model.KindCounter, v, attrs))
		}
		if v, ok := parseInt(s.Map, "rx-drop"); ok {
			samples = append(samples, sampleForInterface("mikrotik.interface.rx.drops",
				"Received packets dropped on interface", "1", model.KindCounter, v, attrs))
		}
		if v, ok := parseInt(s.Map, "tx-drop"); ok {
			samples = append(samples, sampleForInterface("mikrotik.interface.tx.drops",
				"Transmitted packets dropped on interface", "1", model.KindCounter, v, attrs))
		}
		if v, ok := parseInt(s.Map, "link-downs"); ok {
			samples = append(samples, sampleForInterface("mikrotik.interface.link_downs",
				"Number of times the interface link went down", "1", model.KindCounter, v, attrs))
		}

		// running field: emit the up gauge for every interface, defaulting to 0.
		up := int64(0)
		if s.Map["running"] == "true" {
			up = 1
		}
		samples = append(samples, sampleForInterface("mikrotik.interface.up",
			"Whether the interface is running", "1", model.KindGauge, up, attrs))
	}

	return samples, nil
}

// sampleForInterface is retained as a thin alias over sample for the interface
// collector's call sites.
func sampleForInterface(name, desc, unit string, kind model.MetricKind, value int64, attrs map[string]string) model.Sample {
	return sample(name, desc, unit, kind, value, attrs)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// parseInt safely reads an integer field from a RouterOS response map. It
// returns false when the key is absent or the value is not a valid integer.
func parseInt(m map[string]string, key string) (int64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// sample builds a model.Sample, cloning attrs so each sample owns its own map.
func sample(name, desc, unit string, kind model.MetricKind, value int64, attrs map[string]string) model.Sample {
	var clone map[string]string
	if len(attrs) > 0 {
		clone = make(map[string]string, len(attrs))
		for k, v := range attrs {
			clone[k] = v
		}
	}
	return model.Sample{
		Name:        name,
		Description: desc,
		Unit:        unit,
		Kind:        kind,
		Value:       value,
		Attributes:  clone,
	}
}

// runCount executes a `<path> count-only` command and returns the count from
// the !done reply's "ret" attribute.
func runCount(ctx context.Context, r Runner, path string) (int64, bool, error) {
	reply, err := r.Run(ctx, path, "=count-only=")
	if err != nil {
		return 0, false, err
	}
	if reply.Done == nil {
		return 0, false, nil
	}
	v, ok := parseInt(reply.Done.Map, "ret")
	return v, ok, nil
}

// ---------------------------------------------------------------------------
// DefaultCollectors
// ---------------------------------------------------------------------------

// DefaultCollectors returns the standard set of metric collectors, in a stable
// order.
func DefaultCollectors() []Collector {
	return []Collector{
		&systemResourceCollector{},
		&interfaceCollector{},
		&healthCollector{},
		&dhcpLeaseCollector{},
		&connectionCollector{},
		&countsCollector{},
		&firewallCollector{},
		&wirelessCollector{},
		&wireguardCollector{},
		&ipsecCollector{},
		&pppCollector{},
		&queueCollector{},
	}
}

// SelectCollectors returns the collectors whose names appear in the given list,
// preserving DefaultCollectors order. An empty or nil list returns all default
// collectors. Unknown names are reported so misconfiguration is visible.
func SelectCollectors(names []string) ([]Collector, []string) {
	all := DefaultCollectors()
	if len(names) == 0 {
		return all, nil
	}

	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}

	var selected []Collector
	known := map[string]bool{}
	for _, c := range all {
		known[c.Name()] = true
		if want[c.Name()] {
			selected = append(selected, c)
		}
	}

	var unknown []string
	for _, n := range names {
		if !known[n] {
			unknown = append(unknown, n)
		}
	}
	return selected, unknown
}

// Compile-time interface checks.
var (
	_ Collector = (*systemResourceCollector)(nil)
	_ Collector = (*interfaceCollector)(nil)
	_ Collector = (*healthCollector)(nil)
	_ Collector = (*dhcpLeaseCollector)(nil)
	_ Collector = (*connectionCollector)(nil)
	_ Collector = (*countsCollector)(nil)
	_ Collector = (*firewallCollector)(nil)
	_ Collector = (*wirelessCollector)(nil)
	_ Collector = (*wireguardCollector)(nil)
	_ Collector = (*ipsecCollector)(nil)
	_ Collector = (*pppCollector)(nil)
	_ Collector = (*queueCollector)(nil)
)
