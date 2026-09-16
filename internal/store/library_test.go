package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rss-workshop/internal/filter"
	"rss-workshop/internal/model"
)

func libraryStore(t *testing.T) (*Store, func() (*Store, error)) {
	t.Helper()
	open := testStoreOpener(t, 10)
	s, err := open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s, open
}

func saveLibraryFilter(t *testing.T, s *Store, name string, set model.FilterSet) string {
	t.Helper()
	id, err := s.SaveFilter(t.Context(), model.LibraryFilter{Name: name, Filters: set}, SaveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func excludeTitles(keywords ...string) model.FilterSet {
	return model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: keywords}}
}

func TestLibraryFiltersLinkMergeAndPersist(t *testing.T) {
	ctx := t.Context()
	s, open := libraryStore(t)
	sponsored := saveLibraryFilter(t, s, "No sponsored posts", excludeTitles("sponsored"))
	science := saveLibraryFilter(t, s, "Science only", model.FilterSet{Include: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"science"}}})
	f, _ := historyFixture(t, s)
	f.Recipe.Filters = &model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "link", Keywords: []string{"/sport"}}}
	f.FilterIDs = []string{science, sponsored}
	if _, err := s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	other := savedRunFeed(t, s)
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	got, err := s.Get(ctx, f.ID)
	if err != nil || !reflect.DeepEqual(got.FilterIDs, []string{science, sponsored}) {
		t.Fatalf("library filter order did not persist: %v %v", got.FilterIDs, err)
	}
	if got.Recipe.Filters.Include != nil || got.Recipe.Filters.Exclude.Keywords[0] != "/sport" {
		t.Fatal("stored recipe must keep only the feed's own rules")
	}
	feeds, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, listed := range feeds {
		want := []string{}
		if listed.ID == f.ID {
			want = []string{science, sponsored}
		}
		if !reflect.DeepEqual(listed.FilterIDs, want) {
			t.Fatalf("listed filter IDs for %s: %v", listed.Title, listed.FilterIDs)
		}
	}
	library, err := s.Filters(ctx)
	if err != nil || len(library) != 2 || library[0].Name != "No sponsored posts" || !reflect.DeepEqual(library[0].FeedIDs, []string{f.ID}) || !reflect.DeepEqual(library[1].FeedIDs, []string{f.ID}) {
		t.Fatalf("library listing: %+v %v", library, err)
	}
	recipe, err := s.EffectiveRecipe(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := filter.Compile(recipe.Filters)
	if err != nil {
		t.Fatal(err)
	}
	for title, want := range map[string]bool{"Science update": true, "Sponsored science": false, "Sports news": false} {
		item := model.Item{Title: title, URL: "https://example.com/news"}
		if title == "Sports news" {
			item.Title, item.URL = "Science sport", "https://example.com/sport"
		}
		if matcher.Match(item) != want {
			t.Fatalf("merged rules for %q: want %v", title, want)
		}
	}
	if unchanged, err := s.EffectiveRecipe(ctx, other); err != nil || !reflect.DeepEqual(unchanged, other.Recipe) {
		t.Fatal("feed without library filters must keep its recipe", err)
	}

	// Omitting the list keeps it (pause/resume clients); an empty list clears it.
	got.FilterIDs, got.Enabled = nil, !got.Enabled
	if _, err := s.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	if kept, err := s.Get(ctx, f.ID); err != nil || len(kept.FilterIDs) != 2 {
		t.Fatal("a save without filter_ids unlinked library filters", err)
	}
	got.FilterIDs = []string{}
	if _, err := s.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	if cleared, err := s.Get(ctx, f.ID); err != nil || len(cleared.FilterIDs) != 0 {
		t.Fatal("an empty filter_ids list did not unlink", err)
	}

	// Deleting a feed releases its links, so the filter can then be deleted.
	got.FilterIDs = []string{sponsored}
	if _, err := s.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFilter(ctx, sponsored); !errors.Is(err, ErrFilterInUse) {
		t.Fatal("deleted a filter in use", err)
	}
	if err := s.DeleteFilter(ctx, science); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFilter(ctx, science); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("deleting a missing filter", err)
	}
	if err := s.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFilter(ctx, sponsored); err != nil {
		t.Fatal("feed deletion left a library link behind", err)
	}
}

