// Package model defines the backend-agnostic data types exchanged between the
// MikroTik collection layer and the telemetry export layer. Keeping this
// contract free of both RouterOS and OpenTelemetry types lets either side
// evolve (or be swapped) independently.
package model

import "time"

// MetricKind describes how a Sample should be represented in the telemetry
// backend.
type MetricKind int

const (
	// KindGauge is an instantaneous value that can go up or down (CPU load,
	// free memory, temperature).
	KindGauge MetricKind = iota

	// KindCounter is a monotonically increasing cumulative value (interface
	// rx/tx byte totals). The backend computes rates via rate()/irate().
	KindCounter
)

// Sample is a single numeric measurement produced by a collector.
type Sample struct {
	// Name is the metric name, e.g. "mikrotik.system.cpu.load". Collectors
	// should use dotted OpenTelemetry-style names; the exporter maps these to
	// the backend's conventions.
	Name string

	// Description is a human-readable explanation of the metric.
	Description string

	// Unit is a UCUM unit string, e.g. "By" (bytes), "%", "Cel", "1".
	Unit string

	// Kind selects gauge vs counter semantics.
	Kind MetricKind

	// Value is the measured value. Integers are used because RouterOS reports
	// everything as integers or parseable strings; float precision is not
	// needed for these metrics.
	Value int64

	// Attributes are dimensional labels (e.g. interface=ether1). The exporter
	// attaches these plus global resource attributes.
	Attributes map[string]string
}

// LogEntry is a single log line pulled from the router's internal log buffer.
type LogEntry struct {
	// Time is when the router recorded the event.
	Time time.Time

	// Topics are the RouterOS logging topics attached to the entry
	// (e.g. "system", "info", "firewall").
	Topics []string

	// Message is the human-readable log text.
	Message string

	// ID is the RouterOS internal record id (.id), used for de-duplication
	// across polls.
	ID string
}

// Severity maps a set of RouterOS topics to a coarse severity for the log
// backend. RouterOS encodes severity as topics ("error", "warning", "info",
// "debug", "critical") rather than a numeric level.
func (e LogEntry) Severity() Level {
	// Track the highest severity among non-debug topics. If only "debug" is
	// present (and nothing at info or above), the entry is treated as debug;
	// otherwise a debug topic is ignored in favour of the more severe level.
	highest := Level(-1)
	sawDebug := false
	for _, t := range e.Topics {
		switch t {
		case "critical":
			return LevelFatal
		case "error":
			if LevelError > highest {
				highest = LevelError
			}
		case "warning":
			if LevelWarn > highest {
				highest = LevelWarn
			}
		case "info":
			if LevelInfo > highest {
				highest = LevelInfo
			}
		case "debug":
			sawDebug = true
		}
	}
	if highest >= 0 {
		return highest
	}
	if sawDebug {
		return LevelDebug
	}
	// No recognised severity topic: default to info.
	return LevelInfo
}

// Level is a backend-agnostic log severity.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelFatal
)
