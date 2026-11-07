package scheduler

import (
	"sync"
	"time"

	"rss-workshop/internal/diagnostics"
	"rss-workshop/internal/model"
	"rss-workshop/internal/notify"
)

// Notifier receives health events. Notify must return at once: it runs on a
// refresh's goroutine and on the scheduler's tick.
type Notifier interface {
	Notify(notify.Event)
}

// alertInterval limits the events a failing database raises. Each concerns the
// whole server rather than one feed, so repeating them per feed or per tick
// would bury everything else.
const alertInterval = time.Hour

// alerts decides which events reach the notifier. Feed events are edges of
// the failure count the database already stores, so a restart does not repeat
// them; the database events live in memory, like the holds they accompany.
type alerts struct {
	mu  sync.Mutex
	now func() time.Time
	// reported holds the feeds whose failing went out and which have not
	// recovered since. An editor save resets the stored count, so without it a
	// feed repaired in the editor would recover from 0 and never answer its
	// alert. It lives in memory: after a restart the stored count decides.
	reported map[string]bool
	// notSaved is when the last not_saved went out.
	notSaved time.Time
	// unavailable is when the last db_unavailable went out, and announced
	// whether the outage it reported is still unresolved.
	unavailable time.Time
	announced   bool
}

// forget drops reported feeds that no longer exist, so a long-running process
// does not accumulate deleted ones.
func (a *alerts) forget(fs []model.Feed) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.reported) == 0 {
		return
	}
	live := make(map[string]bool, len(fs))
	for _, f := range fs {
		live[f.ID] = true
	}
	for id := range a.reported {
		if !live[id] {
			delete(a.reported, id)
		}
	}
}

func (a *alerts) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// due reports whether an event last sent at last may go out again now, and
// records it as sent if so.
func (a *alerts) due(last *time.Time) bool {
	now := a.clock()
	if !last.IsZero() && now.Sub(*last) < alertInterval {
		return false
	}
	*last = now
	return true
}

// alertRefresh reports a stored refresh. f is the feed as the refresh read it,
// so its failure count is the one before this result: the store saved
// f.Failures+1 for a failure and 0 for a success. A feed fails once per streak
// exactly at the threshold, and recovers from at or past it, or from any
// count once its failing went out in this process.
func (s *Scheduler) alertRefresh(f model.Feed, runErr error) {
	if s.Notifier == nil {
		return
	}
	threshold := max(s.NotifyAfter, 1)
	s.alerts.mu.Lock()
	reported := s.alerts.reported[f.ID]
	var event *notify.Event
	switch {
	case runErr != nil && f.Failures+1 == threshold:
		if s.alerts.reported == nil {
			s.alerts.reported = map[string]bool{}
		}
		s.alerts.reported[f.ID] = true
		event = &notify.Event{Kind: notify.Failing, Feed: f.Title, FeedID: f.ID, Failures: f.Failures + 1,
			// What the store saved as the feed's error, and the card shows.
			Error: diagnostics.SafeText(runErr.Error(), 1000)}
	case runErr == nil && (f.Failures >= threshold || reported):
		delete(s.alerts.reported, f.ID)
		event = &notify.Event{Kind: notify.Recovered, Feed: f.Title, FeedID: f.ID, Failures: f.Failures}
	}
	s.alerts.mu.Unlock()
	if event != nil {
		s.Notifier.Notify(*event)
	}
}

// alertNotSaved reports a result the database refused, at most once an hour
// across every feed: a database that refuses one result usually refuses them
// all. Like db_unavailable it repeats while refusals continue, whichever feed
// meets one. Following each feed's first hold instead would miss an outage
// that begins within the hour of an earlier alert, as when a disk fills again
// soon after space was freed: every first hold falls inside the hour, and
// nothing else reports it, since reads still work and a failure that was not
// stored moves no failure count.
func (s *Scheduler) alertNotSaved(f model.Feed, err error) {
	if s.Notifier == nil {
		return
	}
	s.alerts.mu.Lock()
	send := s.alerts.due(&s.alerts.notSaved)
	s.alerts.mu.Unlock()
	if send {
		s.Notifier.Notify(notify.Event{Kind: notify.NotSaved, Feed: f.Title, FeedID: f.ID, Error: err.Error()})
	}
}

// alertListed follows the tick's read of the library. A failed read stops the
// tick before any refresh, so nothing else would report an outage. It is sent
// at most once an hour while the outage lasts, and its end once, only if it
// was announced.
func (s *Scheduler) alertListed(err error) {
	if s.Notifier == nil {
		return
	}
	s.alerts.mu.Lock()
	var event *notify.Event
	switch {
	case err != nil && s.alerts.due(&s.alerts.unavailable):
		s.alerts.announced = true
		event = &notify.Event{Kind: notify.DBUnavailable, Error: err.Error()}
	case err == nil && s.alerts.announced:
		s.alerts.announced = false
		event = &notify.Event{Kind: notify.DBAvailable}
	}
	s.alerts.mu.Unlock()
	if event != nil {
		s.Notifier.Notify(*event)
	}
}
