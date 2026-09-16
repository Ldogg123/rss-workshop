package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"rss-workshop/internal/model"
)

// changeTimes reads a feed's and each story's recorded output change time.
func changeTimes(t *testing.T, s *Store, id string) (model.Feed, map[string]time.Time) {
	t.Helper()
	f, err := s.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.Items(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	stories := map[string]time.Time{}
	for _, it := range items {
		stories[it.Key] = it.LastChanged
	}
	return f, stories
}

// Reader validators and published dates come from these times, so they must
// move exactly when published output changes: never for a refresh that changes
// nothing, always for one that does, and for edits made before any refresh.
func TestOutputChangeTimesMoveOnlyWithPublishedOutput(t *testing.T) {
	ctx := t.Context()
	s, _ := libraryStore(t)
	f := savedRunFeed(t, s)
	stories := []model.Item{
		{Key: "a", Title: "First", URL: "https://example.com/a", HTML: "<p>One</p>", Published: time.Unix(1700000000, 0)},
		{Key: "b", Title: "Second", URL: "https://example.com/b", HTML: "<p>Two</p>", Published: time.Unix(1700000001, 0)},
	}
	refresh := func(items []model.Item, runErr error) {
		t.Helper()
		current, err := s.Get(ctx, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Complete(ctx, current, items, "etag", "", 200, runErr, 0); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(what string, moved bool, before model.Feed) model.Feed {
		t.Helper()
		after, _ := changeTimes(t, s, f.ID)
		if moved != after.LastChanged.After(before.LastChanged) || after.LastChanged.Before(before.LastChanged) {
			t.Fatalf("%s: change time %v -> %v, want moved=%v", what, before.LastChanged, after.LastChanged, moved)
		}
		return after
	}

	refresh(stories, nil)
	first, times := changeTimes(t, s, f.ID)
	if first.LastChanged.IsZero() || !times["a"].Equal(first.LastChanged) || !times["b"].Equal(first.LastChanged) {
		t.Fatalf("first refresh did not record change times: %v %v", first.LastChanged, times)
	}
	current := first
	current = expect("identical refresh", false, current)
	refresh(stories, nil)
	current = expect("identical refresh", false, current)
	refresh(nil, nil) // a 304 from the source saves no items
	current = expect("source 304", false, current)
	refresh(stories, errors.New("fetch failed"))
	current = expect("failed refresh", false, current)

	// Changing one story moves it and the feed, strictly later even within
	// the same second, and leaves the other story's time alone.
	changed := append([]model.Item(nil), stories...)
	changed[0].Title = "First, corrected"
	refresh(changed, nil)
	current = expect("changed title", true, current)
	_, times = changeTimes(t, s, f.ID)
	if !times["a"].Equal(current.LastChanged) || !times["b"].Equal(first.LastChanged) {
		t.Fatalf("story change times: %v, feed %v", times, current.LastChanged)
	}
	withBody := append([]model.Item(nil), changed...)
	withBody[1].FullHTML = "<p>Full article</p>"
	refresh(withBody, nil)
	current = expect("fetched article body", true, current)
	refresh(changed, nil) // not fetched this time: the stored body stays
	current = expect("article not refetched", false, current)
	refresh(withBody, nil)
	current = expect("same article body again", false, current)

	// Retention removing stories changes output with no story changing. A
	// listed story older than the window is then inserted and removed again on
	// every refresh without ever being published, which is no change.
	s.MaxItems = 1
	refresh(changed, nil)
	current = expect("retention", true, current)
	refresh(changed, nil)
	current = expect("listed story beyond retention", false, current)
	refresh(changed, nil)
	current = expect("listed story beyond retention", false, current)
	s.MaxItems = 10

	// Edits change output before any refresh: a paused feed's rename or
	// interval, cleared article bodies, and pruned history.
	edited := current
	edited.Enabled = false
	edited.Recipe.Items = "article.story"
	if _, err := s.Save(ctx, edited); err != nil {
		t.Fatal(err)
	}
	current = expect("recipe-only edit", false, current)
	edited.Title = "Renamed"
	if _, err := s.Save(ctx, edited); err != nil {
		t.Fatal(err)
	}
	current = expect("rename", true, current)
	edited.Interval = 1200
	if _, err := s.Save(ctx, edited); err != nil {
		t.Fatal(err)
	}
	current = expect("interval", true, current)
	edited.Interval = 1230 // still 20 minutes of ttl
	if _, err := s.Save(ctx, edited); err != nil {
		t.Fatal(err)
	}
	current = expect("interval within the same minute", false, current)
	refresh(withBody, nil)
	current, _ = changeTimes(t, s, f.ID)
	edited = current
	edited.Recipe.Full = &model.FullContent{Selector: "article"}
	if _, err := s.Save(ctx, edited); err != nil {
		t.Fatal(err)
	}
	current = expect("article selector change that cleared a stored body", true, current)
	if _, times := changeTimes(t, s, f.ID); !times["b"].Equal(current.LastChanged) {
		t.Fatalf("cleared story time %v, feed %v", times["b"], current.LastChanged)
	}
	edited = current
	edited.Recipe.Full = nil
	if _, err := s.Save(ctx, edited); err != nil {
		t.Fatal(err)
	}
	current = expect("article selector change with no stored body left", false, current)

	edited = current
	edited.Recipe.Filters = &model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"nothing matches"}}}
	if _, err := s.SaveWithOptions(ctx, edited, SaveOptions{ApplyFiltersToHistory: true}); err != nil {
		t.Fatal(err)
	}
	current = expect("history cleanup that removed nothing", false, current)
	edited.Recipe.Filters = &model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"Second"}}}
	if _, err := s.SaveWithOptions(ctx, edited, SaveOptions{ApplyFiltersToHistory: true}); err != nil {
		t.Fatal(err)
	}
	current = expect("history cleanup that removed a story", true, current)

	// A library filter edit changes a feed's output only when it prunes.
	refresh(stories, nil)
	current, _ = changeTimes(t, s, f.ID)
	current.Recipe.Filters = nil
	libraryID := saveLibraryFilter(t, s, "Library", excludeTitles("nothing matches"))
	current.FilterIDs = []string{libraryID}
	if _, err := s.Save(ctx, current); err != nil {
		t.Fatal(err)
	}
	current, _ = changeTimes(t, s, f.ID)
	if _, err := s.SaveFilter(ctx, model.LibraryFilter{ID: libraryID, Name: "Library", Filters: excludeTitles("still nothing")}, SaveOptions{ApplyFiltersToHistory: true}); err != nil {
		t.Fatal(err)
	}
	current = expect("library edit that pruned nothing", false, current)
	if _, err := s.SaveFilter(ctx, model.LibraryFilter{ID: libraryID, Name: "Library", Filters: excludeTitles("First")}, SaveOptions{ApplyFiltersToHistory: true}); err != nil {
		t.Fatal(err)
	}
	expect("library edit that pruned a story", true, current)
}

