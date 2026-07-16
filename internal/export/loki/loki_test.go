package loki

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/jutaz/tiktelemetry/internal/model"
)

func TestBuildPayload(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 123456789, time.UTC)
	earlier := now.Add(-5 * time.Minute)

	entries := []model.LogEntry{
		{
			Time:    now,
			Topics:  []string{"system", "info"},
			Message: "system started",
			ID:      "123",
		},
		{
			Time:    earlier,
			Topics:  []string{"error", "dhcp"},
			Message: "dhcp lease failed",
			ID:      "456",
		},
		{
			Time:    now,
			Topics:  []string{"warning", "wireless"},
			Message: "signal low",
			ID:      "",
		},
	}

	body, err := buildPayload(entries, "test-service")
	if err != nil {
		t.Fatalf("buildPayload returned error: %v", err)
	}

	// Unmarshal back to verify structure.
	var pr pushRequest
	if err := json.Unmarshal(body, &pr); err != nil {
		t.Fatalf("json.Unmarshal of output failed: %v\nbody: %s", err, string(body))
	}

	// We expect 3 streams: info, error, warn.
	if len(pr.Streams) != 3 {
		t.Fatalf("expected 3 streams, got %d", len(pr.Streams))
	}

	// Collect streams by level for assertions.
	byLevel := make(map[string]stream)
	for _, s := range pr.Streams {
		lvl := s.Stream["level"]
		if lvl == "" {
			t.Errorf("stream missing level label: %v", s.Stream)
		}
		byLevel[lvl] = s
	}

	// Check stream labels.
	t.Run("labels", func(t *testing.T) {
		for level, s := range byLevel {
			if s.Stream["service"] != "test-service" {
				t.Errorf("stream %q: expected service=test-service, got %q",
					level, s.Stream["service"])
			}
			if s.Stream["source"] != "mikrotik" {
				t.Errorf("stream %q: expected source=mikrotik, got %q",
					level, s.Stream["source"])
			}
			if _, ok := s.Stream["target"]; ok {
				t.Errorf("stream %q: should not have target label when Target is empty, got target=%q",
					level, s.Stream["target"])
			}
		}
	})

	// Check timestamps are nanosecond strings, entries are sorted ascending.
	t.Run("timestamps", func(t *testing.T) {
		// "error" stream: single entry, earlier timestamp.
		es, ok := byLevel["error"]
		if !ok {
			t.Fatal("missing 'error' stream")
		}
		if len(es.Values) != 1 {
			t.Fatalf("error stream: expected 1 value, got %d", len(es.Values))
		}
		tsStr, ok := es.Values[0][0].(string)
		if !ok {
			t.Fatalf("error stream: timestamp is not a string: %v", es.Values[0][0])
		}
		ts, err := strconv.ParseInt(tsStr, 10, 64)
		if err != nil {
			t.Fatalf("error stream: timestamp not a valid int64: %v", err)
		}
		// The "error" entry has `earlier` time.
		if ts != earlier.UnixNano() {
			t.Errorf("error stream: expected ts %d, got %d", earlier.UnixNano(), ts)
		}

		// "info" stream: single entry with `now`.
		is, ok := byLevel["info"]
		if !ok {
			t.Fatal("missing 'info' stream")
		}
		if len(is.Values) != 1 {
			t.Fatalf("info stream: expected 1 value, got %d", len(is.Values))
		}
		tsStr2, _ := is.Values[0][0].(string)
		ts2, _ := strconv.ParseInt(tsStr2, 10, 64)
		if ts2 != now.UnixNano() {
			t.Errorf("info stream: expected ts %d, got %d", now.UnixNano(), ts2)
		}

		// "warn" stream: single entry with `now`.
		ws, ok := byLevel["warn"]
		if !ok {
			t.Fatal("missing 'warn' stream")
		}
		if len(ws.Values) != 1 {
			t.Fatalf("warn stream: expected 1 value, got %d", len(ws.Values))
		}
		// Verify warning entry has the message "signal low".
		msg, ok := ws.Values[0][1].(string)
		if !ok || msg != "signal low" {
			t.Errorf("warn stream: expected msg 'signal low', got %v", ws.Values[0][1])
		}
	})

	// Check structured metadata presence.
	t.Run("structured_metadata", func(t *testing.T) {
		// "error" entry has ID "456" and topics ["error", "dhcp"].
		es := byLevel["error"]
		if len(es.Values[0]) != 3 {
			t.Fatalf("error stream: expected 3 elements in value (ts, msg, meta), got %d: %v",
				len(es.Values[0]), es.Values[0])
		}
		meta, ok := es.Values[0][2].(map[string]interface{})
		if !ok {
			t.Fatalf("error stream: metadata is not a map: %T %v", es.Values[0][2], es.Values[0][2])
		}
		if meta["topics"] != "error,dhcp" {
			t.Errorf("error stream: expected topics 'error,dhcp', got %v", meta["topics"])
		}
		if meta["id"] != "456" {
			t.Errorf("error stream: expected id '456', got %v", meta["id"])
		}

		// "info" entry has ID "123" and topics ["system", "info"].
		is := byLevel["info"]
		if len(is.Values[0]) != 3 {
			t.Fatalf("info stream: expected 3 elements in value (ts, msg, meta), got %d: %v",
				len(is.Values[0]), is.Values[0])
		}
		meta2, ok := is.Values[0][2].(map[string]interface{})
		if !ok {
			t.Fatalf("info stream: metadata is not a map: %T %v", is.Values[0][2], is.Values[0][2])
		}
		if meta2["topics"] != "system,info" {
			t.Errorf("info stream: expected topics 'system,info', got %v", meta2["topics"])
		}
		if meta2["id"] != "123" {
			t.Errorf("info stream: expected id '123', got %v", meta2["id"])
		}

		// "warn" entry has no ID and topics ["warning", "wireless"].
		ws := byLevel["warn"]
		if len(ws.Values[0]) != 3 {
			t.Fatalf("warn stream: expected 3 elements in value (ts, msg, meta), got %d: %v",
				len(ws.Values[0]), ws.Values[0])
		}
		meta3, ok := ws.Values[0][2].(map[string]interface{})
		if !ok {
			t.Fatalf("warn stream: metadata is not a map: %T %v", ws.Values[0][2], ws.Values[0][2])
		}
		if meta3["topics"] != "warning,wireless" {
			t.Errorf("warn stream: expected topics 'warning,wireless', got %v", meta3["topics"])
		}
		if _, exists := meta3["id"]; exists {
			t.Errorf("warn stream: expected no 'id' key in metadata, got %v", meta3)
		}
	})

	// Verify sort order within streams: entries within a stream should be
	// sorted ascending by time. Our test entries are already grouped so each
	// stream has exactly one entry, which trivially satisfies this. We add an
	// explicit multi-entry sort test below.
	t.Run("sorting", func(t *testing.T) {
		// Multiple entries at the same severity level.
		t1 := time.Date(2026, 1, 1, 0, 0, 0, 100, time.UTC)
		t2 := time.Date(2026, 1, 1, 0, 0, 0, 200, time.UTC)
		t3 := time.Date(2026, 1, 1, 0, 0, 0, 300, time.UTC)

		multi := []model.LogEntry{
			{Time: t3, Topics: []string{"info"}, Message: "third"},
			{Time: t1, Topics: []string{"info"}, Message: "first"},
			{Time: t2, Topics: []string{"info"}, Message: "second"},
		}

		body, err := buildPayload(multi, "svc")
		if err != nil {
			t.Fatalf("buildPayload: %v", err)
		}

		var pr pushRequest
		if err := json.Unmarshal(body, &pr); err != nil {
			t.Fatalf("json.Unmarshal: %v", err)
		}
		if len(pr.Streams) != 1 {
			t.Fatalf("expected 1 stream, got %d", len(pr.Streams))
		}
		vals := pr.Streams[0].Values
		if len(vals) != 3 {
			t.Fatalf("expected 3 values, got %d", len(vals))
		}

		messages := make([]string, 3)
		for i, v := range vals {
			messages[i], _ = v[1].(string)
		}
		expected := []string{"first", "second", "third"}
		for i, m := range messages {
			if m != expected[i] {
				t.Errorf("position %d: expected %q, got %q", i, expected[i], m)
			}
		}
	})
}

