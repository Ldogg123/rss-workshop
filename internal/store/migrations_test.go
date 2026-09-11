package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"rss-workshop/internal/model"
	"rss-workshop/internal/testutil"
)

// The fixtures contain released table definitions, not a relabeled current schema.
func legacyStore(t *testing.T) (*Store, func() (*Store, error)) {
	t.Helper()
	return releasedStore(t, 1)
}

func releasedStore(t *testing.T, version int) (*Store, func() (*Store, error)) {
	t.Helper()
	var db *sql.DB
	var open func() (*Store, error)
	var err error
	postgres := false
	fixture := "schema_v" + strconv.Itoa(version) + ".sql"
	if raw := testutil.PostgresURL(t); raw != "" {
		postgres = true
		fixture = "postgres_" + fixture
		db, err = sql.Open("pgx", raw)
		open = func() (*Store, error) { return OpenPostgres(t.Context(), raw, 10) }
	} else {
		path := filepath.Join(t.TempDir(), "legacy.db")
		db, err = sql.Open("sqlite", path)
		open = func() (*Store, error) { return Open(path, 10) }
	}
	if err != nil {
		t.Fatal("cannot open legacy fixture")
	}
	t.Cleanup(func() { db.Close() })
	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), string(body)); err != nil {
		t.Fatal("cannot create legacy fixture", err)
	}
	return &Store{DB: db, MaxItems: 10, postgres: postgres}, open
}

