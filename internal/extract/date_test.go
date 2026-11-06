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

func TestParseAbsoluteDateZoneNames(t *testing.T) {
	for _, tc := range []struct {
		name, raw, layout, zone, want string
	}{
		// RFC 5322 names when the recipe has no zone of its own.
		{"EST", "Thu, 05 Nov 2026 10:00:00 EST", "", "", "2026-11-05T15:00:00Z"},
		{"EDT", "Thu, 05 Nov 2026 10:00:00 EDT", "", "", "2026-11-05T14:00:00Z"},
		{"CST", "Thu, 05 Nov 2026 10:00:00 CST", "", "", "2026-11-05T16:00:00Z"},
		{"CDT", "Thu, 05 Nov 2026 10:00:00 CDT", "", "", "2026-11-05T15:00:00Z"},
		{"MST", "Thu, 05 Nov 2026 10:00:00 MST", "", "", "2026-11-05T17:00:00Z"},
		{"MDT", "Thu, 05 Nov 2026 10:00:00 MDT", "", "", "2026-11-05T16:00:00Z"},
		{"PST", "Thu, 05 Nov 2026 10:00:00 PST", "", "", "2026-11-05T18:00:00Z"},
		{"PDT", "Thu, 05 Nov 2026 10:00:00 PDT", "", "", "2026-11-05T17:00:00Z"},
		{"GMT", "Thu, 05 Nov 2026 10:00:00 GMT", "", "", "2026-11-05T10:00:00Z"},
		{"UT", "Thu, 05 Nov 2026 10:00:00 UT", "", "", "2026-11-05T10:00:00Z"},
		{"UTC in another zone", "Thu, 05 Nov 2026 10:00:00 UTC", "", "Asia/Tokyo", "2026-11-05T10:00:00Z"},
		{"one-digit day", "Thu, 5 Nov 2026 10:00:00 GMT", "", "", "2026-11-05T10:00:00Z"},
		{"no seconds", "Thu, 5 Nov 2026 10:00 EST", "", "", "2026-11-05T15:00:00Z"},
		{"no seconds two-digit day", "Thu, 05 Nov 2026 10:00 PDT", "", "", "2026-11-05T17:00:00Z"},
		{"one-digit day offset", "Thu, 5 Nov 2026 10:00:00 -0500", "", "", "2026-11-05T15:00:00Z"},
		{"no seconds offset", "Thu, 5 Nov 2026 10:00 +0100", "", "", "2026-11-05T09:00:00Z"},
		// Go reads the hours of GMT+3 but would leave the clock as UTC.
		{"GMT plus hours", "Thu, 05 Nov 2026 10:00:00 GMT+3", "", "", "2026-11-05T07:00:00Z"},
		{"GMT minus hours", "Thu, 05 Nov 2026 10:00:00 GMT-5", "", "", "2026-11-05T15:00:00Z"},
		// The recipe's zone gives a name its meaning first.
		{"China Standard Time", "Thu, 05 Nov 2026 10:00:00 CST", "", "Asia/Shanghai", "2026-11-05T02:00:00Z"},
		{"Philippine Standard Time", "Thu, 05 Nov 2026 10:00:00 PST", "", "Asia/Manila", "2026-11-05T02:00:00Z"},
		{"Cuba Standard Time", "Thu, 05 Nov 2026 10:00:00 CST", "", "America/Havana", "2026-11-05T15:00:00Z"},
		{"India Standard Time", "Thu, 05 Nov 2026 10:00:00 IST", "", "Asia/Kolkata", "2026-11-05T04:30:00Z"},
		{"Central European Time", "Thu, 05 Nov 2026 10:00:00 CET", "", "Europe/Paris", "2026-11-05T09:00:00Z"},
		{"US Central in Chicago", "Thu, 05 Nov 2026 10:00:00 CST", "", "America/Chicago", "2026-11-05T16:00:00Z"},
		{"zone's own name", "Thu, 05 Nov 2026 10:00:00 EST", "", "America/New_York", "2026-11-05T15:00:00Z"},
		{"name the zone lacks", "Thu, 05 Nov 2026 10:00:00 PST", "", "America/New_York", "2026-11-05T18:00:00Z"},
		{"GMT where the zone lacks it", "Thu, 05 Nov 2026 10:00:00 GMT", "", "America/New_York", "2026-11-05T10:00:00Z"},
		// A name stated out of season keeps the zone's current meaning, not
		// the first historical one Go would choose (Dublin's 1916 +00:34:39).
		{"daylight name in winter", "Thu, 05 Nov 2026 10:00:00 EDT", "", "America/New_York", "2026-11-05T14:00:00Z"},
		{"standard name in summer", "Thu, 02 Jul 2026 10:00:00 EST", "", "America/New_York", "2026-07-02T15:00:00Z"},
		{"Irish Standard Time in summer", "Thu, 02 Jul 2026 10:00:00 IST", "", "Europe/Dublin", "2026-07-02T09:00:00Z"},
		{"Irish Standard Time in winter", "Thu, 05 Nov 2026 10:00:00 IST", "", "Europe/Dublin", "2026-11-05T09:00:00Z"},
		{"GMT in a London summer", "Thu, 02 Jul 2026 10:00:00 GMT", "", "Europe/London", "2026-07-02T10:00:00Z"},
		{"New Zealand standard in summer", "Thu, 01 Jan 2026 10:00:00 NZST", "", "Pacific/Auckland", "2025-12-31T22:00:00Z"},
		// An operator's layout follows the same rules.
		{"layout with a name", "05/11/2026 10:00 EST", "02/01/2006 15:04 MST", "", "2026-11-05T15:00:00Z"},
		{"layout name in recipe zone", "05/11/2026 10:00 CST", "02/01/2006 15:04 MST", "Asia/Shanghai", "2026-11-05T02:00:00Z"},
		{"layout offset beside a name", "05/11/2026 10:00 +0100 XYZ", "02/01/2006 15:04 -0700 MST", "", "2026-11-05T09:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			zone := time.UTC
			if tc.zone != "" {
				zone = dateZone(t, tc.zone)
			}
			got := parseAbsoluteDate(tc.raw, tc.layout, zone)
			if !got.Equal(dateTime(t, tc.want)) || got.Location() != time.UTC {
				t.Fatalf("got %v, want UTC %s", got, tc.want)
			}
		})
	}
	// Go gives a name it cannot resolve an invented zero offset. Publishing
	// that as UTC would be hours wrong, so the date is not recognized.
	for _, tc := range []struct{ raw, layout, zone string }{
		{"Thu, 05 Nov 2026 10:00:00 XYZ", "", ""},
		{"Thu, 05 Nov 2026 10:00:00 CET", "", ""},
		{"Thu, 05 Nov 2026 10:00:00 IST", "", ""},
		{"Thu, 05 Nov 2026 10:00:00 +03", "", ""},
		{"Thu, 05 Nov 2026 10:00:00 CET", "", "America/New_York"},
		{"Thu, 05 Nov 2026 10:00 AEST", "", "Europe/Paris"},
		{"05/11/2026 10:00 XYZ", "02/01/2006 15:04 MST", ""},
		{"Fri, 31 Dec 9999 23:00:00 EST", "", ""},
		{"Mon, 1 Jan 0001 01:00:00 GMT+3", "", ""},
		{"Thu, 05 Nov 2026 10:00:00 UTC UT", "", ""},
	} {
		zone := time.UTC
		if tc.zone != "" {
			zone = dateZone(t, tc.zone)
		}
		if got := parseAbsoluteDate(tc.raw, tc.layout, zone); !got.IsZero() {
			t.Errorf("accepted %q (layout %q, zone %q) as %v", tc.raw, tc.layout, tc.zone, got)
		}
	}
}

