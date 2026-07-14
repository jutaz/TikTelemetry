package mikrotik

import (
	"context"
	"testing"

	"github.com/go-routeros/routeros/v3"
)

// ---------------------------------------------------------------------------
// LogCollector tests
// ---------------------------------------------------------------------------

func TestLogCollector_NameNotApplicable(t *testing.T) {
	// LogCollector is not a Collector (no Name method), but we can at least
	// verify it's constructable.
	lc := NewLogCollector()
	if lc == nil {
		t.Fatal("NewLogCollector returned nil")
	}
}

func TestLogCollector_FirstCallReturnsNil(t *testing.T) {
	lc := NewLogCollector()
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/log/print": {
				replySentences(
					map[string]string{".id": "1", "topics": "system,info", "message": "startup"},
					map[string]string{".id": "2", "topics": "error", "message": "oops"},
					map[string]string{".id": "3", "topics": "", "message": "last"},
				),
			},
		},
	}
	entries, err := lc.Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if entries != nil {
		t.Errorf("expected nil on first call, got %v", entries)
	}
	if lc.lastID != "3" {
		t.Errorf("lastID = %q, want \"3\"", lc.lastID)
	}
}

func TestLogCollector_SecondCallReturnsNewOnly(t *testing.T) {
	lc := NewLogCollector()
	// Seed with previous state via first Collect, then second Collect returns new.
	replies := map[string][]*routeros.Reply{
		"/log/print": {
			// First call: 3 entries.
			replySentences(
				map[string]string{".id": "1", "topics": "info", "message": "first"},
				map[string]string{".id": "2", "topics": "info", "message": "second"},
				map[string]string{".id": "3", "topics": "info", "message": "third"},
			),
			// Second call: 5 entries (3 old + 2 new).
			replySentences(
				map[string]string{".id": "1", "topics": "info", "message": "first"},
				map[string]string{".id": "2", "topics": "info", "message": "second"},
				map[string]string{".id": "3", "topics": "info", "message": "third"},
				map[string]string{".id": "4", "topics": "warning", "message": "fourth"},
				map[string]string{".id": "5", "topics": "error", "message": "fifth"},
			),
		},
	}
	runner := &fakeRunner{replies: replies}

	// First call: records lastID = "3", returns nil.
	entries, err := lc.Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("first Collect error: %v", err)
	}
	if entries != nil {
		t.Fatalf("expected nil on first call, got %d entries", len(entries))
	}

	// Second call: should return entries 4 and 5.
	entries, err = lc.Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("second Collect error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 new entries, got %d", len(entries))
	}
	if entries[0].ID != "4" || entries[0].Message != "fourth" {
		t.Errorf("first new entry: %+v", entries[0])
	}
	if entries[1].ID != "5" || entries[1].Message != "fifth" {
		t.Errorf("second new entry: %+v", entries[1])
	}
	if lc.lastID != "5" {
		t.Errorf("lastID = %q, want \"5\"", lc.lastID)
	}
}

func TestLogCollector_NoNewEntries(t *testing.T) {
	lc := NewLogCollector()
	replies := map[string][]*routeros.Reply{
		"/log/print": {
			// First call: seed with 2 entries.
			replySentences(
				map[string]string{".id": "10", "topics": "info", "message": "a"},
				map[string]string{".id": "11", "topics": "info", "message": "b"},
			),
			// Second call: same set (lastID = 11 is present at end).
			replySentences(
				map[string]string{".id": "10", "topics": "info", "message": "a"},
				map[string]string{".id": "11", "topics": "info", "message": "b"},
			),
		},
	}
	runner := &fakeRunner{replies: replies}

	// First call: seed.
	_, err := lc.Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("first Collect error: %v", err)
	}

	// Second call: no entries after lastID.
	entries, err := lc.Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("second Collect error: %v", err)
	}
	if entries != nil {
		t.Errorf("expected nil when no new entries, got %d entries", len(entries))
	}
}

func TestLogCollector_BufferRotation(t *testing.T) {
	lc := NewLogCollector()
	replies := map[string][]*routeros.Reply{
		"/log/print": {
			// First call: seed with IDs 1-3.
			replySentences(
				map[string]string{".id": "1", "topics": "info", "message": "old"},
				map[string]string{".id": "2", "topics": "info", "message": "mid"},
				map[string]string{".id": "3", "topics": "info", "message": "new"},
			),
			// Second call: buffer rotated; none of the old IDs present.
			replySentences(
				map[string]string{".id": "100", "topics": "error", "message": "fresh"},
				map[string]string{".id": "101", "topics": "error", "message": "newer"},
			),
		},
	}
	runner := &fakeRunner{replies: replies}

	// First call: seed.
	_, err := lc.Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("first Collect error: %v", err)
	}

	// Second call: buffer rotated — should return ALL entries.
	entries, err := lc.Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("second Collect error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (buffer rotated), got %d", len(entries))
	}
	if entries[0].ID != "100" || entries[1].ID != "101" {
		t.Errorf("unexpected entries: %+v", entries)
	}
	if lc.lastID != "101" {
		t.Errorf("lastID = %q, want \"101\"", lc.lastID)
	}
}

func TestLogCollector_EmptyReply(t *testing.T) {
	lc := NewLogCollector()
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/log/print": {
				{Re: nil}, // no sentences
			},
		},
	}
	entries, err := lc.Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	if entries != nil {
		t.Errorf("expected nil for empty reply, got %v", entries)
	}
}

