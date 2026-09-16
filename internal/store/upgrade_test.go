package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sqliteFixture creates a released SQLite schema at a path the test controls.
func sqliteFixture(t *testing.T, version int) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rss.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	body, err := os.ReadFile(filepath.Join("testdata", "schema_v"+strconv.Itoa(version)+".sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	return path, db
}

func preUpgradeBackups(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(path), "backups"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestUpgradeSavesVerifiedCopyOfEveryReleasedSchema(t *testing.T) {
	for version := 1; version < currentSchema; version++ {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			path, legacy := sqliteFixture(t, version)
			// Leave committed rows in the WAL: the copy must include them.
			for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0",
				`INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run) VALUES('kept','token','Kept feed','https://example.com','{}',60,0,0)`,
				`INSERT INTO runs(feed_id,ended,status,count,error) VALUES('kept',1,200,0,'')`} {
				if _, err := legacy.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			s, err := Open(path, 10)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			names := preUpgradeBackups(t, path)
			if len(names) != 1 || !strings.HasPrefix(names[0], "pre-upgrade-schema-"+strconv.Itoa(version)+"-") {
				t.Fatalf("backups after upgrading schema %d: %v", version, names)
			}
			dir := filepath.Join(filepath.Dir(path), "backups", names[0])
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 2 || files[0].Name() != "manifest.json" || files[1].Name() != "rss.db" {
				t.Fatalf("backup contents: %v %v", files, err)
			}
			for name, mode := range map[string]os.FileMode{"": 0700, "rss.db": 0600, "manifest.json": 0600} {
				info, err := os.Stat(filepath.Join(dir, name))
				if err != nil || info.Mode().Perm() != mode {
					t.Fatalf("%s mode: %v %v", name, info.Mode(), err)
				}
			}
			var manifest struct {
				Format        string         `json:"format"`
				Version       int            `json:"version"`
				SchemaVersion int            `json:"schema_version"`
				SHA256        string         `json:"sha256"`
				Counts        map[string]int `json:"counts"`
			}
			raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if err != nil || json.Unmarshal(raw, &manifest) != nil {
				t.Fatal("unreadable manifest", err)
			}
			sum, err := fileSHA256(filepath.Join(dir, "rss.db"))
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]int{"feeds": 1, "items": 0, "runs": 1}
			if manifest.Format != "rss-workshop.sqlite-backup" || manifest.Version != 1 || manifest.SchemaVersion != version || manifest.SHA256 != sum || !reflect.DeepEqual(manifest.Counts, want) {
				t.Fatalf("manifest: %+v", manifest)
			}
			if counts, err := verifyCopy(filepath.Join(dir, "rss.db"), version); err != nil || !reflect.DeepEqual(counts, want) {
				t.Fatalf("copy does not verify at schema %d: %v %v", version, counts, err)
			}
			var live int
			if err := s.DB.QueryRow("SELECT version FROM schema_version").Scan(&live); err != nil || live != currentSchema {
				t.Fatal("database was not upgraded after its copy was saved", err)
			}
			if f, err := s.Get(t.Context(), "kept"); err != nil || f.Title != "Kept feed" {
				t.Fatal("upgrade lost the feed", err)
			}
			s.DB.Close()
			// Reopening an upgraded database copies nothing more.
			again, err := Open(path, 10)
			if err != nil {
				t.Fatal(err)
			}
			again.DB.Close()
			if names := preUpgradeBackups(t, path); len(names) != 1 {
				t.Fatalf("reopening a current database saved another copy: %v", names)
			}
		})
	}
}

func TestNewAndCurrentDatabasesAreNotCopied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rss.db")
	s, err := Open(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	current, _ := sqliteFixture(t, currentSchema)
	s, err = Open(current, 10)
	if err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	for _, p := range []string{path, current} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(p), "backups")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a database that needs no upgrade was copied", err)
		}
	}
}

func TestFailedUpgradeCopyLeavesDatabaseUnmigrated(t *testing.T) {
	path, legacy := sqliteFixture(t, 4)
	// A file where the backups directory belongs makes the copy impossible.
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "backups"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path, 10); s != nil || !errors.Is(err, ErrUpgradeBackup) || !strings.Contains(err.Error(), "schema 4") {
		t.Fatal("upgraded without a copy", err)
	}
	var version int
	var journal string
	if err := legacy.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != 4 {
		t.Fatal("database changed after its copy failed", err)
	}
	if err := legacy.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "delete" {
		t.Fatal("journal mode changed after the copy failed", journal, err)
	}
	for _, table := range []string{"filters", "feed_filters"} {
		if rows, err := legacy.Query("SELECT 1 FROM " + table + " LIMIT 0"); err == nil {
			rows.Close()
			t.Fatalf("%s was created after the copy failed", table)
		}
	}

	// The operator's escape hatch upgrades without a copy.
	if err := os.Remove(filepath.Join(filepath.Dir(path), "backups")); err != nil {
		t.Fatal(err)
	}
	s, err := OpenWithOptions(path, 10, OpenOptions{SkipUpgradeBackup: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if err := s.DB.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != currentSchema {
		t.Fatal("skipping the copy did not upgrade", err)
	}
	if names := preUpgradeBackups(t, path); len(names) != 0 {
		t.Fatalf("skipping the copy still saved one: %v", names)
	}
}

func TestFailedCopyVerificationRemovesPartialCopy(t *testing.T) {
	path, legacy := sqliteFixture(t, 4)
	// A row violating a foreign key survives VACUUM INTO but fails the check
	// backup.py applies, so the copy must be discarded and nothing migrated.
	if _, err := legacy.Exec(`INSERT INTO runs(feed_id,ended,status,count,error) VALUES('missing-feed',1,200,0,'')`); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path, 10); s != nil || !errors.Is(err, ErrUpgradeBackup) || !strings.Contains(err.Error(), "foreign key") {
		t.Fatal("an unverifiable copy was accepted", err)
	}
	if names := preUpgradeBackups(t, path); len(names) != 0 {
		t.Fatalf("a failed copy left files behind: %v", names)
	}
	var version int
	if err := legacy.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != 4 {
		t.Fatal("database changed after verification failed", err)
	}
}

