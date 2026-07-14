package mikrotik

import (
	"context"
	"strings"
	"time"

	"github.com/jutaz/tiktelemetry/internal/model"
)

// LogCollector tails the RouterOS log buffer, producing model.LogEntry values.
//
// RouterOS /log/print time fields usually omit the year and are often just a
// wall clock (e.g. "15:04:05" or "jan/02 15:04:05"). To place entries correctly
// in the log backend, the collector reads the router's current time from
// /system/clock/print once per poll and uses it to resolve each entry's
// absolute timestamp (see parseLogTime). When the router clock is unavailable
// or an entry's time cannot be parsed, it falls back to collection time.
type LogCollector struct {
	lastID string
	// now is overridable in tests; defaults to time.Now.
	now func() time.Time
}

// NewLogCollector returns a LogCollector ready for use.
func NewLogCollector() *LogCollector {
	return &LogCollector{now: time.Now}
}

// Collect runs /log/print and returns entries newer than the last poll.
//
// On the very first call the collector records the latest .id and returns an
// empty slice, avoiding a dump of the entire historical buffer on startup. On
// subsequent calls it scans for the previously returned .id and yields only
// the entries after it. If the buffer has rotated (the old .id is gone) all
// entries are returned.
func (lc *LogCollector) Collect(ctx context.Context, r Runner) ([]model.LogEntry, error) {
	if lc.now == nil {
		lc.now = time.Now
	}

	reply, err := r.Run(ctx, "/log/print")
	if err != nil {
		return nil, err
	}

	// Reference time for resolving year-less log clocks: the router's own wall
	// clock when available, otherwise the local collection time.
	ref := lc.routerNow(ctx, r)
	collected := lc.now()

	// Parse all entries from the reply.
	entries := make([]model.LogEntry, 0, len(reply.Re))
	for _, s := range reply.Re {
		topics := splitTopics(s.Map["topics"])
		ts := collected
		if parsed, ok := parseLogTime(s.Map["time"], ref); ok {
			ts = parsed
		}
		entries = append(entries, model.LogEntry{
			Time:    ts,
			Topics:  topics,
			Message: s.Map["message"],
			ID:      s.Map[".id"],
		})
	}

	if len(entries) == 0 {
		return nil, nil
	}

	// First call: record the latest ID only, return nothing.
	if lc.lastID == "" {
		lc.lastID = entries[len(entries)-1].ID
		return nil, nil
	}

	// Find the index of the last returned ID.
	lastIdx := -1
	for i, e := range entries {
		if e.ID == lc.lastID {
			lastIdx = i
			break
		}
	}

	// lastID not found → buffer rotated; return everything.
	if lastIdx == -1 {
		lc.lastID = entries[len(entries)-1].ID
		return entries, nil
	}

	// Return entries strictly after the last returned ID.
	if lastIdx+1 >= len(entries) {
		return nil, nil
	}

	newEntries := entries[lastIdx+1:]
	lc.lastID = newEntries[len(newEntries)-1].ID
	return newEntries, nil
}

// routerNow reads the router's current wall-clock time from /system/clock. It
// combines the clock's date and time fields into a reference timestamp used to
// resolve year-less log entries. On any failure it falls back to the local
// collection time, which keeps log tailing working even if the clock command is
// restricted or absent.
func (lc *LogCollector) routerNow(ctx context.Context, r Runner) time.Time {
	fallback := lc.now()
	reply, err := r.Run(ctx, "/system/clock/print")
	if err != nil || len(reply.Re) == 0 {
		return fallback
	}
	m := reply.Re[0].Map
	if t, ok := parseRouterClock(m["date"], m["time"], m["gmt-offset"], fallback.Location()); ok {
		return t
	}
	return fallback
}

// splitTopics parses the RouterOS "topics" field, which is a comma-separated
// list of topic names. Returns nil for an empty or absent field.
func splitTopics(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	// Trim whitespace from each topic.
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
