package mikrotik

import (
	"context"
	"strconv"
	"strings"

	"github.com/jutaz/tiktelemetry/internal/model"
)

// ---------------------------------------------------------------------------
// healthCollector
//
// /system/health/print returns one row per hardware sensor (temperature,
// voltage, fan speed, ...). On a CHR VM there are no sensors, so this returns
// zero rows and emits nothing. Rows use dynamic name/value pairs in v7.
// ---------------------------------------------------------------------------

type healthCollector struct{}

func (c *healthCollector) Name() string { return "health" }

func (c *healthCollector) Collect(ctx context.Context, r Runner) ([]model.Sample, error) {
	reply, err := r.Run(ctx, "/system/health/print")
	if err != nil {
		return nil, err
	}

	var samples []model.Sample
	for _, s := range reply.Re {
		name := s.Map["name"]
		raw := s.Map["value"]
		if name == "" || raw == "" {
			continue
		}
		val, ok := parseNumeric(raw)
		if !ok {
			// Non-numeric readings (e.g. psu-state=ok) are skipped; they are
			// better represented as separate state metrics if ever needed.
			continue
		}
		unit := healthUnit(s.Map["type"], name)
		samples = append(samples, sample(
			"mikrotik.system.health",
			"Hardware health sensor reading",
			unit,
			model.KindGauge,
			val,
			map[string]string{"sensor": name},
		))
	}
	return samples, nil
}

// healthUnit maps a RouterOS health "type" (or, as a fallback, the sensor name)
// to a UCUM unit string.
func healthUnit(typ, name string) string {
	switch strings.ToUpper(typ) {
	case "C":
		return "Cel"
	case "V":
		return "V"
	case "A":
		return "A"
	case "W":
		return "W"
	case "RPM":
		return "1"
	}
	// Fall back to inferring from the sensor name.
	switch {
	case strings.Contains(name, "temperature"):
		return "Cel"
	case strings.Contains(name, "voltage"):
		return "V"
	case strings.Contains(name, "fan"):
		return "1"
	case strings.Contains(name, "current"):
		return "A"
	case strings.Contains(name, "power"):
		return "W"
	default:
		return "1"
	}
}

// ---------------------------------------------------------------------------
// dhcpLeaseCollector
//
// /ip/dhcp-server/lease/print returns one row per lease. We aggregate counts by
// (server, status) plus a total. On a CHR without a DHCP server this returns
// zero rows and emits only zero-valued aggregates (none, since there are no
// rows) — which is fine.
// ---------------------------------------------------------------------------

type dhcpLeaseCollector struct{}

func (c *dhcpLeaseCollector) Name() string { return "dhcp" }

func (c *dhcpLeaseCollector) Collect(ctx context.Context, r Runner) ([]model.Sample, error) {
	reply, err := r.Run(ctx, "/ip/dhcp-server/lease/print")
	if err != nil {
		return nil, err
	}

	type key struct{ server, status string }
	counts := map[key]int64{}
	var total int64
	for _, s := range reply.Re {
		status := s.Map["status"]
		if status == "" {
			status = "unknown"
		}
		server := s.Map["server"]
		if server == "" {
			server = "unknown"
		}
		counts[key{server, status}]++
		total++
	}

	var samples []model.Sample
	for k, v := range counts {
		samples = append(samples, sample(
			"mikrotik.dhcp.leases",
			"Number of DHCP leases by server and status",
			"1",
			model.KindGauge,
			v,
			map[string]string{"server": k.server, "status": k.status},
		))
	}
	// Always emit the total, even when zero, so the series exists.
	samples = append(samples, sample(
		"mikrotik.dhcp.leases.total",
		"Total number of DHCP leases",
		"1",
		model.KindGauge,
		total,
		nil,
	))
	return samples, nil
}

// ---------------------------------------------------------------------------
// connectionCollector
//
// /ip/firewall/connection/tracking/print returns a single row with O(1)
// total-entries and max-entries, preferable to the O(n) count-only scan.
// ---------------------------------------------------------------------------

type connectionCollector struct{}

func (c *connectionCollector) Name() string { return "connections" }

func (c *connectionCollector) Collect(ctx context.Context, r Runner) ([]model.Sample, error) {
	reply, err := r.Run(ctx, "/ip/firewall/connection/tracking/print")
	if err != nil {
		return nil, err
	}
	if len(reply.Re) == 0 {
		return nil, nil
	}
	m := reply.Re[0].Map

	var samples []model.Sample
	if v, ok := parseInt(m, "total-entries"); ok {
		samples = append(samples, sample(
			"mikrotik.connections.active",
			"Current number of tracked connections",
			"1", model.KindGauge, v, nil))
	}
	if v, ok := parseInt(m, "max-entries"); ok {
		samples = append(samples, sample(
			"mikrotik.connections.max",
			"Maximum tracked connection capacity",
			"1", model.KindGauge, v, nil))
	}
	return samples, nil
}

