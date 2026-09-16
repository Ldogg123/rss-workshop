package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrUpgradeBackup reports that an SQLite database was left unmigrated because
// its pre-upgrade copy could not be saved and verified.
var ErrUpgradeBackup = errors.New("could not save a verified copy of the database before upgrading it")

// OpenOptions adjusts how Open treats an existing SQLite database.
type OpenOptions struct {
	// SkipUpgradeBackup migrates an older schema without first saving a copy.
	SkipUpgradeBackup bool
}

// Tests replace these to name copies at chosen times and simulate a full disk.
var (
	copyClock = time.Now
	copySpace = freeSpace
)

// backupFormat matches scripts/backup.py, so its verify and restore commands
// accept a pre-upgrade copy exactly like a backup taken with that tool.
const backupFormat = "rss-workshop.sqlite-backup"

// backupTables lists the tables each schema's manifest counts, matching
// SCHEMA_TABLES in scripts/backup.py.
func backupTables(version int) []string {
	tables := []string{"feeds", "items", "runs"}
	if version >= 5 {
		tables = append(tables, "filters", "feed_filters")
	}
	return tables
}

// removeOrphanedRows finishes feed deletions that ran without foreign key
// enforcement, deleting the rows they should have cascaded to. Nothing else can
// reference a deleted feed, so this only removes data the operator already
// deleted. Other foreign key violations are left for the checks to report.
func removeOrphanedRows(db *sql.DB, version int) error {
	tables := []string{"items", "runs"}
	if version >= 5 {
		tables = append(tables, "feed_filters")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	removed := make([]any, 0, 2*len(tables))
	for _, table := range tables {
		result, err := tx.Exec("DELETE FROM " + table + " WHERE feed_id NOT IN (SELECT id FROM feeds)")
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n > 0 {
			removed = append(removed, table, n)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if len(removed) > 0 {
		slog.Warn("removed rows left behind by deleted feeds", removed...)
	}
	return nil
}

// storedSchema reads the version an existing SQLite database declares, or 0
// for a new database. It only reads: the version checks that reject a
// malformed or future schema stay in initializeSQLite.
func storedSchema(db *sql.DB) (int, error) {
	var exists bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='schema_version')").Scan(&exists); err != nil || !exists {
		return 0, err
	}
	var count, version int
	if err := db.QueryRow("SELECT count(*),COALESCE(min(version),0) FROM schema_version").Scan(&count, &version); err != nil {
		return 0, err
	}
	if count != 1 {
		return 0, nil
	}
	return version, nil
}

// backupBeforeUpgrade saves a verified copy of the database at path, still at
// schema version, to backups/pre-upgrade-schema-<version>-<time> beside it.
// The copy is built in a hidden partial directory and renamed into place only
// after it verifies, so a directory with the final name is always complete.
// The source database is only read. It returns the copy's directory, and
// whether that is an earlier identical copy reused instead of a new one.
func backupBeforeUpgrade(db *sql.DB, path string, version int) (_ string, reused bool, err error) {
	parent := filepath.Join(filepath.Dir(path), "backups")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", false, err
	}
	// Only one process may use a database, so a partial directory found now
	// was left by a start that was killed mid-copy. Nothing else removes it.
	if err := removeAbandonedCopies(parent); err != nil {
		return "", false, err
	}
	if err := checkCopySpace(db, parent); err != nil {
		return "", false, err
	}
	name := fmt.Sprintf("pre-upgrade-schema-%d-%s", version, copyClock().UTC().Format("20060102T150405Z"))
	final := filepath.Join(parent, name)
	partial, err := os.MkdirTemp(parent, partialPrefix+name+"-")
	if err != nil {
		return "", false, err
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(partial)
		}
	}()
	copied := filepath.Join(partial, "rss.db")
	// VACUUM INTO reads one consistent snapshot, including committed pages
	// still in the WAL, and writes a standalone rollback-journal database.
	if _, err := db.Exec("VACUUM INTO ?", copied); err != nil {
		return "", false, err
	}
	if err := os.Chmod(copied, 0600); err != nil {
		return "", false, err
	}
	want, err := countTables(db, version)
	if err != nil {
		return "", false, err
	}
	counts, err := verifyCopy(copied, version)
	if err != nil {
		return "", false, err
	}
	for table, n := range want {
		if counts[table] != n {
			return "", false, fmt.Errorf("copy has %d %s rows, the database has %d", counts[table], table, n)
		}
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(copied + suffix); err == nil {
			return "", false, fmt.Errorf("copy left an SQLite %s file", suffix)
		}
	}
	sum, err := fileSHA256(copied)
	if err != nil {
		return "", false, err
	}
	// VACUUM INTO writes identical bytes for an unchanged database. When a
	// migration fails after its copy, a restart policy starts the app again and
	// again; reusing the earlier copy keeps that loop from filling the disk.
	if existing := identicalCopy(parent, version, sum); existing != "" {
		return existing, true, nil
	}
	if _, err := os.Lstat(final); err == nil {
		return "", false, fmt.Errorf("%s already exists with different contents", final)
	}
	manifest, err := json.MarshalIndent(map[string]any{
		"format": backupFormat, "version": 1, "schema_version": version,
		"created_at": copyClock().UTC().Format(time.RFC3339Nano), "sha256": sum, "counts": counts,
		"reason": fmt.Sprintf("automatic copy before upgrading to schema %d", currentSchema),
	}, "", "  ")
	if err != nil {
		return "", false, err
	}
	if err := writeSynced(filepath.Join(partial, "manifest.json"), append(manifest, '\n')); err != nil {
		return "", false, err
	}
	if err := syncPath(copied); err != nil {
		return "", false, err
	}
	if err := syncDirectory(partial); err != nil {
		return "", false, err
	}
	if err := os.Rename(partial, final); err != nil {
		return "", false, err
	}
	keep = true
	return final, false, syncDirectory(parent)
}

