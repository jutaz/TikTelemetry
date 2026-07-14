package mikrotik

import (
	"context"
	"testing"
	"time"

	"github.com/go-routeros/routeros/v3"
	"github.com/go-routeros/routeros/v3/proto"
)

// ref is a fixed reference time used across the timestamp tests:
// 2024-06-15 12:00:00 UTC.
func refTime() time.Time {
	return time.Date(2024, time.June, 15, 12, 0, 0, 0, time.UTC)
}

func TestParseLogTime(t *testing.T) {
	ref := refTime()
	loc := ref.Location()

	tests := []struct {
		name  string
		field string
		want  time.Time
		ok    bool
	}{
		{
			name:  "clock only, earlier today",
			field: "09:30:15",
			want:  time.Date(2024, time.June, 15, 9, 30, 15, 0, loc),
			ok:    true,
		},
		{
			name:  "clock only in the future rolls back a day",
			field: "23:59:00",
			want:  time.Date(2024, time.June, 14, 23, 59, 0, 0, loc),
			ok:    true,
		},
		{
			name:  "month/day + clock, same year",
			field: "jan/02 15:04:05",
			want:  time.Date(2024, time.January, 2, 15, 4, 5, 0, loc),
			ok:    true,
		},
		{
			name:  "month/day + clock in the future infers previous year",
			field: "dec/31 23:59:59",
			want:  time.Date(2023, time.December, 31, 23, 59, 59, 0, loc),
			ok:    true,
		},
		{
			name:  "uppercase month is accepted",
			field: "Mar/05 08:00:00",
			want:  time.Date(2024, time.March, 5, 8, 0, 0, 0, loc),
			ok:    true,
		},
		{
			name:  "iso full date with year",
			field: "2022-11-30 06:07:08",
			want:  time.Date(2022, time.November, 30, 6, 7, 8, 0, loc),
			ok:    true,
		},
		{
			name:  "month/day/year with clock",
			field: "feb/14/2021 10:20:30",
			want:  time.Date(2021, time.February, 14, 10, 20, 30, 0, loc),
			ok:    true,
		},
		{
			name:  "empty field",
			field: "",
			ok:    false,
		},
		{
			name:  "garbage",
			field: "not-a-time",
			ok:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseLogTime(tt.field, ref)
			if ok != tt.ok {
				t.Fatalf("parseLogTime(%q) ok = %v, want %v", tt.field, ok, tt.ok)
			}
			if !tt.ok {
				return
			}
			if !got.Equal(tt.want) {
				t.Errorf("parseLogTime(%q) = %s, want %s", tt.field, got, tt.want)
			}
		})
	}
}

func TestParseLogTimeHonorsLocation(t *testing.T) {
	// A non-UTC reference must produce times in the same location.
	loc := time.FixedZone("TEST", 2*60*60) // UTC+2
	ref := time.Date(2024, time.June, 15, 12, 0, 0, 0, loc)

	got, ok := parseLogTime("jan/02 15:04:05", ref)
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if got.Location() != loc {
		t.Errorf("location = %v, want %v", got.Location(), loc)
	}
	_, off := got.Zone()
	if off != 2*60*60 {
		t.Errorf("offset = %d, want 7200", off)
	}
}

func TestInferYear(t *testing.T) {
	ref := refTime() // June 2024

	// A June date stays in the current year.
	june := time.Date(0, time.June, 1, 0, 0, 0, 0, time.UTC)
	if got := inferYear(june, ref); got.Year() != 2024 {
		t.Errorf("June inferred year = %d, want 2024", got.Year())
	}

	// A December date (in the future for a June reference) rolls to last year.
	dec := time.Date(0, time.December, 25, 0, 0, 0, 0, time.UTC)
	if got := inferYear(dec, ref); got.Year() != 2023 {
		t.Errorf("December inferred year = %d, want 2023", got.Year())
	}
}

// TestInferYearFeb29 guards against the time.Date normalization bug where a
// Feb 29 entry placed in a non-leap year silently becomes March 1.
func TestInferYearFeb29(t *testing.T) {
	feb29 := time.Date(0, time.February, 29, 10, 20, 30, 0, time.UTC)

	// Reference in a non-leap year (2025): the entry must resolve to the nearest
	// earlier leap year (2024), not shift to March 1.
	ref2025 := time.Date(2025, time.March, 10, 12, 0, 0, 0, time.UTC)
	got := inferYear(feb29, ref2025)
	if got.Month() != time.February || got.Day() != 29 {
		t.Errorf("Feb 29 with non-leap ref resolved to %s, want a Feb 29 date", got.Format("2006-01-02"))
	}
	if got.Year() != 2024 {
		t.Errorf("Feb 29 resolved to year %d, want 2024 (nearest leap year)", got.Year())
	}

	// Reference in a leap year (2024): stays in that year.
	ref2024 := time.Date(2024, time.March, 10, 12, 0, 0, 0, time.UTC)
	got = inferYear(feb29, ref2024)
	if got.Year() != 2024 || got.Month() != time.February || got.Day() != 29 {
		t.Errorf("Feb 29 with leap ref = %s, want 2024-02-29", got.Format("2006-01-02"))
	}
}

