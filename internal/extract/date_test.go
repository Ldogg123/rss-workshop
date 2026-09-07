package extract

import (
	"strings"
	"testing"
	"time"
)

func dateZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func dateTime(t *testing.T, text string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseAbsoluteDate(t *testing.T) {
	zone := dateZone(t, "America/New_York")
	for _, tc := range []struct {
		name, raw, layout, want string
	}{
		{"rfc3339 nanoseconds", "2024-06-04T12:30:45.123456789+02:00", "", "2024-06-04T10:30:45.123456789Z"},
		{"rfc1123 offset", "Tue, 04 Jun 2024 12:30:45 +0200", "", "2024-06-04T10:30:45Z"},
		{"rfc1123 UTC", "Tue, 04 Jun 2024 12:30:45 UTC", "", "2024-06-04T12:30:45Z"},
		{"compact offset", "2024-06-04T12:30:45+0200", "", "2024-06-04T10:30:45Z"},
		{"minute offset", "2024-06-04T12:30+02:00", "", "2024-06-04T10:30:00Z"},
		{"minute compact offset", "2024-06-04T12:30+0200", "", "2024-06-04T10:30:00Z"},
		{"space offset", "2024-06-04 12:30:45+02:00", "", "2024-06-04T10:30:45Z"},
		{"space compact offset", "2024-06-04 12:30:45 +0200", "", "2024-06-04T10:30:45Z"},
		{"minute space offset", "2024-06-04 12:30 +02:00", "", "2024-06-04T10:30:00Z"},
		{"date in configured zone", "2024-06-04", "", "2024-06-04T04:00:00Z"},
		{"datetime local T", "2024-06-04T12:30:45", "", "2024-06-04T16:30:45Z"},
		{"datetime local minute", "2024-06-04T12:30", "", "2024-06-04T16:30:00Z"},
		{"datetime local space", "2024-06-04 12:30:45", "", "2024-06-04T16:30:45Z"},
		{"datetime local fraction", "2024-06-04 12:30:45.125", "", "2024-06-04T16:30:45.125Z"},
		{"custom format", "04/06/2024 12:30", "02/01/2006 15:04", "2024-06-04T16:30:00Z"},
		{"custom precedence", "2024-06-04", "2006-02-01", "2024-04-06T04:00:00Z"},
		{"standard fallback", "2024-06-04T12:30:45Z", "02/01/2006", "2024-06-04T12:30:45Z"},
		{"outer whitespace", " \n2024-06-04T12:30:45Z\t", "", "2024-06-04T12:30:45Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAbsoluteDate(tc.raw, tc.layout, zone)
			if !got.Equal(dateTime(t, tc.want)) || got.Location() != time.UTC {
				t.Fatalf("got %v, want UTC %s", got, tc.want)
			}
		})
	}
	for _, raw := range []string{"", "no date", "2 minutes ago", "2023-02-29", "0000-01-01", "10000-01-01", "0001-01-01T00:00:00+01:00", "9999-12-31T23:59:59-01:00", strings.Repeat(" ", 256) + "2024-06-04"} {
		if got := parseAbsoluteDate(raw, "", zone); !got.IsZero() {
			t.Errorf("accepted invalid or out-of-bounds date %q: %v", raw, got)
		}
	}
	if got := parseAbsoluteDate("2024-06-04", "", nil); !got.Equal(dateTime(t, "2024-06-04T00:00:00Z")) {
		t.Errorf("nil zone must default to UTC: %v", got)
	}
}

func TestParseRelativeDateGrammar(t *testing.T) {
	reference := dateTime(t, "2024-06-04T12:30:45.123456789Z")
	for _, tc := range []struct {
		raw string
		ago time.Duration
	}{
		{"2 seconds ago", 2 * time.Second},
		{"1 second ago", time.Second},
		{"a second ago", time.Second},
		{"2s ago", 2 * time.Second},
		{"2 sec ago", 2 * time.Second},
		{"2 secs ago", 2 * time.Second},
		{"2 minutes ago", 2 * time.Minute},
		{"a minute ago", time.Minute},
		{"2m ago", 2 * time.Minute},
		{"2 min ago", 2 * time.Minute},
		{"2 mins ago", 2 * time.Minute},
		{"an hour ago", time.Hour},
		{"2h ago", 2 * time.Hour},
		{"2 hr ago", 2 * time.Hour},
		{"2 hrs ago", 2 * time.Hour},
		{"2 days ago", 48 * time.Hour},
		{"a day ago", 24 * time.Hour},
		{"2d ago", 48 * time.Hour},
		{"2 weeks ago", 14 * 24 * time.Hour},
		{"a week ago", 7 * 24 * time.Hour},
		{"2w ago", 14 * 24 * time.Hour},
		{"2 wk ago", 14 * 24 * time.Hour},
		{"2 wks ago", 14 * 24 * time.Hour},
		{" \tABOUT\u00a0\u00a02 MiNuTeS \n AGO\t", 2 * time.Minute},
		{"about an hour ago", time.Hour},
		{"0 minutes ago", 0},
		{"000 seconds ago", 0},
		{"just now", 0},
		{"NOW", 0},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got := parseRelativeDate(tc.raw, reference, nil)
			if want := reference.Add(-tc.ago); !got.Equal(want) || got.Location() != time.UTC {
				t.Fatalf("got %v, want UTC %v", got, want)
			}
		})
	}
	for _, raw := range []string{
		"", "ago", "minute ago", "am ago", "anhr ago", "two minutes ago", "2 minutes", "2m", "2mo", "2 months",
		"in 2 minutes", "2 minutes from now", "tomorrow", "next month", "-2 minutes ago", "+2 minutes ago", "1.5 hours ago",
		"1 hour 2 minutes ago", "1,000 days ago", "updated 2 minutes ago", "2 minutes ago yesterday", "2 minutes ago!",
		"hace 2 minutos", "il y a 2 minutes", "2 milliseconds ago", "2 ms ago", "2 y ago", "about just now", "about 2 hours",
		"18446744073709551616 seconds ago", "18446744073709551615 months ago", "9999999999 years ago",
		"9223372037 seconds ago", "153722868 minutes ago", "106752 days ago", strings.Repeat(" ", 256) + "now",
	} {
		if got := parseRelativeDate(raw, reference, time.UTC); !got.IsZero() {
			t.Errorf("accepted unsupported relative date %q: %v", raw, got)
		}
	}
}