func TestLibraryFilterEditInvalidatesOnlyLinkedFeeds(t *testing.T) {
	ctx := t.Context()
	s, _ := libraryStore(t)
	id := saveLibraryFilter(t, s, "Exclude adverts", excludeTitles("advert"))
	linked, before := historyFixture(t, s)
	linked.FilterIDs = []string{id}
	if _, err := s.Save(ctx, linked); err != nil {
		t.Fatal(err)
	}
	unlinked, _ := historyFixture(t, s)
	var err error
	for _, f := range []*model.Feed{&linked, &unlinked} {
		if err := s.Complete(ctx, *f, nil, "etag", "modified", 200, nil, 0); err != nil && !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
		if *f, err = s.Get(ctx, f.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SaveFilter(ctx, model.LibraryFilter{ID: id, Name: "  Exclude sponsored  ", Filters: excludeTitles("sponsored")}, SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, linked, before, "old", "", 200, nil, 0); !errors.Is(err, ErrStale) {
		t.Fatal("a refresh started before the filter edit was saved", err)
	}
	after, err := s.Get(ctx, linked.ID)
	if err != nil || after.Version != linked.Version+1 || after.ETag != "" || after.LastModified != "" || after.RSSToken != linked.RSSToken {
		t.Fatal("filter edit did not invalidate the linked feed's version and validators", err)
	}
	if items, err := s.Items(ctx, linked.ID); err != nil || !reflect.DeepEqual(items, before) {
		t.Fatal("filter edit without the history option changed saved stories", err)
	}
	untouched, err := s.Get(ctx, unlinked.ID)
	if err != nil || untouched.Version != unlinked.Version || untouched.ETag != "etag" {
		t.Fatal("filter edit changed a feed that does not use it", err)
	}
	library, err := s.Filters(ctx)
	if err != nil || library[0].Name != "Exclude sponsored" || library[0].Filters.Exclude.Keywords[0] != "sponsored" {
		t.Fatalf("edited filter: %+v %v", library, err)
	}

	// The history option prunes every linked feed with its merged rules.
	if _, err := s.SaveFilter(ctx, model.LibraryFilter{ID: id, Name: "Exclude sponsored", Filters: excludeTitles("sponsored")}, SaveOptions{ApplyFiltersToHistory: true}); err != nil {
		t.Fatal(err)
	}
	items, err := s.Items(ctx, linked.ID)
	if err != nil || len(items) != len(before)-1 {
		t.Fatal("history option did not remove the nonmatching story", err)
	}
	for _, item := range items {
		if strings.Contains(item.Title, "Sponsored") {
			t.Fatal("pruning kept an excluded story")
		}
	}
	if kept, err := s.Items(ctx, unlinked.ID); err != nil || len(kept) != len(before) {
		t.Fatal("history option pruned a feed that does not use the filter", err)
	}
	if _, err := s.SaveFilter(ctx, model.LibraryFilter{ID: "missing", Name: "x", Filters: excludeTitles("x")}, SaveOptions{}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("editing a missing filter", err)
	}
}

func TestLibraryFilterLimitsRejectAndRollBack(t *testing.T) {
	ctx := t.Context()
	s, _ := libraryStore(t)
	half := make([]string, filter.MaxKeywords/2+1)
	for i := range half {
		half[i] = "keyword-" + strconv.Itoa(i)
	}
	small := saveLibraryFilter(t, s, "Small", excludeTitles("advert"))
	large := saveLibraryFilter(t, s, "Large", excludeTitles(half...))
	f := savedRunFeed(t, s)
	f.Recipe.Filters = &model.FilterSet{Include: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: half}}
	f.FilterIDs = []string{small}
	var validated []model.Feed
	options := SaveOptions{Validate: func(f model.Feed) error { validated = append(validated, f); return nil }}
	if _, err := s.SaveWithOptions(ctx, f, options); err != nil {
		t.Fatal(err)
	}
	if len(validated) != 1 || validated[0].Recipe.Filters.Exclude == nil || validated[0].Recipe.Filters.Include == nil {
		t.Fatal("validation did not receive the merged recipe")
	}
	f, err := s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}

	var bad *InvalidError
	f.FilterIDs = []string{small, large}
	if _, err := s.Save(ctx, f); !errors.As(err, &bad) || !strings.Contains(err.Error(), "library filters") {
		t.Fatal("a feed save exceeding merged keyword limits was accepted", err)
	}
	f.FilterIDs = []string{small, small}
	if _, err := s.Save(ctx, f); !errors.As(err, &bad) {
		t.Fatal("a duplicate library filter was accepted", err)
	}
	f.FilterIDs = []string{"missing"}
	if _, err := s.Save(ctx, f); !errors.Is(err, ErrUnknownFilter) {
		t.Fatal("an unknown library filter was accepted", err)
	}
	f.FilterIDs = make([]string, MaxFiltersPerFeed+1)
	if _, err := s.Save(ctx, f); !errors.As(err, &bad) {
		t.Fatal("too many library filters were accepted", err)
	}
	rejected := errors.New("feed configuration exceeds the 64 KiB editor limit")
	f.FilterIDs = []string{small}
	if _, err := s.SaveWithOptions(ctx, f, SaveOptions{Validate: func(model.Feed) error { return rejected }}); !errors.As(err, &bad) || !errors.Is(err, rejected) {
		t.Fatal("merged validation failure was not reported as invalid", err)
	}
	if still, err := s.Get(ctx, f.ID); err != nil || still.Version != f.Version || !reflect.DeepEqual(still.FilterIDs, []string{small}) {
		t.Fatal("rejected feed saves changed the feed", err)
	}

	// Growing the linked filter past the merged limit names the feed and
	// leaves both the filter and the feed as they were.
	if _, err := s.SaveFilter(ctx, model.LibraryFilter{ID: small, Name: "Small", Filters: excludeTitles(half...)}, SaveOptions{}); !errors.As(err, &bad) || !strings.Contains(err.Error(), `"Run history"`) {
		t.Fatal("a library edit exceeding a linked feed's limits was accepted", err)
	}
	library, err := s.Filters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, lf := range library {
		if lf.ID == small && !reflect.DeepEqual(lf.Filters, excludeTitles("advert")) {
			t.Fatal("rejected library edit was saved")
		}
	}
	if still, err := s.Get(ctx, f.ID); err != nil || still.Version != f.Version {
		t.Fatal("rejected library edit changed the linked feed", err)
	}
	for _, empty := range []model.FilterSet{{}, {Include: &model.FilterRule{Op: "all"}}} {
		if _, err := s.SaveFilter(ctx, model.LibraryFilter{Name: "Empty", Filters: empty}, SaveOptions{}); !errors.As(err, &bad) {
			t.Fatal("an empty or invalid library filter was accepted", err)
		}
	}
}

