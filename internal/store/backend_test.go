package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"rss-workshop/internal/model"
	"rss-workshop/internal/testutil"
)

// The normal suite uses SQLite; the PostgreSQL matrix runs these exact same
// behavior tests against isolated schemas, including close/reopen scenarios.
func testStoreOpener(t *testing.T, maxItems int) func() (*Store, error) {
	t.Helper()
	if raw := testutil.PostgresURL(t); raw != "" {
		return func() (*Store, error) { return OpenPostgres(t.Context(), raw, maxItems) }
	}
	path := filepath.Join(t.TempDir(), "rss.db")
	return func() (*Store, error) { return Open(path, maxItems) }
}

func TestRetentionOrderingTokensAndLongKeys(t *testing.T) {
	ctx := t.Context()
	s, err := testStoreOpener(t, 3)()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	f := model.Feed{Title: "History", URL: "https://example.com", Interval: 60, Enabled: true}
	f.ID, err = s.Save(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	f, err = s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldToken := f.RSSToken
	if got, err := s.GetByToken(ctx, oldToken); err != nil || got.ID != f.ID {
		t.Fatalf("token lookup: %v", err)
	}
	if err := s.RotateToken(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetByToken(ctx, oldToken); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("rotated token remained usable", err)
	}
	f, err = s.Get(ctx, f.ID)
	if err != nil || f.RSSToken == oldToken {
		t.Fatal("token rotation failed", err)
	}
	if err := s.RotateToken(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing token rotation", err)
	}
	// These timestamps also exercise BIGINT beyond the signed 32-bit epoch.
	published := time.Date(2100, 1, 2, 0, 0, 0, 0, time.UTC)
	longKey := "https://example.com/" + strings.Repeat("long-url-", 1000)
	items := []model.Item{
		{Key: "ä", Title: "Unicode", Published: published},
		{Key: "z", Title: "ASCII", Published: published},
		{Key: "A", Title: "Uppercase", Published: published},
		{Key: longKey, URL: longKey, Title: "Long URL", Published: published.Add(time.Hour)},
	}
	if err := s.Complete(ctx, f, items, "etag", "modified", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	before, err := s.Items(ctx, f.ID)
	if err != nil || len(before) != 3 || before[0].Key != longKey || before[1].Key != "A" || before[2].Key != "z" {
		t.Fatalf("retention/order/long URL failed: count=%d, err=%v", len(before), err)
	}
	items[3].Title = "Updated long URL"
	items[3].Published = published.Add(2 * time.Hour)
	if err := s.Complete(ctx, f, items[3:], "", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Items(ctx, f.ID)
	if err != nil || len(updated) != 3 || updated[0].Title != items[3].Title || updated[0].GUID != before[0].GUID || !updated[0].Published.Equal(before[0].Published) || !updated[0].FirstSeen.Equal(before[0].FirstSeen) {
		t.Fatal("long key merge changed identity/date or removed absent items", err)
	}
	s.MaxItems = 1
	if err := s.Complete(ctx, f, nil, "", "", 500, errors.New("x\x00"+strings.Repeat("日", 400)), 0); err != nil {
		t.Fatal("recording a bounded Unicode error failed", err)
	}
	failedFeed, err := s.Get(ctx, f.ID)
	if err != nil || failedFeed.Failures != 1 || failedFeed.Error == "" || len(failedFeed.Error) > 1000 || !utf8.ValidString(failedFeed.Error) || strings.ContainsRune(failedFeed.Error, 0) {
		t.Fatal("failure message was not safely bounded and recorded", err)
	}
	afterFailure, err := s.Items(ctx, f.ID)
	if err != nil || !reflect.DeepEqual(afterFailure, updated) {
		t.Fatal("failure pruned history", err)
	}
	// A valid not-modified success still enforces a changed retention limit.
	if err := s.Complete(ctx, f, nil, "", "", 304, nil, 0); err != nil {
		t.Fatal(err)
	}
	retained, err := s.Items(ctx, f.ID)
	if err != nil || len(retained) != 1 || retained[0].Key != longKey {
		t.Fatal("not-modified success did not prune", err)
	}
	for range 51 {
		if err := s.Complete(ctx, f, nil, "", "", 304, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.DB.QueryRowContext(ctx, s.bind("SELECT count(*) FROM runs WHERE feed_id=?"), f.ID).Scan(&count); err != nil || count != 50 {
		t.Fatalf("run retention: count=%d err=%v", count, err)
	}
	if err := s.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"items", "runs"} {
		if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("delete did not cascade %s: count=%d err=%v", table, count, err)
		}
	}
}

func TestPostgresConfigurationErrorsAreSafe(t *testing.T) {
	secret := "secret-that-must-not-leak"
	for _, raw := range []string{"postgres://admin:" + secret + "@127.0.0.1:bad/rss", "postgres://admin:" + secret + "@127.0.0.1/rss?sslmode=invalid", "password=" + secret, ""} {
		s, err := OpenPostgres(t.Context(), raw, 10)
		if s != nil || err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), raw) && raw != "" {
			t.Fatal("invalid configuration was accepted or exposed")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	started := time.Now()
	s, err := OpenPostgres(ctx, "postgres://admin:"+secret+"@127.0.0.1:1/rss?sslmode=disable", 10)
	if s != nil || err == nil || strings.Contains(err.Error(), secret) || time.Since(started) > time.Second {
		t.Fatal("canceled startup did not fail promptly and safely")
	}
}

func TestPostgresConcurrentRecipeInvalidation(t *testing.T) {
	for _, mutation := range []string{"edit", "delete", "cancel"} {
		t.Run(mutation, func(t *testing.T) {
			raw := testutil.PostgresURL(t)
			if raw == "" {
				t.Skip("set RSS_TEST_POSTGRES_URL to test PostgreSQL row locking")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			s, err := OpenPostgres(ctx, raw, 10)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			id, err := s.Save(ctx, model.Feed{Title: "Before", URL: "https://example.com", Interval: 60})
			if err != nil {
				t.Fatal(err)
			}
			f, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := s.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var version, blockerPID int
			if err := tx.QueryRowContext(ctx, "SELECT version FROM feeds WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			refreshCtx, stopRefresh := context.WithCancel(ctx)
			defer stopRefresh()
			go func() {
				done <- s.Complete(refreshCtx, f, []model.Item{{Key: "stale", Title: "Stale"}}, "stale-etag", "", 200, nil, 0)
			}()
			// Wait for the actual version read to block on our row lock. This
			// distinguishes FOR UPDATE from an unlocked, stale MVCC snapshot.
			deadline := time.Now().Add(3 * time.Second)
			for {
				var blocked bool
				if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE 'SELECT version FROM feeds%')`, blockerPID).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("refresh did not lock the recipe version before merging")
				}
				time.Sleep(10 * time.Millisecond)
			}
			query := "UPDATE feeds SET title='Edited',version=version+1 WHERE id=$1"
			want := ErrStale
			if mutation == "delete" {
				query = "DELETE FROM feeds WHERE id=$1"
				want = sql.ErrNoRows
			}
			if mutation == "cancel" {
				stopRefresh()
				want = context.Canceled
			} else {
				if _, err := tx.ExecContext(ctx, query, id); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("refresh after concurrent %s: %v", mutation, err)
				}
			case <-ctx.Done():
				t.Fatal("refresh did not finish after recipe lock was released")
			}
			for _, table := range []string{"items", "runs"} {
				var count int
				if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("stale refresh left %s: count=%d err=%v", table, count, err)
				}
			}
		})
	}
}

func TestPostgresConcurrentInitializationAndVersionGate(t *testing.T) {
	raw := testutil.PostgresURL(t)
	if raw == "" {
		t.Skip("set RSS_TEST_POSTGRES_URL to test PostgreSQL schema initialization")
	}
	ready := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-ready
			s, err := OpenPostgres(t.Context(), raw, 10)
			if err == nil {
				err = s.DB.Close()
			}
			results <- err
		}()
	}
	close(ready)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	s, err := OpenPostgres(t.Context(), raw, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if _, err := s.DB.Exec("UPDATE schema_version SET version=99"); err != nil {
		t.Fatal(err)
	}
	if other, err := OpenPostgres(t.Context(), raw, 10); err == nil || other != nil {
		t.Fatal("unsupported schema version was accepted")
	}
	var version int
	if err := s.DB.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != 99 {
		t.Fatal("unsupported schema version was modified", err)
	}
}

func TestPostgresInitializationDoesNotAdoptExistingTables(t *testing.T) {
	raw := testutil.PostgresURL(t)
	if raw == "" {
		t.Skip("set RSS_TEST_POSTGRES_URL to test PostgreSQL initialization rollback")
	}
	db, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal("cannot configure isolated PostgreSQL fixture")
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE feeds(marker TEXT); INSERT INTO feeds VALUES('preserve me')"); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenPostgres(t.Context(), raw, 10); s != nil || err == nil {
		t.Fatal("unrelated existing tables were adopted")
	}
	var marker string
	if err := db.QueryRowContext(t.Context(), "SELECT marker FROM feeds").Scan(&marker); err != nil || marker != "preserve me" {
		t.Fatal("failed initialization modified existing data", err)
	}
	var exists bool
	if err := db.QueryRowContext(t.Context(), "SELECT to_regclass('schema_version') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatal("failed initialization committed a version table", err)
	}
}