func TestBuildPayloadMultiTarget(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	entries := []model.LogEntry{
		{Time: now, Topics: []string{"info"}, Message: "core-info-1", Target: "core"},
		{Time: now, Topics: []string{"error"}, Message: "core-error-1", Target: "core"},
		{Time: now, Topics: []string{"info"}, Message: "edge-info-1", Target: "edge"},
		{Time: now.Add(-1 * time.Minute), Topics: []string{"info"}, Message: "core-info-2", Target: "core"},
	}

	body, err := buildPayload(entries, "test-service")
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}

	var pr pushRequest
	if err := json.Unmarshal(body, &pr); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// Expected 3 streams sorted by (target, level):
	//   (core, error), (core, info), (edge, info)
	if len(pr.Streams) != 3 {
		t.Fatalf("expected 3 streams, got %d", len(pr.Streams))
	}

	// Common labels for all streams.
	for i, s := range pr.Streams {
		if s.Stream["service"] != "test-service" {
			t.Errorf("stream %d: service = %q", i, s.Stream["service"])
		}
		if s.Stream["source"] != "mikrotik" {
			t.Errorf("stream %d: source = %q", i, s.Stream["source"])
		}
	}

	// Stream 0: core + error.
	s0 := pr.Streams[0]
	if s0.Stream["target"] != "core" || s0.Stream["level"] != "error" {
		t.Errorf("stream 0: labels = %v", s0.Stream)
	}
	if len(s0.Values) != 1 {
		t.Fatalf("stream 0: expected 1 value, got %d", len(s0.Values))
	}
	if s0.Values[0][1] != "core-error-1" {
		t.Errorf("stream 0: message = %v", s0.Values[0][1])
	}

	// Stream 1: core + info (2 entries, sorted ascending by time).
	s1 := pr.Streams[1]
	if s1.Stream["target"] != "core" || s1.Stream["level"] != "info" {
		t.Errorf("stream 1: labels = %v", s1.Stream)
	}
	if len(s1.Values) != 2 {
		t.Fatalf("stream 1: expected 2 values, got %d", len(s1.Values))
	}
	// core-info-2 (earlier) then core-info-1 (later).
	if s1.Values[0][1] != "core-info-2" {
		t.Errorf("stream 1[0]: message = %v", s1.Values[0][1])
	}
	if s1.Values[1][1] != "core-info-1" {
		t.Errorf("stream 1[1]: message = %v", s1.Values[1][1])
	}

	// Stream 2: edge + info.
	s2 := pr.Streams[2]
	if s2.Stream["target"] != "edge" || s2.Stream["level"] != "info" {
		t.Errorf("stream 2: labels = %v", s2.Stream)
	}
	if len(s2.Values) != 1 {
		t.Fatalf("stream 2: expected 1 value, got %d", len(s2.Values))
	}
	if s2.Values[0][1] != "edge-info-1" {
		t.Errorf("stream 2: message = %v", s2.Values[0][1])
	}
}

