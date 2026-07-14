package mikrotik

import (
	"context"
	"strings"
	"time"

	"github.com/jutaz/tiktelemetry/internal/model"
)

// LogCollector tails the RouterOS log buffer, producing model.LogEntry values.
//
// RouterOS log timestamps are unreliable for absolute time because they
// typically contain only a relative clock (e.g. "jan/02 15:04:05" or
// "15:04:05") without a reliable year or date. The collector therefore sets
// LogEntry.Time to time.Now() at collection time. This is a known limitation.
type LogCollector struct {
	lastID string
}

// NewLogCollector returns a LogCollector ready for use.
func NewLogCollector() *LogCollector {
	return &LogCollector{}
}

// Collect runs /log/print and returns entries newer than the last poll.
//
// On the very first call the collector records the latest .id and returns an
// empty slice, avoiding a dump of the entire historical buffer on startup. On
// subsequent calls it scans for the previously returned .id and yields only
// the entries after it. If the buffer has rotated (the old .id is gone) all
// entries are returned.
func (lc *LogCollector) Collect(ctx context.Context, r Runner) ([]model.LogEntry, error) {
	reply, err := r.Run(ctx, "/log/print")
	if err != nil {
		return nil, err
	}

	// Parse all entries from the reply.
	entries := make([]model.LogEntry, 0, len(reply.Re))
	for _, s := range reply.Re {
		topics := splitTopics(s.Map["topics"])
		entries = append(entries, model.LogEntry{
			Time:    time.Now(), // see known-limitation doc on LogCollector
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
