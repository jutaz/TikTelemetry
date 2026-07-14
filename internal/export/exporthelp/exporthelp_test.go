package exporthelp

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestBasicAuth(t *testing.T) {
	t.Run("user_and_pass", func(t *testing.T) {
		got := BasicAuth("12345", "token")
		if !strings.HasPrefix(got, "Basic ") {
			t.Fatalf("expected 'Basic ' prefix, got %q", got)
		}
		decoded, err := base64.StdEncoding.DecodeString(got[len("Basic "):])
		if err != nil {
			t.Fatalf("base64 decode error: %v", err)
		}
		if string(decoded) != "12345:token" {
			t.Errorf("decoded = %q, want %q", string(decoded), "12345:token")
		}
	})

	t.Run("both_empty", func(t *testing.T) {
		if got := BasicAuth("", ""); got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("user_only", func(t *testing.T) {
		got := BasicAuth("u", "")
		if got == "" {
			t.Fatal("expected non-empty result")
		}
		if !strings.HasPrefix(got, "Basic ") {
			t.Fatalf("expected 'Basic ' prefix, got %q", got)
		}
		decoded, _ := base64.StdEncoding.DecodeString(got[len("Basic "):])
		if string(decoded) != "u:" {
			t.Errorf("decoded = %q, want %q", string(decoded), "u:")
		}
	})

	t.Run("pass_only", func(t *testing.T) {
		got := BasicAuth("", "p")
		if got == "" {
			t.Fatal("expected non-empty result")
		}
		if !strings.HasPrefix(got, "Basic ") {
			t.Fatalf("expected 'Basic ' prefix, got %q", got)
		}
		decoded, _ := base64.StdEncoding.DecodeString(got[len("Basic "):])
		if string(decoded) != ":p" {
			t.Errorf("decoded = %q, want %q", string(decoded), ":p")
		}
	})
}

func TestSanitizeLabelName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"valid_name", "valid_name"},
		{"has.dots", "has_dots"},
		{"has-dash", "has_dash"},
		{"9leading", "_9leading"},
		{"", "_"},
		{"a b", "a_b"},
		{"mixed.9-x", "mixed_9_x"},
		{"UPPER", "UPPER"},
		{"_underscore", "_underscore"},
		{"123", "_123"},
		{"a!b@c#", "a_b_c_"},
		{"___", "___"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := SanitizeLabelName(tc.input)
			if got != tc.want {
				t.Errorf("SanitizeLabelName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSanitizeMetricName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"mikrotik.interface.rx.bytes", "mikrotik_interface_rx_bytes"},
		{"mikrotik.system.cpu.load", "mikrotik_system_cpu_load"},
		{"plain", "plain"},
		{"", "_"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := SanitizeMetricName(tc.input)
			if got != tc.want {
				t.Errorf("SanitizeMetricName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