func TestBuildPayloadTargetLabel(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	entries := []model.LogEntry{
		{Time: now, Topics: []string{"info"}, Message: "has-target", Target: "r1"},
		{Time: now, Topics: []string{"info"}, Message: "no-target", Target: ""},
	}

	body, err := buildPayload(entries, "svc")
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}

	var pr pushRequest
	if err := json.Unmarshal(body, &pr); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// 2 streams sorted by (target, level): ("", info) then (r1, info).
	if len(pr.Streams) != 2 {
		t.Fatalf("expected 2 streams, got %d", len(pr.Streams))
	}

	// Stream 0: target="", level=info → no target label.
	s0 := pr.Streams[0]
	if s0.Stream["level"] != "info" {
		t.Errorf("stream 0: level = %q", s0.Stream["level"])
	}
	if _, hasTarget := s0.Stream["target"]; hasTarget {
		t.Errorf("stream 0: should not have target label, got %v", s0.Stream)
	}
	if len(s0.Values) != 1 || s0.Values[0][1] != "no-target" {
		t.Errorf("stream 0: expected message 'no-target'")
	}

	// Stream 1: target="r1", level=info → has target label.
	s1 := pr.Streams[1]
	if s1.Stream["level"] != "info" || s1.Stream["target"] != "r1" {
		t.Errorf("stream 1: expected level=info, target=r1, got %v", s1.Stream)
	}
	if len(s1.Values) != 1 || s1.Values[0][1] != "has-target" {
		t.Errorf("stream 1: expected message 'has-target'")
	}
}

func TestBuildPayloadEmpty(t *testing.T) {
	body, err := buildPayload(nil, "svc")
	if err != nil {
		t.Fatalf("buildPayload returned error for empty input: %v", err)
	}
	var pr pushRequest
	if err := json.Unmarshal(body, &pr); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(pr.Streams) != 0 {
		t.Errorf("expected 0 streams for empty input, got %d", len(pr.Streams))
	}
}
