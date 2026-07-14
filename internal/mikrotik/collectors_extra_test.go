package mikrotik

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-routeros/routeros/v3"
	"github.com/go-routeros/routeros/v3/proto"
	"github.com/jutaz/tiktelemetry/internal/model"
)

// ---------------------------------------------------------------------------
// Local fakes for wireless tests (and any other command-specific error test)
// ---------------------------------------------------------------------------

// errCmdRunner returns a fixed error on every Run call. Useful for testing
// that a non-unavailable error is propagated rather than swallowed.
type errCmdRunner struct {
	msg string
}

func (r *errCmdRunner) Run(_ context.Context, _ ...string) (*routeros.Reply, error) {
	return nil, errors.New(r.msg)
}

// scriptedErrRunner returns errors for specific command keys and delegates
// to a fallback map of replies for the rest. When cmd[0] matches an entry in
// errs, the corresponding error is returned. Otherwise it looks up the reply
// queue like fakeRunner.
type scriptedErrRunner struct {
	replies map[string][]*routeros.Reply
	errs    map[string]error // keyed by cmd[0]; if present, Run returns this error
	calls   []string
}

func (r *scriptedErrRunner) Run(_ context.Context, cmd ...string) (*routeros.Reply, error) {
	r.calls = append(r.calls, cmd[0])
	if err, ok := r.errs[cmd[0]]; ok {
		return nil, err
	}
	queue := r.replies[cmd[0]]
	if len(queue) == 0 {
		return &routeros.Reply{}, nil
	}
	reply := queue[0]
	r.replies[cmd[0]] = queue[1:]
	return reply, nil
}

// ---------------------------------------------------------------------------
// Tests for parseNumeric
// ---------------------------------------------------------------------------

func TestParseNumeric(t *testing.T) {
	tests := []struct {
		in     string
		want   int64
		wantOK bool
	}{
		{"42", 42, true},
		{"24.1", 24, true},
		{"-5", -5, true},
		{"", 0, false},
		{"abc", 0, false},
		{"  7  ", 7, true},
		{"0", 0, true},
		{"-0", 0, true},
		{"3.999", 3, true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("parseNumeric(%q)", tt.in), func(t *testing.T) {
			got, ok := parseNumeric(tt.in)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("parseNumeric(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests for isUnavailableCommand
// ---------------------------------------------------------------------------

func TestIsUnavailableCommand(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"no such command", errors.New("no such command or directory"), true},
		{"bad command name", errors.New("bad command name routerboard"), true},
		{"unknown command", errors.New("unknown command /ip/foo"), true},
		{"syntax error", errors.New("syntax error at token"), true},
		{"timeout", errors.New("connection timeout"), false},
		{"permission denied", errors.New("permission denied"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUnavailableCommand(tt.err); got != tt.want {
				t.Errorf("isUnavailableCommand(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// healthCollector
// ---------------------------------------------------------------------------

func TestHealthCollector_Name(t *testing.T) {
	c := &healthCollector{}
	if got := c.Name(); got != "health" {
		t.Errorf("Name() = %q, want health", got)
	}
}

func TestHealthCollector(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/system/health/print": {
				replySentences(
					map[string]string{"name": "temperature", "value": "42", "type": "C"},
					map[string]string{"name": "voltage", "value": "24.1", "type": "V"},
					map[string]string{"name": "fan1-speed", "value": "3000", "type": "RPM"},
				),
			},
		},
	}
	samples, err := (&healthCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(samples))
	}

	// temperature
	s := findSample(t, samples, "mikrotik.system.health")
	if s == nil {
		t.Fatal("missing mikrotik.system.health")
	}
	// there are multiple with the same name; pick the one with sensor=temperature
	for i := range samples {
		if samples[i].Name == "mikrotik.system.health" && samples[i].Attributes["sensor"] == "temperature" {
			s = &samples[i]
			break
		}
	}
	if s.Attributes["sensor"] != "temperature" {
		t.Fatal("temperature sensor not found")
	}
	if s.Value != 42 {
		t.Errorf("temperature value = %d, want 42", s.Value)
	}
	if s.Unit != "Cel" {
		t.Errorf("temperature unit = %q, want Cel", s.Unit)
	}
	if s.Kind != model.KindGauge {
		t.Errorf("temperature kind = %d, want KindGauge", s.Kind)
	}

	// voltage
	for i := range samples {
		if samples[i].Attributes["sensor"] == "voltage" {
			s = &samples[i]
			break
		}
	}
	if s.Attributes["sensor"] != "voltage" {
		t.Fatal("voltage sensor not found")
	}
	if s.Value != 24 {
		t.Errorf("voltage value = %d, want 24 (truncated)", s.Value)
	}
	if s.Unit != "V" {
		t.Errorf("voltage unit = %q, want V", s.Unit)
	}
	if s.Kind != model.KindGauge {
		t.Errorf("voltage kind = %d, want KindGauge", s.Kind)
	}

	// fan1-speed
	for i := range samples {
		if samples[i].Attributes["sensor"] == "fan1-speed" {
			s = &samples[i]
			break
		}
	}
	if s.Attributes["sensor"] != "fan1-speed" {
		t.Fatal("fan1-speed sensor not found")
	}
	if s.Value != 3000 {
		t.Errorf("fan1-speed value = %d, want 3000", s.Value)
	}
	if s.Unit != "1" {
		t.Errorf("fan1-speed unit = %q, want 1", s.Unit)
	}
	if s.Kind != model.KindGauge {
		t.Errorf("fan1-speed kind = %d, want KindGauge", s.Kind)
	}
}

func TestHealthCollector_NonNumericSkipped(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/system/health/print": {
				replySentence(map[string]string{"name": "psu-state", "value": "ok"}),
			},
		},
	}
	samples, err := (&healthCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 0 {
		t.Errorf("expected 0 samples for non-numeric value, got %d", len(samples))
	}
}

func TestHealthCollector_EmptyNameValueSkipped(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/system/health/print": {
				replySentences(
					map[string]string{"name": "", "value": "42"},
					map[string]string{"name": "temp", "value": ""},
				),
			},
		},
	}
	samples, err := (&healthCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 0 {
		t.Errorf("expected 0 samples for empty name/value, got %d", len(samples))
	}
}

func TestHealthCollector_EmptyReply(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/system/health/print": {{Re: nil}},
		},
	}
	samples, err := (&healthCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if samples != nil {
		t.Errorf("expected nil samples for empty reply, got %v", samples)
	}
}

