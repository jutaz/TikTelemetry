package model

import "testing"

func TestLogEntrySeverity(t *testing.T) {
	tests := []struct {
		name   string
		topics []string
		want   Level
	}{
		{"plain info", []string{"system", "info"}, LevelInfo},
		{"empty defaults info", nil, LevelInfo},
		{"warning", []string{"system", "warning"}, LevelWarn},
		{"error beats warning", []string{"warning", "error"}, LevelError},
		{"critical is fatal", []string{"error", "critical"}, LevelFatal},
		{"debug only", []string{"debug"}, LevelDebug},
		{"debug plus info stays info", []string{"debug", "info"}, LevelInfo},
	}
	for _, tt := range tests {
		e := LogEntry{Topics: tt.topics}
		if got := e.Severity(); got != tt.want {
			t.Errorf("%s: Severity() = %d, want %d", tt.name, got, tt.want)
		}
	}
}
