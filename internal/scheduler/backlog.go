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
// refresh added to the listed stories stored with one. A 304 queues nothing,
// so it ends the backlog.
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

// progressed reads what the refresh stored: more of the listed stories have a
// body than before it, and a story it left is still stored without one. While
// the list page stays the same, a backlog therefore continues only while that
// count grows, and the stories listed and MAX_ITEMS bound it. Articles that
// keep failing add nothing, nor do bodies retention removes. Undated stories
// are why it counts stored bodies rather than checking that a fetched story
// kept one: once retention has removed them, each full request stores them
// again as the newest, in place of others, so a fetched story always keeps its
// body and the feed would change on every refresh. A failed read counts as no
// progress, so conditional requests resume.
func (s *Scheduler) progressed(ctx context.Context, id string, run articleRun) bool {
	stored, err := s.Store.ArticleStates(ctx, id)
	if err != nil {
		return false
	}
	has, waiting := 0, false
	for _, key := range run.listed {
		if stored[key].HasBody {
			has++
		}
	}
	for _, key := range run.left {
		state, ok := stored[key]
		waiting = waiting || (ok && !state.HasBody)
	}
	return has > run.had && waiting
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
