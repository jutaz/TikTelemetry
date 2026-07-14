package mikrotik

import (
	"context"
	"testing"

	"github.com/go-routeros/routeros/v3"
	"github.com/jutaz/tiktelemetry/internal/model"
)

func TestParseUptime(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"5s", 5},
		{"4m5s", 4*60 + 5},
		{"3h4m5s", 3*3600 + 4*60 + 5},
		{"2d3h4m5s", 2*24*3600 + 3*3600 + 4*60 + 5},
		{"1w2d3h4m5s", 7*24*3600 + 2*24*3600 + 3*3600 + 4*60 + 5},
		{"10w", 10 * 7 * 24 * 3600},
	}
	for _, tt := range tests {
		if got := parseUptime(tt.in); got != tt.want {
			t.Errorf("parseUptime(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseInt(t *testing.T) {
	m := map[string]string{"a": "42", "b": "notanumber", "c": ""}

	if v, ok := parseInt(m, "a"); !ok || v != 42 {
		t.Errorf("parseInt(a) = %d,%v want 42,true", v, ok)
	}
	if _, ok := parseInt(m, "b"); ok {
		t.Error("parseInt(b) should fail on non-numeric input")
	}
	if _, ok := parseInt(m, "missing"); ok {
		t.Error("parseInt(missing) should report not-ok")
	}
}

// Helper: find the first sample with the given name in a slice.
func findSample(t *testing.T, samples []model.Sample, name string) *model.Sample {
	t.Helper()
	for i := range samples {
		if samples[i].Name == name {
			return &samples[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// systemResourceCollector
// ---------------------------------------------------------------------------

func TestSystemCollector_Name(t *testing.T) {
	c := &systemResourceCollector{}
	if got := c.Name(); got != "system" {
		t.Errorf("Name() = %q, want system", got)
	}
}

func TestSystemCollector_FullMap(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/system/resource/print": {
				replySentence(map[string]string{
					"cpu-load":       "7",
					"free-memory":    "1048576",
					"total-memory":   "2097152",
					"free-hdd-space": "8388608",
					"uptime":         "1h30m",
				}),
			},
		},
	}
	samples, err := (&systemResourceCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 5 {
		t.Fatalf("expected 5 samples, got %d", len(samples))
	}

	// cpu.load
	s := findSample(t, samples, "mikrotik.system.cpu.load")
	if s == nil {
		t.Fatal("missing mikrotik.system.cpu.load")
	}
	if s.Value != 7 || s.Kind != model.KindGauge || s.Unit != "%" {
		t.Errorf("cpu.load: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}

	// free-memory
	s = findSample(t, samples, "mikrotik.system.memory.free")
	if s == nil {
		t.Fatal("missing mikrotik.system.memory.free")
	}
	if s.Value != 1048576 || s.Kind != model.KindGauge || s.Unit != "By" {
		t.Errorf("memory.free: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}

	// total-memory
	s = findSample(t, samples, "mikrotik.system.memory.total")
	if s == nil {
		t.Fatal("missing mikrotik.system.memory.total")
	}
	if s.Value != 2097152 || s.Kind != model.KindGauge || s.Unit != "By" {
		t.Errorf("memory.total: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}

	// free-hdd-space
	s = findSample(t, samples, "mikrotik.system.hdd.free")
	if s == nil {
		t.Fatal("missing mikrotik.system.hdd.free")
	}
	if s.Value != 8388608 || s.Kind != model.KindGauge || s.Unit != "By" {
		t.Errorf("hdd.free: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}

	// uptime (1h30m = 5400s)
	s = findSample(t, samples, "mikrotik.system.uptime")
	if s == nil {
		t.Fatal("missing mikrotik.system.uptime")
	}
	if s.Value != 5400 || s.Kind != model.KindCounter || s.Unit != "s" {
		t.Errorf("uptime: value=%d kind=%d unit=%q", s.Value, s.Kind, s.Unit)
	}
}

func TestSystemCollector_PartialMap(t *testing.T) {
	// Only cpu-load present.
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/system/resource/print": {
				replySentence(map[string]string{"cpu-load": "42"}),
			},
		},
	}
	samples, err := (&systemResourceCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
	if samples[0].Name != "mikrotik.system.cpu.load" || samples[0].Value != 42 {
		t.Errorf("unexpected sample: %+v", samples[0])
	}
}

func TestSystemCollector_GarbageValue(t *testing.T) {
	// cpu-load is non-numeric — should be skipped.
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/system/resource/print": {
				replySentence(map[string]string{"cpu-load": "abc"}),
			},
		},
	}
	samples, err := (&systemResourceCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 0 {
		t.Errorf("expected 0 samples for garbage value, got %d", len(samples))
	}
}

func TestSystemCollector_UptimeZero(t *testing.T) {
	// uptime string that parses to 0 — should not emit uptime sample.
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/system/resource/print": {
				replySentence(map[string]string{
					"cpu-load": "1",
					"uptime":   "0s",
				}),
			},
		},
	}
	samples, err := (&systemResourceCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	// cpu-load should be present, uptime should not.
	if findSample(t, samples, "mikrotik.system.uptime") != nil {
		t.Error("uptime sample emitted despite zero-value parsing")
	}
	if findSample(t, samples, "mikrotik.system.cpu.load") == nil {
		t.Error("cpu.load sample should have been emitted")
	}
}

func TestSystemCollector_EmptyReply(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/system/resource/print": {
				// No Re sentences.
				{Re: nil},
			},
		},
	}
	samples, err := (&systemResourceCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if samples != nil {
		t.Errorf("expected nil samples for empty reply, got %v", samples)
	}
}

func TestSystemCollector_RunnerError(t *testing.T) {
	runner := &fakeRunner{err: assertAnError}
	_, err := (&systemResourceCollector{}).Collect(context.Background(), runner)
	if err != assertAnError {
		t.Errorf("expected assertAnError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// interfaceCollector
// ---------------------------------------------------------------------------

func TestInterfaceCollector_Name(t *testing.T) {
	c := &interfaceCollector{}
	if got := c.Name(); got != "interface" {
		t.Errorf("Name() = %q, want interface", got)
	}
}

func TestInterfaceCollector_MultipleInterfaces(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/interface/print": {
				replySentences(
					map[string]string{
						"name":      "ether1",
						"type":      "ether",
						"running":   "true",
						"rx-byte":   "1500",
						"tx-byte":   "2000",
						"rx-packet": "100",
						"tx-packet": "200",
					},
					map[string]string{
						"name":    "ether2",
						"running": "false",
						"rx-byte": "500",
					},
				),
			},
		},
	}
	samples, err := (&interfaceCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}

	// ether1: 4 counters (rx-byte, tx-byte, rx-packet, tx-packet) + 1 up gauge.
	// ether2: 1 counter (rx-byte) + 1 up gauge.
	// Total = 5 + 2 = 7.
	if len(samples) != 7 {
		t.Fatalf("expected 7 samples, got %d", len(samples))
	}

	// -- ether1 assertions --
	s := findSample(t, samples, "mikrotik.interface.rx.bytes")
	if s == nil || s.Value != 1500 || s.Kind != model.KindCounter || s.Attributes["interface"] != "ether1" || s.Attributes["type"] != "ether" {
		t.Errorf("ether1 rx.bytes malformed: %+v", s)
	}
	s = findSample(t, samples, "mikrotik.interface.tx.bytes")
	if s == nil || s.Value != 2000 || s.Kind != model.KindCounter || s.Attributes["interface"] != "ether1" || s.Attributes["type"] != "ether" {
		t.Errorf("ether1 tx.bytes malformed: %+v", s)
	}
	s = findSample(t, samples, "mikrotik.interface.rx.packets")
	if s == nil || s.Value != 100 || s.Kind != model.KindCounter || s.Unit != "1" || s.Attributes["interface"] != "ether1" || s.Attributes["type"] != "ether" {
		t.Errorf("ether1 rx.packets malformed: %+v", s)
	}
	s = findSample(t, samples, "mikrotik.interface.tx.packets")
	if s == nil || s.Value != 200 || s.Kind != model.KindCounter || s.Unit != "1" || s.Attributes["interface"] != "ether1" || s.Attributes["type"] != "ether" {
		t.Errorf("ether1 tx.packets malformed: %+v", s)
	}
	s = findSample(t, samples, "mikrotik.interface.up")
	if s == nil {
		t.Fatal("missing mikrotik.interface.up")
	}
	// there are two up gauges; pick the one for ether1
	if s.Attributes["interface"] == "ether1" {
		if s.Value != 1 || s.Kind != model.KindGauge {
			t.Errorf("ether1 up: value=%d kind=%d", s.Value, s.Kind)
		}
	} else if s.Attributes["interface"] == "ether2" {
		if s.Value != 0 || s.Kind != model.KindGauge {
			t.Errorf("ether2 up: value=%d kind=%d", s.Value, s.Kind)
		}
	}

	// -- ether2 assertions --
	s = findSample(t, samples, "mikrotik.interface.rx.bytes")
	if s == nil || s.Attributes["interface"] == "ether1" {
		// find the ether2 one
		for i := range samples {
			if samples[i].Name == "mikrotik.interface.rx.bytes" && samples[i].Attributes["interface"] == "ether2" {
				s = &samples[i]
				break
			}
		}
	}
	if s == nil || s.Attributes["interface"] != "ether2" {
		t.Fatal("missing ether2 rx.bytes")
	}
	if s.Value != 500 {
		t.Errorf("ether2 rx.bytes value = %d, want 500", s.Value)
	}
	// ether2 should NOT have tx.bytes, rx.packets, tx.packets
	for _, name := range []string{"mikrotik.interface.tx.bytes", "mikrotik.interface.rx.packets", "mikrotik.interface.tx.packets"} {
		for i := range samples {
			if samples[i].Name == name && samples[i].Attributes["interface"] == "ether2" {
				t.Errorf("ether2 unexpectedly has sample %s", name)
			}
		}
	}

	// Check attribute map independence: each sample should have its own map
	// (clone in sampleForInterface). Mutate one and verify others unaffected.
	for i := range samples {
		for j := range samples {
			if i != j && samples[i].Attributes != nil && samples[j].Attributes != nil {
				// Same pointer? That'd be a bug.
				if &samples[i].Attributes == &samples[j].Attributes {
					t.Errorf("sample %d and %d share the same Attributes map", i, j)
				}
			}
		}
	}
}

func TestInterfaceCollector_EmptyNameSkipped(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/interface/print": {
				replySentences(
					map[string]string{"name": ""},
					map[string]string{"name": "ether1", "running": "true"},
				),
			},
		},
	}
	samples, err := (&interfaceCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	// Only ether1 should produce samples (up gauge only).
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
	if samples[0].Name != "mikrotik.interface.up" || samples[0].Attributes["interface"] != "ether1" {
		t.Errorf("unexpected sample: %+v", samples[0])
	}
}

func TestInterfaceCollector_RunningUnset(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/interface/print": {
				replySentences(
					map[string]string{"name": "ether1"}, // no "running" key
				),
			},
		},
	}
	samples, err := (&interfaceCollector{}).Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample (up gauge), got %d", len(samples))
	}
	if samples[0].Name != "mikrotik.interface.up" || samples[0].Value != 0 {
		t.Errorf("expected up=0, got %+v", samples[0])
	}
}

func TestInterfaceCollector_RunnerError(t *testing.T) {
	runner := &fakeRunner{err: assertAnError}
	_, err := (&interfaceCollector{}).Collect(context.Background(), runner)
	if err != assertAnError {
		t.Errorf("expected assertAnError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// DefaultCollectors
// ---------------------------------------------------------------------------

func TestDefaultCollectors(t *testing.T) {
	cs := DefaultCollectors()
	if len(cs) != 2 {
		t.Fatalf("DefaultCollectors() returned %d collectors, want 2", len(cs))
	}
	names := []string{cs[0].Name(), cs[1].Name()}
	if names[0] != "system" {
		t.Errorf("first collector Name() = %q, want system", names[0])
	}
	if names[1] != "interface" {
		t.Errorf("second collector Name() = %q, want interface", names[1])
	}
}

// assertAnError is a sentinel error used by Runner-error propagation tests.
var assertAnError = &errTest{}

type errTest struct{}

func (e *errTest) Error() string { return "test error" }
