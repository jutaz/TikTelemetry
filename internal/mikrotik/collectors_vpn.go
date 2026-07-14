package mikrotik

import (
	"context"
	"strconv"
	"strings"

	"github.com/jutaz/tiktelemetry/internal/model"
)

// ---------------------------------------------------------------------------
// wireguardCollector
//
// /interface/wireguard/peers/print exposes per-peer cumulative rx/tx byte
// counters and the seconds since the last handshake. WireGuard is built into
// RouterOS 7, so on a CHR with none configured this simply returns zero rows.
// Note: the byte fields are literally "rx"/"tx" (not "rx-bytes"), and
// "last-handshake" is an integer number of seconds (absent when never
// handshaked).
// ---------------------------------------------------------------------------

type wireguardCollector struct{}

func (c *wireguardCollector) Name() string { return "wireguard" }

func (c *wireguardCollector) Collect(ctx context.Context, r Runner) ([]model.Sample, error) {
	reply, err := r.Run(ctx, "/interface/wireguard/peers/print")
	if err != nil {
		return nil, err
	}

	var samples []model.Sample
	for _, s := range reply.Re {
		iface := s.Map["interface"]
		if iface == "" {
			continue
		}
		attrs := map[string]string{"interface": iface}
		// A peer's comment is a stable, low-cardinality label when present.
		if cmt := s.Map["comment"]; cmt != "" {
			attrs["comment"] = cmt
		}

		if v, ok := parseInt(s.Map, "rx"); ok {
			samples = append(samples, sample(
				"mikrotik.wireguard.rx.bytes",
				"Bytes received from a WireGuard peer",
				"By", model.KindCounter, v, attrs))
		}
		if v, ok := parseInt(s.Map, "tx"); ok {
			samples = append(samples, sample(
				"mikrotik.wireguard.tx.bytes",
				"Bytes transmitted to a WireGuard peer",
				"By", model.KindCounter, v, attrs))
		}
		// last-handshake is seconds since the last handshake; absent means the
		// peer has never completed one, which we surface as a large sentinel is
		// avoided — instead we simply omit the sample so "never" is a gap, not a
		// misleading zero.
		if v, ok := parseInt(s.Map, "last-handshake"); ok {
			samples = append(samples, sample(
				"mikrotik.wireguard.last_handshake",
				"Seconds since the last WireGuard handshake",
				"s", model.KindGauge, v, attrs))
		}
	}
	return samples, nil
}

// ---------------------------------------------------------------------------
// ipsecCollector
//
// /ip/ipsec/active-peers/print exposes per-peer aggregate byte/packet counters
// for established IKE peers. IPsec is built-in; CHR with none configured
// returns zero rows.
// ---------------------------------------------------------------------------

type ipsecCollector struct{}

func (c *ipsecCollector) Name() string { return "ipsec" }

func (c *ipsecCollector) Collect(ctx context.Context, r Runner) ([]model.Sample, error) {
	reply, err := r.Run(ctx, "/ip/ipsec/active-peers/print")
	if err != nil {
		return nil, err
	}

	var samples []model.Sample
	var active int64
	for _, s := range reply.Re {
		active++
		// Label by remote address (bounded set of tunnels).
		remote := s.Map["remote-address"]
		if remote == "" {
			remote = "unknown"
		}
		attrs := map[string]string{"remote": remote}

		for _, m := range []struct {
			field, name, desc, unit string
		}{
			{"rx-bytes", "mikrotik.ipsec.rx.bytes", "Bytes received over an IPsec peer", "By"},
			{"tx-bytes", "mikrotik.ipsec.tx.bytes", "Bytes transmitted over an IPsec peer", "By"},
			{"rx-packets", "mikrotik.ipsec.rx.packets", "Packets received over an IPsec peer", "1"},
			{"tx-packets", "mikrotik.ipsec.tx.packets", "Packets transmitted over an IPsec peer", "1"},
		} {
			if v, ok := parseInt(s.Map, m.field); ok {
				samples = append(samples, sample(m.name, m.desc, m.unit, model.KindCounter, v, attrs))
			}
		}
	}

	// Always emit the active-peer count, even when zero, so the series exists.
	samples = append(samples, sample(
		"mikrotik.ipsec.active_peers",
		"Number of established IPsec peers",
		"1", model.KindGauge, active, nil))
	return samples, nil
}