func TestLogCollector_TopicsParsing(t *testing.T) {
	lc := NewLogCollector()
	runner := &fakeRunner{
		replies: map[string][]*routeros.Reply{
			"/log/print": {
				replySentences(
					// Topics with spaces around them; empty field; absent key.
					map[string]string{".id": "1", "topics": "system, info , error", "message": "m1"},
					map[string]string{".id": "2", "topics": "", "message": "m2"},
					map[string]string{".id": "3", "message": "m3"}, // no topics key
				),
			},
		},
	}
	// First call to seed (returns nil).
	_, err := lc.Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect error: %v", err)
	}
}

func TestLogCollector_TopicsParsedCorrectly(t *testing.T) {
	// Use a fresh runner with the same data but exercise the return path on a
	// second call (we need to have a first call first to get past dedup).
	// Instead, use the buffer-rotation trick: first call seeds, second call
	// returns all entries including the ones with interesting topic structures.
	lc := NewLogCollector()
	replies := map[string][]*routeros.Reply{
		"/log/print": {
			// First call: seed with some entries (lastID will be the last one).
			replySentences(
				map[string]string{".id": "seed", "topics": "info", "message": "seed"},
			),
			// Second call: buffer rotation — all entries returned.
			replySentences(
				map[string]string{".id": "1", "topics": "system, info , error", "message": "multi space"},
				map[string]string{".id": "2", "topics": "", "message": "empty topics"},
				map[string]string{".id": "3", "message": "no topics key"},
				map[string]string{".id": "4", "topics": "  ", "message": "whitespace only"},
				map[string]string{".id": "5", "topics": "single", "message": "one topic"},
			),
		},
	}
	runner := &fakeRunner{replies: replies}

	// First call: seed.
	_, err := lc.Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("first Collect error: %v", err)
	}

	// Second call: buffer rotated, returns everything.
	entries, err := lc.Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("second Collect error: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}

	// Entries are in order from the reply.
	// Entry 1: "system, info , error" -> trimmed tokens.
	topics := entries[0].Topics
	if len(topics) != 3 || topics[0] != "system" || topics[1] != "info" || topics[2] != "error" {
		t.Errorf("entry 0 topics = %q, want [system info error]", topics)
	}
	if entries[0].Message != "multi space" || entries[0].ID != "1" {
		t.Errorf("entry 0 fields: %+v", entries[0])
	}

	// Entry 2: empty topics string -> nil slice.
	if entries[1].Topics != nil {
		t.Errorf("entry 1 topics should be nil for empty string, got %v", entries[1].Topics)
	}
	if entries[1].Message != "empty topics" || entries[1].ID != "2" {
		t.Errorf("entry 1 fields: %+v", entries[1])
	}

	// Entry 3: no topics key -> nil slice.
	if entries[2].Topics != nil {
		t.Errorf("entry 2 topics should be nil for absent key, got %v", entries[2].Topics)
	}
	if entries[2].Message != "no topics key" || entries[2].ID != "3" {
		t.Errorf("entry 2 fields: %+v", entries[2])
	}

	// Entry 4: whitespace-only -> nil slice.
	if entries[3].Topics != nil {
		t.Errorf("entry 3 topics should be nil for whitespace-only, got %v", entries[3].Topics)
	}
	if entries[3].Message != "whitespace only" || entries[3].ID != "4" {
		t.Errorf("entry 3 fields: %+v", entries[3])
	}

	// Entry 5: single topic.
	if len(entries[4].Topics) != 1 || entries[4].Topics[0] != "single" {
		t.Errorf("entry 4 topics = %q, want [single]", entries[4].Topics)
	}
	if entries[4].Message != "one topic" || entries[4].ID != "5" {
		t.Errorf("entry 4 fields: %+v", entries[4])
	}
}

func TestLogCollector_MessageAndIDMapping(t *testing.T) {
	lc := NewLogCollector()
	replies := map[string][]*routeros.Reply{
		"/log/print": {
			// Seed.
			replySentences(
				map[string]string{".id": "s", "topics": "info", "message": "seed"},
			),
			// Buffer rotation — return everything.
			replySentences(
				map[string]string{".id": "*42", "topics": "system", "message": "hello world"},
			),
		},
	}
	runner := &fakeRunner{replies: replies}

	// Seed.
	_, _ = lc.Collect(context.Background(), runner)

	// Rotated read.
	entries, err := lc.Collect(context.Background(), runner)
	if err != nil {
		t.Fatalf("Collect error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ID != "*42" {
		t.Errorf("ID = %q, want \"*42\"", entries[0].ID)
	}
	if entries[0].Message != "hello world" {
		t.Errorf("Message = %q, want \"hello world\"", entries[0].Message)
	}
}

func TestLogCollector_RunnerError(t *testing.T) {
	lc := NewLogCollector()
	runner := &fakeRunner{err: assertAnError}
	_, err := lc.Collect(context.Background(), runner)
	if err != assertAnError {
		t.Errorf("expected assertAnError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// splitTopics unit tests (the internal helper, exposed via in-package tests)
// ---------------------------------------------------------------------------

func TestSplitTopics(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"  ", nil},
		{"single", []string{"single"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{"system, info , error", []string{"system", "info", "error"}},
		{"  leading,trailing  ", []string{"leading", "trailing"}},
	}
	for _, tt := range tests {
		got := splitTopics(tt.raw)
		if len(got) != len(tt.want) {
			t.Errorf("splitTopics(%q) = %v, want %v", tt.raw, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitTopics(%q) = %v, want %v", tt.raw, got, tt.want)
				break
			}
		}
	}
	if got := splitTopics(""); got != nil {
		t.Error("splitTopics(\"\") should return nil, not an empty slice")
	}
}
