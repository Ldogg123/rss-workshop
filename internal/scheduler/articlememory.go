package scheduler

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
)

const (
	// articleBackoff is how long a story waits after its article failed
	// before a refresh tries it again. It doubles with each further failure,
	// up to articleBackoffMax.
	articleBackoff    = time.Hour
	articleBackoffMax = 24 * time.Hour
)

// articleKey identifies a story in the memory. A story's key can be a link
// of several kilobytes, so the memory keeps a hash of it.
type articleKey [sha256.Size]byte

func keyOf(key string) articleKey { return sha256.Sum256([]byte(key)) }

// articleFailure is what this process remembers of a listed story whose
// article it tried without a body being stored for it.
type articleFailure struct {
	// tried is when the story was last tried. The least recently tried goes
	// first among those tried again.
	tried time.Time
	// count is the story's own failures (see ownFault), and retry the end of
	// the backoff after the latest. A try that failed for another reason
	// leaves both alone, so a story can be remembered with neither.
	count int
	retry time.Time
	// unkept says that the latest try fetched the article and the refresh did
	// not store it (see unkept). Such a story goes behind the failed ones.
	unkept bool
}

// articleMemory holds a feed's failures for one recipe version: any edit may
// have fixed what they came from.
type articleMemory struct {
	version int
	failed  map[articleKey]articleFailure
}

type articleOutcome int

const (
	// articleStored: the round gave the story a body. It is forgotten once
	// that body is stored (see unkept).
	articleStored articleOutcome = iota
	// articleFault: the story's own failure, which backs it off.
	articleFault
	// articleDeferred: a try that says nothing about the story. It only moves
	// the story behind those never tried.
	articleDeferred
)

// articleTry is the outcome for one story without a stored body that a
// refresh tried. It names the story by its key, which only the memory hashes.
type articleTry struct {
	key     string
	outcome articleOutcome
}

// pageFault marks an error as coming from the article page that was read: an
// extraction that failed, or a 304 in place of a page.
type pageFault struct{ error }

func (e pageFault) Unwrap() error { return e.error }

// ownFault reports whether an article failed for a reason that belongs to the
// story, which the next refresh would most likely meet again: its page
// answered with an HTTP error status or a 304, could not be extracted, or
// gave no body. Everything else describes this server, the network or the
// refresh's deadline (Chromium, a missing renderer, FlareSolverr, a
// connection that failed, a wait for a slot), and backing a story off for
// those would leave a whole feed without articles for an hour after a short
// outage.
func ownFault(body string, err error) bool {
	if err == nil {
		return body == ""
	}
	var status *fetch.HTTPError
	var page pageFault
	return errors.As(err, &status) || errors.As(err, &page)
}

// articleFailures returns a copy of the failures remembered for a feed, or
// nil when there are none for its recipe version.
func (s *Scheduler) articleFailures(f model.Feed) map[articleKey]articleFailure {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.articles[f.ID]
	if !ok || m.version != f.Version {
		return nil
	}
	out := make(map[articleKey]articleFailure, len(m.failed))
	for k, v := range m.failed {
		out[k] = v
	}
	return out
}

// unkept returns the stories a round gave a body that the refresh did not
// store with one. MAX_ITEMS retention removes the oldest stories in the save
// that stored their articles, and a list page longer than MAX_ITEMS shows
// them again at every refresh. They are remembered as fetched in vain, which
// says nothing against them: forgotten like the stories that kept a body,
// they would count as never tried each time and lead the queue for good,
// ahead of every story whose article failed. Remembered, they are fetched
// again as before, after those stories. A read that fails forgets them, so
// they lead once more at the next refresh.
func (s *Scheduler) unkept(ctx context.Context, id string, run articleRun) map[string]bool {
	fetched := false
	for _, try := range run.tried {
		fetched = fetched || try.outcome == articleStored
	}
	if !fetched {
		return nil
	}
	stored, err := s.Store.ArticleStates(ctx, id)
	if err != nil {
		return nil
	}
	var out map[string]bool
	for _, try := range run.tried {
		if try.outcome == articleStored && !stored[try.key].HasBody {
			if out == nil {
				out = map[string]bool{}
			}
			out[try.key] = true
		}
	}
	return out
}

// rememberArticles follows a stored successful refresh that read the list
// page. A refresh that was not stored, or that failed, has changed nothing,
// and a 304 lists no stories, so neither may touch the memory. Only listed
// stories are kept, which bounds a feed's memory by the stories one list page
// shows, and a story stored with its body is forgotten. The memory lives in
// this process alone: a restart tries every story again, as does an edit,
// because it changes the version.
func (s *Scheduler) rememberArticles(ctx context.Context, f model.Feed, run articleRun) {
	// Read before taking the lock, which no query may hold up.
	unkept := s.unkept(ctx, f.ID, run)
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Recipe.Full == nil || f.Recipe.Full.Selector == "" {
		delete(s.articles, f.ID)
		return
	}
	m, ok := s.articles[f.ID]
	if !ok || m.version != f.Version {
		m = &articleMemory{version: f.Version, failed: map[articleKey]articleFailure{}}
	}
	listed := make(map[articleKey]bool, len(run.listed))
	for _, key := range run.listed {
		listed[keyOf(key)] = true
	}
	for key := range m.failed {
		if !listed[key] {
			delete(m.failed, key)
		}
	}
	now := time.Now()
	for _, try := range run.tried {
		key := keyOf(try.key)
		if try.outcome == articleStored {
			delete(m.failed, key)
			if unkept[try.key] {
				// The page gave its article, so earlier failures are over.
				m.failed[key] = articleFailure{tried: now, unkept: true}
			}
			continue
		}
		failure := m.failed[key]
		failure.tried, failure.unkept = now, false
		if try.outcome == articleFault {
			failure.count++
			failure.retry = now.Add(min(articleBackoff<<min(failure.count-1, 5), articleBackoffMax))
		}
		m.failed[key] = failure
	}
	if len(m.failed) == 0 {
		delete(s.articles, f.ID)
		return
	}
	if s.articles == nil {
		s.articles = map[string]*articleMemory{}
	}
	s.articles[f.ID] = m
}

// retryArticles lifts a feed's article backoffs, with mu held. The counts
// stay, as a hold's do: an article that fails again waits twice as long as
// the last time rather than starting over at an hour.
func (s *Scheduler) retryArticles(id string) {
	m, ok := s.articles[id]
	if !ok {
		return
	}
	for key, failure := range m.failed {
		failure.retry = time.Time{}
		m.failed[key] = failure
	}
}

// forgetArticles drops the memory of feeds that no longer exist, as
// forgetHolds does for holds.
func (s *Scheduler) forgetArticles(fs []model.Feed) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.articles) == 0 {
		return
	}
	live := make(map[string]bool, len(fs))
	for _, f := range fs {
		live[f.ID] = true
	}
	for id := range s.articles {
		if !live[id] {
			delete(s.articles, id)
		}
	}
}
