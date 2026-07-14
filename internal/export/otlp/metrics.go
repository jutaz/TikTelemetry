package otlp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/jutaz/tiktelemetry/internal/model"
)

// metricsPipeline owns the OTel MeterProvider, a meter, lazily-created
// instruments, and a last-value map for computing counter deltas.
type metricsPipeline struct {
	mp    *sdkmetric.MeterProvider
	meter metric.Meter

	mu         sync.Mutex
	gauges     map[string]metric.Int64Gauge
	counters   map[string]metric.Int64Counter
	lastValues map[string]int64
}

// hasScheme reports whether s contains a URI scheme (e.g. "https://").
func hasScheme(s string) bool {
	return strings.Contains(s, "://")
}

// newMetricsPipeline constructs the metrics pipeline: exporter, PeriodicReader,
// and MeterProvider.
func newMetricsPipeline(ctx context.Context, endpoint string, headers map[string]string, insecure bool, pollInterval time.Duration, res *resource.Resource) (*metricsPipeline, error) {
	expOpts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpointURL(endpoint),
	}
	if len(headers) > 0 {
		expOpts = append(expOpts, otlpmetrichttp.WithHeaders(headers))
	}
	if insecure && !hasScheme(endpoint) {
		expOpts = append(expOpts, otlpmetrichttp.WithInsecure())
	}

	exp, err := otlpmetrichttp.New(ctx, expOpts...)
	if err != nil {
		return nil, fmt.Errorf("create OTLP metrics exporter: %w", err)
	}

	reader := sdkmetric.NewPeriodicReader(exp,
		sdkmetric.WithInterval(pollInterval),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	)

	return &metricsPipeline{
		mp:         mp,
		meter:      mp.Meter("github.com/jutaz/tiktelemetry"),
		gauges:     make(map[string]metric.Int64Gauge),
		counters:   make(map[string]metric.Int64Counter),
		lastValues: make(map[string]int64),
	}, nil
}

// consumeMetrics records a batch of model samples.
func (m *metricsPipeline) consumeMetrics(ctx context.Context, samples []model.Sample) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, s := range samples {
		attrs := sortedAttrs(s.Attributes)
		attrOpt := metric.WithAttributes(attrs...)

		switch s.Kind {
		case model.KindGauge:
			g, ok := m.gauges[s.Name]
			if !ok {
				var err error
				g, err = m.meter.Int64Gauge(s.Name,
					metric.WithDescription(s.Description),
					metric.WithUnit(s.Unit),
				)
				if err != nil {
					return fmt.Errorf("create gauge %q: %w", s.Name, err)
				}
				m.gauges[s.Name] = g
			}
			g.Record(ctx, s.Value, attrOpt)

		case model.KindCounter:
			c, ok := m.counters[s.Name]
			if !ok {
				var err error
				c, err = m.meter.Int64Counter(s.Name,
					metric.WithDescription(s.Description),
					metric.WithUnit(s.Unit),
				)
				if err != nil {
					return fmt.Errorf("create counter %q: %w", s.Name, err)
				}
				m.counters[s.Name] = c
			}

			key := counterKey(s.Name, s.Attributes)
			last, seen := m.lastValues[key]

			var delta int64
			if !seen || s.Value < last {
				// First observation or counter reset: emit the full current value.
				delta = s.Value
			} else {
				delta = s.Value - last
			}
			m.lastValues[key] = s.Value

			if delta > 0 || !seen {
				c.Add(ctx, delta, attrOpt)
			}
		}
	}
	return nil
}

// shutdownMetrics shuts down the MeterProvider (and its underlying reader/exporter).
func (m *metricsPipeline) shutdownMetrics(ctx context.Context) error {
	return m.mp.Shutdown(ctx)
}

// sortedAttrs returns a sorted slice of attribute.KeyValue from a string map.
func sortedAttrs(attrs map[string]string) []attribute.KeyValue {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, k := range keys {
		out = append(out, attribute.String(k, attrs[k]))
	}
	return out
}

// counterKey builds a stable map key from a metric name and its attribute map.
func counterKey(name string, attrs map[string]string) string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('|')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(attrs[k])
	}
	return b.String()
}
