// Package testutil contains helpers for isolated integration-test fixtures.
// It is not imported by the application executable.
package testutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresURL returns an isolated schema URL for this test, or an empty string
// when PostgreSQL testing was not requested. It never reads DATABASE_URL.
// The caller must close its store before cleanup drops this test's schema.
func PostgresURL(t testing.TB) string {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("RSS_TEST_POSTGRES_URL"))
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		t.Fatal("RSS_TEST_POSTGRES_URL must be a PostgreSQL URL for a disposable test database")
	}
	db, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal("could not configure the PostgreSQL test database")
	}
	db.SetMaxOpenConns(1)
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		db.Close()
		t.Fatal(err)
	}
	schema := "rss_test_" + hex.EncodeToString(random[:])
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		db.Close()
		t.Fatal("could not create an isolated PostgreSQL test schema; check connectivity and CREATE permission")
	}
	t.Cleanup(func() {
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error("could not remove isolated PostgreSQL test schema: " + schema)
		}
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	return u.String()
}
