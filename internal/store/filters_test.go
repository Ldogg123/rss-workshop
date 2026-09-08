package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/extract"
	"rss-workshop/internal/model"
)

func historyFilters() *model.FilterSet {
	return &model.FilterSet{
		Include: &model.FilterRule{Op: "all", Rules: []model.FilterRule{
			{Op: "contains_any", Field: "title", Keywords: []string{"SCIENCE", "research"}},
			{Op: "any", Rules: []model.FilterRule{
				{Op: "contains_all", Field: "description", Keywords: []string{"space", "orbit"}},
				{Op: "contains_any", Field: "link", Keywords: []string{"/science/"}},
			}},
		}},
		Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"sponsored"}},
	}
}

func historyFixture(t *testing.T, s *Store) (model.Feed, []model.Item) {
	t.Helper()
	f := savedRunFeed(t, s)
	items := []model.Item{
		{Key: "keep\x00\xff", Title: "Science update", HTML: "<p>Space &amp; <b>orbit</b></p>", URL: "https://example.com/one"},
		{Key: "keep-two", Title: "Research today", URL: "https://example.com/science/two"},
		{Key: "reject\x00\xff", Title: "Sponsored science", URL: "https://example.com/science/advert"},
		{Key: "reject-other", Title: "Sports news", HTML: "<p>Other news</p>", URL: "https://example.com/sport"},
	}
	for i := range items {
		items[i].Published = time.Date(2025, 1, 2, 3, i, 0, 0, time.UTC)
	}
	if err := s.CompleteWithDiagnostics(t.Context(), f, items, "etag", "modified", 200, nil, 0, testRunDiagnostics()); err != nil {
		t.Fatal(err)
	}
	f, err := s.Get(t.Context(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.Items(t.Context(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f, stored
}

func TestFilterSaveDefaultPreservesHistoryAndExplicitPruningIsDurable(t *testing.T) {
	ctx := t.Context()
	open := testStoreOpener(t, 10)
	s, err := open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	f, before := historyFixture(t, s)
	beforeRuns, err := s.Runs(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale := f
	f.Recipe.Filters = historyFilters()
	if _, err := s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	unchanged, err := s.Items(ctx, f.ID)
	if err != nil || !reflect.DeepEqual(unchanged, before) {
		t.Fatal("default save pruned or rewrote history", err)
	}
	if err := s.Complete(ctx, stale, before, "stale-etag", "", 200, nil, 0); !errors.Is(err, ErrStale) {
		t.Fatal("filter edit accepted an old refresh", err)
	}
	f, err = s.Get(ctx, f.ID)
	if err != nil || f.Version != stale.Version+1 || f.ETag != "" || f.LastModified != "" || f.RSSToken != stale.RSSToken {
		t.Fatal("filter edit did not invalidate validators/version or changed token", err)
	}
	if _, err := s.SaveWithOptions(ctx, f, SaveOptions{ApplyFiltersToHistory: true}); err != nil {
		t.Fatal(err)
	}
	var want []model.Item
	for _, item := range before {
		if strings.HasPrefix(item.Key, "keep") {
			want = append(want, item)
		}
	}
	if len(want) != 2 {
		t.Fatal("invalid fixture")
	}
	if err := s.CompleteWithDiagnostics(ctx, f, before, "old", "", 200, nil, 0, testRunDiagnostics()); !errors.Is(err, ErrStale) {
		t.Fatal("stale refresh resurrected pruned history", err)
	}
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = open()
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Items(ctx, f.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("pruning/reopen changed matching identity, dates or content", err)
	}
	f, err = s.Get(ctx, f.ID)
	if err != nil || !reflect.DeepEqual(f.Recipe.Filters, historyFilters()) {
		t.Fatal("nested filters did not persist", err)
	}
	afterRuns, err := s.Runs(ctx, f.ID)
	if err != nil || !reflect.DeepEqual(afterRuns, beforeRuns) {
		t.Fatal("save/prune changed run history or recorded stale diagnostics", err)
	}
	for _, empty := range []*model.FilterSet{nil, {}} {
		f.Recipe.Filters = empty
		if _, err := s.SaveWithOptions(ctx, f, SaveOptions{ApplyFiltersToHistory: true}); err != nil {
			t.Fatal(err)
		}
		got, err := s.Items(ctx, f.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("empty filters changed history", err)
		}
	}
	if err := s.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveWithOptions(ctx, f, SaveOptions{ApplyFiltersToHistory: true}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("saving missing feed was accepted", err)
	}
}

func TestFilteredRefreshLeavesPreviouslySavedNonmatchesUntouched(t *testing.T) {
	s, err := testStoreOpener(t, 10)()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	f := savedRunFeed(t, s)
	f.Recipe = model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "a"}, Link: model.Field{Selector: "a", Attr: "href"}}
	if _, err := s.Save(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	f, _ = s.Get(t.Context(), f.ID)
	page := []byte(`<article><a href="/keep">Science original</a></article><article><a href="/reject">Sport original</a></article>`)
	p, err := extract.RunAt(page, f.URL, f.Recipe, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(t.Context(), f, p.Items, "", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Items(t.Context(), f.ID)
	f.Recipe.Filters = &model.FilterSet{Include: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"science"}}}
	if _, err := s.Save(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	f, _ = s.Get(t.Context(), f.ID)
	page = []byte(`<article><a href="/keep">Science updated</a></article><article><a href="/reject">Sport updated</a></article><article><a href="/new-rejected">Sport new</a></article>`)
	p, err = extract.RunAt(page, f.URL, f.Recipe, time.Now())
	if err != nil || len(p.Items) != 1 {
		t.Fatal("filtered extraction failed", err)
	}
	if err := s.Complete(t.Context(), f, p.Items, "", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	after, err := s.Items(t.Context(), f.ID)
	if err != nil || len(after) != 2 {
		t.Fatal("refresh pruned an old nonmatch or stored a new nonmatch", err)
	}
	for _, old := range before {
		for _, item := range after {
			if item.Key != old.Key {
				continue
			}
			if strings.HasSuffix(old.URL, "/reject") && !reflect.DeepEqual(item, old) {
				t.Fatal("rejected existing item was modified")
			}
			if item.GUID != old.GUID || !item.Published.Equal(old.Published) || !item.FirstSeen.Equal(old.FirstSeen) {
				t.Fatal("refresh changed existing identity or dates")
			}
		}
	}
}

func TestFilterValidationAndImportAreAtomic(t *testing.T) {
	s, err := testStoreOpener(t, 10)()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	f, before := historyFixture(t, s)
	invalid := f
	invalid.Title = "Invalid edit"
	invalid.Recipe.Filters = &model.FilterSet{Include: &model.FilterRule{Op: "contains_any", Field: "secret-field", Keywords: []string{"private-term"}}}
	if _, err := s.SaveWithOptions(t.Context(), invalid, SaveOptions{ApplyFiltersToHistory: true}); err == nil || strings.Contains(err.Error(), "private-term") {
		t.Fatal("invalid filters accepted or leaked keyword")
	}
	if _, err := s.Import(t.Context(), []model.Feed{f, invalid}); err == nil {
		t.Fatal("invalid import accepted")
	}
	got, err := s.Get(t.Context(), f.ID)
	if err != nil || !reflect.DeepEqual(got, f) {
		t.Fatal("invalid operation changed existing feed", err)
	}
	items, err := s.Items(t.Context(), f.ID)
	if err != nil || !reflect.DeepEqual(items, before) {
		t.Fatal("invalid operation changed history", err)
	}
	var count int
	if err := s.DB.QueryRowContext(t.Context(), "SELECT count(*) FROM feeds").Scan(&count); err != nil || count != 1 {
		t.Fatal("invalid import left partial feeds", err)
	}
	terms := make([]string, 500)
	for i := range terms {
		terms[i] = fmt.Sprintf("topic %d", i)
	}
	f.Enabled = true
	f.Recipe.Filters = &model.FilterSet{Include: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: terms}}
	ids, err := s.Import(t.Context(), []model.Feed{f})
	if err != nil || len(ids) != 1 {
		t.Fatal("500-term import failed", err)
	}
	imported, err := s.Get(t.Context(), ids[0])
	if err != nil || imported.Enabled || imported.ID == f.ID || imported.RSSToken == f.RSSToken || !reflect.DeepEqual(imported.Recipe.Filters, f.Recipe.Filters) {
		t.Fatal("import failed to preserve filters and create a fresh paused feed", err)
	}
}

func TestFilterPruningFailureRollsBackRecipeValidatorsAndHistory(t *testing.T) {
	for _, failure := range []string{"error", "ignored-delete"} {
		t.Run(failure, func(t *testing.T) {
			s, err := testStoreOpener(t, 1000)()
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			f, before := historyFixture(t, s)
			bulk := make([]model.Item, 300)
			for i := range bulk {
				bulk[i] = model.Item{Key: fmt.Sprintf("bulk-%d", i), Title: "Sports"}
			}
			if err := s.Complete(t.Context(), f, bulk, "etag", "modified", 200, nil, 0); err != nil {
				t.Fatal(err)
			}
			f, err = s.Get(t.Context(), f.ID)
			if err != nil {
				t.Fatal(err)
			}
			before, err = s.Items(t.Context(), f.ID)
			if err != nil || len(before) != 304 {
				t.Fatal("cannot seed multiple delete batches", err)
			}
			// Fail only after the first 250-row batch has been deleted. The
			// transaction must roll that successful batch back as well.
			query := `CREATE TRIGGER prevent_filter_delete BEFORE DELETE ON items WHEN (SELECT count(*) FROM items)<=54 BEGIN SELECT RAISE(ABORT, 'fixture blocks delete'); END`
			if failure == "ignored-delete" {
				query = `CREATE TRIGGER prevent_filter_delete BEFORE DELETE ON items WHEN (SELECT count(*) FROM items)<=54 BEGIN SELECT RAISE(IGNORE); END`
			}
			if s.postgres {
				body := `RAISE EXCEPTION 'fixture blocks delete';`
				if failure == "ignored-delete" {
					body = `RETURN NULL;`
				}
				query = `CREATE FUNCTION prevent_filter_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF (SELECT count(*) FROM items)<=54 THEN ` + body + ` END IF; RETURN OLD; END $$; CREATE TRIGGER prevent_filter_delete BEFORE DELETE ON items FOR EACH ROW EXECUTE FUNCTION prevent_filter_delete()`
			}
			if _, err := s.DB.ExecContext(t.Context(), query); err != nil {
				t.Fatal(err)
			}
			edited := f
			edited.Title = "Should roll back"
			edited.Recipe.Filters = historyFilters()
			if _, err := s.SaveWithOptions(t.Context(), edited, SaveOptions{ApplyFiltersToHistory: true}); err == nil {
				t.Fatal("incomplete pruning was accepted")
			}
			got, err := s.Get(t.Context(), f.ID)
			if err != nil || !reflect.DeepEqual(got, f) {
				t.Fatal("failed pruning committed recipe, scheduling or validators", err)
			}
			after, err := s.Items(t.Context(), f.ID)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatal("failed pruning changed saved history", err)
			}
		})
	}
}

func TestPostgresRefreshWaitsForHistoricalPruning(t *testing.T) {
	s, err := testStoreOpener(t, 10)()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if !s.postgres {
		t.Skip("set RSS_TEST_POSTGRES_URL to verify concurrent historical pruning")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	f, before := historyFixture(t, s)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var heldGUID string
	if err := tx.QueryRowContext(ctx, "SELECT guid FROM items WHERE feed_id=$1 AND title='Sports news' FOR UPDATE", f.ID).Scan(&heldGUID); err != nil {
		t.Fatal(err)
	}
	var blockerPID int
	if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	waitForBlockedQuery := func(blocker int, prefix string) int {
		t.Helper()
		for {
			var pid int
			err := s.DB.QueryRowContext(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE $2 LIMIT 1`, blocker, prefix+"%").Scan(&pid)
			if err == nil {
				return pid
			}
			if !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("cannot observe expected database lock", err)
			}
			select {
			case <-ctx.Done():
				t.Fatal("operation did not acquire expected database lock")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	saved := make(chan error, 1)
	go func() {
		edited := f
		edited.Recipe.Filters = historyFilters()
		_, err := s.SaveWithOptions(ctx, edited, SaveOptions{ApplyFiltersToHistory: true})
		saved <- err
	}()
	savePID := waitForBlockedQuery(blockerPID, "DELETE FROM items")
	refreshed := make(chan error, 1)
	go func() {
		refreshed <- s.Complete(ctx, f, before, "old", "old", 200, nil, 0)
	}()
	waitForBlockedQuery(savePID, "SELECT version FROM feeds")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-saved; err != nil {
		t.Fatal("pruning did not finish after releasing item lock", err)
	}
	if err := <-refreshed; !errors.Is(err, ErrStale) {
		t.Fatal("concurrent refresh ignored the newly committed recipe", err)
	}
	after, err := s.Items(ctx, f.ID)
	if err != nil || len(after) != 2 {
		t.Fatal("concurrent stale refresh restored pruned items", err)
	}
	for _, item := range after {
		if !strings.HasPrefix(item.Key, "keep") {
			t.Fatal("concurrent stale refresh restored a nonmatch")
		}
	}
}

func TestFilterPruningBoundAndCancellationRollBack(t *testing.T) {
	s, err := testStoreOpener(t, 10000)()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	f := savedRunFeed(t, s)
	// Simulate a database populated under an unsupported retention setting.
	// A single recursive statement avoids a fixture benchmark or many round trips.
	keyExpression := "CAST(v AS TEXT)"
	if s.postgres {
		keyExpression = "convert_to(CAST(v AS TEXT), 'UTF8')"
	}
	_, err = s.DB.ExecContext(t.Context(), s.bind(`WITH RECURSIVE n(v) AS (SELECT 1 UNION ALL SELECT v+1 FROM n WHERE v<10001)
INSERT INTO items(feed_id,key,guid,title,url,html,image,published,first_seen,last_seen)
SELECT ?,`+keyExpression+`,CAST(v AS TEXT),'Sports','','','',1,1,1 FROM n`), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	edited := f
	edited.Recipe.Filters = historyFilters()
	if _, err := s.SaveWithOptions(t.Context(), edited, SaveOptions{ApplyFiltersToHistory: true}); err == nil {
		t.Fatal("oversized history was silently partially pruned")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.SaveWithOptions(canceled, edited, SaveOptions{ApplyFiltersToHistory: true}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled save accepted", err)
	}
	got, err := s.Get(t.Context(), f.ID)
	if err != nil || got.Version != f.Version || got.Recipe.Filters != nil {
		t.Fatal("failed bounded operation committed recipe", err)
	}
	var count int
	if err := s.DB.QueryRowContext(t.Context(), s.bind("SELECT count(*) FROM items WHERE feed_id=?"), f.ID).Scan(&count); err != nil || count != 10001 {
		t.Fatal("failed bounded operation partially removed history", err)
	}
}

func TestFilterPruningHasIndependentTransactionDeadline(t *testing.T) {
	s, err := testStoreOpener(t, 10)()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	f, before := historyFixture(t, s)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if s.postgres {
		var id string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM feeds WHERE id=$1 FOR UPDATE", f.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
	}
	edited := f
	edited.Recipe.Filters = historyFilters()
	started := time.Now()
	_, err = s.SaveWithOptions(ctx, edited, SaveOptions{ApplyFiltersToHistory: true})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 10*time.Second || ctx.Err() != nil {
		t.Fatal("pruning did not enforce its own shorter transaction deadline", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, f.ID)
	if err != nil || !reflect.DeepEqual(got, f) {
		t.Fatal("timed-out pruning changed the recipe or validators", err)
	}
	after, err := s.Items(ctx, f.ID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("timed-out pruning changed saved history", err)
	}
}
