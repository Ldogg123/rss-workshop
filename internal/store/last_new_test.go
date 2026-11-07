package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/feed"
	"rss-workshop/internal/model"
)

// readerOutput is what a reader receives for a feed: both formats' bytes and
// ETags, and the Last-Modified time, which falls back to the last success the
// way the reader handler does.
func readerOutput(t *testing.T, f model.Feed, items []model.Item) []string {
	t.Helper()
	rss, rssTag, err := feed.Render(f, items, "https://reader.example/feeds/"+f.RSSToken+".xml", "RSS Workshop")
	if err != nil {
		t.Fatal(err)
	}
	atom, atomTag, err := feed.RenderAtom(f, items, "https://reader.example")
	if err != nil {
		t.Fatal(err)
	}
	modified := f.LastChanged
	if modified.IsZero() {
		modified = f.LastSuccess
	}
	return []string{string(rss), rssTag, string(atom), atomTag, modified.UTC().String()}
}

// Schema 6 starts each feed's last_new at its newest stored story, and 0 for a
// feed with none, without changing anything a reader receives: an upgrade must
// keep every subscriber on 304. Every released schema takes the same path.
func TestSchemaSixBackfillsLastNewWithoutChangingOutput(t *testing.T) {
	for original := 1; original < currentSchema; original++ {
		t.Run(strconv.Itoa(original), func(t *testing.T) {
			ctx := t.Context()
			legacy, open := releasedStore(t, original)
			for _, id := range []string{"stories", "empty"} {
				if _, err := legacy.DB.ExecContext(ctx, legacy.bind(`INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run,last_attempt,last_success,version)
VALUES(?,?,?,'https://example.com/news','{}',600,?,1700001500,1700000900,1700000900,3)`), id, id+"-token", "Feed "+id, true); err != nil {
					t.Fatal(err)
				}
			}
			// The newest story by first_seen is neither the newest published
			// nor the last stored.
			for i, firstSeen := range []int64{1700000100, 1700000500, 1700000300} {
				key := string(rune('a' + i))
				guid := fmt.Sprintf("urn:sha256:%x", sha256.Sum256([]byte("stories\x00"+key)))
				if _, err := legacy.DB.ExecContext(ctx, legacy.bind(`INSERT INTO items(feed_id,key,guid,title,url,html,image,published,first_seen,last_seen)
VALUES('stories',?,?,?,?,'<p>Teaser</p>','https://example.com/image.png',?,?,1700000900)`), legacy.opaque(key), guid, "Story "+key, "https://example.com/"+key, 1700000000+int64(i), firstSeen); err != nil {
					t.Fatal(err)
				}
			}
			if original >= 4 {
				if _, err := legacy.DB.ExecContext(ctx, "UPDATE items SET content_full='<p>Full article</p>' WHERE title='Story b'"); err != nil {
					t.Fatal(err)
				}
			}
			if original >= 5 {
				if _, err := legacy.DB.ExecContext(ctx, "UPDATE feeds SET last_changed=1700000700; UPDATE items SET last_changed=1700000600 WHERE title='Story c'"); err != nil {
					t.Fatal(err)
				}
			}
			before := map[string][]string{}
			expected := map[string]model.Feed{}
			for _, id := range []string{"stories", "empty"} {
				f, err := releasedGet(t, legacy, id, original)
				if err != nil {
					t.Fatal(err)
				}
				expected[id] = f
				// The released application had no last_new to render.
				f.LastNew = time.Time{}
				before[id] = readerOutput(t, f, releasedItems(t, legacy, id, original))
			}
			legacy.DB.Close()

			s, err := open()
			if err != nil {
				t.Fatal(err)
			}
			for id, newest := range map[string]int64{"stories": 1700000500, "empty": 0} {
				f, err := s.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if !f.LastNew.Equal(stamp(newest)) {
					t.Fatalf("%s: last_new %v after upgrading schema %d, want %d", id, f.LastNew, original, newest)
				}
				if !reflect.DeepEqual(f, expected[id]) {
					t.Fatalf("%s: upgrade changed the feed:\n got %+v\nwant %+v", id, f, expected[id])
				}
				items, err := s.Items(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if got := readerOutput(t, f, items); !reflect.DeepEqual(got, before[id]) {
					t.Fatalf("%s: upgrade changed reader output", id)
				}
			}
			// The backfill runs once: a later value survives reopening.
			if _, err := s.DB.ExecContext(ctx, "UPDATE feeds SET last_new=1700002000 WHERE id='empty'"); err != nil {
				t.Fatal(err)
			}
			s.DB.Close()
			s, err = open()
			if err != nil {
				t.Fatal("reopening the upgraded database failed", err)
			}
			defer s.DB.Close()
			if f, err := s.Get(ctx, "empty"); err != nil || f.LastNew.Unix() != 1700002000 {
				t.Fatal("reopening the upgraded database rewrote last_new", f.LastNew, err)
			}
		})
	}
}

// ALTER TABLE appends last_new, so a fresh database lists it last as well, with
// the same type, nullability and default, whichever schema it migrated from.
func TestSchemaSixFeedColumnsMatchFreshSchema(t *testing.T) {
	definitions := func(s *Store) [][]any {
		t.Helper()
		query := `SELECT cid,name,type,"notnull",COALESCE(dflt_value,''),pk FROM pragma_table_info('feeds') ORDER BY cid`
		if s.postgres {
			query = `SELECT ordinal_position,column_name,data_type,COALESCE(collation_name,''),is_nullable,COALESCE(column_default,'') FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='feeds' ORDER BY ordinal_position`
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
		if len(out) == 0 || fmt.Sprint(out[len(out)-1][1]) != "last_new" {
			t.Fatalf("feeds does not end with last_new: %v", out)
		}
		return out
	}
	fresh, err := testStoreOpener(t, 10)()
	if err != nil {
		t.Fatal(err)
	}
	want := definitions(fresh)
	fresh.DB.Close()
	for original := 1; original < currentSchema; original++ {
		t.Run(strconv.Itoa(original), func(t *testing.T) {
			legacy, open := releasedStore(t, original)
			legacy.DB.Close()
			upgraded, err := open()
			if err != nil {
				t.Fatal(err)
			}
			defer upgraded.DB.Close()
			if got := definitions(upgraded); !reflect.DeepEqual(got, want) {
				t.Fatalf("migrated feeds columns differ from a fresh database:\nmigrated %v\n   fresh %v", got, want)
			}
		})
	}
}

// A failed step rolls back the whole chain: the database keeps its released
// schema, without last_new and without anything an earlier step added.
func TestSchemaSixMigrationFailureRollsBackEntireUpgrade(t *testing.T) {
	for _, original := range []int{4, 5} {
		t.Run(strconv.Itoa(original), func(t *testing.T) {
			legacy, open := releasedStore(t, original)
			query := `CREATE TRIGGER prevent_version_six BEFORE UPDATE ON schema_version WHEN NEW.version=6 BEGIN SELECT RAISE(ABORT, 'fixture blocks final migration'); END`
			if legacy.postgres {
				query = `CREATE FUNCTION prevent_version_six() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=6 THEN RAISE EXCEPTION 'fixture blocks final migration'; END IF; RETURN NEW; END $$;
CREATE TRIGGER prevent_version_six BEFORE UPDATE ON schema_version FOR EACH ROW EXECUTE FUNCTION prevent_version_six()`
			}
			if _, err := legacy.DB.ExecContext(t.Context(), query); err != nil {
				t.Fatal(err)
			}
			if _, err := legacy.DB.ExecContext(t.Context(), `INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run) VALUES('kept','kept-token','Kept','https://example.com','{}',600,false,0)`); err != nil {
				t.Fatal(err)
			}
			if s, err := open(); s != nil || err == nil {
				t.Fatal("blocked schema 6 migration was accepted")
			}
			var version int
			if err := legacy.DB.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&version); err != nil || version != original {
				t.Fatal("failed migration changed the schema version", version, err)
			}
			absent := []string{"SELECT last_new FROM feeds"}
			if original < 5 {
				absent = append(absent, "SELECT last_changed FROM feeds", "SELECT 1 FROM filters")
			}
			for _, query := range absent {
				rows, err := legacy.DB.QueryContext(t.Context(), query+" LIMIT 0")
				if rows != nil {
					rows.Close()
				}
				if err == nil {
					t.Fatalf("failed migration left a change behind: %s", query)
				}
			}
			var title string
			if err := legacy.DB.QueryRowContext(t.Context(), "SELECT title FROM feeds WHERE id='kept'").Scan(&title); err != nil || title != "Kept" {
				t.Fatal("failed migration changed stored feeds", err)
			}
		})
	}
}

// The fixture for the current schema is frozen when it is released, and every
// later migration test starts from it, so until then it must be exactly what a
// fresh database is created from. A change to either shipped schema needs the
// fixture regenerated in the same change.
func TestCurrentSchemaFixturesMatchShippedSchemas(t *testing.T) {
	header := fmt.Sprintf("-- Frozen schema-%d fixture for migration compatibility tests.\n", currentSchema)
	for _, c := range []struct{ fixture, shipped, prefix string }{
		{"schema_v%d.sql", schema, ""},
		{"postgres_schema_v%d.sql", postgresSchema, fmt.Sprintf("CREATE TABLE schema_version(singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK(singleton), version INTEGER NOT NULL);\nINSERT INTO schema_version(version) VALUES(%d);\n", currentSchema)},
	} {
		name := fmt.Sprintf(c.fixture, currentSchema)
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if got := string(raw); got != header+c.prefix+c.shipped {
			t.Errorf("%s does not match the shipped schema; regenerate it", name)
		}
	}
	// The shipped SQLite schema records its own version.
	if want := fmt.Sprintf("INSERT INTO schema_version VALUES(%d);\n", currentSchema); !strings.Contains(schema, want) {
		t.Errorf("schema.sql does not record schema %d", currentSchema)
	}
}

// A new database starts at the current schema on either backend, so reopening
// it finds nothing to migrate.
func TestNewDatabaseRecordsCurrentSchema(t *testing.T) {
	open := testStoreOpener(t, 10)
	for attempt := range 2 {
		s, err := open()
		if err != nil {
			t.Fatal("opening a new database failed", attempt, err)
		}
		var version int
		err = s.DB.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&version)
		s.DB.Close()
		if err != nil || version != currentSchema {
			t.Fatalf("a new database records schema %d, want %d (%v)", version, currentSchema, err)
		}
	}
}