// Upgrading must not change a single published byte, so a feed starts from
// its last successful refresh and a story from the last_seen it was rendered
// with; a story takes that value at its next unchanged merge.
func TestSchemaFiveStartsChangeTimesFromPublishedValues(t *testing.T) {
	ctx := t.Context()
	legacy, open := releasedStore(t, 4)
	if _, err := legacy.DB.ExecContext(ctx, legacy.bind(`INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run,last_success,version) VALUES('feed','token','Feed','https://example.com','{}',600,?,0,1700000500,3)`), false); err != nil {
		t.Fatal(err)
	}
	// The GUID a v1.0.0 merge derived; PostgreSQL identifies stories by it.
	guid := fmt.Sprintf("urn:sha256:%x", sha256.Sum256([]byte("feed\x00a")))
	if _, err := legacy.DB.ExecContext(ctx, legacy.bind(`INSERT INTO items(feed_id,key,guid,title,url,html,image,content_full,published,first_seen,last_seen) VALUES('feed',?,?,'Story','https://example.com/a','<p>One</p>','','',1700000000,1700000100,1700000400)`), legacy.opaque("a"), guid); err != nil {
		t.Fatal(err)
	}
	legacy.DB.Close()
	s, err := open()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	f, times := changeTimes(t, s, "feed")
	if f.LastChanged.Unix() != 1700000500 || !times["a"].IsZero() {
		t.Fatalf("upgraded change times: feed %v, story %v", f.LastChanged, times["a"])
	}
	if err := s.Complete(ctx, f, []model.Item{{Key: "a", Title: "Story", URL: "https://example.com/a", HTML: "<p>One</p>", Published: time.Unix(1700000000, 0)}}, "", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	f, times = changeTimes(t, s, "feed")
	if f.LastChanged.Unix() != 1700000500 || times["a"].Unix() != 1700000400 {
		t.Fatalf("unchanged merge after upgrade: feed %v, story %v", f.LastChanged, times["a"])
	}
}

// Several changes inside one second push the feed's time ahead of the clock.
// A story whose body is then cleared must still move forward, or its Atom
// updated goes backwards while its content changes.
func TestClearedArticleBodyMovesStoryForward(t *testing.T) {
	ctx := t.Context()
	s, _ := libraryStore(t)
	f := savedRunFeed(t, s)
	story := []model.Item{{Key: "a", Title: "Story", URL: "https://example.com/a", HTML: "<p>Teaser</p>", FullHTML: "<p>Body</p>", Published: time.Unix(1700000000, 0)}}
	for i := range 4 {
		current, err := s.Get(ctx, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		story[0].Title = "Story " + string(rune('a'+i))
		if err := s.Complete(ctx, current, story, "", "", 200, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	current, before := changeTimes(t, s, f.ID)
	current.Recipe.Full = &model.FullContent{Selector: "article"}
	if _, err := s.Save(ctx, current); err != nil {
		t.Fatal(err)
	}
	after, times := changeTimes(t, s, f.ID)
	if !times["a"].After(before["a"]) || !times["a"].Equal(after.LastChanged) {
		t.Fatalf("cleared story moved %v -> %v, feed %v", before["a"], times["a"], after.LastChanged)
	}
}
