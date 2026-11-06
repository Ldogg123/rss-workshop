package model

import "time"

// A feed is quiet when it keeps refreshing successfully but its published
// output has not changed for QuietAfter(interval): the source may have stopped
// publishing, or the selectors may match a part of the page that no longer
// changes. Neither fails a refresh, so without this nothing reports it.
//
// The floor covers sources that publish weekly, a common pace for blogs and
// newsletters, so one late post does not flag them; a short interval says how
// often the operator checks, not how often the site publishes. The multiple
// takes over for long intervals, where the floor alone could flag a feed after
// one or two unchanged refreshes, and lets a longer interval mark a source the
// operator expects to be slow.
const (
	QuietFloor     = 14 * 24 * time.Hour
	QuietIntervals = 10
)

// QuietAfter is how long a feed refreshed every interval seconds must publish
// unchanged output before it counts as quiet.
func QuietAfter(interval int) time.Duration {
	return max(QuietFloor, QuietIntervals*time.Duration(interval)*time.Second)
}

// IsQuiet reports whether f is an enabled feed whose latest refresh succeeded
// and whose published output has not changed for QuietAfter(f.Interval) at
// now. It reads only the feed row, so listing the library or serving metrics
// costs no extra query.
//
// LastChanged moves when the output changes, which is not the same as a new
// story: a story whose text keeps changing, such as a relative age or a comment
// count, keeps a feed from ever being quiet, and so does an edit to its title,
// URL or interval. A source that answers 304 indefinitely is quiet.
func (f Feed) IsQuiet(now time.Time) bool {
	// Failures is zero after an edit as well as after a success, so also
	// require that the latest attempt was the latest success: a success sets
	// both to the same second.
	if !f.Enabled || f.Failures > 0 || f.LastSuccess.IsZero() || f.LastChanged.IsZero() || f.LastSuccess.Before(f.LastAttempt) {
		return false
	}
	return now.Sub(f.LastChanged) >= QuietAfter(f.Interval)
}