func TestIsLeap(t *testing.T) {
	cases := map[int]bool{
		2024: true, 2025: false, 2000: true, 1900: false, 2100: false, 2400: true,
	}
	for y, want := range cases {
		if got := isLeap(y); got != want {
			t.Errorf("isLeap(%d) = %v, want %v", y, got, want)
		}
	}
}

// TestParseLogTimeFeb29 exercises the full parse path for a Feb 29 clock entry.
func TestParseLogTimeFeb29(t *testing.T) {
	ref := time.Date(2025, time.March, 1, 0, 5, 0, 0, time.UTC) // non-leap year
	got, ok := parseLogTime("feb/29 10:20:30", ref)
	if !ok {
		t.Fatal("expected feb/29 to parse")
	}
	if got.Month() != time.February || got.Day() != 29 {
		t.Errorf("parseLogTime(feb/29) = %s, want a Feb 29 date (not normalized to Mar 1)", got.Format("2006-01-02"))
	}
}

func TestParseRouterClock(t *testing.T) {
	tests := []struct {
		name   string
		date   string
		clock  string
		offset string
		want   time.Time
		ok     bool
	}{
		{
			name:   "iso date, UTC offset",
			date:   "2024-06-15",
			clock:  "12:30:00",
			offset: "+00:00",
			want:   time.Date(2024, time.June, 15, 12, 30, 0, 0, time.UTC),
			ok:     true,
		},
		{
			name:   "month/day/year date",
			date:   "jun/15/2024",
			clock:  "12:30:00",
			offset: "+00:00",
			want:   time.Date(2024, time.June, 15, 12, 30, 0, 0, time.UTC),
			ok:     true,
		},
		{
			name:   "positive offset pins the instant",
			date:   "2024-06-15",
			clock:  "12:30:00",
			offset: "+02:00",
			// 12:30 at +02:00 == 10:30 UTC.
			want: time.Date(2024, time.June, 15, 10, 30, 0, 0, time.UTC),
			ok:   true,
		},
		{
			name:   "negative offset",
			date:   "2024-06-15",
			clock:  "12:30:00",
			offset: "-05:00",
			// 12:30 at -05:00 == 17:30 UTC.
			want: time.Date(2024, time.June, 15, 17, 30, 0, 0, time.UTC),
			ok:   true,
		},
		{
			name:   "missing date",
			date:   "",
			clock:  "12:30:00",
			offset: "+00:00",
			ok:     false,
		},
		{
			name:   "missing time",
			date:   "2024-06-15",
			clock:  "",
			offset: "+00:00",
			ok:     false,
		},
		{
			name:   "bad date",
			date:   "nope",
			clock:  "12:30:00",
			offset: "+00:00",
			ok:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRouterClock(tt.date, tt.clock, tt.offset, time.UTC)
			if ok != tt.ok {
				t.Fatalf("parseRouterClock(%q,%q,%q) ok = %v, want %v", tt.date, tt.clock, tt.offset, ok, tt.ok)
			}
			if tt.ok && !got.Equal(tt.want) {
				t.Errorf("parseRouterClock(%q,%q,%q) = %s (%s UTC), want %s",
					tt.date, tt.clock, tt.offset, got, got.UTC(), tt.want)
			}
		})
	}
}

func TestZoneFromGMTOffset(t *testing.T) {
	cases := []struct {
		in      string
		wantNil bool
		offset  int // seconds east of UTC, when not nil
	}{
		{"+00:00", false, 0},
		{"+02:00", false, 2 * 3600},
		{"-05:00", false, -5 * 3600},
		{"+05:30", false, 5*3600 + 30*60},
		{"+0200", false, 2 * 3600},
		{"", true, 0},
		{"garbage", true, 0},
		{"+99:99", true, 0},
	}
	for _, c := range cases {
		loc := zoneFromGMTOffset(c.in)
		if c.wantNil {
			if loc != nil {
				t.Errorf("zoneFromGMTOffset(%q) = %v, want nil", c.in, loc)
			}
			continue
		}
		if loc == nil {
			t.Errorf("zoneFromGMTOffset(%q) = nil, want a zone", c.in)
			continue
		}
		_, off := time.Now().In(loc).Zone()
		if off != c.offset {
			t.Errorf("zoneFromGMTOffset(%q) offset = %d, want %d", c.in, off, c.offset)
		}
	}
}

