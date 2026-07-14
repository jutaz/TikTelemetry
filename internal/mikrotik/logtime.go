package mikrotik

import (
	"strings"
	"time"
)

// RouterOS /log/print "time" fields come in several shapes and, critically,
// usually omit the year:
//
//	"15:04:05"                 clock only (entries from "today")
//	"jan/02 15:04:05"          month/day + clock (older entries, no year)
//	"2006-01-02 15:04:05"      ISO-ish full date (some clock configs)
//	"jan/02/2006 15:04:05"     month/day/year + clock (some exports)
//	"dec/31 23:59:59"          near a year boundary
//
// Newer RouterOS also prefixes some entries with relative markers we ignore
// here. parseLogTime resolves a field into an absolute time, anchored to a
// reference "now" (the router's current time) so year/date inference is
// correct even across a new-year boundary. On any parse failure it returns
// ok=false and the caller falls back to collection time.

// These layouts include a year and can be parsed directly. Month tokens use
// Go's reference "Jan"; RouterOS lowercase months are title-cased first.
var logTimeLayoutsWithYear = []string{
	"2006-01-02 15:04:05",
	"Jan/02/2006 15:04:05",
	"2006-01-02 15:04:05.000",
}

// These layouts lack a year; the year is inferred from the reference time.
var logTimeLayoutsNoYear = []string{
	"Jan/02 15:04:05", // "jan/02 15:04:05" after title-casing the month
	"01/02 15:04:05",
}

// parseLogTime converts a RouterOS log time string into an absolute time using
// ref (the router's current wall clock) to supply the missing year/date and the
// location. It returns ok=false when the field cannot be parsed.
func parseLogTime(field string, ref time.Time) (time.Time, bool) {
	field = strings.TrimSpace(field)
	if field == "" {
		return time.Time{}, false
	}
	loc := ref.Location()
	if loc == nil {
		loc = time.Local
	}

	// RouterOS emits lowercase month abbreviations ("jan"); Go's reference
	// layout expects title case ("Jan"). Normalise the first letter of a
	// leading alphabetic token.
	normalized := titleCaseMonth(field)

	// 1. Full-date layouts: parse directly in the reference location.
	for _, layout := range logTimeLayoutsWithYear {
		if t, err := time.ParseInLocation(layout, normalized, loc); err == nil {
			return t, true
		}
	}

	// 2. Month/day + clock (no year): parse, then attach the inferred year.
	for _, layout := range logTimeLayoutsNoYear {
		if t, err := time.ParseInLocation(layout, normalized, loc); err == nil {
			return inferYear(t, ref), true
		}
	}

	// 3. Clock only ("15:04:05"): the entry is on the reference date.
	if t, err := time.ParseInLocation("15:04:05", normalized, loc); err == nil {
		candidate := time.Date(ref.Year(), ref.Month(), ref.Day(),
			t.Hour(), t.Minute(), t.Second(), 0, loc)
		// If that clock is in the future relative to ref (e.g. an entry at
		// 23:59 read just after midnight), it belongs to the previous day.
		if candidate.After(ref.Add(1 * time.Minute)) {
			candidate = candidate.AddDate(0, 0, -1)
		}
		return candidate, true
	}

	return time.Time{}, false
}

// inferYear attaches a year to a month/day time parsed with year 0. It picks the
// year that places the entry at or before ref; if the resulting date would be in
// the future (e.g. a Dec entry read in early Jan), it uses the previous year.
func inferYear(t, ref time.Time) time.Time {
	loc := ref.Location()
	if loc == nil {
		loc = time.Local
	}
	candidate := dateInYear(ref.Year(), t, loc)
	// Allow a small skew so an entry stamped a few seconds ahead of the
	// reference (clock jitter) is not pushed back a whole year.
	if candidate.After(ref.Add(24 * time.Hour)) {
		candidate = dateInYear(ref.Year()-1, t, loc)
	}
	return candidate
}

// dateInYear builds a time at the given year using t's month/day/clock. It
// guards against Go's time.Date normalization silently shifting an invalid
// date: a Feb 29 entry placed in a non-leap year would otherwise become Mar 1.
// In that case it walks back to the nearest earlier leap year so the day is
// preserved rather than corrupted.
func dateInYear(year int, t time.Time, loc *time.Location) time.Time {
	if t.Month() == time.February && t.Day() == 29 && !isLeap(year) {
		for y := year - 1; y >= year-4; y-- {
			if isLeap(y) {
				year = y
				break
			}
		}
	}
	return time.Date(year, t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second(), 0, loc)
}

func isLeap(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

// zoneFromGMTOffset parses a RouterOS gmt-offset string like "+00:00",
// "-05:30", or "+0200" into a *time.Location. Returns nil when the input is
// empty or malformed so the caller can fall back.
func zoneFromGMTOffset(s string) *time.Location {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	sign := 1
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		sign = -1
		s = s[1:]
	}
	s = strings.ReplaceAll(s, ":", "")
	if len(s) != 4 {
		return nil
	}
	hh := int(s[0]-'0')*10 + int(s[1]-'0')
	mm := int(s[2]-'0')*10 + int(s[3]-'0')
	if s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' ||
		s[2] < '0' || s[2] > '9' || s[3] < '0' || s[3] > '9' {
		return nil
	}
	if hh > 14 || mm > 59 {
		return nil
	}
	offset := sign * (hh*3600 + mm*60)
	if offset == 0 {
		return time.UTC
	}
	return time.FixedZone("router", offset)
}

// clockDateLayouts are the date formats RouterOS /system/clock returns across
// versions.
var clockDateLayouts = []string{
	"2006-01-02",  // newer RouterOS
	"Jan/02/2006", // older RouterOS (after month title-casing)
	"jan/2/2006",  // defensive: single-digit day variants
	"2006/01/02",  //
}

// parseRouterClock combines the date, time, and gmt-offset fields from
// /system/clock/print into an absolute time. The gmt-offset (e.g. "+00:00",
// "-05:00") pins the router's wall clock to a real instant regardless of where
// the agent runs; when it is empty or unparseable, fallbackLoc is used. Returns
// ok=false when the date or time field is missing or unparseable.
func parseRouterClock(date, clock, gmtOffset string, fallbackLoc *time.Location) (time.Time, bool) {
	date = strings.TrimSpace(date)
	clock = strings.TrimSpace(clock)
	if date == "" || clock == "" {
		return time.Time{}, false
	}
	loc := zoneFromGMTOffset(gmtOffset)
	if loc == nil {
		loc = fallbackLoc
	}
	if loc == nil {
		loc = time.Local
	}

	var day time.Time
	var ok bool
	normalizedDate := titleCaseMonth(date)
	for _, layout := range clockDateLayouts {
		if d, err := time.ParseInLocation(layout, normalizedDate, loc); err == nil {
			day, ok = d, true
			break
		}
	}
	if !ok {
		return time.Time{}, false
	}

	clk, err := time.ParseInLocation("15:04:05", clock, loc)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(day.Year(), day.Month(), day.Day(),
		clk.Hour(), clk.Minute(), clk.Second(), 0, loc), true
}

// titleCaseMonth upper-cases the first letter of a leading alphabetic month
// token (e.g. "jan/02 ..." -> "Jan/02 ...") so Go's time layouts match. It
// leaves numeric and already-capitalised input untouched.
func titleCaseMonth(s string) string {
	if s == "" {
		return s
	}
	c := s[0]
	if c >= 'a' && c <= 'z' {
		return string(c-'a'+'A') + s[1:]
	}
	return s
}
