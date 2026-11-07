package scheduler

import (
	"context"

	"rss-workshop/internal/model"
)

// draining reports whether a refresh requests this feed's list page without
// its validators. Articles are fetched only for the stories a list page
// returns, and a 304 returns none, so a full-content feed listing more stories
// than one refresh fetches would otherwise stop at its first bodies until the
// page changed. The flag lives in memory: a feed with no refresh stored since
// the server started counts as draining once. The stored validators are kept,
// and come back into use when the backlog ends. Previews never ask.
func (s *Scheduler) draining(f model.Feed) bool {
	if f.Recipe.Full == nil || f.Recipe.Full.Selector == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	backlog, known := s.backlog[f.ID]
	return backlog || !known
}

// recordBacklog follows a stored successful refresh. The backlog keeps
// draining only while stories without a body were left unqueued and this
// refresh stored at least one body: articles that keep failing, or stories
// that retention removes as soon as they are stored, would otherwise make
// every list request unconditional for good. A 304 queues nothing, so it ends
// the backlog.
func (s *Scheduler) recordBacklog(ctx context.Context, f model.Feed, run articleRun) {
	if f.Recipe.Full == nil || f.Recipe.Full.Selector == "" {
		return
	}
	backlog := len(run.left) > 0 && len(run.fetched) > 0 && s.progressed(ctx, f.ID, run)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backlog == nil {
		s.backlog = map[string]bool{}
	}
	s.backlog[f.ID] = backlog
}

// progressed reads what the refresh stored: a body for a story it fetched,
// and a story it left that is still stored without one. A failed read counts
// as no progress, so conditional requests resume.
func (s *Scheduler) progressed(ctx context.Context, id string, run articleRun) bool {
	stored, err := s.Store.ArticleStates(ctx, id)
	if err != nil {
		return false
	}
	saved, waiting := false, false
	for _, key := range run.fetched {
		saved = saved || stored[key].HasBody
	}
	for _, key := range run.left {
		state, ok := stored[key]
		waiting = waiting || (ok && !state.HasBody)
	}
	return saved && waiting
}

// forgetBacklog drops the flags of feeds that no longer exist, as forgetHolds
// does for holds.
func (s *Scheduler) forgetBacklog(fs []model.Feed) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.backlog) == 0 {
		return
	}
	live := make(map[string]bool, len(fs))
	for _, f := range fs {
		live[f.ID] = true
	}
	for id := range s.backlog {
		if !live[id] {
			delete(s.backlog, id)
		}
	}
}