const partialPrefix = ".pre-upgrade-partial-"

func removeAbandonedCopies(parent string) error {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), partialPrefix) {
			if err := os.RemoveAll(filepath.Join(parent, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// identicalCopy returns an earlier complete copy of this schema whose manifest
// records the same checksum, or "" if there is none.
func identicalCopy(parent string, version int, sum string) string {
	matches, err := filepath.Glob(filepath.Join(parent, fmt.Sprintf("pre-upgrade-schema-%d-*", version)))
	if err != nil {
		return ""
	}
	for i := len(matches) - 1; i >= 0; i-- {
		raw, err := os.ReadFile(filepath.Join(matches[i], "manifest.json"))
		if err != nil {
			continue
		}
		var manifest struct {
			SchemaVersion int    `json:"schema_version"`
			SHA256        string `json:"sha256"`
		}
		if json.Unmarshal(raw, &manifest) != nil || manifest.SchemaVersion != version || manifest.SHA256 != sum {
			continue
		}
		if info, err := os.Lstat(filepath.Join(matches[i], "rss.db")); err == nil && info.Mode().IsRegular() {
			return matches[i]
		}
	}
	return ""
}

// checkCopySpace refuses to start a copy that cannot fit, rather than filling
// the filesystem on every attempt of a restart loop. The page count bounds
// the size VACUUM INTO writes; unknown free space does not block the copy.
func checkCopySpace(db *sql.DB, parent string) error {
	var pages, size int64
	if err := db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		return err
	}
	if err := db.QueryRow("PRAGMA page_size").Scan(&size); err != nil {
		return err
	}
	available, ok := copySpace(parent)
	if need := uint64(pages*size) + 1<<20; ok && available < need {
		return fmt.Errorf("the copy needs about %d MiB free in %s, but only %d MiB is available", need>>20, parent, available>>20)
	}
	return nil
}

func countTables(db *sql.DB, version int) (map[string]int, error) {
	counts := map[string]int{}
	for _, table := range backupTables(version) {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
			return nil, err
		}
		counts[table] = n
	}
	return counts, nil
}

// verifyCopy applies the checks scripts/backup.py makes before it accepts a
// backup, through a read-only connection that cannot change the copy.
func verifyCopy(path string, version int) (map[string]int, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro&immutable=1"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return nil, err
	}
	if integrity != "ok" {
		return nil, fmt.Errorf("copy failed its integrity check: %s", integrity)
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return nil, err
	}
	violation := rows.Next()
	rows.Close()
	if violation {
		return nil, errors.New("copy failed its foreign key check")
	}
	if stored, err := storedSchema(db); err != nil || stored != version {
		return nil, fmt.Errorf("copy declares schema %d, expected %d (%v)", stored, version, err)
	}
	return countTables(db, version)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// syncPath flushes a file, so a completed copy survives a crash or power loss
// immediately after the upgrade that follows it.
func syncPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// syncDirectory flushes a directory entry. Some filesystems cannot sync
// directories and report EINVAL or ENOTSUP; like SQLite, treat that as done.
func syncDirectory(path string) error {
	err := syncPath(path)
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
		return nil
	}
	return err
}

// upgradeBackupError keeps the reason while naming the recovery options; the
// message may include local file paths but never database contents.
func upgradeBackupError(version int, err error) error {
	reason := strings.TrimSpace(err.Error())
	return fmt.Errorf("%w from schema %d; the database was not upgraded: %s", ErrUpgradeBackup, version, reason)
}