func TestParseMonthNameDate(t *testing.T) {
	zone := dateZone(t, "America/New_York")
	for _, tc := range []struct{ raw, want string }{
		{"5 November 2026", "2026-11-05T05:00:00Z"},
		{"November 5, 2026", "2026-11-05T05:00:00Z"},
		{"Nov 5, 2026", "2026-11-05T05:00:00Z"},
		{"5 Nov 2026", "2026-11-05T05:00:00Z"},
		{"Nov. 5 2026", "2026-11-05T05:00:00Z"},
		{"NOVEMBER 5TH, 2026", "2026-11-05T05:00:00Z"},
		{"1st May 2026", "2026-05-01T04:00:00Z"},
		{"Sept. 22nd, 2026", "2026-09-22T04:00:00Z"},
		{"sept 3 2026", "2026-09-03T04:00:00Z"},
		{"29 February 2024", "2024-02-29T05:00:00Z"},
		{" Nov 5,\n  2026\t", "2026-11-05T05:00:00Z"},
		{"Thursday, 5th November 2026 at 3:04 pm", "2026-11-05T20:04:00Z"},
		{"Thu, Nov 5, 2026, 3:04 PM", "2026-11-05T20:04:00Z"},
		{"thu. 5 nov. 2026 at 3:04:05 p.m.", "2026-11-05T20:04:05Z"},
		{"November 5, 2026, at 9:30am", "2026-11-05T14:30:00Z"},
		{"5 Nov 2026 15:04", "2026-11-05T20:04:00Z"},
		{"5 Nov 2026 12:00 am", "2026-11-05T05:00:00Z"},
		{"5 Nov 2026 12:30 PM", "2026-11-05T17:30:00Z"},
		{"5 Nov 2026 10:00 EST", "2026-11-05T15:00:00Z"},
		{"5 Nov 2026 10:00 PST", "2026-11-05T18:00:00Z"},
		{"Nov 5, 2026 10:00 AM GMT", "2026-11-05T10:00:00Z"},
		{"5 Nov 2026 10:00 UTC", "2026-11-05T10:00:00Z"},
		{"5 Nov 2026 10:00 UT", "2026-11-05T10:00:00Z"},
		{"5 Nov 2026 10:00 +0100", "2026-11-05T09:00:00Z"},
		{"5 Nov 2026 10:00:30 -05:30", "2026-11-05T15:30:30Z"},
		// Examples in docs/dates.md.
		{"5th Nov. 2026", "2026-11-05T05:00:00Z"},
		{"Nov 5 2026", "2026-11-05T05:00:00Z"},
		{"Thu, Nov 5, 2026, 15:04", "2026-11-05T20:04:00Z"},
		{"Nov 5, 2026 10:00:30 AM +01:00", "2026-11-05T09:00:30Z"},
		{"Mon, 7 Sep 2026 14:28 EDT", "2026-09-07T18:28:00Z"},
		{"Mon, 07 Sep 2026 14:28:00 -0400", "2026-09-07T18:28:00Z"},
		{"2026-09-07 14:28:00+02:00", "2026-09-07T12:28:00Z"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got := parseDateText(tc.raw, "", zone)
			if !got.Equal(dateTime(t, tc.want)) || got.Location() != time.UTC {
				t.Fatalf("got %v, want UTC %s", got, tc.want)
			}
		})
	}
	if got := parseDateText("5 Nov 2026 10:00 CST", "", dateZone(t, "Asia/Shanghai")); !got.Equal(dateTime(t, "2026-11-05T02:00:00Z")) {
		t.Errorf("recipe zone must give CST its meaning first: %v", got)
	}
	if got := parseDateText("5 November 2026", "", nil); !got.Equal(dateTime(t, "2026-11-05T00:00:00Z")) {
		t.Errorf("nil zone must default to UTC: %v", got)
	}
	for _, raw := range []string{
		"31 February 2026", "29 February 2026", "Nov 31, 2026", "0 Nov 2026", "32 Nov 2026", "5 Nov 0000",
		"Monday, November 5, 2026", "Mon, 5 Nov 2026", "Nov 5, 26", "5 Nov 26", "Nov 5", "November 2026", "5 Nov, 2026",
		"Updated Nov 5, 2026", "Posted on 5 November 2026", "Nov 5, 2026.", "Nov 5, 2026 by Staff", "5 Novembre 2026",
		"5 Nov 2026 13:00 pm", "5 Nov 2026 0:30 am", "5 Nov 2026 24:00", "5 Nov 2026 10:60", "5 Nov 2026 10:00:60",
		"5 Nov 2026 10", "5 Nov 2026 3 pm", "5 Nov 2026 EST", "5 Nov 2026 10:00 XYZ", "5 Nov 2026 10:00 CET", "5 Nov 2026 10:00 +2500",
		"31 Dec 9999 23:00 EST", "ſept 5, 2026", strings.Repeat(" ", 256) + "5 Nov 2026",
	} {
		if got := parseDateText(raw, "", zone); !got.IsZero() {
			t.Errorf("accepted unsupported month-name date %q: %v", raw, got)
		}
	}
	// Month names are for text; automatically read datetime attributes keep
	// the standard machine formats.
	if got := parseAbsoluteDate("5 November 2026", "", zone); !got.IsZero() {
		t.Errorf("standard formats accepted a month-name date: %v", got)
	}
}
