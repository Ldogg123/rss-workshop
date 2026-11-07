package model

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A feed is quiet when it keeps refreshing successfully but has stored no new
// story for its threshold: the source may have stopped publishing, or the
// selectors may match a part of the page that no longer lists new stories.
// Neither fails a refresh, so without this nothing reports it.
//
// The automatic threshold, QuietAfter(interval), has a floor that covers
// sources that publish weekly, a common pace for blogs and newsletters, so one
// late post does not flag them; a short interval says how often the operator
// checks, not how often the site publishes. The multiple takes over for long
// intervals, where the floor alone could flag a feed after one or two
// refreshes, and lets a longer interval mark a source the operator expects to
// be slow. A recipe can replace it with QuietDays.
const (
	QuietFloor     = 14 * 24 * time.Hour
	QuietIntervals = 10
	// QuietNever is the QuietDays that never marks a feed quiet.
	QuietNever = -1
	// QuietMargin is how long past one refresh interval a QuietDays
	// threshold waits at least; see QuietThreshold.
	QuietMargin = time.Hour
)

// QuietDayChoices are the thresholds, in days, a recipe may set besides
// automatic (0) and QuietNever. A fixed list rather than a range keeps every
// stored value one the editor's select can show, so a save from the editor
// cannot silently replace it.
var QuietDayChoices = []int{7, 30, 90, 365}

// ValidateQuietDays accepts 0, QuietNever and QuietDayChoices.
func ValidateQuietDays(days int) error {
	if days == 0 || days == QuietNever || slices.Contains(QuietDayChoices, days) {
		return nil
	}
	choices := make([]string, len(QuietDayChoices))
	for i, d := range QuietDayChoices {
		choices[i] = strconv.Itoa(d)
	}
	return fmt.Errorf("Mark as Quiet after (quiet_days) must be 0 for automatic, %d for never, or %s or %s days",
		QuietNever, strings.Join(choices[:len(choices)-1], ", "), choices[len(choices)-1])
}

// QuietAfter is how long a feed refreshed every interval seconds must go
// without a new story before it counts as quiet, unless its recipe sets
// QuietDays.
func QuietAfter(interval int) time.Duration {
	return max(QuietFloor, QuietIntervals*time.Duration(interval)*time.Second)
}

// QuietThreshold is how long f must go without a new story before it counts as
// quiet: its recipe's QuietDays, or QuietAfter(f.Interval) when that is 0. It
// reports false for a feed set never to be quiet.
//
// QuietDays ignores the interval multiple but still waits one interval and
// QuietMargin. A refresh that stores a new story schedules the next one an
// interval later, plus jitter, queueing and the fetch itself, so 7 days on a
// weekly feed would otherwise flag a feed whose every refresh finds a story
// from the moment the next one falls due until it is stored.
func (f Feed) QuietThreshold() (time.Duration, bool) {
	switch days := f.Recipe.QuietDays; {
	case days == QuietNever:
		return 0, false
	case days > 0:
		return max(time.Duration(days)*24*time.Hour, time.Duration(f.Interval)*time.Second+QuietMargin), true
	}
	return QuietAfter(f.Interval), true
}

// QuietSince is the time Quiet measures from: when the feed last stored a new
// story, or, for a feed that has stored none, such as one whose filters have
// excluded every story, when its output last changed: its first successful
// refresh, or an edit to its title, URL or interval, so such an edit restarts
// the count of a feed that has stored no story. Nothing stored records the
// first success on its own.
//
// Once a feed has stored a story, only a new story moves it. A story whose
// text keeps changing, such as a
// relative age or a comment count, an image, a story that retention removes
// and an edit to the title, URL or interval all change the output, and so
// LastChanged, but none of them is a new story.
func (f Feed) QuietSince() time.Time {
	if !f.LastNew.IsZero() {
		return f.LastNew
	}
	return f.LastChanged
}

// IsQuiet reports whether f is an enabled feed whose latest refresh succeeded
// and that has stored no new story for its QuietThreshold at now. It reads
// only the feed row, so listing the library or serving metrics costs no extra
// query. A source that answers 304 indefinitely is quiet.
func (f Feed) IsQuiet(now time.Time) bool {
	after, flagged := f.QuietThreshold()
	// Failures is zero after an edit as well as after a success, so also
	// require that the latest attempt was the latest success: a success sets
	// both to the same second.
	if !flagged || !f.Enabled || f.Failures > 0 || f.LastSuccess.IsZero() || f.LastChanged.IsZero() || f.LastSuccess.Before(f.LastAttempt) {
		return false
	}
	return now.Sub(f.QuietSince()) >= after
}
