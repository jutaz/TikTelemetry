package agent

import "github.com/jutaz/tiktelemetry/internal/model"

// selfMetrics accumulates the agent's own operational metrics so an operator can
// tell whether TikTelemetry itself is healthy — scrape success/duration, per
// collector failures, export failures, and router reachability. These are
// emitted through the same export pipeline as router metrics (prefixed
// "tiktelemetry.") so they land in the same backend with no extra endpoint.
//
// selfMetrics is used from the single scrape-loop goroutine and needs no
// locking.
type selfMetrics struct {
	// Cumulative counters (monotonic).
	scrapesTotal        int64
	collectorErrorTotal map[string]int64 // per collector name
	metricExportErrors  int64
	logExportErrors     int64
	logCollectErrors    int64

	// Last-scrape gauges.
	lastScrapeDurationMs int64
	lastScrapeSamples    int64
	routerUp             int64 // 1 if the last metrics scrape reached the router
}

func newSelfMetrics() *selfMetrics {
	return &selfMetrics{collectorErrorTotal: map[string]int64{}}
}

// recordScrape notes the outcome of a completed metrics scrape.
func (m *selfMetrics) recordScrape(durationMs, samples int64, routerReached bool) {
	m.scrapesTotal++
	m.lastScrapeDurationMs = durationMs
	m.lastScrapeSamples = samples
	if routerReached {
		m.routerUp = 1
	} else {
		m.routerUp = 0
	}
}

func (m *selfMetrics) recordCollectorError(name string) {
	m.collectorErrorTotal[name]++
}

func (m *selfMetrics) recordMetricExportError() { m.metricExportErrors++ }
func (m *selfMetrics) recordLogExportError()    { m.logExportErrors++ }
func (m *selfMetrics) recordLogCollectError()   { m.logCollectErrors++ }

// samples renders the current self-metric state as model.Samples to be appended
// to the outgoing metrics batch.
func (m *selfMetrics) samples() []model.Sample {
	out := []model.Sample{
		selfSample("tiktelemetry.scrapes.total",
			"Total metric scrapes performed by the agent",
			"1", model.KindCounter, m.scrapesTotal, nil),
		selfSample("tiktelemetry.scrape.duration",
			"Duration of the last metric scrape",
			"ms", model.KindGauge, m.lastScrapeDurationMs, nil),
		selfSample("tiktelemetry.scrape.samples",
			"Number of samples collected in the last scrape",
			"1", model.KindGauge, m.lastScrapeSamples, nil),
		selfSample("tiktelemetry.router.up",
			"Whether the router API was reachable during the last scrape (1/0)",
			"1", model.KindGauge, m.routerUp, nil),
		selfSample("tiktelemetry.export.errors",
			"Total metric export failures",
			"1", model.KindCounter, m.metricExportErrors,
			map[string]string{"signal": "metrics"}),
		selfSample("tiktelemetry.export.errors",
			"Total log export failures",
			"1", model.KindCounter, m.logExportErrors,
			map[string]string{"signal": "logs"}),
		selfSample("tiktelemetry.log.collect.errors",
			"Total log collection failures",
			"1", model.KindCounter, m.logCollectErrors, nil),
	}

	// Per-collector error counters.
	for name, n := range m.collectorErrorTotal {
		out = append(out, selfSample("tiktelemetry.collector.errors",
			"Total collector failures",
			"1", model.KindCounter, n,
			map[string]string{"collector": name}))
	}
	return out
}

// selfSample builds a model.Sample, cloning attrs so callers can reuse maps.
func selfSample(name, desc, unit string, kind model.MetricKind, value int64, attrs map[string]string) model.Sample {
	var clone map[string]string
	if len(attrs) > 0 {
		clone = make(map[string]string, len(attrs))
		for k, v := range attrs {
			clone[k] = v
		}
	}
	return model.Sample{
		Name:        name,
		Description: desc,
		Unit:        unit,
		Kind:        kind,
		Value:       value,
		Attributes:  clone,
	}
}