// ---------------------------------------------------------------------------
// countsCollector
//
// Cheap count-only scrapes for configured IP addresses and routes.
// ---------------------------------------------------------------------------

type countsCollector struct{}

func (c *countsCollector) Name() string { return "counts" }

func (c *countsCollector) Collect(ctx context.Context, r Runner) ([]model.Sample, error) {
	var samples []model.Sample

	if v, ok, err := runCount(ctx, r, "/ip/address/print"); err != nil {
		return nil, err
	} else if ok {
		samples = append(samples, sample(
			"mikrotik.ip.addresses",
			"Number of configured IP addresses",
			"1", model.KindGauge, v, nil))
	}

	if v, ok, err := runCount(ctx, r, "/ip/route/print"); err != nil {
		return nil, err
	} else if ok {
		samples = append(samples, sample(
			"mikrotik.ip.routes",
			"Number of routes in the routing table",
			"1", model.KindGauge, v, nil))
	}

	return samples, nil
}

// ---------------------------------------------------------------------------
// firewallCollector
//
// /ip/firewall/filter/print exposes per-rule byte/packet counters. Rules are
// labelled by chain, action, and comment (when present). A default CHR has no
// rules, so this emits nothing until rules are configured.
// ---------------------------------------------------------------------------

type firewallCollector struct{}

func (c *firewallCollector) Name() string { return "firewall" }

func (c *firewallCollector) Collect(ctx context.Context, r Runner) ([]model.Sample, error) {
	reply, err := r.Run(ctx, "/ip/firewall/filter/print")
	if err != nil {
		return nil, err
	}

	var samples []model.Sample
	for _, s := range reply.Re {
		attrs := map[string]string{
			"chain":  s.Map["chain"],
			"action": s.Map["action"],
		}
		if cmt := s.Map["comment"]; cmt != "" {
			attrs["comment"] = cmt
		}
		if v, ok := parseInt(s.Map, "bytes"); ok {
			samples = append(samples, sample(
				"mikrotik.firewall.filter.bytes",
				"Bytes matched by a firewall filter rule",
				"By", model.KindCounter, v, attrs))
		}
		if v, ok := parseInt(s.Map, "packets"); ok {
			samples = append(samples, sample(
				"mikrotik.firewall.filter.packets",
				"Packets matched by a firewall filter rule",
				"1", model.KindCounter, v, attrs))
		}
	}
	return samples, nil
}

// ---------------------------------------------------------------------------
// wirelessCollector
//
// Wireless is hardware-only. On CHR the wireless/wifi packages are absent and
// the commands return a !trap error. This collector tries the legacy and the
// new WiFi registration tables and treats a "no such command" error as "no
// wireless present" — returning no samples and no error so the scrape is not
// disrupted.
// ---------------------------------------------------------------------------

type wirelessCollector struct{}

func (c *wirelessCollector) Name() string { return "wireless" }

func (c *wirelessCollector) Collect(ctx context.Context, r Runner) ([]model.Sample, error) {
	// Try both drivers; whichever succeeds wins. If both are unavailable
	// (typical on CHR / non-wireless boards), emit nothing.
	for _, path := range []string{
		"/interface/wireless/registration-table/print",
		"/interface/wifi/registration-table/print",
	} {
		reply, err := r.Run(ctx, path)
		if err != nil {
			if IsFeatureAbsent(err) {
				continue
			}
			return nil, err
		}

		perIface := map[string]int64{}
		for _, s := range reply.Re {
			iface := s.Map["interface"]
			if iface == "" {
				iface = "unknown"
			}
			perIface[iface]++
		}

		var samples []model.Sample
		for iface, n := range perIface {
			samples = append(samples, sample(
				"mikrotik.wireless.clients",
				"Number of connected wireless clients",
				"1", model.KindGauge, n,
				map[string]string{"interface": iface}))
		}
		return samples, nil
	}
	return nil, nil
}

// IsFeatureAbsent reports whether an error means the RouterOS command does not
// exist on this board (e.g. `/system/health` on a device with no sensors, or
// the wireless package not being installed) — as opposed to a real failure.
// RouterOS answers such a request over a healthy session with a !trap whose
// message is like "no such command prefix" / "no such command or directory".
//
// Collectors and the scrape loop treat this as "feature not present": emit no
// samples and no warning, rather than logging a recurring error every scrape.
func IsFeatureAbsent(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such command") ||
		strings.Contains(msg, "bad command name") ||
		strings.Contains(msg, "unknown command") ||
		strings.Contains(msg, "syntax error")
}

// parseNumeric parses a possibly-decimal RouterOS value (e.g. "24.1") into an
// int64, truncating any fractional part. Returns false when non-numeric.
func parseNumeric(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, true
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f), true
	}
	return 0, false
}