// A migration that keeps failing, restarted by a restart policy, must not save
// a new copy of the same database each time, or the loop fills the disk.
func TestRepeatedFailedUpgradesReuseIdenticalCopy(t *testing.T) {
	path, legacy := sqliteFixture(t, 4)
	if _, err := legacy.Exec(`CREATE TRIGGER prevent_version_five BEFORE UPDATE ON schema_version WHEN NEW.version=5 BEGIN SELECT RAISE(ABORT, 'fixture blocks final migration'); END`); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	t.Cleanup(func() { copyClock = time.Now })
	for attempt := range 3 {
		copyClock = func() time.Time { return start.Add(time.Duration(attempt) * time.Minute) }
		if s, err := Open(path, 10); s != nil || err == nil || errors.Is(err, ErrUpgradeBackup) {
			t.Fatal("blocked migration was accepted, or its copy failed", err)
		}
	}
	names := preUpgradeBackups(t, path)
	if len(names) != 1 || names[0] != "pre-upgrade-schema-4-20260916T120000Z" {
		t.Fatalf("repeated failed upgrades of an unchanged database: %v", names)
	}
	if _, err := verifyCopy(filepath.Join(filepath.Dir(path), "backups", names[0], "rss.db"), 4); err != nil {
		t.Fatal(err)
	}

	// A database that changed since the last copy gets a copy of its own.
	if _, err := legacy.Exec(`INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run) VALUES('new','token','New feed','https://example.com','{}',60,0,0)`); err != nil {
		t.Fatal(err)
	}
	copyClock = func() time.Time { return start.Add(time.Hour) }
	if s, err := Open(path, 10); s != nil || err == nil || errors.Is(err, ErrUpgradeBackup) {
		t.Fatal("blocked migration was accepted, or its copy failed", err)
	}
	if names := preUpgradeBackups(t, path); len(names) != 2 || names[1] != "pre-upgrade-schema-4-20260916T130000Z" {
		t.Fatalf("a changed database was not copied again: %v", names)
	}
	// Within the same second, a different database cannot replace a copy.
	if _, err := legacy.Exec(`DELETE FROM feeds WHERE id='new'`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run) VALUES('other','token2','Other feed','https://example.com','{}',60,0,0)`); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path, 10); s != nil || !errors.Is(err, ErrUpgradeBackup) || !strings.Contains(err.Error(), "different contents") {
		t.Fatal("a copy with the same name and different contents was replaced", err)
	}
}

func TestAbandonedPartialCopyIsRemoved(t *testing.T) {
	path, _ := sqliteFixture(t, 4)
	abandoned := filepath.Join(filepath.Dir(path), "backups", ".pre-upgrade-partial-pre-upgrade-schema-4-20260101T000000Z-123")
	if err := os.MkdirAll(abandoned, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abandoned, "rss.db"), []byte("half a copy"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	if names := preUpgradeBackups(t, path); len(names) != 1 || strings.HasPrefix(names[0], ".") {
		t.Fatalf("abandoned partial copy was kept: %v", names)
	}
}

func TestCopyThatCannotFitIsRefusedBeforeWriting(t *testing.T) {
	path, legacy := sqliteFixture(t, 4)
	t.Cleanup(func() { copySpace = freeSpace })
	copySpace = func(string) (uint64, bool) { return 4096, true }
	if s, err := Open(path, 10); s != nil || !errors.Is(err, ErrUpgradeBackup) || !strings.Contains(err.Error(), "MiB free") {
		t.Fatal("a copy that cannot fit was attempted", err)
	}
	if names := preUpgradeBackups(t, path); len(names) != 0 {
		t.Fatalf("a refused copy wrote files: %v", names)
	}
	var version int
	if err := legacy.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != 4 {
		t.Fatal("database changed after the copy was refused", err)
	}
	copySpace = func(string) (uint64, bool) { return 0, false }
	s, err := Open(path, 10)
	if err != nil {
		t.Fatal("unknown free space blocked the copy", err)
	}
	s.DB.Close()
}
