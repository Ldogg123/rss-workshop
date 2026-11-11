package main

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// A read-only mount cannot be made in a test, so the failure is supplied: each
// refusal names what could not be written and its own fix, a read-only file
// system is never told to change an owner, and a failure that is neither is
// left to the database.
func TestDataDirectoryRefusalNamesTheFixForItsCause(t *testing.T) {
	const (
		chown       = "chown 65532:65532 <your RSS_DATA_DIR>"
		chownFiles  = "chown -R 65532:65532 <your RSS_DATA_DIR>"
		remount     = "mount the directory read-write"
		remountFile = "mount the file read-write"
	)
	for _, tc := range []struct {
		file             string
		cause            syscall.Errno
		what, says, hint string
	}{
		{"", syscall.EACCES, "the data directory /data (DATA_DIR)", "permission denied", chown},
		{"", syscall.EPERM, "the data directory /data (DATA_DIR)", "operation not permitted", chown},
		{"", syscall.EROFS, "the data directory /data (DATA_DIR)", "read-only file system", remount},
		{"", syscall.ENOSPC, "", "", ""},
		{"rss.db", syscall.EACCES, "the database file /data/rss.db (in DATA_DIR)", "permission denied", chownFiles},
		{"rss.db-wal", syscall.EPERM, "the database file /data/rss.db-wal (in DATA_DIR)", "operation not permitted", chownFiles},
		{"rss.db", syscall.EROFS, "the database file /data/rss.db (in DATA_DIR)", "read-only file system", remountFile},
		{"rss.db", syscall.EISDIR, "", "", ""},
	} {
		name := fmt.Sprintf("%q %v", tc.file, tc.cause)
		failure := &fs.PathError{Op: "open", Path: filepath.Join("/data", dataDirProbe), Err: tc.cause}
		err := dataDirError("/data", tc.file, failure)
		if tc.hint == "" {
			if err != nil {
				t.Errorf("%s: startup reported what the database should: %v", name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: startup was not refused", name)
			continue
		}
		for _, want := range []string{
			"cannot write to " + tc.what + " as uid", tc.says, tc.hint, "then start again", "docs/deployment.md",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the error does not mention %q: %v", name, want, err)
			}
		}
		for _, other := range []string{chown, chownFiles, remount, remountFile, dataDirProbe} {
			if other != tc.hint && strings.Contains(err.Error(), other) {
				t.Errorf("%s: the error mentions %q: %v", name, other, err)
			}
		}
		if !errors.Is(err, tc.cause) {
			t.Errorf("%s: the error lost its cause: %v", name, err)
		}
	}
	if err := dataDirError("/data", "", nil); err != nil {
		t.Errorf("a successful check was refused: %v", err)
	}
}

// A directory copied or restored as another user holds a database that is not
// the app's either. Making only the directory writable used to lead straight
// back to SQLite's own error, so the file is named and the fix covers the
// files. File modes stand in for another owner here.
func TestUnwritableDatabaseFileNamesTheFix(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write to any file, so none can be made unwritable")
	}
	for _, tc := range []struct {
		name, file string
		mode       fs.FileMode
	}{
		{"read-only database", "rss.db", 0444},
		{"unreadable database", "rss.db", 0000},
		{"read-only write-ahead log", "rss.db-wal", 0444},
		{"read-only shared memory file", "rss.db-shm", 0444},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "data")
			s, err := openStore(config.Config{DataDir: directory, MaxItems: 500})
			if err != nil {
				t.Fatal(err)
			}
			s.DB.Close()
			file := filepath.Join(directory, tc.file)
			if tc.file != "rss.db" {
				// Startup stops before SQLite reads the file, so any content
				// stands for the journal a copied library brings with it.
				if err := os.WriteFile(file, []byte("journal"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			stored, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(file, tc.mode); err != nil {
				t.Fatal(err)
			}
			before := dataFiles(t, directory)
			info, err := os.Stat(file)
			if err != nil {
				t.Fatal(err)
			}

			s, err = openStore(config.Config{DataDir: directory, MaxItems: 500, UpgradeBackup: true})
			if s != nil {
				s.DB.Close()
			}
			if err == nil {
				t.Fatal("startup accepted a database file it may not write to")
			}
			for _, want := range []string{
				"the database file " + file + " (in DATA_DIR)",
				fmt.Sprintf("uid %d, gid %d", os.Geteuid(), os.Getegid()),
				"permission denied",
				"chown -R 65532:65532 <your RSS_DATA_DIR>",
				"docs/deployment.md",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error does not mention %q: %v", want, err)
				}
			}
			if !errors.Is(err, fs.ErrPermission) {
				t.Errorf("the error lost its cause: %v", err)
			}
			// SQLite would have left its journal files beside the database.
			if after := dataFiles(t, directory); !equalNames(before, after) {
				t.Errorf("the refused start changed the directory: %v became %v", before, after)
			}
			now, err := os.Stat(file)
			if err != nil {
				t.Fatal(err)
			}
			if now.Mode() != info.Mode() || !now.ModTime().Equal(info.ModTime()) {
				t.Errorf("the refused start changed the file: %v %v became %v %v", info.Mode(), info.ModTime(), now.Mode(), now.ModTime())
			}
			if err := os.Chmod(file, 0600); err != nil {
				t.Fatal(err)
			}
			if after, _ := os.ReadFile(file); !bytes.Equal(stored, after) {
				t.Error("the refused start changed the file's contents")
			}
		})
	}

	// SQLite gives an empty journal file it owns its mode back, so the start
	// after the one that meets it succeeds. The check must not turn that into
	// a refusal at every start.
	directory := t.TempDir()
	for _, name := range []string{"rss.db-wal", "rss.db-shm"} {
		if err := os.WriteFile(filepath.Join(directory, name), nil, 0444); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkDataDir(directory); err != nil {
		t.Errorf("empty journal files were refused: %v", err)
	}
}

