package extract

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxDateTextBytes = 256

var absoluteDateLayouts = []string{
	time.RFC3339Nano,
	time.RFC1123Z,
	time.RFC1123,
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
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil && supportedDate(t) {
			return t.UTC()
		}
	}
	for _, candidate := range absoluteDateLayouts {
		if t, err := time.ParseInLocation(candidate, raw, loc); err == nil && supportedDate(t) {
			return t.UTC()
		}
	}
	return time.Time{}
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