func TestHealthCollector_UnitInference(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/system/health/print": {
				replySentence(map[string]string{"name": "cpu-temperature", "value": "55"}),
			},
		},
	}
	samples, err := (&healthCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
	if samples[0].Unit != "Cel" {
		t.Errorf("unit = %q, want Cel (inferred from name 'cpu-temperature')", samples[0].Unit)
	}
}

func TestHealthCollector_RunnerError(t *testing.T) {
	runner := &fakeRunner{err: assertAnError}
	_, err := (&healthCollector{}).Collect(context.Background(), runner)
	if err != assertAnError {
		t.Errorf("expected assertAnError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// dhcpLeaseCollector
// ---------------------------------------------------------------------------

func TestDHCPLeaseCollector_Name(t *testing.T) {
	c := &dhcpLeaseCollector{}
	if got := c.Name(); got != "dhcp" {
		t.Errorf("Name() = %q, want dhcp", got)
	}
}

func TestDHCPLeaseCollector(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ip/dhcp-server/lease/print": {
				replySentences(
					map[string]string{"server": "dhcp1", "status": "bound"},
					map[string]string{"server": "dhcp1", "status": "bound"},
					map[string]string{"server": "dhcp1", "status": "waiting"},
					map[string]string{"server": "dhcp2", "status": "bound"},
				),
			},
		},
	}
	samples, err := (&dhcpLeaseCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}

	// 4 leases -> 3 groups (dhcp1/bound=2, dhcp1/waiting=1, dhcp2/bound=1) + total = 4
	if len(samples) != 4 {
		t.Fatalf("expected 4 samples, got %d", len(samples))
	}

	// Check total
	total := findSample(t, samples, "mikrotik.dhcp.leases.total")
	if total == nil {
		t.Fatal("missing mikrotik.dhcp.leases.total")
	}
	if total.Value != 4 {
		t.Errorf("total value = %d, want 4", total.Value)
	}
	if total.Kind != model.KindGauge {
		t.Errorf("total kind = %d, want KindGauge", total.Kind)
	}

	// Check group counts by (server, status)
	expected := map[string]int64{
		"dhcp1/bound":   2,
		"dhcp1/waiting": 1,
		"dhcp2/bound":   1,
	}
	for i := range samples {
		if samples[i].Name != "mikrotik.dhcp.leases" {
			continue
		}
		key := samples[i].Attributes["server"] + "/" + samples[i].Attributes["status"]
		want, ok := expected[key]
		if !ok {
			t.Errorf("unexpected group: %s = %d", key, samples[i].Value)
			continue
		}
		if samples[i].Value != want {
			t.Errorf("group %s value = %d, want %d", key, samples[i].Value, want)
		}
		if samples[i].Kind != model.KindGauge {
			t.Errorf("group %s kind = %d, want KindGauge", key, samples[i].Kind)
		}
		delete(expected, key)
	}
	for key := range expected {
		t.Errorf("missing expected group: %s", key)
	}
}

