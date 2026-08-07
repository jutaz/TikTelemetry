// Package exporthelp holds small helpers shared by the concrete export
// adapters: HTTP basic-auth header construction, response handling, and
// label-name sanitisation.
package exporthelp

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxErrorBodyBytes bounds how much of a failed response body is quoted back
// in the returned error, so a misconfigured endpoint returning an HTML page
// cannot flood the logs.
const maxErrorBodyBytes = 1024

// maxDrainBytes bounds the discard-read that enables connection reuse.
// Remote_write and Loki both answer with an empty body, so the realistic drain
// is zero bytes; the cap only exists to stop an endpoint that streams something
// unexpected. It is deliberately small — past this point the connection is not
// worth recovering, and reading on costs push latency (see CheckResponse).
const maxDrainBytes = 4 << 10

// CheckResponse drains resp.Body and returns a non-nil error when the status is
// outside the 2xx range. what names the operation and is used as the error
// prefix (e.g. "prometheus: remote_write"). resp must be a response from
// http.Client.Do, which guarantees a non-nil Body.
//
// The caller still owns the body and must Close it — deferring the Close right
// after the round-trip, as both sinks do, runs it after this function has
// drained. Draining first is what makes connection reuse work: net/http only
// returns a connection to the idle pool once its body has been read to EOF and
// closed. The agent pushes on a fixed interval for its entire lifetime, so
// skipping the drain means every push pays for a fresh TCP + TLS handshake —
// costly on the small MikroTik boards this runs on.
//
// The drain is bounded twice over: by maxDrainBytes, and by the sink's
// http.Client.Timeout, which covers body reads as well as the round-trip. A
// backend that trickles bytes therefore delays one push by at most that timeout
// rather than stalling the agent. When Content-Length already says the body is
// larger than the cap, the drain is skipped outright — the connection could not
// be returned to the pool anyway, so reading further would buy nothing.
func CheckResponse(what string, resp *http.Response) error {
	// Drain whatever the status-specific handling below leaves behind.
	defer func() {
		if resp.ContentLength > maxDrainBytes {
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
	}()

	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	return fmt.Errorf("%s returned %d: %s", what, resp.StatusCode, strings.TrimSpace(string(body)))
}

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
