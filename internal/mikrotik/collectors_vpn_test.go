package mikrotik

import (
	"context"
	"testing"

	"github.com/go-routeros/routeros/v3"
	"github.com/jutaz/tiktelemetry/internal/model"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// findByNameAttr finds the first sample with the given name whose attributes
// contain every key-value pair in attrs. Use this when the same metric name
// can appear for multiple label sets.
func findByNameAttr(t *testing.T, samples []model.Sample, name string, attrs map[string]string) *model.Sample {
	t.Helper()
	for i := range samples {
		if samples[i].Name != name {
			continue
		}
		match := true
		for k, v := range attrs {
			if samples[i].Attributes[k] != v {
				match = false
				break
			}
		}
		if match {
			return &samples[i]
		}
	}
	return nil
}

// mustFindByNameAttr is like findByNameAttr but fatally fails if not found.
func mustFindByNameAttr(t *testing.T, samples []model.Sample, name string, attrs map[string]string) *model.Sample {
	t.Helper()
	s := findByNameAttr(t, samples, name, attrs)
	if s == nil {
		t.Fatalf("sample %s with attrs %v not found among %d samples", name, attrs, len(samples))
	}
	return s
}

// ---------------------------------------------------------------------------
// wireguardCollector
// ---------------------------------------------------------------------------

func TestWireguardCollector_Name(t *testing.T) {
	c := &wireguardCollector{}
	if got := c.Name(); got != "wireguard" {
		t.Errorf("Name() = %q, want wireguard", got)
	}
}

func TestWireguardCollector_TwoPeers(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/interface/wireguard/peers/print": {
				replySentences(
					map[string]string{
						"interface":      "wg0",
						"comment":        "phone",
						"rx":             "1000",
						"tx":             "2000",
						"last-handshake": "30",
					},
					map[string]string{
						"interface": "wg0",
						"rx":        "50",
						"tx":        "60",
						// no comment, no last-handshake
					},
				),
			},
		},
	}
	samples, err := (&wireguardCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 5 {
		t.Fatalf("expected 5 samples, got %d", len(samples))
	}

	// -- Peer 1: interface=wg0, comment=phone --
	attrs1 := map[string]string{"interface": "wg0", "comment": "phone"}

	s := mustFindByNameAttr(t, samples, "mikrotik.wireguard.rx.bytes", attrs1)
	if s.Value != 1000 || s.Kind != model.KindCounter || s.Unit != "By" {
		t.Errorf("peer1 rx.bytes: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}

	s = mustFindByNameAttr(t, samples, "mikrotik.wireguard.tx.bytes", attrs1)
	if s.Value != 2000 || s.Kind != model.KindCounter || s.Unit != "By" {
		t.Errorf("peer1 tx.bytes: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}

	s = mustFindByNameAttr(t, samples, "mikrotik.wireguard.last_handshake", attrs1)
	if s.Value != 30 || s.Kind != model.KindGauge || s.Unit != "s" {
		t.Errorf("peer1 last_handshake: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}

	// -- Peer 2: interface=wg0, no comment --
	// findByNameAttr with {interface:wg0} would match peer1 first, so we search
	// for a sample that has name+interface match but lacks the "comment" key.
	for _, name := range []string{"mikrotik.wireguard.rx.bytes", "mikrotik.wireguard.tx.bytes"} {
		var found *model.Sample
		for i := range samples {
			if samples[i].Name == name && samples[i].Attributes["interface"] == "wg0" {
				if _, has := samples[i].Attributes["comment"]; !has {
					found = &samples[i]
					break
				}
			}
		}
		if found == nil {
			t.Fatalf("missing peer2 %s", name)
		}
		if found.Kind != model.KindCounter || found.Unit != "By" {
			t.Errorf("peer2 %s: kind=%d unit=%q", name, found.Kind, found.Unit)
		}
		switch name {
		case "mikrotik.wireguard.rx.bytes":
			if found.Value != 50 {
				t.Errorf("peer2 rx.bytes value = %d, want 50", found.Value)
			}
		case "mikrotik.wireguard.tx.bytes":
			if found.Value != 60 {
				t.Errorf("peer2 tx.bytes value = %d, want 60", found.Value)
			}
		}
	}

	// Peer 2 must NOT have a last_handshake sample (field absent).
	for i := range samples {
		if samples[i].Name == "mikrotik.wireguard.last_handshake" &&
			samples[i].Attributes["interface"] == "wg0" {
			if _, has := samples[i].Attributes["comment"]; !has {
				t.Error("peer2 should not have a last_handshake sample (field absent)")
				break
			}
		}
	}
}

func TestWireguardCollector_EmptyInterfaceSkipped(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/interface/wireguard/peers/print": {
				replySentences(
					map[string]string{"interface": "", "rx": "999"},
					map[string]string{"interface": "wg0", "rx": "1"},
				),
			},
		},
	}
	samples, err := (&wireguardCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	// Only the wg0 row should produce a sample.
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
	if samples[0].Attributes["interface"] != "wg0" {
		t.Errorf("expected interface=wg0, got %v", samples[0].Attributes)
	}
}

func TestWireguardCollector_EmptyReply(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/interface/wireguard/peers/print": {{Re: nil}},
		},
	}
	samples, err := (&wireguardCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if samples != nil {
		t.Errorf("expected nil samples for empty reply, got %v", samples)
	}
}

func TestWireguardCollector_RunnerError(t *testing.T) {
	runner := &fakeRunner{err: assertAnError}
	_, err := (&wireguardCollector{}).Collect(context.Background(), runner)
	if err != assertAnError {
		t.Errorf("expected assertAnError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ipsecCollector
// ---------------------------------------------------------------------------

func TestIPSecCollector_Name(t *testing.T) {
	c := &ipsecCollector{}
	if got := c.Name(); got != "ipsec" {
		t.Errorf("Name() = %q, want ipsec", got)
	}
}

func TestIPSecCollector_TwoPeers(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ip/ipsec/active-peers/print": {
				replySentences(
					map[string]string{
						"remote-address": "203.0.113.1",
						"rx-bytes":       "1000",
						"tx-bytes":       "2000",
						"rx-packets":     "10",
						"tx-packets":     "20",
					},
					map[string]string{
						"remote-address": "",
						"tx-bytes":       "5",
					},
				),
			},
		},
	}
	samples, err := (&ipsecCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	// Peer1: 4 counters. Peer2: 1 counter. + active_peers = 6
	if len(samples) != 6 {
		t.Fatalf("expected 6 samples, got %d", len(samples))
	}

	// -- Peer1: remote=203.0.113.1 --
	rem1 := map[string]string{"remote": "203.0.113.1"}

	s := mustFindByNameAttr(t, samples, "mikrotik.ipsec.rx.bytes", rem1)
	if s.Value != 1000 || s.Kind != model.KindCounter || s.Unit != "By" {
		t.Errorf("peer1 rx.bytes: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}

	s = mustFindByNameAttr(t, samples, "mikrotik.ipsec.tx.bytes", rem1)
	if s.Value != 2000 || s.Kind != model.KindCounter || s.Unit != "By" {
		t.Errorf("peer1 tx.bytes: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}

	s = mustFindByNameAttr(t, samples, "mikrotik.ipsec.rx.packets", rem1)
	if s.Value != 10 || s.Kind != model.KindCounter || s.Unit != "1" {
		t.Errorf("peer1 rx.packets: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}

	s = mustFindByNameAttr(t, samples, "mikrotik.ipsec.tx.packets", rem1)
	if s.Value != 20 || s.Kind != model.KindCounter || s.Unit != "1" {
		t.Errorf("peer1 tx.packets: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}

	// -- Peer2: remote=unknown --
	rem2 := map[string]string{"remote": "unknown"}
	s = mustFindByNameAttr(t, samples, "mikrotik.ipsec.tx.bytes", rem2)
	if s.Value != 5 || s.Kind != model.KindCounter || s.Unit != "By" {
		t.Errorf("peer2 tx.bytes: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}
	// Peer2 should NOT have rx.bytes, rx.packets, tx.packets
	for _, name := range []string{"mikrotik.ipsec.rx.bytes", "mikrotik.ipsec.rx.packets", "mikrotik.ipsec.tx.packets"} {
		if s := findByNameAttr(t, samples, name, rem2); s != nil {
			t.Errorf("peer2 unexpectedly has sample %s", name)
		}
	}

	// -- active_peers gauge --
	ag := findSample(t, samples, "mikrotik.ipsec.active_peers")
	if ag == nil {
		t.Fatal("missing mikrotik.ipsec.active_peers")
	}
	if ag.Value != 2 || ag.Kind != model.KindGauge || ag.Unit != "1" {
		t.Errorf("active_peers: value=%d kind=%d unit=%q", ag.Value, ag.Kind, ag.Unit)
	}
	if len(ag.Attributes) != 0 {
		t.Errorf("active_peers should have no attributes, got %v", ag.Attributes)
	}
}

func TestIPSecCollector_EmptyReply(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ip/ipsec/active-peers/print": {{Re: nil}},
		},
	}
	samples, err := (&ipsecCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample (active_peers=0), got %d", len(samples))
	}
	if samples[0].Name != "mikrotik.ipsec.active_peers" || samples[0].Value != 0 {
		t.Errorf("expected active_peers=0, got %+v", samples[0])
	}
}

func TestIPSecCollector_RunnerError(t *testing.T) {
	runner := &fakeRunner{err: assertAnError}
	_, err := (&ipsecCollector{}).Collect(context.Background(), runner)
	if err != assertAnError {
		t.Errorf("expected assertAnError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// pppCollector
// ---------------------------------------------------------------------------

func TestPPPCollector_Name(t *testing.T) {
	c := &pppCollector{}
	if got := c.Name(); got != "ppp" {
		t.Errorf("Name() = %q, want ppp", got)
	}
}

func TestPPPCollector_MultipleSessions(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ppp/active/print": {
				replySentences(
					map[string]string{"service": "pppoe", "name": "user1"},
					map[string]string{"service": "pppoe", "name": "user2"},
					map[string]string{"service": "l2tp", "name": "user3"},
					map[string]string{"name": "user4"}, // no service -> unknown
				),
			},
		},
	}
	samples, err := (&pppCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	// 3 per-service samples + 1 total = 4
	if len(samples) != 4 {
		t.Fatalf("expected 4 samples, got %d", len(samples))
	}

	// Check per-service gauges
	s := mustFindByNameAttr(t, samples, "mikrotik.ppp.sessions", map[string]string{"service": "pppoe"})
	if s.Value != 2 || s.Kind != model.KindGauge || s.Unit != "1" {
		t.Errorf("pppoe: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}

	s = mustFindByNameAttr(t, samples, "mikrotik.ppp.sessions", map[string]string{"service": "l2tp"})
	if s.Value != 1 || s.Kind != model.KindGauge || s.Unit != "1" {
		t.Errorf("l2tp: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}

	s = mustFindByNameAttr(t, samples, "mikrotik.ppp.sessions", map[string]string{"service": "unknown"})
	if s.Value != 1 || s.Kind != model.KindGauge || s.Unit != "1" {
		t.Errorf("unknown: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}

	// Check total
	total := findSample(t, samples, "mikrotik.ppp.sessions.total")
	if total == nil {
		t.Fatal("missing mikrotik.ppp.sessions.total")
	}
	if total.Value != 4 || total.Kind != model.KindGauge || total.Unit != "1" {
		t.Errorf("total: value=%d kind=%d unit=%q", total.Value, total.Kind, total.Unit)
	}
	if len(total.Attributes) != 0 {
		t.Errorf("total should have no attributes, got %v", total.Attributes)
	}
}

func TestPPPCollector_EmptyReply(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ppp/active/print": {{Re: nil}},
		},
	}
	samples, err := (&pppCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample (total=0), got %d", len(samples))
	}
	if samples[0].Name != "mikrotik.ppp.sessions.total" || samples[0].Value != 0 {
		t.Errorf("expected total=0, got %+v", samples[0])
	}
}

func TestPPPCollector_RunnerError(t *testing.T) {
	runner := &fakeRunner{err: assertAnError}
	_, err := (&pppCollector{}).Collect(context.Background(), runner)
	if err != assertAnError {
		t.Errorf("expected assertAnError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// queueCollector
// ---------------------------------------------------------------------------

func TestQueueCollector_Name(t *testing.T) {
	c := &queueCollector{}
	if got := c.Name(); got != "queue" {
		t.Errorf("Name() = %q, want queue", got)
	}
}

func TestQueueCollector_OneQueue(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/queue/simple/print": {
				replySentence(map[string]string{
					"name":    "q1",
					"bytes":   "1000/9000",
					"packets": "10/90",
					"dropped": "1/2",
				}),
			},
		},
	}
	samples, err := (&queueCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 6 {
		t.Fatalf("expected 6 samples, got %d", len(samples))
	}

	q := map[string]string{"queue": "q1"}
	one := int64(1)

	tests := []struct {
		name  string
		value int64
		unit  string
	}{
		{"mikrotik.queue.tx.bytes", 1000, "By"},
		{"mikrotik.queue.rx.bytes", 9000, "By"},
		{"mikrotik.queue.tx.packets", 10, "1"},
		{"mikrotik.queue.rx.packets", 90, "1"},
		{"mikrotik.queue.tx.dropped", one, "1"},
		{"mikrotik.queue.rx.dropped", 2, "1"},
	}
	for _, tt := range tests {
		s := mustFindByNameAttr(t, samples, tt.name, q)
		if s.Value != tt.value || s.Kind != model.KindCounter || s.Unit != tt.unit {
			t.Errorf("%s: value=%d kind=%d unit=%q", tt.name, s.Value, s.Kind, s.Unit)
		}
	}
}

func TestQueueCollector_MalformedPair(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/queue/simple/print": {
				replySentence(map[string]string{
					"name":    "q2",
					"bytes":   "nothing", // no slash -> skip
					"packets": "5/6",     // valid
				}),
			},
		},
	}
	samples, err := (&queueCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(samples))
	}

	q := map[string]string{"queue": "q2"}

	// tx.packets=5, rx.packets=6 should be present
	s := mustFindByNameAttr(t, samples, "mikrotik.queue.tx.packets", q)
	if s.Value != 5 {
		t.Errorf("tx.packets = %d, want 5", s.Value)
	}
	s = mustFindByNameAttr(t, samples, "mikrotik.queue.rx.packets", q)
	if s.Value != 6 {
		t.Errorf("rx.packets = %d, want 6", s.Value)
	}

	// No bytes samples for q2
	for _, name := range []string{"mikrotik.queue.tx.bytes", "mikrotik.queue.rx.bytes"} {
		if s := findByNameAttr(t, samples, name, q); s != nil {
			t.Errorf("unexpected sample %s for q2", name)
		}
	}
}

func TestQueueCollector_NonNumericSide(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/queue/simple/print": {
				replySentence(map[string]string{
					"name":  "q3",
					"bytes": "abc/500", // tx side "abc" doesn't parse
				}),
			},
		},
	}
	samples, err := (&queueCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}

	q := map[string]string{"queue": "q3"}
	s := mustFindByNameAttr(t, samples, "mikrotik.queue.rx.bytes", q)
	if s.Value != 500 {
		t.Errorf("rx.bytes = %d, want 500", s.Value)
	}
	if s := findByNameAttr(t, samples, "mikrotik.queue.tx.bytes", q); s != nil {
		t.Error("tx.bytes should not be emitted for non-numeric tx side")
	}
}

func TestQueueCollector_EmptyNameSkipped(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/queue/simple/print": {
				replySentences(
					map[string]string{"name": ""},
					map[string]string{"name": "valid", "bytes": "1/2"},
				),
			},
		},
	}
	samples, err := (&queueCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	// Only the valid row should produce samples (tx.bytes and rx.bytes).
	if len(samples) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(samples))
	}
	for _, s := range samples {
		if s.Attributes["queue"] != "valid" {
			t.Errorf("expected queue=valid, got %v", s.Attributes)
		}
	}
}

func TestQueueCollector_EmptyReply(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/queue/simple/print": {{Re: nil}},
		},
	}
	samples, err := (&queueCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if samples != nil {
		t.Errorf("expected nil samples for empty reply, got %v", samples)
	}
}

func TestQueueCollector_RunnerError(t *testing.T) {
	runner := &fakeRunner{err: assertAnError}
	_, err := (&queueCollector{}).Collect(context.Background(), runner)
	if err != assertAnError {
		t.Errorf("expected assertAnError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers: splitPair and parseInt64
// ---------------------------------------------------------------------------

func TestSplitPair(t *testing.T) {
	tests := []struct {
		in string
		tx string
		rx string
		ok bool
	}{
		{"1/2", "1", "2", true},
		{"1 / 2", "1", "2", true},
		{"  foo / bar ", "foo", "bar", true},
		{"", "", "", false},
		{"noslash", "", "", false},
	}
	for _, tt := range tests {
		tx, rx, ok := splitPair(tt.in)
		if tx != tt.tx || rx != tt.rx || ok != tt.ok {
			t.Errorf("splitPair(%q) = (%q,%q,%v), want (%q,%q,%v)",
				tt.in, tx, rx, ok, tt.tx, tt.rx, tt.ok)
		}
	}
}

func TestParseInt64(t *testing.T) {
	tests := []struct {
		in  string
		val int64
		ok  bool
	}{
		{"42", 42, true},
		{" 7 ", 7, true},
		{"", 0, false},
		{"x", 0, false},
		{"-5", -5, true},
		{"  0  ", 0, true},
	}
	for _, tt := range tests {
		val, ok := parseInt64(tt.in)
		if val != tt.val || ok != tt.ok {
			t.Errorf("parseInt64(%q) = (%d,%v), want (%d,%v)",
				tt.in, val, ok, tt.val, tt.ok)
		}
	}
}
