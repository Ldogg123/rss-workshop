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
	old := now.Add(-QuietFloor)
	quiet := Feed{Enabled: true, Interval: 900, LastAttempt: succeeded, LastSuccess: succeeded, LastChanged: old, LastNew: old}
	if !quiet.IsQuiet(now) {
		t.Fatal("a feed without a new story for exactly the threshold is not quiet")
	}
	for name, change := range map[string]func(*Feed){
		"a new story just under the threshold": func(f *Feed) { f.LastNew = now.Add(-QuietFloor + time.Second) },
		// Ten weekly refreshes, not the floor, set the threshold here.
		"a weekly feed past the floor": func(f *Feed) {
			f.Interval = 604800
			f.LastNew = now.Add(-QuietFloor - 30*24*time.Hour)
		},
		"paused":  func(f *Feed) { f.Enabled = false },
		"failing": func(f *Feed) { f.Failures = 1 },
		"never refreshed": func(f *Feed) {
			f.LastSuccess, f.LastAttempt, f.LastChanged, f.LastNew = time.Time{}, time.Time{}, time.Time{}, time.Time{}
		},
		"never published":               func(f *Feed) { f.LastChanged, f.LastNew = time.Time{}, time.Time{} },
		"last attempt failed then edit": func(f *Feed) { f.LastAttempt = succeeded.Add(time.Minute) },
		"a new story in the future":     func(f *Feed) { f.LastNew = now.Add(time.Second) },
		// No new story yet, so the first output stands in for one.
		"no story yet, output just under the threshold": func(f *Feed) {
			f.LastNew = time.Time{}
			f.LastChanged = now.Add(-QuietFloor + time.Second)
		},
	} {
		f := quiet
		change(&f)
		if f.IsQuiet(now) {
			t.Errorf("%s: reported quiet", name)
		}
	}
	weekly := quiet
	weekly.Interval = 604800
	weekly.LastNew = now.Add(-QuietAfter(weekly.Interval))
	if !weekly.IsQuiet(now) {
		t.Error("a weekly feed without a new story for ten intervals is not quiet")
	}
}

// Quiet follows new stories, not output: a description that changes on every
// refresh, an edit to the title, URL or interval, or retention removing an
// old story all move LastChanged, and none of them is a new story.
func TestQuietMeasuresFromTheLastNewStory(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	succeeded := now.Add(-time.Hour)
	churning := Feed{Enabled: true, Interval: 900, LastAttempt: succeeded, LastSuccess: succeeded,
		LastChanged: succeeded, LastNew: now.Add(-QuietFloor - time.Hour)}
	if got := churning.QuietSince(); !got.Equal(churning.LastNew) {
		t.Fatalf("QuietSince = %v, want the last new story %v", got, churning.LastNew)
	}
	if !churning.IsQuiet(now) {
		t.Error("a feed whose output keeps changing without a new story is not quiet")
	}
	// A feed that has never stored a story, such as one whose filters exclude
	// every story, is measured from its first output instead.
	filtered := churning
	filtered.LastNew = time.Time{}
	filtered.LastChanged = now.Add(-QuietFloor)
	if got := filtered.QuietSince(); !got.Equal(filtered.LastChanged) {
		t.Fatalf("QuietSince without a story = %v, want the last output change %v", got, filtered.LastChanged)
	}
	if !filtered.IsQuiet(now) {
		t.Error("a feed that never stored a story is not quiet after the threshold")
	}
}
