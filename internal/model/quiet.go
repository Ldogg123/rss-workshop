package model

import "time"

// A feed is quiet when it keeps refreshing successfully but has stored no new
// story for QuietAfter(interval): the source may have stopped publishing, or
// the selectors may match a part of the page that no longer lists new stories.
// Neither fails a refresh, so without this nothing reports it.
//
// The floor covers sources that publish weekly, a common pace for blogs and
// newsletters, so one late post does not flag them; a short interval says how
// often the operator checks, not how often the site publishes. The multiple
// takes over for long intervals, where the floor alone could flag a feed after
// one or two refreshes, and lets a longer interval mark a source the operator
// expects to be slow.
const (
	QuietFloor     = 14 * 24 * time.Hour
	QuietIntervals = 10
)

// QuietAfter is how long a feed refreshed every interval seconds must go
// without a new story before it counts as quiet.
func QuietAfter(interval int) time.Duration {
	return max(QuietFloor, QuietIntervals*time.Duration(interval)*time.Second)
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
// and that has stored no new story for QuietAfter(f.Interval) at now. It reads
// only the feed row, so listing the library or serving metrics costs no extra
// query. A source that answers 304 indefinitely is quiet.
func (f Feed) IsQuiet(now time.Time) bool {
	// Failures is zero after an edit as well as after a success, so also
	// require that the latest attempt was the latest success: a success sets
	// both to the same second.
	if !f.Enabled || f.Failures > 0 || f.LastSuccess.IsZero() || f.LastChanged.IsZero() || f.LastSuccess.Before(f.LastAttempt) {
		return false
	}
	return now.Sub(f.QuietSince()) >= QuietAfter(f.Interval)
}
