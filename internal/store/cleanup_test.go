package store

import (
	"context"
	"testing"

	"rss-workshop/internal/model"
)

// cleanupFeed stores an article feed with two stories and their bodies:
// "listed", which its latest stored merge listed, and "old", which an earlier
// one did. Both merges are moved 1000 seconds into the past, and then between
// runs whatever happens before the edit.
func cleanupFeed(t *testing.T, s *Store, r model.Recipe, between func(model.Feed)) string {
	t.Helper()
	ctx := context.Background()
	f := articleFeed(".body")
	f.Recipe = r
	id, err := s.Save(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	stories := []model.Item{
		{Key: "listed", Title: "Listed", URL: "https://example.com/listed", HTML: "<p>listed teaser</p>", FullHTML: "<p>listed body</p>"},
		{Key: "old", Title: "Old", URL: "https://example.com/old", HTML: "<p>old teaser</p>", FullHTML: "<p>old body</p>"},
	}
	if err := s.Complete(ctx, saved, stories, "", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	for _, age := range []struct {
		query string
		args  []any
	}{
		{"UPDATE items SET last_seen=last_seen-1000 WHERE feed_id=?", []any{id}},
		{"UPDATE items SET last_seen=last_seen-1000 WHERE feed_id=? AND key=?", []any{id, s.opaque("old")}},
		{"UPDATE feeds SET last_success=last_success-1000 WHERE id=?", []any{id}},
	} {
		if _, err := s.DB.ExecContext(ctx, s.bind(age.query), age.args...); err != nil {
			t.Fatal(err)
		}
	}
	if between != nil {
		current, err := s.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		between(current)
	}
	return id
}

func bodies(t *testing.T, s *Store, id string) map[string]model.Item {
	t.Helper()
	items, err := s.Items(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]model.Item{}
	for _, it := range items {
		out[it.Key] = it
	}
	return out
}

// Changing the cleanup rules of a full-content feed discards the bodies of
// the stories its latest stored merge listed, which the next refreshes fetch
// again with the new rules. Older stories are no longer listed, so a
// discarded body would never come back: they keep theirs. Readers are told
// through last_changed, strictly later, as for any edit that changes output.
func TestCleanupRuleEditsClearTheListedStoriesBodies(t *testing.T) {
	ctx := context.Background()
	css := articleFeed(".body").Recipe
	attribute := css
	attribute.Full = &model.FullContent{Selector: ".body", Attr: "data-body"}
	withRules := func(r model.Recipe, rules ...string) model.Recipe {
		r.Remove = rules
		return r
	}
	refresh := func(s *Store, items []model.Item, status int) func(model.Feed) {
		return func(f model.Feed) {
			if err := s.Complete(ctx, f, items, "etag", "", status, nil, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct {
		name          string
		stored        model.Recipe
		between       func(*Store) func(model.Feed)
		edit          func(*model.Feed)
		listed, old   bool // whether each keeps its body
		changesOutput bool
	}{
		{name: "rules added", stored: css, edit: func(f *model.Feed) { f.Recipe.Remove = []string{".share"} }, old: true, changesOutput: true},
		{name: "rules changed", stored: withRules(css, ".share"), edit: func(f *model.Feed) { f.Recipe.Remove = []string{".share", ".related"} }, old: true, changesOutput: true},
		{name: "rules removed", stored: withRules(css, ".share"), edit: func(f *model.Feed) { f.Recipe.Remove = nil }, old: true, changesOutput: true},
		// A source that answers 304, the steady state for one that sends
		// validators, succeeds without merging a story: last_success moves
		// and last_seen does not. So does a refresh that filters every story.
		{name: "rules added after a 304", stored: css, between: func(s *Store) func(model.Feed) { return refresh(s, nil, 304) },
			edit: func(f *model.Feed) { f.Recipe.Remove = []string{".share"} }, old: true, changesOutput: true},
		{name: "rules added after every story was filtered out", stored: css, between: func(s *Store) func(model.Feed) { return refresh(s, []model.Item{}, 200) },
			edit: func(f *model.Feed) { f.Recipe.Remove = []string{".share"} }, old: true, changesOutput: true},
		// The same rules read differently in the other selector language.
		{name: "selector type changed with rules", stored: withRules(css, ".share"), edit: func(f *model.Feed) { f.Recipe.Type = "xpath" }, old: true, changesOutput: true},
		{name: "selector type changed without rules", stored: css, edit: func(f *model.Feed) { f.Recipe.Type = "xpath" }, listed: true, old: true},
		{name: "same rules", stored: withRules(css, ".share"), edit: func(f *model.Feed) { f.Recipe.UserAgent = "Agent/1" }, listed: true, old: true},
		{name: "fetch mode", stored: withRules(css, ".share"), edit: func(f *model.Feed) { f.Recipe.Full.Browser = true }, listed: true, old: true},
		// Rules never apply to a body read from an attribute.
		{name: "attribute body", stored: attribute, edit: func(f *model.Feed) { f.Recipe.Remove = []string{".share"} }, listed: true, old: true},
		// A different article selector still discards every body.
		{name: "article selector changed with rules", stored: css, edit: func(f *model.Feed) {
			f.Recipe.Full = &model.FullContent{Selector: ".article"}
			f.Recipe.Remove = []string{".share"}
		}, changesOutput: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := testStoreOpener(t, 50)()
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			var between func(model.Feed)
			if tc.between != nil {
				between = tc.between(s)
			}
			id := cleanupFeed(t, s, tc.stored, between)
			before, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			stories := bodies(t, s, id)
			edited := before
			edited.Recipe.Full = &model.FullContent{Selector: before.Recipe.Full.Selector, Attr: before.Recipe.Full.Attr, Browser: before.Recipe.Full.Browser}
			edited.Recipe.Remove = append([]string(nil), before.Recipe.Remove...)
			tc.edit(&edited)
			if _, err := s.Save(ctx, edited); err != nil {
				t.Fatal(err)
			}
			after, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			now := bodies(t, s, id)
			for key, keeps := range map[string]bool{"listed": tc.listed, "old": tc.old} {
				story, was := now[key], stories[key]
				if (story.FullHTML == was.FullHTML) != keeps || !keeps && story.FullHTML != "" {
					t.Errorf("%s body %q -> %q, want kept=%v", key, was.FullHTML, story.FullHTML, keeps)
				}
				// Nothing else about a story changes, so readers see no
				// duplicate, and a cleared story's time moves past the feed's
				// previous one.
				if story.GUID != was.GUID || story.HTML != was.HTML || !story.Published.Equal(was.Published) || !story.LastSeen.Equal(was.LastSeen) {
					t.Errorf("%s changed beyond its body: %+v -> %+v", key, was, story)
				}
				if keeps != story.LastChanged.Equal(was.LastChanged) || !keeps && !story.LastChanged.After(before.LastChanged) {
					t.Errorf("%s change time %v -> %v (feed %v), want moved=%v", key, was.LastChanged, story.LastChanged, before.LastChanged, !keeps)
				}
			}
			if after.LastChanged.After(before.LastChanged) != tc.changesOutput || after.LastChanged.Before(before.LastChanged) {
				t.Errorf("feed change time %v -> %v, want moved=%v", before.LastChanged, after.LastChanged, tc.changesOutput)
			}
			if after.Version != before.Version+1 {
				t.Errorf("version %d -> %d", before.Version, after.Version)
			}
		})
	}
}

// Rules that change only descriptions publish nothing until the next refresh
// re-extracts the list page, so the edit leaves the output time alone and, like
// any recipe edit, drops the source's validators so that refresh is a full one.
// So does a rule change whose listed stories have no body to clear.
func TestCleanupRuleEditsWithNothingToClearLeaveTheOutputTime(t *testing.T) {
	ctx := context.Background()
	for name, setup := range map[string]func(*Store) string{
		"no article content": func(s *Store) string {
			f := articleFeed("")
			id, err := s.Save(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Complete(ctx, saved, []model.Item{{Key: "listed", Title: "Listed", HTML: "<p>teaser</p>"}}, "etag", "", 200, nil, 0); err != nil {
				t.Fatal(err)
			}
			return id
		},
		"listed stories without a body": func(s *Store) string {
			id := cleanupFeed(t, s, articleFeed(".body").Recipe, nil)
			if _, err := s.DB.ExecContext(ctx, s.bind("UPDATE items SET content_full='' WHERE feed_id=? AND key=?"), id, s.opaque("listed")); err != nil {
				t.Fatal(err)
			}
			return id
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := testStoreOpener(t, 50)()
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			id := setup(s)
			before, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			stories := bodies(t, s, id)
			before.Recipe.Remove = []string{".share"}
			if _, err := s.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			after, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if !after.LastChanged.Equal(before.LastChanged) || after.ETag != "" || after.Version != before.Version+1 || len(after.Recipe.Remove) != 1 {
				t.Errorf("change time %v -> %v, validator %q, version %d -> %d, rules %q", before.LastChanged, after.LastChanged, after.ETag, before.Version, after.Version, after.Recipe.Remove)
			}
			for key, story := range bodies(t, s, id) {
				if story.FullHTML != stories[key].FullHTML || !story.LastChanged.Equal(stories[key].LastChanged) {
					t.Errorf("%s changed: %+v -> %+v", key, stories[key], story)
				}
			}
		})
	}
}
