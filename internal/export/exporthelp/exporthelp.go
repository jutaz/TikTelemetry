// Package exporthelp holds small helpers shared by the concrete export
// adapters: HTTP basic-auth header construction and label-name sanitisation.
package exporthelp

import (
	"encoding/base64"
	"strings"
)

// BasicAuth returns the value for an Authorization header from a user and
// password/token pair, or "" if both are empty.
func BasicAuth(user, pass string) string {
	if user == "" && pass == "" {
		return ""
	}
	enc := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	return "Basic " + enc
}

// SanitizeLabelName converts an arbitrary string into a valid Prometheus/Loki
// label name: it must match [a-zA-Z_][a-zA-Z0-9_]*. Invalid characters become
// underscores; a leading digit is prefixed with an underscore. An empty input
// yields "_".
func SanitizeLabelName(name string) string {
	if name == "" {
		return "_"
	}
	var b strings.Builder
	b.Grow(len(name))
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if i == 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// SanitizeMetricName converts a dotted OpenTelemetry-style metric name (e.g.
// "mikrotik.interface.rx.bytes") into a Prometheus metric name by replacing
// dots and other invalid characters with underscores
// ("mikrotik_interface_rx_bytes"). Prometheus metric names allow the same
// character set as label names plus ':', but ':' is reserved for recording
// rules, so we exclude it.
func SanitizeMetricName(name string) string {
	return SanitizeLabelName(name)
}