func TestDHCPLeaseCollector_MissingDefaults(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ip/dhcp-server/lease/print": {
				replySentences(
					map[string]string{"server": "", "status": ""},
					map[string]string{"server": "dhcp1", "status": ""},
					map[string]string{"server": "", "status": "bound"},
				),
			},
		},
	}
	samples, err := (&dhcpLeaseCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	// 3 groups (unknown/unknown, dhcp1/unknown, unknown/bound) + total = 4
	if len(samples) != 4 {
		t.Fatalf("expected 4 samples, got %d", len(samples))
	}
	total := findSample(t, samples, "mikrotik.dhcp.leases.total")
	if total == nil {
		t.Fatal("missing total")
	}
	if total.Value != 3 {
		t.Errorf("total value = %d, want 3", total.Value)
	}
}

func TestDHCPLeaseCollector_EmptyReply(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ip/dhcp-server/lease/print": {{Re: nil}},
		},
	}
	samples, err := (&dhcpLeaseCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	// Only the total sample should be present.
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample (total only), got %d", len(samples))
	}
	if samples[0].Name != "mikrotik.dhcp.leases.total" {
		t.Errorf("sample name = %q, want mikrotik.dhcp.leases.total", samples[0].Name)
	}
	if samples[0].Value != 0 {
		t.Errorf("total value = %d, want 0", samples[0].Value)
	}
	// No mikrotik.dhcp.leases samples
	for i := range samples {
		if samples[i].Name == "mikrotik.dhcp.leases" {
			t.Error("unexpected mikrotik.dhcp.leases sample present in empty reply")
		}
	}
}

func TestDHCPLeaseCollector_RunnerError(t *testing.T) {
	runner := &fakeRunner{err: assertAnError}
	_, err := (&dhcpLeaseCollector{}).Collect(context.Background(), runner)
	if err != assertAnError {
		t.Errorf("expected assertAnError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// connectionCollector
// ---------------------------------------------------------------------------

func TestConnectionCollector_Name(t *testing.T) {
	c := &connectionCollector{}
	if got := c.Name(); got != "connections" {
		t.Errorf("Name() = %q, want connections", got)
	}
}

func TestConnectionCollector(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ip/firewall/connection/tracking/print": {
				replySentence(map[string]string{
					"total-entries": "128",
					"max-entries":   "1048576",
				}),
			},
		},
	}
	samples, err := (&connectionCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(samples))
	}

	active := findSample(t, samples, "mikrotik.connections.active")
	if active == nil {
		t.Fatal("missing mikrotik.connections.active")
	}
	if active.Value != 128 {
		t.Errorf("active value = %d, want 128", active.Value)
	}
	if active.Kind != model.KindGauge {
		t.Errorf("active kind = %d, want KindGauge", active.Kind)
	}

	max := findSample(t, samples, "mikrotik.connections.max")
	if max == nil {
		t.Fatal("missing mikrotik.connections.max")
	}
	if max.Value != 1048576 {
		t.Errorf("max value = %d, want 1048576", max.Value)
	}
	if max.Kind != model.KindGauge {
		t.Errorf("max kind = %d, want KindGauge", max.Kind)
	}
}

