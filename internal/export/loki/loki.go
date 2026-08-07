// Package loki implements the export.Sink interface for Grafana Loki, pushing
// log entries via the JSON push API. It uses only the standard library.
package loki

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/export"
	"github.com/jutaz/tiktelemetry/internal/export/exporthelp"
	"github.com/jutaz/tiktelemetry/internal/model"
)

func init() {
	export.Register("loki", New)
}

// Compile-time interface check.
var _ export.Sink = (*lokiSink)(nil)

// levelNames maps model.Level to the string used in the Loki stream label.
var levelNames = map[model.Level]string{
	model.LevelDebug: "debug",
	model.LevelInfo:  "info",
	model.LevelWarn:  "warn",
	model.LevelError: "error",
	model.LevelFatal: "fatal",
}

// lokiSink pushes log entries to a Grafana Loki instance via the JSON push API.
// It is stateless: each ConsumeLogs call builds and sends a fresh request.
type lokiSink struct {
	endpoint    string
	headers     map[string]string
	client      *http.Client
	logger      *slog.Logger
	serviceName string
}

// Name returns the stable identifier "loki".
func (s *lokiSink) Name() string { return "loki" }

// Capabilities reports that this sink accepts logs but not metrics.
func (s *lokiSink) Capabilities() export.Capabilities {
	return export.Capabilities{Metrics: false, Logs: true}
}

// ConsumeMetrics is a no-op — this sink only handles logs.
func (s *lokiSink) ConsumeMetrics(_ context.Context, _ []model.Sample) error {
	return nil
}

// Shutdown is a no-op — the sink has no background goroutines or buffers.
func (s *lokiSink) Shutdown(_ context.Context) error {
	return nil
}

// ConsumeLogs encodes log entries as a Loki JSON push request and sends it.
func (s *lokiSink) ConsumeLogs(ctx context.Context, entries []model.LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	body, err := buildPayload(entries, s.serviceName)
	if err != nil {
		return fmt.Errorf("loki: build payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("loki: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "tiktelemetry/"+s.serviceName)

	for k, v := range s.headers {
		req.Header.Set(k, v)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("loki: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return exporthelp.CheckResponse("loki: push", resp)
}

// --- Payload construction (pure, testable) ---------------------------------

// pushRequest is the top-level Loki JSON push body.
type pushRequest struct {
	Streams []stream `json:"streams"`
}

// stream is one label-set within the push request.
type stream struct {
	Stream map[string]string `json:"stream"`
	Values [][]any           `json:"values"`
}

// streamKey groups log entries into one Loki stream. Both dimensions are
// low-cardinality: one router × five severity levels.
type streamKey struct {
	target string
	level  string
}

// buildPayload assembles the JSON body for a Loki push request from log
// entries. It groups entries by (target, severity level) so each router's logs
// carry a distinct "target" stream label, and sorts each group's entries by
// timestamp ascending to avoid "too far behind" rejections.
func buildPayload(entries []model.LogEntry, serviceName string) ([]byte, error) {
	groups := make(map[streamKey][]model.LogEntry)
	for _, e := range entries {
		key := streamKey{target: e.Target, level: levelNames[e.Severity()]}
		groups[key] = append(groups[key], e)
	}

	// Deterministic stream ordering.
	keys := make([]streamKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].target != keys[j].target {
			return keys[i].target < keys[j].target
		}
		return keys[i].level < keys[j].level
	})

	streams := make([]stream, 0, len(keys))
	for _, key := range keys {
		grp := groups[key]

		// Sort by timestamp ascending within the stream.
		sort.Slice(grp, func(i, j int) bool {
			return grp[i].Time.Before(grp[j].Time)
		})

		labels := map[string]string{
			"service": serviceName,
			"source":  "mikrotik",
			"level":   key.level,
		}
		// Only add the target label when set (it always is in normal operation).
		if key.target != "" {
			labels["target"] = key.target
		}
		// Sanitize label names (the static names above are already valid, but
		// being explicit keeps this consistent and future-proof).
		sanitized := make(map[string]string, len(labels))
		for k, v := range labels {
			sanitized[exporthelp.SanitizeLabelName(k)] = v
		}

		values := make([][]any, 0, len(grp))
		for _, e := range grp {
			ts := strconv.FormatInt(e.Time.UnixNano(), 10)
			val := []any{ts, e.Message}

			// Structured metadata (optional 3rd element).
			if len(e.Topics) > 0 || e.ID != "" {
				meta := make(map[string]string)
				if len(e.Topics) > 0 {
					meta["topics"] = strings.Join(e.Topics, ",")
				}
				if e.ID != "" {
					meta["id"] = e.ID
				}
				val = append(val, meta)
			}

			values = append(values, val)
		}

		streams = append(streams, stream{
			Stream: sanitized,
			Values: values,
		})
	}

	return json.Marshal(pushRequest{Streams: streams})
}

// --- Factory -----------------------------------------------------------------

// New is the export.Factory for the "loki" sink.
//
// Required env vars:
//
//	LOKI_ENDPOINT  — base URL (e.g. https://logs-prod-012.grafana.net)
//
// Optional env vars:
//
//	LOKI_USER                — basic auth username (Grafana Cloud: Logs instance ID)
//	LOKI_PASS                — basic auth password / access policy token
//	LOKI_HEADERS             — comma-separated "Key=Value" pairs for extra HTTP headers
//	LOKI_INSECURE_SKIP_VERIFY — set "true" to skip TLS verification (dev/testing)
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (export.Sink, error) {
	endpoint := cfg.Env("LOKI_ENDPOINT")
	if endpoint == "" {
		return nil, fmt.Errorf("loki: LOKI_ENDPOINT is required")
	}

	// If the user already supplied the full push path, use it as-is.
	if !strings.HasSuffix(endpoint, "/loki/api/v1/push") {
		endpoint = strings.TrimRight(endpoint, "/") + "/loki/api/v1/push"
	}

	user := cfg.Env("LOKI_USER")
	pass := cfg.Env("LOKI_PASS")

	headers := make(map[string]string)
	if auth := exporthelp.BasicAuth(user, pass); auth != "" {
		headers["Authorization"] = auth
	}

	extraHeaders, err := cfg.EnvHeaders("LOKI_HEADERS")
	if err != nil {
		return nil, fmt.Errorf("loki: parsing LOKI_HEADERS: %w", err)
	}
	for k, v := range extraHeaders {
		headers[k] = v
	}

	var client *http.Client
	if cfg.EnvBool("LOKI_INSECURE_SKIP_VERIFY", false) {
		client = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	} else {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	return &lokiSink{
		endpoint:    endpoint,
		headers:     headers,
		client:      client,
		logger:      logger,
		serviceName: cfg.ServiceName,
	}, nil
}