func TestParseRelativeCalendarAndZones(t *testing.T) {
	for _, tc := range []struct {
		name, raw, reference, zone, want string
	}{
		{"leap month end", "1 month ago", "2024-03-31T16:30:00Z", "America/New_York", "2024-02-29T17:30:00Z"},
		{"normal month end", "a month ago", "2023-03-31T16:30:00Z", "America/New_York", "2023-02-28T17:30:00Z"},
		{"compact month", "2mo ago", "2024-03-31T16:30:00Z", "America/New_York", "2024-01-31T17:30:00Z"},
		{"month abbreviation", "2 mos ago", "2024-03-31T16:30:00Z", "America/New_York", "2024-01-31T17:30:00Z"},
		{"year crossing", "3 months ago", "2024-02-29T10:30:00Z", "UTC", "2023-11-29T10:30:00Z"},
		{"leap year clamp", "1 year ago", "2024-02-29T12:30:45.123456789Z", "UTC", "2023-02-28T12:30:45.123456789Z"},
		{"leap year preserve", "4yr ago", "2024-02-29T12:30:00Z", "UTC", "2020-02-29T12:30:00Z"},
		{"year abbreviation", "2 yrs ago", "2024-02-29T12:30:00Z", "UTC", "2022-02-28T12:30:00Z"},
		{"year article", "about a year ago", "2024-02-29T12:30:00Z", "UTC", "2023-02-28T12:30:00Z"},
		{"zero months", "0 months ago", "2024-06-04T12:30:45.123456789Z", "America/New_York", "2024-06-04T12:30:45.123456789Z"},
		{"zero years", "0 years ago", "2024-06-04T12:30:45Z", "UTC", "2024-06-04T12:30:45Z"},
		{"zero months at repeated DST hour", "0 months ago", "2024-11-03T06:30:00Z", "America/New_York", "2024-11-03T06:30:00Z"},
		{"zero years at repeated DST hour", "0 years ago", "2024-11-03T06:30:00Z", "America/New_York", "2024-11-03T06:30:00Z"},
		{"calendar today at DST", "today", "2024-03-10T18:30:00Z", "America/New_York", "2024-03-10T05:00:00Z"},
		{"calendar yesterday at DST", "yesterday", "2024-03-11T04:30:00Z", "America/New_York", "2024-03-10T05:00:00Z"},
		{"elapsed day at DST", "1 day ago", "2024-03-10T18:30:00Z", "America/New_York", "2024-03-09T18:30:00Z"},
		{"elapsed week at DST", "1 week ago", "2024-03-12T18:30:00Z", "America/New_York", "2024-03-05T18:30:00Z"},
		{"today offset changes UTC day", "today", "2024-06-04T02:30:00Z", "America/Los_Angeles", "2024-06-03T07:00:00Z"},
		{"yesterday offset changes UTC day", "yesterday", "2024-06-04T23:30:00Z", "Asia/Tokyo", "2024-06-03T15:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reference := dateTime(t, tc.reference)
			got := parseRelativeDate(tc.raw, reference, dateZone(t, tc.zone))
			if !got.Equal(dateTime(t, tc.want)) || got.After(reference) || got.Location() != time.UTC {
				t.Fatalf("got %v, want UTC %s", got, tc.want)
			}
		})
	}
}

func TestParseRelativeDateBounds(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		reference time.Time
	}{
		{"now", time.Time{}},
		{"today", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"now", time.Date(0, 12, 31, 0, 0, 0, 0, time.UTC)},
		{"2 seconds ago", dateTime(t, "0001-01-01T00:00:01Z")},
		{"yesterday", dateTime(t, "0001-01-01T12:00:00Z")},
		{"1 month ago", dateTime(t, "0001-01-31T12:00:00Z")},
		{"2024 years ago", dateTime(t, "2024-06-04T12:00:00Z")},
		{"24282 months ago", dateTime(t, "2024-06-04T12:00:00Z")},
	} {
		if got := parseRelativeDate(tc.raw, tc.reference, time.UTC); !got.IsZero() {
			t.Errorf("expected failure for %q at %v, got %v", tc.raw, tc.reference, got)
		}
	}
	if got := parseRelativeDate("9998 years ago", dateTime(t, "9999-06-04T12:00:00Z"), time.UTC); !got.Equal(dateTime(t, "0001-06-04T12:00:00Z")) {
		t.Errorf("valid bounded calendar subtraction failed: %v", got)
	}
}