// A directory that accepts new files but refuses to remove them (append-only,
// or a share without the delete right) ran a library before the check existed.
// It must still start, and however often it does, it holds one check file.
func TestDataDirectoryThatKeepsItsCheckFileIsNotRefused(t *testing.T) {
	var log bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	remove := removeProbe
	t.Cleanup(func() {
		slog.SetDefault(previous)
		removeProbe = remove
	})
	removeProbe = func(path string) error {
		return &fs.PathError{Op: "remove", Path: path, Err: syscall.EPERM}
	}
	leftovers := func(directory string) (found []string) {
		for _, name := range dataFiles(t, directory) {
			if name != "rss.db" && name != "rss.db-wal" && name != "rss.db-shm" {
				found = append(found, name)
			}
		}
		return found
	}

	directory := filepath.Join(t.TempDir(), "data")
	for _, start := range []string{"first start", "second start"} {
		log.Reset()
		s, err := openStore(config.Config{DataDir: directory, MaxItems: 500, UpgradeBackup: true})
		if err != nil {
			t.Fatalf("%s: a directory that takes new files was refused: %v", start, err)
		}
		s.DB.Close()
		if left := leftovers(directory); !equalNames(left, []string{dataDirProbe}) {
			t.Fatalf("%s: the directory holds %v, not the one check file", start, left)
		}
		if !strings.Contains(log.String(), "level=WARN") || !strings.Contains(log.String(), dataDirProbe) {
			t.Errorf("%s: no warning names the file that stays: %q", start, log.String())
		}
	}

	// Once the file can be removed, as after a start that was killed before it
	// removed its own, the next start clears it and has nothing to report.
	removeProbe = remove
	log.Reset()
	s, err := openStore(config.Config{DataDir: directory, MaxItems: 500, UpgradeBackup: true})
	if err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	if left := leftovers(directory); len(left) != 0 {
		t.Errorf("a start that could remove the check file left %v", left)
	}
	if strings.Contains(log.String(), "level=WARN") {
		t.Errorf("a start that removed the check file still warned: %q", log.String())
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
