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

// A recipe's QuietDays replaces the automatic threshold, interval multiple
// included, and QuietNever turns Quiet off however long the feed has gone
// without a new story.
func TestQuietDaysReplaceTheAutomaticThreshold(t *testing.T) {
	day := 24 * time.Hour
	for _, c := range []struct {
		days, interval int
		want           time.Duration
		flagged        bool
	}{
		{0, 900, 14 * day, true},
		{0, 604800, 70 * day, true},
		{7, 900, 7 * day, true},
		// A chosen time waits at least one interval and QuietMargin, which
		// only 7 days on an interval longer than 6 days 23 hours reaches.
		{7, 518400, 7 * day, true},
		{7, 601200, 7 * day, true},
		{7, 601260, 7*day + time.Minute, true},
		{7, 604800, 7*day + time.Hour, true},
		{30, 900, 30 * day, true},
		{365, 604800, 365 * day, true},
		{QuietNever, 900, 0, false},
	} {
		f := Feed{Interval: c.interval, Recipe: Recipe{QuietDays: c.days}}
		if got, flagged := f.QuietThreshold(); got != c.want || flagged != c.flagged {
			t.Errorf("QuietThreshold with quiet_days %d every %d s = %v, %v; want %v, %v", c.days, c.interval, got, flagged, c.want, c.flagged)
		}
	}

	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	succeeded := now.Add(-time.Hour)
	f := Feed{Enabled: true, Interval: 900, LastAttempt: succeeded, LastSuccess: succeeded, LastChanged: succeeded, LastNew: now.Add(-8 * day)}
	if f.IsQuiet(now) {
		t.Fatal("eight days without a new story is quiet at the automatic threshold")
	}
	f.Recipe.QuietDays = 7
	if !f.IsQuiet(now) {
		t.Error("eight days without a new story is not quiet with quiet_days 7")
	}
	f.Recipe.QuietDays = QuietNever
	f.LastNew = now.Add(-10 * 365 * day)
	if f.IsQuiet(now) {
		t.Error("a feed set never to be quiet is quiet")
	}
}

// A weekly feed set to 7 days whose every refresh finds a new story must not
// turn quiet while its next refresh is due but not yet stored: that refresh is
// scheduled an interval after the last, plus up to 29 seconds of jitter, and
// then waits for a free worker and its fetch.
func TestQuietDaysOutlastTheNextRefresh(t *testing.T) {
	stored := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	f := Feed{Enabled: true, Interval: 604800, Recipe: Recipe{QuietDays: 7},
		LastAttempt: stored, LastSuccess: stored, LastChanged: stored, LastNew: stored}
	due := stored.Add(time.Duration(f.Interval)*time.Second + 29*time.Second)
	for _, at := range []time.Time{due, due.Add(30 * time.Minute)} {
		if f.IsQuiet(at) {
			t.Errorf("quiet %v after the last new story, before the next refresh could be stored", at.Sub(stored))
		}
	}
	if !f.IsQuiet(stored.Add(7*24*time.Hour + QuietMargin)) {
		t.Error("a weekly feed set to 7 days is not quiet one interval and the margin after its last new story")
	}
}

func TestOnlyTheEditorsQuietDaysAreValid(t *testing.T) {
	for _, days := range []int{0, QuietNever, 7, 30, 90, 365} {
		if err := ValidateQuietDays(days); err != nil {
			t.Errorf("ValidateQuietDays(%d) = %v", days, err)
		}
	}
	for _, days := range []int{-2, 1, 3, 14, 31, 366, 1 << 40} {
		err := ValidateQuietDays(days)
		if err == nil || err.Error() != "Mark as Quiet after (quiet_days) must be 0 for automatic, -1 for never, or 7, 30, 90 or 365 days" {
			t.Errorf("ValidateQuietDays(%d) = %v", days, err)
		}
	}
}