// last_new records a new story, not a changed one. Only a stored refresh that
// inserts a story the feed does not have stored, and that retention keeps,
// moves it.
func TestLastNewMovesOnlyWhenAKeptStoryIsInserted(t *testing.T) {
	ctx := t.Context()
	s, _ := libraryStore(t)
	f := savedRunFeed(t, s)
	if !f.LastNew.IsZero() {
		t.Fatal("a feed that never refreshed has a new story time", f.LastNew)
	}
	stories := []model.Item{
		{Key: "a", Title: "First", URL: "https://example.com/a", HTML: "<p>One</p>", Published: time.Unix(1700000000, 0)},
		{Key: "b", Title: "Second", URL: "https://example.com/b", HTML: "<p>Two</p>", Published: time.Unix(1700000001, 0)},
	}
	get := func() model.Feed {
		t.Helper()
		current, err := s.Get(ctx, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	refresh := func(items []model.Item, runErr error) {
		t.Helper()
		if err := s.Complete(ctx, get(), items, "etag", "", 200, runErr, 0); err != nil {
			t.Fatal(err)
		}
	}
	firstSeen := func(key string) time.Time {
		t.Helper()
		var seen int64
		if err := s.DB.QueryRowContext(ctx, s.bind("SELECT first_seen FROM items WHERE feed_id=? AND key=?"), f.ID, s.opaque(key)).Scan(&seen); err != nil {
			t.Fatal(key, err)
		}
		return stamp(seen)
	}
	// Moves the stored stories and last_new into the past, so a story
	// counts as new only if this refresh inserted it, even within a second.
	old := time.Unix(1600000000, 0).UTC()
	age := func() {
		t.Helper()
		for _, query := range []string{"UPDATE feeds SET last_new=? WHERE id=?", "UPDATE items SET first_seen=? WHERE feed_id=?"} {
			if _, err := s.DB.ExecContext(ctx, s.bind(query), old.Unix(), f.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	unchanged := func(what string) {
		t.Helper()
		if got := get().LastNew; !got.Equal(old) {
			t.Fatalf("%s moved last_new to %v", what, got)
		}
	}

	refresh(stories, nil)
	if got := get(); got.LastNew.IsZero() || !got.LastNew.Equal(firstSeen("a")) {
		t.Fatalf("first stories stored: last_new %v, first_seen %v", got.LastNew, firstSeen("a"))
	}
	age()
	refresh(stories, nil)
	unchanged("an identical refresh")
	refresh(nil, nil) // a 304 from the source saves no items
	unchanged("a source 304")
	refresh(append(stories, model.Item{Key: "failed", Title: "Never stored", Published: time.Unix(1700000005, 0)}), errors.New("fetch failed"))
	unchanged("a failed refresh")

	edited := append([]model.Item(nil), stories...)
	edited[0].Title = "First, corrected"
	edited[1].FullHTML = "<p>Full article</p>"
	before := get().LastChanged
	refresh(edited, nil)
	if !get().LastChanged.After(before) {
		t.Fatal("the changed stories did not change output; this test no longer covers them")
	}
	unchanged("changed stories")

	// A listed story older than the retention window is inserted and removed
	// in the same refresh, every refresh, without ever being published.
	s.MaxItems = 2
	refresh(append(edited, model.Item{Key: "too-old", Title: "Too old", Published: time.Unix(1600000000, 0)}), nil)
	unchanged("a story retention removed in the same refresh")
	s.MaxItems = 10

	// Edits, pauses and history pruning publish or store no new story, and a
	// refresh discarded by an edit stores nothing.
	stale := get()
	renamed := stale
	renamed.Title = "Renamed"
	if _, err := s.Save(ctx, renamed); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, stale, append(edited, model.Item{Key: "stale", Title: "Stale", Published: time.Unix(1700000006, 0)}), "", "", 200, nil, 0); !errors.Is(err, ErrStale) {
		t.Fatal("a refresh started before an edit was stored", err)
	}
	unchanged("an edit, and the refresh it discarded")
	for _, enabled := range []bool{false, true} {
		if err := s.SetEnabled(ctx, f.ID, enabled); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Queue(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	unchanged("pause, resume and refresh now")
	pruned := get()
	pruned.Recipe.Filters = &model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"Second"}}}
	if _, err := s.SaveWithOptions(ctx, pruned, SaveOptions{ApplyFiltersToHistory: true}); err != nil {
		t.Fatal(err)
	}
	if got := get(); got.Count != 1 {
		t.Fatalf("history pruning kept %d stories; this test no longer covers it", got.Count)
	}
	unchanged("history pruning")

	// Nothing remembers deleted rows: a story the feed no longer has stored is
	// new again when its source still lists it and the filter is removed.
	unfiltered := get()
	unfiltered.Recipe.Filters = nil
	if _, err := s.Save(ctx, unfiltered); err != nil {
		t.Fatal(err)
	}
	refresh(edited, nil)
	if got := get(); got.Count != 2 || got.LastNew.Equal(old) || !got.LastNew.Equal(firstSeen("b")) {
		t.Fatalf("a story stored again after history pruning: last_new %v, first_seen %v, %d stored", got.LastNew, firstSeen("b"), got.Count)
	}
	age()

	// A new story retention keeps is new, even when retention removes an older
	// one to make room for it.
	third := model.Item{Key: "c", Title: "Third", URL: "https://example.com/c", Published: time.Unix(1700000010, 0)}
	s.MaxItems = 1
	refresh([]model.Item{edited[0], third}, nil)
	got := get()
	if got.Count != 1 || got.LastNew.Equal(old) || !got.LastNew.Equal(firstSeen("c")) {
		t.Fatalf("a new kept story: last_new %v, first_seen %v, %d stored", got.LastNew, firstSeen("c"), got.Count)
	}

	// The story that refresh removed counts as new again when a raised
	// MAX_ITEMS stores it once more.
	age()
	s.MaxItems = 10
	refresh([]model.Item{edited[0], third}, nil)
	if got := get(); got.Count != 2 || got.LastNew.Equal(old) || !got.LastNew.Equal(firstSeen("a")) {
		t.Fatalf("a story stored again after a raised MAX_ITEMS: last_new %v, first_seen %v, %d stored", got.LastNew, firstSeen("a"), got.Count)
	}
}
