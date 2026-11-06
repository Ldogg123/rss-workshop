package model

import (
	"testing"
	"time"
)

func TestQuietThresholdScalesWithTheIntervalAboveAFloor(t *testing.T) {
	day := 24 * time.Hour
	for _, c := range []struct {
		interval int
		want     time.Duration
	}{
		{60, 14 * day},     // the editor's shortest interval
		{900, 14 * day},    // the default
		{86400, 14 * day},  // ten days is below the floor
		{172800, 20 * day}, // every two days
		{604800, 70 * day}, // the longest interval
	} {
		if got := QuietAfter(c.interval); got != c.want {
			t.Errorf("QuietAfter(%d) = %v, want %v", c.interval, got, c.want)
		}
	}
}

func TestQuietNeedsAnEnabledFeedThatKeepsSucceeding(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	succeeded := now.Add(-time.Hour)
	quiet := Feed{Enabled: true, Interval: 900, LastAttempt: succeeded, LastSuccess: succeeded, LastChanged: now.Add(-QuietFloor)}
	if !quiet.IsQuiet(now) {
		t.Fatal("a feed unchanged for exactly the threshold is not quiet")
	}
	for name, change := range map[string]func(*Feed){
		"just under the threshold": func(f *Feed) { f.LastChanged = now.Add(-QuietFloor + time.Second) },
		// Ten weekly refreshes, not the floor, set the threshold here.
		"a weekly feed past the floor":  func(f *Feed) { f.Interval = 604800; f.LastChanged = now.Add(-QuietFloor - 30*24*time.Hour) },
		"paused":                        func(f *Feed) { f.Enabled = false },
		"failing":                       func(f *Feed) { f.Failures = 1 },
		"never refreshed":               func(f *Feed) { f.LastSuccess, f.LastAttempt, f.LastChanged = time.Time{}, time.Time{}, time.Time{} },
		"never published":               func(f *Feed) { f.LastChanged = time.Time{} },
		"last attempt failed then edit": func(f *Feed) { f.LastAttempt = succeeded.Add(time.Minute) },
		"changed in the future":         func(f *Feed) { f.LastChanged = now.Add(time.Second) },
	} {
		f := quiet
		change(&f)
		if f.IsQuiet(now) {
			t.Errorf("%s: reported quiet", name)
		}
	}
	weekly := quiet
	weekly.Interval = 604800
	weekly.LastChanged = now.Add(-QuietAfter(weekly.Interval))
	if !weekly.IsQuiet(now) {
		t.Error("a weekly feed unchanged for ten intervals is not quiet")
	}
}
