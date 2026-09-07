package main

import (
	"context"
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