// ---------------------------------------------------------------------------
// pppCollector
//
// /ppp/active/print lists active PPP sessions (PPPoE, L2TP, PPTP, SSTP, ...).
// We report a count grouped by service plus a total. Byte stats require the
// =stats= argument which has a spotty history across versions, so we rely on
// the always-present session rows for a reliable count. PPP is built-in; CHR
// with no sessions returns zero rows.
// ---------------------------------------------------------------------------

type pppCollector struct{}

func (c *pppCollector) Name() string { return "ppp" }

func (c *pppCollector) Collect(ctx context.Context, r Runner) ([]model.Sample, error) {
	reply, err := r.Run(ctx, "/ppp/active/print")
	if err != nil {
		return nil, err
	}

	byService := map[string]int64{}
	var total int64
	for _, s := range reply.Re {
		total++
		svc := s.Map["service"]
		if svc == "" {
			svc = "unknown"
		}
		byService[svc]++
	}

	var samples []model.Sample
	for svc, n := range byService {
		samples = append(samples, sample(
			"mikrotik.ppp.sessions",
			"Active PPP sessions by service type",
			"1", model.KindGauge, n,
			map[string]string{"service": svc}))
	}
	samples = append(samples, sample(
		"mikrotik.ppp.sessions.total",
		"Total active PPP sessions",
		"1", model.KindGauge, total, nil))
	return samples, nil
}

// ---------------------------------------------------------------------------
// queueCollector
//
// /queue/simple/print with =stats= exposes per-queue cumulative byte/packet
// counters as "TX/RX" slash-separated pairs, plus cumulative dropped packets.
// Queues are built-in; CHR with none configured returns zero rows.
// ---------------------------------------------------------------------------

type queueCollector struct{}

func (c *queueCollector) Name() string { return "queue" }

func (c *queueCollector) Collect(ctx context.Context, r Runner) ([]model.Sample, error) {
	// =stats= is passed as a separate API word to include statistics fields.
	reply, err := r.Run(ctx, "/queue/simple/print", "=stats=")
	if err != nil {
		return nil, err
	}

	var samples []model.Sample
	for _, s := range reply.Re {
		name := s.Map["name"]
		if name == "" {
			continue
		}
		base := map[string]string{"queue": name}

		// bytes/packets/dropped are "tx/rx" pairs (upload/download).
		emitPair(&samples, base, "mikrotik.queue.tx.bytes", "mikrotik.queue.rx.bytes",
			"Bytes sent/received through a simple queue", "By", model.KindCounter, s.Map["bytes"])
		emitPair(&samples, base, "mikrotik.queue.tx.packets", "mikrotik.queue.rx.packets",
			"Packets sent/received through a simple queue", "1", model.KindCounter, s.Map["packets"])
		emitPair(&samples, base, "mikrotik.queue.tx.dropped", "mikrotik.queue.rx.dropped",
			"Packets dropped by a simple queue", "1", model.KindCounter, s.Map["dropped"])
	}
	return samples, nil
}

// emitPair parses a RouterOS "tx/rx" slash-separated counter pair and appends a
// sample for each side that parses. Missing or malformed input is skipped.
func emitPair(dst *[]model.Sample, attrs map[string]string, txName, rxName, desc, unit string, kind model.MetricKind, raw string) {
	tx, rx, ok := splitPair(raw)
	if !ok {
		return
	}
	if v, ok := parseInt64(tx); ok {
		*dst = append(*dst, sample(txName, desc, unit, kind, v, attrs))
	}
	if v, ok := parseInt64(rx); ok {
		*dst = append(*dst, sample(rxName, desc, unit, kind, v, attrs))
	}
}

// splitPair splits a RouterOS "tx/rx" pair on the first slash. Returns ok=false
// when the input is empty or has no slash.
func splitPair(raw string) (tx, rx string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	tx, rx, ok = strings.Cut(raw, "/")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(tx), strings.TrimSpace(rx), true
}

// parseInt64 parses a plain integer string, returning ok=false on failure.
func parseInt64(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