func TestConnectionCollector_MissingMax(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ip/firewall/connection/tracking/print": {
				replySentence(map[string]string{"total-entries": "50"}),
			},
		},
	}
	samples, err := (&connectionCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
	if samples[0].Name != "mikrotik.connections.active" {
		t.Errorf("name = %q, want mikrotik.connections.active", samples[0].Name)
	}
}

func TestConnectionCollector_EmptyReply(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ip/firewall/connection/tracking/print": {{Re: nil}},
		},
	}
	samples, err := (&connectionCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if samples != nil {
		t.Errorf("expected nil samples for empty reply, got %v", samples)
	}
}

func TestConnectionCollector_RunnerError(t *testing.T) {
	runner := &fakeRunner{err: assertAnError}
	_, err := (&connectionCollector{}).Collect(context.Background(), runner)
	if err != assertAnError {
		t.Errorf("expected assertAnError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// countsCollector
// ---------------------------------------------------------------------------

func TestCountsCollector_Name(t *testing.T) {
	c := &countsCollector{}
	if got := c.Name(); got != "counts" {
		t.Errorf("Name() = %q, want counts", got)
	}
}

func TestCountsCollector(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ip/address/print": {
				{Done: &proto.Sentence{Map: map[string]string{"ret": "3"}}},
			},
			"/ip/route/print": {
				{Done: &proto.Sentence{Map: map[string]string{"ret": "10"}}},
			},
		},
	}
	samples, err := (&countsCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(samples))
	}

	addrs := findSample(t, samples, "mikrotik.ip.addresses")
	if addrs == nil {
		t.Fatal("missing mikrotik.ip.addresses")
	}
	if addrs.Value != 3 {
		t.Errorf("addresses value = %d, want 3", addrs.Value)
	}
	if addrs.Kind != model.KindGauge {
		t.Errorf("addresses kind = %d, want KindGauge", addrs.Kind)
	}

	routes := findSample(t, samples, "mikrotik.ip.routes")
	if routes == nil {
		t.Fatal("missing mikrotik.ip.routes")
	}
	if routes.Value != 10 {
		t.Errorf("routes value = %d, want 10", routes.Value)
	}
	if routes.Kind != model.KindGauge {
		t.Errorf("routes kind = %d, want KindGauge", routes.Kind)
	}
}

func TestCountsCollector_MissingDone(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ip/address/print": {
				{Done: &proto.Sentence{Map: map[string]string{"ret": "3"}}},
			},
			"/ip/route/print": {
				{}, // no Done
			},
		},
	}
	samples, err := (&countsCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample (routes missing), got %d", len(samples))
	}
	if samples[0].Name != "mikrotik.ip.addresses" {
		t.Errorf("name = %q, want mikrotik.ip.addresses", samples[0].Name)
	}
}

