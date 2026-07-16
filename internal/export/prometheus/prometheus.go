package prometheus

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/klauspost/compress/snappy"

	"github.com/jutaz/tiktelemetry/internal/config"
	"github.com/jutaz/tiktelemetry/internal/export"
	"github.com/jutaz/tiktelemetry/internal/export/exporthelp"
	"github.com/jutaz/tiktelemetry/internal/model"
)

func init() {
	export.Register("prometheus", New)
}

// prometheusSink pushes metric samples to a Prometheus remote_write endpoint.
// It is stateless: each call to ConsumeMetrics builds and sends a fresh
// Snappy-compressed protobuf WriteRequest.
type prometheusSink struct {
	endpoint string
	headers  map[string]string
	client   *http.Client
	logger   *slog.Logger
	svcName  string
	svcVer   string
}

// Compile-time interface check.
var _ export.Sink = (*prometheusSink)(nil)

// Name returns "prometheus".
func (p *prometheusSink) Name() string { return "prometheus" }

// Capabilities reports that this sink handles metrics but not logs.
func (p *prometheusSink) Capabilities() export.Capabilities {
	return export.Capabilities{Metrics: true, Logs: false}
}

// ConsumeLogs is a no-op — this sink only handles metrics.
func (p *prometheusSink) ConsumeLogs(_ context.Context, _ []model.LogEntry) error {
	return nil
}

// Shutdown is a no-op — the sink has no background goroutines or buffers.
func (p *prometheusSink) Shutdown(_ context.Context) error {
	return nil
}

// ConsumeMetrics encodes samples into a Snappy-compressed protobuf
// WriteRequest and pushes it to the configured remote_write endpoint.
func (p *prometheusSink) ConsumeMetrics(ctx context.Context, samples []model.Sample) error {
	if len(samples) == 0 {
		return nil
	}

	now := time.Now().UnixMilli() // single timestamp for the whole batch
	tsList := make([]timeSeries, 0, len(samples))

	for _, smpl := range samples {
		// Pre-allocate for: __name__ + N attributes + service + instance.
		labels := make([]label, 0, 2+len(smpl.Attributes)+2)

		// Metric name carried as the special __name__ label (Prometheus convention).
		labels = append(labels, label{
			Name:  "__name__",
			Value: exporthelp.SanitizeMetricName(smpl.Name),
		})

		// Dimension attributes from the collector.
		// Sort attribute keys first so label ordering is deterministic.
		attrKeys := make([]string, 0, len(smpl.Attributes))
		for k := range smpl.Attributes {
			attrKeys = append(attrKeys, k)
		}
		sort.Strings(attrKeys)
		for _, k := range attrKeys {
			v := smpl.Attributes[k]
			if v == "" {
				continue // skip empty values (protobuf strings must not be empty)
			}
			labels = append(labels, label{
				Name:  exporthelp.SanitizeLabelName(k),
				Value: v,
			})
		}

		// The agent (hub) identity. Per-router identity travels as the "target"
		// sample attribute, added by the collection layer.
		labels = append(labels, label{Name: "service", Value: p.svcName})

		// Labels MUST be sorted lexicographically by name (remote_write contract).
		sort.Slice(labels, func(i, j int) bool {
			return labels[i].Name < labels[j].Name
		})

		// Remote_write is cumulative like a scrape: send absolute values for
		// both gauges and counters (no delta tracking).
		tsList = append(tsList, timeSeries{
			Labels:  labels,
			Samples: []psample{{Value: float64(smpl.Value), TimestampMs: now}},
		})
	}

	// Marshal, compress, POST.
	protoBytes := (&writeRequest{Timeseries: tsList}).marshal()
	compressed := snappy.Encode(nil, protoBytes)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(compressed))
	if err != nil {
		return fmt.Errorf("prometheus: create request: %w", err)
	}

	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	req.Header.Set("User-Agent", "tiktelemetry/"+p.svcVer)

	// Apply any extra headers (including Authorization from factory).
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("prometheus: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("prometheus: remote_write returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// New is the export.Factory for the "prometheus" sink.
//
// Required env vars:
//
//	PROMETHEUS_ENDPOINT   — remote_write URL (e.g. https://prometheus-prod-XX.grafana.net/api/prom/push)
//
// Optional env vars:
//
//	PROMETHEUS_USER       — basic auth username (Grafana Cloud: metrics instance ID)
//	PROMETHEUS_PASS       — basic auth password / access policy token
//	PROMETHEUS_HEADERS    — comma-separated "Key=Value" pairs for extra HTTP headers
//	PROMETHEUS_INSECURE_SKIP_VERIFY — set to "true" to skip TLS verification
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (export.Sink, error) {
	endpoint := cfg.Env("PROMETHEUS_ENDPOINT")
	if endpoint == "" {
		return nil, fmt.Errorf("prometheus: PROMETHEUS_ENDPOINT is required")
	}

	user := cfg.Env("PROMETHEUS_USER")
	pass := cfg.Env("PROMETHEUS_PASS")

	headers := make(map[string]string)

	if auth := exporthelp.BasicAuth(user, pass); auth != "" {
		headers["Authorization"] = auth
	}

	extraHeaders, err := cfg.EnvHeaders("PROMETHEUS_HEADERS")
	if err != nil {
		return nil, fmt.Errorf("prometheus: parsing PROMETHEUS_HEADERS: %w", err)
	}
	for k, v := range extraHeaders {
		headers[k] = v
	}

	var client *http.Client
	if cfg.EnvBool("PROMETHEUS_INSECURE_SKIP_VERIFY", false) {
		client = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	} else {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	return &prometheusSink{
		endpoint: endpoint,
		headers:  headers,
		client:   client,
		logger:   logger,
		svcName:  cfg.ServiceName,
		svcVer:   cfg.ServiceVersion,
	}, nil
}
