package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"rss-workshop/internal/model"
	"rss-workshop/internal/testutil"
)

// The fixtures are the released version-1 table definitions, rather than the
// current schema with migration-specific substitutions applied to it.
func legacyStore(t *testing.T) (*Store, func() (*Store, error)) {
	t.Helper()
	var db *sql.DB
	var open func() (*Store, error)
	var err error
	postgres := false
	fixture := "schema_v1.sql"
	if raw := testutil.PostgresURL(t); raw != "" {
		postgres = true
		fixture = "postgres_schema_v1.sql"
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
	beforeItems, err := legacy.Items(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.DB.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := open()
	if err != nil {
		t.Fatal("version-1 migration failed", err)
	}
	t.Cleanup(func() { s.DB.Close() })
	var version int
	if err = s.DB.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil || version != 2 {
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

func TestUnsupportedSchemaDoesNotMigrateOrChangeJournal(t *testing.T) {
	for _, version := range []int{0, 3, 99} {
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