func TestCountsCollector_RunnerError(t *testing.T) {
	runner := &fakeRunner{err: assertAnError}
	_, err := (&countsCollector{}).Collect(context.Background(), runner)
	if err != assertAnError {
		t.Errorf("expected assertAnError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// firewallCollector
// ---------------------------------------------------------------------------

func TestFirewallCollector_Name(t *testing.T) {
	c := &firewallCollector{}
	if got := c.Name(); got != "firewall" {
		t.Errorf("Name() = %q, want firewall", got)
	}
}

func TestFirewallCollector(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ip/firewall/filter/print": {
				replySentences(
					map[string]string{
						"chain":   "input",
						"action":  "accept",
						"bytes":   "1000",
						"packets": "10",
						"comment": "allow-est",
					},
					map[string]string{
						"chain":   "forward",
						"action":  "drop",
						"bytes":   "50",
						"packets": "2",
					},
				),
			},
		},
	}
	samples, err := (&firewallCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 4 {
		t.Fatalf("expected 4 samples, got %d", len(samples))
	}

	// Rule 1: chain=input, action=accept, comment=allow-est
	// bytes
	for i := range samples {
		if samples[i].Name == "mikrotik.firewall.filter.bytes" && samples[i].Attributes["chain"] == "input" {
			if samples[i].Value != 1000 {
				t.Errorf("input bytes value = %d, want 1000", samples[i].Value)
			}
			if samples[i].Attributes["comment"] != "allow-est" {
				t.Errorf("input bytes comment = %q, want allow-est", samples[i].Attributes["comment"])
			}
			if samples[i].Kind != model.KindCounter {
				t.Errorf("input bytes kind = %d, want KindCounter", samples[i].Kind)
			}
			if samples[i].Unit != "By" {
				t.Errorf("input bytes unit = %q, want By", samples[i].Unit)
			}
			goto inputPackets
		}
	}
	t.Error("missing input bytes sample")
inputPackets:
	for i := range samples {
		if samples[i].Name == "mikrotik.firewall.filter.packets" && samples[i].Attributes["chain"] == "input" {
			if samples[i].Value != 10 {
				t.Errorf("input packets value = %d, want 10", samples[i].Value)
			}
			if samples[i].Attributes["comment"] != "allow-est" {
				t.Errorf("input packets comment = %q, want allow-est", samples[i].Attributes["comment"])
			}
			if samples[i].Kind != model.KindCounter {
				t.Errorf("input packets kind = %d, want KindCounter", samples[i].Kind)
			}
			if samples[i].Unit != "1" {
				t.Errorf("input packets unit = %q, want 1", samples[i].Unit)
			}
			goto forwardBytes
		}
	}
	t.Error("missing input packets sample")
forwardBytes:
	// Rule 2: chain=forward, action=drop, no comment
	for i := range samples {
		if samples[i].Name == "mikrotik.firewall.filter.bytes" && samples[i].Attributes["chain"] == "forward" {
			if samples[i].Value != 50 {
				t.Errorf("forward bytes value = %d, want 50", samples[i].Value)
			}
			if _, hasComment := samples[i].Attributes["comment"]; hasComment {
				t.Error("forward bytes should not have comment attribute")
			}
			if samples[i].Attributes["chain"] != "forward" {
				t.Errorf("forward bytes chain = %q, want forward", samples[i].Attributes["chain"])
			}
			if samples[i].Attributes["action"] != "drop" {
				t.Errorf("forward bytes action = %q, want drop", samples[i].Attributes["action"])
			}
			goto forwardPackets
		}
	}
	t.Error("missing forward bytes sample")
forwardPackets:
	for i := range samples {
		if samples[i].Name == "mikrotik.firewall.filter.packets" && samples[i].Attributes["chain"] == "forward" {
			if samples[i].Value != 2 {
				t.Errorf("forward packets value = %d, want 2", samples[i].Value)
			}
			if _, hasComment := samples[i].Attributes["comment"]; hasComment {
				t.Error("forward packets should not have comment attribute")
			}
			goto done
		}
	}
	t.Error("missing forward packets sample")
done:
}

func TestFirewallCollector_MissingBytes(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ip/firewall/filter/print": {
				replySentence(map[string]string{
					"chain":   "input",
					"action":  "accept",
					"packets": "5",
				}),
			},
		},
	}
	samples, err := (&firewallCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
	if samples[0].Name != "mikrotik.firewall.filter.packets" {
		t.Errorf("name = %q, want mikrotik.firewall.filter.packets", samples[0].Name)
	}
}

func TestFirewallCollector_EmptyReply(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/ip/firewall/filter/print": {{Re: nil}},
		},
	}
	samples, err := (&firewallCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if samples != nil {
		t.Errorf("expected nil samples for empty reply, got %v", samples)
	}
}

