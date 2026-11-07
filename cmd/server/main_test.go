package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"rss-workshop/internal/config"
	"rss-workshop/internal/model"
	"rss-workshop/internal/testutil"
)

func TestDefaultStoreCreatesSQLite(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "data")
	s, err := openStore(config.Config{DataDir: directory, MaxItems: 500})
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if _, err := os.Stat(filepath.Join(directory, "rss.db")); err != nil {
		t.Fatal("SQLite database was not created", err)
	}
}

func TestPostgresFailureDoesNotCreateSQLite(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "must-not-exist")
	s, err := openStore(config.Config{DataDir: directory, MaxItems: 500,
		DatabaseURL: "postgres://rss:private-fixture-password%zz@localhost/rss_workshop"})
	if s != nil || err == nil {
		t.Fatal("invalid PostgreSQL configuration silently opened a store")
	}
	if strings.Contains(err.Error(), "private-fixture-password") {
		t.Fatal("database error exposed the credential")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("PostgreSQL failure touched SQLite's data directory")
	}
}

func TestPostgresDoesNotRequireDataDirectory(t *testing.T) {
	databaseURL := testutil.PostgresURL(t)
	if databaseURL == "" {
		t.Skip("set RSS_TEST_POSTGRES_URL to a disposable database")
	}
	// A file cannot be used as a data directory. PostgreSQL must not touch it.
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("keep this file"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(config.Config{DataDir: path, MaxItems: 500, DatabaseURL: databaseURL})
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if _, err := s.Save(context.Background(), model.Feed{Title: "PostgreSQL startup", URL: "https://example.com", Interval: 60}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep this file" {
		t.Fatal("PostgreSQL startup changed the data directory path")
	}
}

// Startup copies an older SQLite database before upgrading it, and a failed
// copy names both ways forward instead of upgrading anyway.
func TestStartupUpgradeCopyFollowsConfiguration(t *testing.T) {
	legacy := func(t *testing.T) string {
		t.Helper()
		directory := filepath.Join(t.TempDir(), "data")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		schema, err := os.ReadFile("../../internal/store/testdata/schema_v4.sql")
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(directory, "rss.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(string(schema)); err != nil {
			t.Fatal(err)
		}
		return directory
	}
	directory := legacy(t)
	s, err := openStore(config.Config{DataDir: directory, MaxItems: 500, UpgradeBackup: true})
	if err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	if copies, err := filepath.Glob(filepath.Join(directory, "backups", "pre-upgrade-schema-4-*", "manifest.json")); err != nil || len(copies) != 1 {
		t.Fatal("startup did not copy the database before upgrading it", copies, err)
	}

	directory = legacy(t)
	if err := os.WriteFile(filepath.Join(directory, "backups"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := openStore(config.Config{DataDir: directory, MaxItems: 500, UpgradeBackup: true}); s != nil || err == nil || !strings.Contains(err.Error(), "UPGRADE_BACKUP=false") {
		t.Fatal("a failed copy did not explain how to continue", err)
	}
	s, err = openStore(config.Config{DataDir: directory, MaxItems: 500, UpgradeBackup: false})
	if err != nil {
		t.Fatal("UPGRADE_BACKUP=false did not upgrade without a copy", err)
	}
	s.DB.Close()
}

// A stop signal during a PostgreSQL schema upgrade, such as the SIGTERM of a
// container restart, rolls the upgrade back on the server before the process
// exits. Otherwise PostgreSQL went on running it with its locks held, for up
// to the upgrade's time limit, and every start until then failed at the lock.
func TestStopSignalRollsBackPostgresUpgrade(t *testing.T) {
	if raw := os.Getenv("RSS_TEST_UPGRADE_CHILD_URL"); raw != "" {
		// The child starts as the server does and exits as main does.
		_, err := openStore(config.Config{DatabaseURL: raw, MaxItems: 500})
		fmt.Println(err)
		os.Exit(1)
	}
	raw := testutil.PostgresURL(t)
	if raw == "" {
		t.Skip("set RSS_TEST_POSTGRES_URL to a disposable database")
	}
	db, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal("cannot configure the PostgreSQL fixture")
	}
	defer db.Close()
	schema, err := os.ReadFile("../../internal/store/testdata/postgres_schema_v5.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), string(schema)+`;
INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run) VALUES('kept','kept-token','Kept','https://example.com','{}',600,true,0);
CREATE FUNCTION slow_upgrade() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(30); RETURN NEW; END $$;
CREATE TRIGGER slow_upgrade BEFORE UPDATE ON schema_version FOR EACH ROW EXECUTE FUNCTION slow_upgrade()`); err != nil {
		t.Fatal(err)
	}
	// Only this test's upgrade locks this schema's feeds table.
	locked := func(query string) bool {
		t.Helper()
		var found bool
		if err := db.QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE l.relation='feeds'::regclass`+query+`)`).Scan(&found); err != nil {
			t.Fatal(err)
		}
		return found
	}
	child := exec.Command(os.Args[0], "-test.run=^TestStopSignalRollsBackPostgresUpgrade$")
	child.Env = append(os.Environ(), "RSS_TEST_UPGRADE_CHILD_URL="+raw)
	var out bytes.Buffer
	child.Stdout, child.Stderr = &out, &out
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(20 * time.Second); !locked(" AND a.wait_event='PgSleep'"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			child.Process.Kill()
			child.Wait()
			t.Fatal("the upgrade did not start")
		}
	}
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	child.Wait()
	want := "cannot migrate PostgreSQL database from schema version 5 to 6: startup was interrupted; the database is unchanged"
	if child.ProcessState.ExitCode() != 1 || !strings.Contains(out.String(), want) {
		t.Fatalf("a stop signal during the upgrade did not stop startup with %q:\n%s", want, out.String())
	}
	for deadline := time.Now().Add(3 * time.Second); locked(""); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("PostgreSQL went on running the upgrade after the process exited")
		}
	}
	var version int
	var title string
	if err := db.QueryRowContext(t.Context(), "SELECT version,(SELECT title FROM feeds WHERE id='kept') FROM schema_version").Scan(&version, &title); err != nil || version != 5 || title != "Kept" {
		t.Fatal("the interrupted upgrade changed the database", version, title, err)
	}
}