func TestTitleCaseMonth(t *testing.T) {
	cases := map[string]string{
		"jan/02":     "Jan/02",
		"Jan/02":     "Jan/02",
		"2024-01-02": "2024-01-02",
		"":           "",
	}
	for in, want := range cases {
		if got := titleCaseMonth(in); got != want {
			t.Errorf("titleCaseMonth(%q) = %q, want %q", in, got, want)
		}
	}
}

// clockReply builds a /system/clock/print reply anchored to UTC.
func clockReply(date, clk string) *routeros.Reply {
	return &routeros.Reply{Re: []*proto.Sentence{{Map: map[string]string{
		"date": date, "time": clk, "gmt-offset": "+00:00",
	}}}}
}

// logReplyWithTime builds a /log/print reply with the given per-entry maps.
func logReplyWithTime(maps ...map[string]string) *routeros.Reply {
	s := make([]*proto.Sentence, len(maps))
	for i, m := range maps {
		s[i] = &proto.Sentence{Map: m}
	}
	return &routeros.Reply{Re: s}
}

// TestLogCollectorUsesRouterClock verifies that Collect anchors year-less log
// timestamps to the router's clock rather than the local collection time.
func TestLogCollectorUsesRouterClock(t *testing.T) {
	// Router clock: 2024-06-15 12:00:00 UTC. A log entry stamped "09:30:15"
	// (clock only) must resolve to 2024-06-15 09:30:15, regardless of the
	// local machine's wall clock.
	fr := &fakeRunner{replies: map[string][]*routeros.Reply{
		"/system/clock/print": {
			clockReply("2024-06-15", "12:00:00"),
			clockReply("2024-06-15", "12:00:05"),
		},
		"/log/print": {
			// First poll primes the cursor (returns nothing).
			logReplyWithTime(map[string]string{".id": "*1", "time": "09:00:00", "topics": "system,info", "message": "boot"}),
			// Second poll: a new entry after *1.
			logReplyWithTime(
				map[string]string{".id": "*1", "time": "09:00:00", "topics": "system,info", "message": "boot"},
				map[string]string{".id": "*2", "time": "09:30:15", "topics": "system,info", "message": "later"},
			),
		},
	}}

	lc := NewLogCollector()
	// Force the fallback location to UTC so the router clock (which carries no
	// timezone) is anchored in UTC, making the assertion deterministic.
	lc.now = func() time.Time { return time.Date(2024, time.June, 15, 12, 0, 0, 0, time.UTC) }

	// First poll primes; returns nothing.
	if entries, err := lc.Collect(context.Background(), fr); err != nil || len(entries) != 0 {
		t.Fatalf("first poll: entries=%d err=%v, want 0 entries", len(entries), err)
	}

	// Second poll: the new entry's time is anchored to the router clock's date.
	entries, err := lc.Collect(context.Background(), fr)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("second poll returned %d entries, want 1", len(entries))
	}
	want := time.Date(2024, time.June, 15, 9, 30, 15, 0, time.UTC)
	if !entries[0].Time.Equal(want) {
		t.Errorf("entry time = %s, want %s", entries[0].Time, want)
	}
}

// TestLogCollectorFallsBackWhenClockUnavailable verifies that when the router
// clock command is unavailable and the entry time is unparseable, Collect uses
// the injected collection time.
func TestLogCollectorFallsBackWhenClockUnavailable(t *testing.T) {
	fixed := time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)

	fr := &fakeRunner{replies: map[string][]*routeros.Reply{
		// No /system/clock/print scripted -> empty reply -> fallback.
		"/log/print": {
			logReplyWithTime(map[string]string{".id": "*1", "message": "a"}),
			logReplyWithTime(
				map[string]string{".id": "*1", "message": "a"},
				// Unparseable time -> fallback to collection time.
				map[string]string{".id": "*2", "time": "???", "message": "b"},
			),
		},
	}}

	lc := NewLogCollector()
	lc.now = func() time.Time { return fixed }

	if _, err := lc.Collect(context.Background(), fr); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	entries, err := lc.Collect(context.Background(), fr)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("returned %d entries, want 1", len(entries))
	}
	if !entries[0].Time.Equal(fixed) {
		t.Errorf("entry time = %s, want fallback %s", entries[0].Time, fixed)
	}
}
