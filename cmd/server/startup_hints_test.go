package main

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rss-workshop/internal/config"
)

// dataFiles lists everything under a data directory, so a test can tell that a
// start left it exactly as it was.
func dataFiles(t *testing.T, directory string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != directory {
			name, _ := filepath.Rel(directory, path)
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// A data directory the process may not write to is what a first Docker start
// meets when the host directory still belongs to root. SQLite's own errors for
// it name neither the directory nor the fix, so startup refuses the directory
// first, says what to change, and leaves it as it was found.
func TestUnwritableDataDirectoryNamesTheFix(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write to any directory, so none can be made unwritable")
	}
	for _, tc := range []struct {
		name     string
		database bool
	}{
		{"empty directory", false},
		{"directory holding a database", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "data")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			if tc.database {
				s, err := openStore(config.Config{DataDir: directory, MaxItems: 500})
				if err != nil {
					t.Fatal(err)
				}
				s.DB.Close()
			}
			before := dataFiles(t, directory)
			stored, _ := os.ReadFile(filepath.Join(directory, "rss.db"))
			if err := os.Chmod(directory, 0500); err != nil {
				t.Fatal(err)
			}
			// The test's own cleanup has to be able to empty the directory.
			t.Cleanup(func() { os.Chmod(directory, 0700) })

			s, err := openStore(config.Config{DataDir: directory, MaxItems: 500, UpgradeBackup: true})
			if s != nil {
				s.DB.Close()
			}
			if err == nil {
				t.Fatal("startup accepted a data directory it may not write to")
			}
			for _, want := range []string{
				directory, "DATA_DIR",
				fmt.Sprintf("uid %d, gid %d", os.Geteuid(), os.Getegid()),
				"permission denied",
				"chown 65532:65532 <your RSS_DATA_DIR>",
				"docs/deployment.md",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error does not mention %q: %v", want, err)
				}
			}
			if !errors.Is(err, fs.ErrPermission) {
				t.Errorf("the error lost its cause: %v", err)
			}
			if strings.Contains(err.Error(), ".write-check") {
				t.Errorf("the error names the probe file instead of the directory: %v", err)
			}
			if after := dataFiles(t, directory); !equalNames(before, after) {
				t.Errorf("the refused start changed the directory: %v became %v", before, after)
			}
			if after, _ := os.ReadFile(filepath.Join(directory, "rss.db")); !bytes.Equal(stored, after) {
				t.Error("the refused start changed the database")
			}
		})
	}
}

// The probe must never be found afterwards: not beside a new database, not at
// a later start, and not in the copy saved before a schema upgrade.
func TestWritableDataDirectoryKeepsNoProbeFile(t *testing.T) {
	onlyDatabase := func(t *testing.T, directory string) {
		t.Helper()
		for _, name := range dataFiles(t, directory) {
			// The store was closed, but SQLite may keep its journal files.
			if name != "rss.db" && name != "rss.db-wal" && name != "rss.db-shm" {
				t.Fatalf("startup left %q in the data directory", name)
			}
		}
	}
	directory := filepath.Join(t.TempDir(), "data")
	for _, start := range []string{"first start", "second start"} {
		s, err := openStore(config.Config{DataDir: directory, MaxItems: 500, UpgradeBackup: true})
		if err != nil {
			t.Fatal(start, err)
		}
		s.DB.Close()
		onlyDatabase(t, directory)
	}

	// An upgrade still saves its copy, and only the database is in it.
	directory = filepath.Join(t.TempDir(), "data")
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
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := openStore(config.Config{DataDir: directory, MaxItems: 500, UpgradeBackup: true})
	if err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	copies, err := filepath.Glob(filepath.Join(directory, "backups", "pre-upgrade-schema-4-*"))
	if err != nil || len(copies) != 1 {
		t.Fatal("startup did not copy the database before upgrading it", copies, err)
	}
	if saved := dataFiles(t, copies[0]); !equalNames(saved, []string{"manifest.json", "rss.db"}) {
		t.Fatalf("the copy holds %v", saved)
	}
	if err := os.RemoveAll(filepath.Join(directory, "backups")); err != nil {
		t.Fatal(err)
	}
	onlyDatabase(t, directory)
}

// Only a directory the process may not write to is refused. Every other
// failure of the probe, a full disk for one, is left to the database, which
// reported it before: the probe must not stop a start the database allows.
func TestDataDirectoryProbeRefusesOnlyPermissionFailures(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	for name, directory := range map[string]string{"not a directory": file, "missing": missing} {
		if err := checkDataDir(directory); err != nil {
			t.Errorf("%s: the probe reported what the database should: %v", name, err)
		}
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("the probe created the directory it was asked to check")
	}
}

func equalNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