func TestLibraryFilterCountLimit(t *testing.T) {
	s, _ := libraryStore(t)
	for i := 0; i < MaxLibraryFilters; i++ {
		saveLibraryFilter(t, s, "Filter "+strconv.Itoa(i), excludeTitles("x"))
	}
	var bad *InvalidError
	if _, err := s.SaveFilter(t.Context(), model.LibraryFilter{Name: "One too many", Filters: excludeTitles("x")}, SaveOptions{}); !errors.As(err, &bad) {
		t.Fatal("library grew past its limit", err)
	}
}

func TestSchemaFiveMigrationFailureRollsBack(t *testing.T) {
	legacy, open := releasedStore(t, 4)
	query := `CREATE TRIGGER prevent_version_five BEFORE UPDATE ON schema_version WHEN NEW.version=5 BEGIN SELECT RAISE(ABORT, 'fixture blocks final migration'); END`
	if legacy.postgres {
		query = `CREATE FUNCTION prevent_version_five() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=5 THEN RAISE EXCEPTION 'fixture blocks final migration'; END IF; RETURN NEW; END $$;
CREATE TRIGGER prevent_version_five BEFORE UPDATE ON schema_version FOR EACH ROW EXECUTE FUNCTION prevent_version_five()`
	}
	if _, err := legacy.DB.ExecContext(t.Context(), query); err != nil {
		t.Fatal(err)
	}
	if s, err := open(); s != nil || err == nil {
		t.Fatal("blocked schema 5 migration was accepted")
	}
	var version int
	if err := legacy.DB.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&version); err != nil || version != 4 {
		t.Fatal("failed migration changed the schema version", err)
	}
	for _, table := range []string{"filters", "feed_filters"} {
		rows, err := legacy.DB.QueryContext(t.Context(), "SELECT 1 FROM "+table+" LIMIT 0")
		if rows != nil {
			rows.Close()
		}
		if err == nil {
			t.Fatalf("failed migration left the %s table behind", table)
		}
	}
}