func TestFirewallCollector_RunnerError(t *testing.T) {
	runner := &fakeRunner{err: assertAnError}
	_, err := (&firewallCollector{}).Collect(context.Background(), runner)
	if err != assertAnError {
		t.Errorf("expected assertAnError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// wirelessCollector
// ---------------------------------------------------------------------------

func TestWirelessCollector_Name(t *testing.T) {
	c := &wirelessCollector{}
	if got := c.Name(); got != "wireless" {
		t.Errorf("Name() = %q, want wireless", got)
	}
}

func TestWirelessCollector_LegacyPath(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/interface/wireless/registration-table/print": {
				replySentences(
					map[string]string{"interface": "wlan1"},
					map[string]string{"interface": "wlan1"},
					map[string]string{"interface": "wlan2"},
				),
			},
		},
	}
	samples, err := (&wirelessCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(samples))
	}

	for i := range samples {
		if samples[i].Name != "mikrotik.wireless.clients" {
			t.Errorf("sample name = %q, want mikrotik.wireless.clients", samples[i].Name)
		}
		if samples[i].Kind != model.KindGauge {
			t.Errorf("sample kind = %d, want KindGauge", samples[i].Kind)
		}
		switch samples[i].Attributes["interface"] {
		case "wlan1":
			if samples[i].Value != 2 {
				t.Errorf("wlan1 count = %d, want 2", samples[i].Value)
			}
		case "wlan2":
			if samples[i].Value != 1 {
				t.Errorf("wlan2 count = %d, want 1", samples[i].Value)
			}
		default:
			t.Errorf("unexpected interface = %q", samples[i].Attributes["interface"])
		}
	}
}

func TestWirelessCollector_FallbackToWifi(t *testing.T) {
	runner := &scriptedErrRunner{
		errs: map[string]error{
			"/interface/wireless/registration-table/print": errors.New("no such command or directory (wireless)"),
		},
		replies: map[string][]*routeros.Reply{
			"/interface/wifi/registration-table/print": {
				replySentences(
					map[string]string{"interface": "wifi1"},
					map[string]string{"interface": "wifi1"},
				),
			},
		},
	}
	samples, err := (&wirelessCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
	if samples[0].Attributes["interface"] != "wifi1" {
		t.Errorf("interface = %q, want wifi1", samples[0].Attributes["interface"])
	}
	if samples[0].Value != 2 {
		t.Errorf("wifi1 count = %d, want 2", samples[0].Value)
	}
}

func TestWirelessCollector_BothUnavailable(t *testing.T) {
	runner := &scriptedErrRunner{
		errs: map[string]error{
			"/interface/wireless/registration-table/print": errors.New("no such command or directory"),
			"/interface/wifi/registration-table/print":     errors.New("no such command or directory"),
		},
	}
	samples, err := (&wirelessCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if samples != nil {
		t.Errorf("expected nil samples when both commands unavailable, got %v", samples)
	}
}

func TestWirelessCollector_NonUnavailableError(t *testing.T) {
	runner := &errCmdRunner{msg: "timeout"}
	_, err := (&wirelessCollector{}).Collect(context.Background(), runner)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "timeout" {
		t.Errorf("error = %q, want timeout", err.Error())
	}
}

func TestWirelessCollector_NonUnavailableErrorFirstPath(t *testing.T) {
	// The wireless path returns a non-unavailable error; should be propagated
	// without trying the wifi path.
	runner := &scriptedErrRunner{
		errs: map[string]error{
			"/interface/wireless/registration-table/print": errors.New("timeout"),
		},
		replies: map[string][]*routeros.Reply{
			"/interface/wifi/registration-table/print": {
				replySentences(map[string]string{"interface": "wifi1"}),
			},
		},
	}
	_, err := (&wirelessCollector{}).Collect(context.Background(), runner)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "timeout" {
		t.Errorf("error = %q, want timeout", err.Error())
	}
}

func TestWirelessCollector_EmptyInterface(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/interface/wireless/registration-table/print": {
				replySentences(
					map[string]string{"interface": ""},
					map[string]string{"interface": "wlan1"},
				),
			},
		},
	}
	samples, err := (&wirelessCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(samples))
	}
	for i := range samples {
		switch samples[i].Attributes["interface"] {
		case "unknown":
			if samples[i].Value != 1 {
				t.Errorf("unknown interface count = %d, want 1", samples[i].Value)
			}
		case "wlan1":
			if samples[i].Value != 1 {
				t.Errorf("wlan1 count = %d, want 1", samples[i].Value)
			}
		default:
			t.Errorf("unexpected interface = %q", samples[i].Attributes["interface"])
		}
	}
}

func TestWirelessCollector_RunnerError(t *testing.T) {
	runner := &fakeRunner{err: assertAnError}
	_, err := (&wirelessCollector{}).Collect(context.Background(), runner)
	if err != assertAnError {
		t.Errorf("expected assertAnError, got %v", err)
	}
}
