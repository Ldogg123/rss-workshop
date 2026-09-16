package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