// The schema 5 tables are written twice, once for fresh databases and once for
// the migration. Both paths must produce the same definitions.
func TestSchemaFiveMigrationMatchesFreshSchema(t *testing.T) {
	definitions := func(s *Store) [][]any {
		t.Helper()
		// The added last_changed columns cannot compare CREATE statements, since
		// ALTER TABLE appends them, so compare their column definitions.
		query := `SELECT type,name,tbl_name,sql FROM sqlite_master WHERE tbl_name IN ('filters','feed_filters')
UNION ALL SELECT 'column',m.name||'.'||p.name,p.type,p."notnull"||' '||COALESCE(p.dflt_value,'') FROM sqlite_master m JOIN pragma_table_info(m.name) p WHERE m.type='table' AND m.name IN ('feeds','items') AND p.name='last_changed'
ORDER BY 2`
		if s.postgres {
			query = `SELECT table_name,column_name,data_type,COALESCE(collation_name,''),is_nullable,COALESCE(column_default,'') FROM information_schema.columns WHERE table_schema=current_schema() AND (table_name IN ('filters','feed_filters') OR (table_name IN ('feeds','items') AND column_name='last_changed'))
UNION ALL SELECT tablename,indexname,replace(indexdef,' ON '||current_schema()||'.',' ON '),'','','' FROM pg_indexes WHERE schemaname=current_schema() AND tablename IN ('filters','feed_filters')
UNION ALL SELECT conrelid::regclass::text,conname,pg_get_constraintdef(oid),'','','' FROM pg_constraint WHERE conrelid IN ('filters'::regclass,'feed_filters'::regclass)
ORDER BY 1,2,3`
		}
		rows, err := s.DB.QueryContext(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		columns, _ := rows.Columns()
		var out [][]any
		for rows.Next() {
			values, destinations := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				t.Fatal(err)
			}
			out = append(out, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if len(out) < 5 {
			t.Fatalf("schema 5 tables or columns are missing: %v", out)
		}
		return out
	}
	legacy, open := releasedStore(t, 4)
	legacy.DB.Close()
	upgraded, err := open()
	if err != nil {
		t.Fatal(err)
	}
	upgradedDefinitions := definitions(upgraded)
	upgraded.DB.Close()
	// Each opener gets its own database or PostgreSQL schema.
	fresh, err := testStoreOpener(t, 10)()
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.DB.Close()
	if got := definitions(fresh); !reflect.DeepEqual(got, upgradedDefinitions) {
		t.Fatalf("migrated schema 5 tables differ from a fresh database:\nmigrated %v\n   fresh %v", upgradedDefinitions, got)
	}
}

// Feed saves share-lock their library filters before the feed row, and filter
// edits lock the filter before feed rows in ID order, so concurrent saves on
// overlapping feeds and filters wait for each other instead of deadlocking.
func TestConcurrentLibraryAndFeedSavesDoNotDeadlock(t *testing.T) {
	ctx := t.Context()
	s, _ := libraryStore(t)
	filters := []string{saveLibraryFilter(t, s, "A", excludeTitles("a")), saveLibraryFilter(t, s, "B", excludeTitles("b"))}
	feeds := make([]model.Feed, 4)
	for i := range feeds {
		feeds[i] = savedRunFeed(t, s)
		feeds[i].FilterIDs = filters
		if _, err := s.Save(ctx, feeds[i]); err != nil {
			t.Fatal(err)
		}
	}
	errs := make(chan error, 64)
	var wg sync.WaitGroup
	for round := 0; round < 8; round++ {
		for i, id := range filters {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := s.SaveFilter(ctx, model.LibraryFilter{ID: id, Name: "Edited", Filters: excludeTitles("round-" + strconv.Itoa(round+i))}, SaveOptions{ApplyFiltersToHistory: round%2 == 0})
				errs <- err
			}()
		}
		for i := range feeds {
			wg.Add(1)
			go func() {
				defer wg.Done()
				f := feeds[i]
				if (round+i)%2 == 0 {
					f.FilterIDs = []string{filters[1], filters[0]}
				}
				_, err := s.SaveWithOptions(ctx, f, SaveOptions{ApplyFiltersToHistory: true})
				errs <- err
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("concurrent library and feed saves failed", err)
		}
	}
}

// postgresRace opens a PostgreSQL store and a transaction that plays the
// concurrent save; SQLite runs one transaction at a time and has no such race.
func postgresRace(t *testing.T) (*Store, context.Context, *sql.Tx, func(prefix string)) {
	t.Helper()
	s, _ := libraryStore(t)
	if !s.postgres {
		t.Skip("set RSS_TEST_POSTGRES_URL to verify concurrent library filter saves")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	var blocker int
	if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	waitBlocked := func(prefix string) {
		t.Helper()
		for {
			var pid int
			err := s.DB.QueryRowContext(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE $2 LIMIT 1`, blocker, prefix+"%").Scan(&pid)
			if err == nil {
				return
			}
			if !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("cannot observe expected database lock", err)
			}
			select {
			case <-ctx.Done():
				t.Fatalf("no query starting %q waited for the concurrent transaction", prefix)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	return s, ctx, tx, waitBlocked
}

func TestPostgresDeleteFilterSeesLinkCommittedWhileWaiting(t *testing.T) {
	s, ctx, tx, waitBlocked := postgresRace(t)
	id := saveLibraryFilter(t, s, "Racing", excludeTitles("x"))
	f := savedRunFeed(t, s)
	// A feed save share-locks the filter, then links it.
	if _, err := tx.ExecContext(ctx, "SELECT id FROM filters WHERE id=$1 FOR SHARE", id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO feed_filters(feed_id,filter_id,position) VALUES($1,$2,0)", f.ID, id); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- s.DeleteFilter(ctx, id) }()
	waitBlocked("SELECT id FROM filters")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; !errors.Is(err, ErrFilterInUse) {
		t.Fatal("deleting a filter linked while the delete waited", err)
	}
}

func TestPostgresFilterEditUsesFeedStateCommittedWhileWaiting(t *testing.T) {
	s, ctx, tx, waitBlocked := postgresRace(t)
	half := make([]string, filter.MaxKeywords/2)
	for i := range half {
		half[i] = "keyword-" + strconv.Itoa(i)
	}
	a := saveLibraryFilter(t, s, "A", excludeTitles("a"))
	b := saveLibraryFilter(t, s, "B", excludeTitles("b"))
	f, before := historyFixture(t, s)
	f.FilterIDs = []string{a, b}
	if _, err := s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	// A concurrent edit of B grows it while holding the shared feed row. The
	// edit of A must validate against that committed B, not the one it saw first.
	grown, _ := json.Marshal(excludeTitles(half...))
	if _, err := tx.ExecContext(ctx, "UPDATE filters SET rules=$1 WHERE id=$2", string(grown), b); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "SELECT id FROM feeds WHERE id=$1 FOR UPDATE", f.ID); err != nil {
		t.Fatal(err)
	}
	saved := make(chan error, 1)
	go func() {
		_, err := s.SaveFilter(ctx, model.LibraryFilter{ID: a, Name: "A", Filters: excludeTitles(append(half, "one more")...)}, SaveOptions{})
		saved <- err
	}()
	waitBlocked("SELECT id FROM feeds")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var bad *InvalidError
	if err := <-saved; !errors.As(err, &bad) {
		t.Fatal("filter edit validated against a sibling filter replaced while it waited", err)
	}

	// A concurrent save that removes A from the feed: the edit must neither
	// invalidate nor prune a feed that no longer uses it.
	current, err := s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	unlink, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unlink.Rollback()
	var blocker int
	if err := unlink.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := unlink.ExecContext(ctx, "UPDATE feeds SET version=version+1 WHERE id=$1", f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := unlink.ExecContext(ctx, "DELETE FROM feed_filters WHERE feed_id=$1 AND filter_id=$2", f.ID, a); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, err := s.SaveFilter(ctx, model.LibraryFilter{ID: a, Name: "A", Filters: excludeTitles("science")}, SaveOptions{ApplyFiltersToHistory: true})
		saved <- err
	}()
	for {
		var pid int
		err := s.DB.QueryRowContext(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE 'SELECT id FROM feeds%' LIMIT 1`, blocker).Scan(&pid)
		if err == nil {
			break
		}
		if !errors.Is(err, sql.ErrNoRows) || ctx.Err() != nil {
			t.Fatal("filter edit did not wait for the feed row", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := unlink.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, f.ID)
	if err != nil || after.Version != current.Version+1 || !reflect.DeepEqual(after.FilterIDs, []string{b}) {
		t.Fatalf("filter edit changed a feed unlinked while it waited: %+v %v", after, err)
	}
	if items, err := s.Items(ctx, f.ID); err != nil || len(items) != len(before) {
		t.Fatal("filter edit pruned a feed unlinked while it waited", err)
	}
}

func TestPostgresSaveKeepingLinksRejectsLinksChangedWhileWaiting(t *testing.T) {
	s, ctx, tx, waitBlocked := postgresRace(t)
	a := saveLibraryFilter(t, s, "A", excludeTitles("a"))
	b := saveLibraryFilter(t, s, "B", excludeTitles("b"))
	f := savedRunFeed(t, s)
	f.FilterIDs = []string{a}
	if _, err := s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	// An editor save swaps A for B while this pause, which omits filter_ids,
	// has already read and validated the old links.
	if _, err := tx.ExecContext(ctx, "UPDATE feeds SET version=version+1 WHERE id=$1", f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM feed_filters WHERE feed_id=$1", f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO feed_filters(feed_id,filter_id,position) VALUES($1,$2,0)", f.ID, b); err != nil {
		t.Fatal(err)
	}
	saved := make(chan error, 1)
	go func() {
		paused := f
		paused.FilterIDs, paused.Enabled = nil, false
		_, err := s.Save(ctx, paused)
		saved <- err
	}()
	waitBlocked("UPDATE feeds SET last_changed=CASE WHEN title")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var bad *InvalidError
	if err := <-saved; !errors.As(err, &bad) || !strings.Contains(err.Error(), "reload") {
		t.Fatal("a save kept links it had not validated", err)
	}
}

// A refresh holding the feed row may be merging bodies fetched with the old
// article selector. Clearing them before that row lock would miss those rows.
func TestPostgresSelectorChangeClearsBodiesMergedWhileWaiting(t *testing.T) {
	s, ctx, tx, waitBlocked := postgresRace(t)
	f := savedRunFeed(t, s)
	f.Recipe.Full = &model.FullContent{Selector: "div.wrong"}
	if _, err := s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	f, err := s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "SELECT id FROM feeds WHERE id=$1 FOR UPDATE", f.ID); err != nil {
		t.Fatal(err)
	}
	guid := fmt.Sprintf("urn:sha256:%x", sha256.Sum256([]byte(f.ID+"\x00late")))
	if _, err := tx.ExecContext(ctx, `INSERT INTO items(feed_id,key,guid,title,url,html,image,content_full,published,first_seen,last_seen) VALUES($1,$2,$3,'Late','','','','<p>wrong block</p>',1,1,1)`, f.ID, []byte("late"), guid); err != nil {
		t.Fatal(err)
	}
	saved := make(chan error, 1)
	go func() {
		edited := f
		edited.Recipe.Full = &model.FullContent{Selector: "article"}
		_, err := s.Save(ctx, edited)
		saved <- err
	}()
	waitBlocked("UPDATE feeds SET last_changed=CASE WHEN title")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	items, err := s.Items(ctx, f.ID)
	if err != nil || len(items) != 1 || items[0].FullHTML != "" {
		t.Fatalf("a body merged while the selector change waited survived: %+v %v", items, err)
	}
}
