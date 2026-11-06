package extract

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxDateTextBytes = 256

var absoluteDateLayouts = []string{
	time.RFC3339Nano,
	// RFC 1123 and RFC 5322 dates. A day of 2 reads one or two digits.
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 2 Jan 2006 15:04 -0700",
	"Mon, 2 Jan 2006 15:04 MST",
	"2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04Z07:00",
	"2006-01-02T15:04Z0700",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05Z0700",
	"2006-01-02 15:04Z07:00",
	"2006-01-02 15:04Z0700",
	"2006-01-02 15:04:05 Z07:00",
	"2006-01-02 15:04:05 Z0700",
	"2006-01-02 15:04 Z07:00",
	"2006-01-02 15:04 Z0700",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// rfc5322Zones holds the zone names RFC 5322 section 4.3 defines, used when
// the recipe's zone does not give a name a meaning of its own. Go does not
// read a two-letter zone name, so UT is passed to Go as UTC instead.
var rfc5322Zones = map[string]int{
	"GMT": 0,
	"EST": -5 * 3600, "EDT": -4 * 3600,
	"CST": -6 * 3600, "CDT": -5 * 3600,
	"MST": -7 * 3600, "MDT": -6 * 3600,
	"PST": -8 * 3600, "PDT": -7 * 3600,
}

// parseAbsoluteDate prefers an explicit layout, then recognizes common machine
// dates independently of that layout. A zone-less date uses the recipe's zone.
func parseAbsoluteDate(raw, layout string, loc *time.Location) time.Time {
	if len(raw) > maxDateTextBytes {
		return time.Time{}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if loc == nil {
		loc = time.UTC
	}
	if layout != "" {
		if t, ok := parseInZone(layout, raw, loc); ok {
			return t
		}
	}
	// The standard layouts read RFC 5322's UT as UTC. An operator's layout
	// has already seen the value as written.
	if strings.HasSuffix(raw, " UT") {
		raw += "C"
	}
	for _, candidate := range absoluteDateLayouts {
		if t, ok := parseInZone(candidate, raw, loc); ok {
			return t
		}
	}
	return time.Time{}
}

// parseInZone parses raw with layout in loc. A zone name in the value takes
// the meaning the recipe's zone gives it, then the meaning RFC 5322 gives it.
// Go accepts any other name as a zone with no offset, which would publish the
// wrong time, so such a date is refused instead.
func parseInZone(layout, raw string, loc *time.Location) (time.Time, bool) {
	t, err := time.ParseInLocation(layout, raw, loc)
	if err != nil {
		return time.Time{}, false
	}
	// A numeric offset decides the time even beside a name, and Go reads
	// "UTC" itself, so only a layout with a name alone needs resolving.
	if strings.Contains(layout, "MST") && !strings.Contains(layout, "-07") && !strings.Contains(layout, "Z07") && t.Location() != time.UTC {
		var ok bool
		if t, ok = resolveZoneName(layout, raw, t, loc); !ok {
			return time.Time{}, false
		}
	}
	if !supportedDate(t) {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// resolveZoneName corrects t, which Go parsed from a value naming its zone.
func resolveZoneName(layout, raw string, t time.Time, loc *time.Location) (time.Time, bool) {
	// Parsed in UTC, where Go knows no names, the value keeps its stated name
	// and its wall clock reads as UTC, so subtracting an offset gives the time.
	wall, err := time.ParseInLocation(layout, raw, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	name, offset := wall.Zone()
	if t.Location() == loc {
		// The recipe's zone uses this name. Go takes the meaning in effect at
		// that time, but for a name stated out of season, such as IST in a
		// Dublin winter, it takes the zone's first use, which can be a
		// historical offset. The zone's use nearest the date is the meaning;
		// a name the zone has not used within a year keeps Go's reading.
		if current, _ := t.Zone(); current == name {
			return t, true
		}
		if nearby, ok := nearbyZoneOffset(loc, name, t); ok {
			return wall.Add(-time.Duration(nearby) * time.Second), true
		}
		return t, true
	}
	if standard, ok := rfc5322Zones[name]; ok {
		return wall.Add(-time.Duration(standard) * time.Second), true
	}
	// Go reads the offset of a name such as GMT+3 but leaves the wall clock
	// as UTC. Any other name it does not know gets an invented zero offset.
	if offset != 0 {
		return wall.Add(-time.Duration(offset) * time.Second), true
	}
	return time.Time{}, false
}

// nearbyZoneOffset reports the offset loc gives the zone name closest to t,
// looking a week at a time up to a year either side.
func nearbyZoneOffset(loc *time.Location, name string, t time.Time) (int, bool) {
	const week = 7 * 24 * time.Hour
	for i := time.Duration(1); i <= 53; i++ {
		for _, probe := range [...]time.Time{t.Add(-i * week), t.Add(i * week)} {
			if current, offset := probe.In(loc).Zone(); current == name {
				return offset, true
			}
		}
	}
	return 0, false
}

// parseDateText reads element text or an explicitly selected value. After the
// layouts parseAbsoluteDate knows, it recognizes English dates written with a
// month name, which automatically detected datetime attributes do not use.
func parseDateText(raw, layout string, loc *time.Location) time.Time {
	if t := parseAbsoluteDate(raw, layout, loc); !t.IsZero() {
		return t
	}
	return parseMonthNameDate(raw, loc)
}

const (
	weekdayNames = `(?:mon|tue|wed|thu|fri|sat|sun)\.?|monday|tuesday|wednesday|thursday|friday|saturday|sunday`
	monthNames   = `january|february|march|april|may|june|july|august|september|october|november|december|(?:jan|feb|mar|apr|jun|jul|aug|sept?|oct|nov|dec)\.?`
	dayOfMonth   = `(\d{1,2})(?:st|nd|rd|th)?`
)

// monthNameDatePattern matches a whole date such as "5 November 2026",
// "Thursday, Nov. 5th, 2026 at 3:04 pm" or "5 Nov 2026 10:00 EST". Text
// around the date, a two-digit year or a missing year does not match.
var monthNameDatePattern = regexp.MustCompile(`(?i)^(?:(` + weekdayNames + `),? )?` +
	`(?:` + dayOfMonth + ` (` + monthNames + `) (\d{4})|(` + monthNames + `) ` + dayOfMonth + `,? (\d{4}))` +
	`(?:(?:,? at|,)? (\d{1,2}):(\d{2})(?::(\d{2}))?(?: ?([ap])\.?m\.?)?` +
	`(?: ([+-]\d{2}:?\d{2}|(?-i:[A-Z][A-Za-z]{1,4})))?)?$`)

var (
	monthNumbers = map[string]time.Month{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	weekdays     = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}
)

// parseMonthNameDate recognizes an English date with a month name. A date
// without a time is local midnight in loc, as an ISO date is, and a stated
// zone name resolves as it does in the standard layouts. A weekday that does
// not match the date makes the date unrecognized, since either may be wrong.
func parseMonthNameDate(raw string, loc *time.Location) time.Time {
	if len(raw) > maxDateTextBytes {
		return time.Time{}
	}
	if loc == nil {
		loc = time.UTC
	}
	m := monthNameDatePattern.FindStringSubmatch(strings.Join(strings.Fields(raw), " "))
	if m == nil {
		return time.Time{}
	}
	day, monthName, year := m[2], m[3], m[4]
	if monthName == "" {
		monthName, day, year = m[5], m[6], m[7]
	}
	month, ok := monthNumbers[nameKey(monthName)]
	if !ok {
		return time.Time{}
	}
	d, _ := strconv.Atoi(day)
	y, _ := strconv.Atoi(year)
	layout, value := "2006-01-02", fmt.Sprintf("%s-%02d-%02d", year, month, d)
	if m[8] != "" {
		hour, _ := strconv.Atoi(m[8])
		if m[11] != "" {
			if hour < 1 || hour > 12 {
				return time.Time{}
			}
			hour %= 12
			if strings.EqualFold(m[11], "p") {
				hour += 12
			}
		}
		second := m[10]
		if second == "" {
			second = "00"
		}
		layout += " 15:04:05"
		value += fmt.Sprintf(" %02d:%s:%s", hour, m[9], second)
		switch zone := m[12]; {
		case zone == "":
		case zone[0] == '+' || zone[0] == '-':
			layout, value = layout+" -0700", value+" "+strings.Replace(zone, ":", "", 1)
		case zone == "UT":
			layout, value = layout+" MST", value+" UTC"
		default:
			layout, value = layout+" MST", value+" "+zone
		}
	}
	t, ok := parseInZone(layout, value, loc)
	if !ok {
		return time.Time{}
	}
	if m[1] != "" {
		if weekday, ok := weekdays[nameKey(m[1])]; !ok || time.Date(y, month, d, 0, 0, 0, 0, time.UTC).Weekday() != weekday {
			return time.Time{}
		}
	}
	return t
}

// nameKey gives the three-letter key of a month or weekday name.
func nameKey(name string) string {
	name = strings.ToLower(name)
	if len(name) < 3 {
		return ""
	}
	return name[:3]
}

var relativeDatePattern = regexp.MustCompile(`^(?:about )?([0-9]+|a|an)( ?)(seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h|days?|d|weeks?|wks?|w|months?|mos?|years?|yrs?|yr) ago$`)

// parseRelativeDate deliberately recognizes complete, past-only English labels.
// Numeric days and weeks are elapsed durations; months and years preserve the
// local clock and clamp to the last valid day of the destination month.
func parseRelativeDate(raw string, reference time.Time, loc *time.Location) time.Time {
	if len(raw) > maxDateTextBytes || !supportedDate(reference) {
		return time.Time{}
	}
	if loc == nil {
		loc = time.UTC
	}
	raw = strings.ToLower(strings.Join(strings.Fields(raw), " "))
	var result time.Time
	switch raw {
	case "now", "just now":
		result = reference
	case "today", "yesterday":
		local := reference.In(loc)
		year, month, day := local.Date()
		if raw == "yesterday" {
			day--
		}
		result = time.Date(year, month, day, 0, 0, 0, 0, loc)
	default:
		parts := relativeDatePattern.FindStringSubmatch(raw)
		if parts == nil {
			return time.Time{}
		}
		var count uint64
		if parts[1] == "a" || parts[1] == "an" {
			if parts[2] == "" {
				return time.Time{}
			}
			count = 1
		} else {
			var err error
			count, err = strconv.ParseUint(parts[1], 10, 64)
			if err != nil {
				return time.Time{}
			}
		}
		var unit time.Duration
		switch parts[3] {
		case "second", "seconds", "sec", "secs", "s":
			unit = time.Second
		case "minute", "minutes", "min", "mins", "m":
			unit = time.Minute
		case "hour", "hours", "hr", "hrs", "h":
			unit = time.Hour
		case "day", "days", "d":
			unit = 24 * time.Hour
		case "week", "weeks", "wk", "wks", "w":
			unit = 7 * 24 * time.Hour
		case "month", "months", "mo", "mos":
			result = subtractCalendar(reference, loc, count, false)
		case "year", "years", "yr", "yrs":
			result = subtractCalendar(reference, loc, count, true)
		}
		if unit != 0 {
			if count > uint64(1<<63-1)/uint64(unit) {
				return time.Time{}
			}
			result = reference.Add(-time.Duration(count) * unit)
		}
	}
	if !supportedDate(result) || result.After(reference) {
		return time.Time{}
	}
	return result.UTC()
}

func subtractCalendar(reference time.Time, loc *time.Location, count uint64, years bool) time.Time {
	// Reconstructing a repeated local hour during a DST transition can select its
	// other occurrence, so a zero subtraction must preserve the original instant.
	if count == 0 {
		return reference
	}
	local := reference.In(loc)
	year, month, day := local.Date()
	if year < 1 || year > 9999 {
		return time.Time{}
	}
	if years {
		if count >= uint64(year) {
			return time.Time{}
		}
		year -= int(count)
	} else {
		monthIndex := (year-1)*12 + int(month) - 1
		if count > uint64(monthIndex) {
			return time.Time{}
		}
		monthIndex -= int(count)
		year, month = monthIndex/12+1, time.Month(monthIndex%12+1)
	}
	// Compute the day count in UTC so a local midnight transition cannot alter it.
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > lastDay {
		day = lastDay
	}
	hour, minute, second := local.Clock()
	return time.Date(year, month, day, hour, minute, second, local.Nanosecond(), loc)
}

func supportedDate(t time.Time) bool {
	return !t.IsZero() && t.UTC().Year() >= 1 && t.UTC().Year() <= 9999
}