// legacyItems reads items with the pre-schema-4 column list. The current
// Items() selects content_full, which a schema 1-3 database does not have, so
// migration tests must capture "before" rows the way those releases stored them.
func legacyItems(t *testing.T, s *Store, feedID string) []model.Item {
	t.Helper()
	rows, err := s.DB.QueryContext(t.Context(), s.bind(`SELECT key,guid,title,url,html,image,published,first_seen,last_seen FROM items WHERE feed_id=? ORDER BY published DESC,key`), feedID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []model.Item{}
	for rows.Next() {
		var it model.Item
		var p, f, l int64
		if err := rows.Scan(&it.Key, &it.GUID, &it.Title, &it.URL, &it.HTML, &it.Image, &p, &f, &l); err != nil {
			t.Fatal(err)
		}
		it.Published, it.FirstSeen, it.LastSeen = stamp(p), stamp(f), stamp(l)
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// legacyInsertItem stores one item the way schema 1-3 did, deriving the same
// GUID the merge does so the PostgreSQL identity matches.
func legacyInsertItem(t *testing.T, s *Store, feedID, key, title string) {
	t.Helper()
	guid := fmt.Sprintf("urn:sha256:%x", sha256.Sum256([]byte(feedID+"\x00"+key)))
	now := time.Now().UTC().Unix()
	if _, err := s.DB.ExecContext(t.Context(), s.bind(`INSERT INTO items(feed_id,key,guid,title,url,html,image,published,first_seen,last_seen) VALUES(?,?,?,?,?,?,?,?,?,?)`),
		feedID, s.opaque(key), guid, title, "", "", "", now, now, now); err != nil {
		t.Fatal(err)
	}
}

// Keep every released schema in this matrix as new migrations are added. The
// current opener must upgrade the entire chain without an intermediate binary.
func TestEveryReleasedSchemaPreservesStoredRowsAcrossDirectUpgrade(t *testing.T) {
	for _, original := range []int{1, 2, 3, 4} {
		t.Run(strconv.Itoa(original), func(t *testing.T) {
			legacy, open := releasedStore(t, original)
			ctx := t.Context()
			tx, err := legacy.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			// Use released SQL directly rather than current Save/Complete, which
			// can normalize recipes, reset validators, and enforce retention.
			recipe := `{ "mode":"static", "type":"xpath", "items":"//article", "title":{"selector":".//h2"} }`
			if original == 3 {
				// Previously accepted history may no longer match current filters.
				// Merely opening a database must never apply historical pruning.
				recipe = `{ "mode":"static", "type":"css", "items":"article", "filters":{"exclude":{"op":"contains_any","field":"title","keywords":["Saved"]}} }`
			}
			for _, enabled := range []bool{false, true} {
				id := "preserved-" + strconv.FormatBool(enabled)
				_, err = tx.ExecContext(ctx, legacy.bind(`INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run,last_attempt,last_success,error,failures,etag,modified,version)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), id, id+"-reader-token", "Existing feed Σ", "https://example.org/news", recipe, 777, enabled,
					1400000600, 1400000300, 1400000000, "Previously saved failure", 3, legacy.opaque("etag\x00\xff"), legacy.opaque("modified\x00\xff"), 19)
				if err != nil {
					t.Fatal("cannot seed released feed state", err)
				}
				// Both histories deliberately exceed today's configured limits.
				for i := 0; i < 12; i++ {
					key := "key-" + strconv.Itoa(i)
					_, err = tx.ExecContext(ctx, legacy.bind(`INSERT INTO items(feed_id,key,guid,title,url,html,image,published,first_seen,last_seen)
VALUES(?,?,?,?,?,?,?,?,?,?)`), id, legacy.opaque(key+"\x00\xff"), id+"-guid-"+strconv.Itoa(i), "Saved story σ "+strconv.Itoa(i),
						"https://example.org/"+key, "<p>Stored <b>description</b> &amp; text</p>", "https://example.org/image.png", 1300000000+i, 1400000000+i, 1400000300+i)
					if err != nil {
						t.Fatal("cannot seed released item history", err)
					}
				}
				for i := 0; i < 52; i++ {
					_, err = tx.ExecContext(ctx, legacy.bind("INSERT INTO runs(feed_id,ended,status,count,error) VALUES(?,?,?,?,?)"), id, 1400000000+i, 503, i, "Original run summary")
					if err != nil {
						t.Fatal("cannot seed released run history", err)
					}
				}
			}
			if original >= 2 {
				if _, err := tx.ExecContext(ctx, legacy.bind("UPDATE runs SET diagnostics=?"), `{ "version":1, "requested_mode":"static", "attempts":[{"mode":"static","outcome":"failed","stage":"fetch","status":503}] }`); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			before := releasedRows(t, legacy.DB, original)
			if err := legacy.DB.Close(); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				s, err := open()
				if err != nil {
					t.Fatal("direct upgrade or reopen failed", err)
				}
				var current int
				if err := s.DB.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&current); err != nil || current != 4 {
					s.DB.Close()
					t.Fatal("direct upgrade did not reach current schema", err)
				}
				after := releasedRows(t, s.DB, original)
				for table, want := range before {
					if !reflect.DeepEqual(after[table], want) {
						s.DB.Close()
						t.Fatalf("direct upgrade or reopen changed released %s values", table)
					}
				}
				if err := s.DB.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func releasedRows(t *testing.T, db *sql.DB, version int) map[string][][]any {
	t.Helper()
	queries := map[string]string{
		"feeds": "SELECT id,rss_token,title,url,recipe,interval,enabled,next_run,last_attempt,last_success,error,failures,etag,modified,version FROM feeds ORDER BY id",
		"items": "SELECT feed_id,key,guid,title,url,html,image,published,first_seen,last_seen FROM items ORDER BY feed_id,guid",
		"runs":  "SELECT id,feed_id,ended,status,count,error FROM runs ORDER BY id",
	}
	if version >= 2 {
		queries["runs"] = "SELECT id,feed_id,ended,status,count,error,diagnostics FROM runs ORDER BY id"
	}
	if version >= 4 {
		queries["items"] = "SELECT feed_id,key,guid,title,url,html,image,content_full,published,first_seen,last_seen FROM items ORDER BY feed_id,guid"
	}
	out := make(map[string][][]any, len(queries))
	for table, query := range queries {
		rows, err := db.QueryContext(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values, destinations := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out[table] = append(out[table], values)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
	return out
}

func TestSchemaOneMigrationPreservesLibraryAndRunSummaries(t *testing.T) {
	ctx := t.Context()
	legacy, open := legacyStore(t)
	id, err := legacy.Save(ctx, model.Feed{Title: "Existing feed", URL: "https://example.com", Interval: 600, Recipe: model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.DB.ExecContext(ctx, legacy.bind(`INSERT INTO items(feed_id,key,guid,title,url,html,image,published,first_seen,last_seen) VALUES(?,?,?,?,?,?,?,?,?,?)`), id, legacy.opaque("saved-key"), "saved-guid", "Saved story", "https://example.com/story", "<p>Saved</p>", "", 123, 124, 125)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{200, 503} {
		if _, err = legacy.DB.ExecContext(ctx, legacy.bind("INSERT INTO runs(feed_id,ended,status,count,error) VALUES(?,?,?,?,?)"), id, 1000+status, status, 1, "Original summary"); err != nil {
			t.Fatal(err)
		}
	}
	beforeFeed, err := legacy.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	beforeItems := legacyItems(t, legacy, id)
	if err := legacy.DB.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := open()
	if err != nil {
		t.Fatal("version-1 migration failed", err)
	}
	t.Cleanup(func() { s.DB.Close() })
	var version int
	if err = s.DB.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil || version != 4 {
		t.Fatal("migration did not advance schema version", err)
	}
	afterFeed, err := s.Get(ctx, id)
	if err != nil || !reflect.DeepEqual(afterFeed, beforeFeed) {
		t.Fatal("migration changed feed configuration, token or scheduling", err)
	}
	afterItems, err := s.Items(ctx, id)
	if err != nil || !reflect.DeepEqual(afterItems, beforeItems) {
		t.Fatal("migration changed saved items or identity", err)
	}
	runs, err := s.Runs(ctx, id)
	if err != nil || len(runs) != 2 || runs[0].Status != 503 || runs[1].Status != 200 || runs[0].Ended.Unix() != 1503 {
		t.Fatal("migration lost existing run history", err)
	}
	for _, run := range runs {
		if run.Count != 1 || run.Error != "Original summary" || run.Diagnostics != nil {
			t.Fatal("legacy run summaries acquired fabricated details")
		}
	}
	if err := s.CompleteWithDiagnostics(ctx, afterFeed, nil, "", "", 304, nil, 0, testRunDiagnostics()); err != nil {
		t.Fatal("migrated database cannot record new diagnostics", err)
	}
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = open()
	if err != nil {
		t.Fatal("reopening migrated database failed", err)
	}
	runs, err = s.Runs(ctx, id)
	if err != nil || len(runs) != 3 || runs[0].Diagnostics == nil || runs[0].Status != 304 || runs[1].Diagnostics != nil {
		t.Fatal("reopening lost mixed old/new history", err)
	}
}

func TestSchemaOneMigrationFailureRollsBack(t *testing.T) {
	legacy, open := legacyStore(t)
	query := `CREATE TRIGGER prevent_version_update BEFORE UPDATE ON schema_version BEGIN SELECT RAISE(ABORT, 'fixture blocks migration'); END`
	if legacy.postgres {
		query = `CREATE FUNCTION prevent_version_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture blocks migration'; END $$;
CREATE TRIGGER prevent_version_update BEFORE UPDATE ON schema_version FOR EACH ROW EXECUTE FUNCTION prevent_version_update()`
	}
	if _, err := legacy.DB.ExecContext(t.Context(), query); err != nil {
		t.Fatal("cannot install failure fixture", err)
	}
	if s, err := open(); err == nil || s != nil {
		t.Fatal("failed migration was accepted")
	}
	var version int
	if err := legacy.DB.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&version); err != nil || version != 1 {
		t.Fatal("failed migration changed version", err)
	}
	if rows, err := legacy.DB.QueryContext(t.Context(), "SELECT diagnostics FROM runs LIMIT 0"); err == nil {
		rows.Close()
		t.Fatal("failed migration left its new column behind")
	}
	if !legacy.postgres {
		var count int
		if err := legacy.DB.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name='runs_feed_history'").Scan(&count); err != nil || count != 0 {
			t.Fatal("failed migration left its new index behind", err)
		}
	}
}

func TestSchemaTwoMigrationPreservesDiagnosticsAndHistory(t *testing.T) {
	ctx := t.Context()
	legacy, open := releasedStore(t, 2)
	f := savedRunFeed(t, legacy)
	// The run and feed state come from the real writer, because schema 2 is
	// about run diagnostics. The item is inserted with the released column
	// list: the current merge writes content_full, which schema 2 lacks.
	if err := legacy.CompleteWithDiagnostics(ctx, f, nil, "validator", "modified", 200, nil, 0, testRunDiagnostics()); err != nil {
		t.Fatal(err)
	}
	legacyInsertItem(t, legacy, f.ID, "saved", "Saved story")
	beforeFeed, err := legacy.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeItems := legacyItems(t, legacy, f.ID)
	beforeRuns, err := legacy.Runs(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacy.DB.Close()
	s, err := open()
	if err != nil {
		t.Fatal("version-2 migration failed", err)
	}
	defer s.DB.Close()
	var version int
	if err := s.DB.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil || version != 4 {
		t.Fatal("schema 2 did not migrate to 3", err)
	}
	afterFeed, err := s.Get(ctx, f.ID)
	if err != nil || !reflect.DeepEqual(afterFeed, beforeFeed) {
		t.Fatal("migration changed feed state", err)
	}
	afterItems, err := s.Items(ctx, f.ID)
	if err != nil || !reflect.DeepEqual(afterItems, beforeItems) {
		t.Fatal("migration changed saved history", err)
	}
	afterRuns, err := s.Runs(ctx, f.ID)
	if err != nil || !reflect.DeepEqual(afterRuns, beforeRuns) {
		t.Fatal("migration changed diagnostics", err)
	}
}

func TestSchemaThreeMigrationFailureRollsBackEntireUpgrade(t *testing.T) {
	for _, original := range []int{1, 2} {
		t.Run(strconv.Itoa(original), func(t *testing.T) {
			legacy, open := releasedStore(t, original)
			query := `CREATE TRIGGER prevent_version_three BEFORE UPDATE ON schema_version WHEN NEW.version=3 BEGIN SELECT RAISE(ABORT, 'fixture blocks final migration'); END`
			if legacy.postgres {
				query = `CREATE FUNCTION prevent_version_three() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=3 THEN RAISE EXCEPTION 'fixture blocks final migration'; END IF; RETURN NEW; END $$;
CREATE TRIGGER prevent_version_three BEFORE UPDATE ON schema_version FOR EACH ROW EXECUTE FUNCTION prevent_version_three()`
			}
			if _, err := legacy.DB.ExecContext(t.Context(), query); err != nil {
				t.Fatal(err)
			}
			if s, err := open(); s != nil || err == nil {
				t.Fatal("blocked final migration was accepted")
			}
			var version int
			if err := legacy.DB.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&version); err != nil || version != original {
				t.Fatal("failed final migration left a partial version upgrade", err)
			}
			rows, err := legacy.DB.QueryContext(t.Context(), "SELECT diagnostics FROM runs LIMIT 0")
			if rows != nil {
				rows.Close()
			}
			if (err == nil) != (original == 2) {
				t.Fatal("failed final migration changed original diagnostic columns")
			}
			if !legacy.postgres {
				var indexes int
				if err := legacy.DB.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name='runs_feed_history'").Scan(&indexes); err != nil || indexes != original-1 {
					t.Fatal("failed final migration changed original history index", err)
				}
			}
		})
	}
}

func TestUnsupportedSchemaDoesNotMigrateOrChangeJournal(t *testing.T) {
	for _, version := range []int{0, 5, 99} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			legacy, open := legacyStore(t)
			if _, err := legacy.DB.ExecContext(t.Context(), legacy.bind("UPDATE schema_version SET version=?"), version); err != nil {
				t.Fatal(err)
			}
			if s, err := open(); err == nil || s != nil {
				t.Fatal("unsupported schema was accepted")
			}
			var after int
			if err := legacy.DB.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&after); err != nil || after != version {
				t.Fatal("unsupported version was changed", err)
			}
			if rows, err := legacy.DB.QueryContext(t.Context(), "SELECT diagnostics FROM runs LIMIT 0"); err == nil {
				rows.Close()
				t.Fatal("unsupported schema was migrated")
			}
			if !legacy.postgres {
				var journal string
				if err := legacy.DB.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&journal); err != nil || journal != "delete" {
					t.Fatal("unsupported SQLite journal mode was changed", err)
				}
			}
		})
	}
}

func TestSQLiteInitializationDoesNotAdoptUnrelatedTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unrelated.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE feeds(marker TEXT); INSERT INTO feeds VALUES('preserve me')"); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path, 10); s != nil || err == nil {
		t.Fatal("unrelated SQLite tables were adopted")
	}
	var marker string
	if err := db.QueryRow("SELECT marker FROM feeds").Scan(&marker); err != nil || marker != "preserve me" {
		t.Fatal("failed initialization changed existing table", err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='schema_version'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed initialization committed a version table", err)
	}
}
