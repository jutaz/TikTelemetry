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

	body, err := buildPayload(entries, "test-service", "test-instance")
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
			if s.Stream["instance"] != "test-instance" {
				t.Errorf("stream %q: expected instance=test-instance, got %q",
					level, s.Stream["instance"])
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

		body, err := buildPayload(multi, "svc", "inst")
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

func TestBuildPayloadEmpty(t *testing.T) {
	body, err := buildPayload(nil, "svc", "inst")
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
