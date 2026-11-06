package testutil

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
)

// BlockItemWrites makes storing a story fail while reads and other writes keep
// working, which is what a full disk or a revoked permission can look like to
// the scheduler: a small update to a feed row may still succeed while new
// stories are refused. It returns a function that lifts the block. The trigger
// syntax differs per backend, which is chosen the same way PostgresURL does.
func BlockItemWrites(t testing.TB, db *sql.DB) (restore func()) {
	t.Helper()
	ctx := context.Background()
	create := `CREATE TRIGGER block_item_writes BEFORE INSERT ON items BEGIN SELECT RAISE(ABORT, 'disk full'); END`
	drop := "DROP TRIGGER block_item_writes"
	if strings.TrimSpace(os.Getenv("RSS_TEST_POSTGRES_URL")) != "" {
		create = `CREATE OR REPLACE FUNCTION block_item_writes() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'disk full'; END $$;
CREATE TRIGGER block_item_writes BEFORE INSERT ON items FOR EACH ROW EXECUTE FUNCTION block_item_writes()`
		drop = "DROP TRIGGER block_item_writes ON items"
	}
	if _, err := db.ExecContext(ctx, create); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if _, err := db.ExecContext(ctx, drop); err != nil {
			t.Fatal(err)
		}
	}
}
