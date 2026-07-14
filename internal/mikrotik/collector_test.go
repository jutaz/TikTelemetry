package mikrotik

import "testing"

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
