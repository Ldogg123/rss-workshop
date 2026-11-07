package store

import (
	"bytes"
	"database/sql"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/testutil"
)

// postgresAtSchema returns the URL of an isolated PostgreSQL database at a
// released schema, holding a feed and its stories, and a connection to it for
// the fixture's own statements. It skips without RSS_TEST_POSTGRES_URL.
func postgresAtSchema(t *testing.T, version int) (string, *sql.DB) {
	t.Helper()
	raw := testutil.PostgresURL(t)
	if raw == "" {
		t.Skip("set RSS_TEST_POSTGRES_URL to test PostgreSQL schema upgrades")
	}
	db, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal("cannot configure the PostgreSQL fixture")
	}
	t.Cleanup(func() { db.Close() })
	body, err := os.ReadFile(filepath.Join("testdata", "postgres_schema_v"+strconv.Itoa(version)+".sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), string(body)); err != nil {
		t.Fatal("cannot create the released schema", err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run,last_success)
VALUES('kept','kept-token','Kept feed','https://example.com/news','{}',600,true,1700001500,1700000900)`); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := db.ExecContext(t.Context(), `INSERT INTO items(feed_id,key,guid,title,url,html,image,published,first_seen,last_seen)
VALUES('kept',$1,$2,$3,'https://example.com/story','<p>Saved</p>','',1700000000,$4,1700000900)`, []byte{byte('a' + i)}, "guid-"+strconv.Itoa(i), "Story "+strconv.Itoa(i), 1700000100+i); err != nil {
			t.Fatal(err)
		}
	}
	return raw, db
}

// slowUpgrade makes the last step of an upgrade sleep inside its transaction.
func slowUpgrade(t *testing.T, db *sql.DB, seconds string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `CREATE FUNCTION slow_upgrade() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(`+seconds+`); RETURN NEW; END $$;
CREATE TRIGGER slow_upgrade BEFORE UPDATE ON schema_version FOR EACH ROW EXECUTE FUNCTION slow_upgrade()`); err != nil {
		t.Fatal(err)
	}
}

// assertNotUpgraded checks that a failed upgrade left the released schema 5
// database as it was.
func assertNotUpgraded(t *testing.T, db *sql.DB, before map[string][][]any) {
	t.Helper()
	var version int
	if err := db.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&version); err != nil || version != 5 {
		t.Fatal("a failed upgrade changed the schema version", version, err)
	}
	rows, err := db.QueryContext(t.Context(), "SELECT last_new FROM feeds LIMIT 0")
	if rows != nil {
		rows.Close()
	}
	if err == nil {
		t.Fatal("a failed upgrade left its new column behind")
	}
	if after := releasedRows(t, db, 5); !reflect.DeepEqual(after, before) {
		t.Fatal("a failed upgrade changed stored rows")
	}
}

func setLimits(t *testing.T, start, upgrade time.Duration) {
	t.Helper()
	savedStart, savedUpgrade := postgresStartTimeout, postgresUpgradeTimeout
	t.Cleanup(func() { postgresStartTimeout, postgresUpgradeTimeout = savedStart, savedUpgrade })
	postgresStartTimeout, postgresUpgradeTimeout = start, upgrade
}

// An upgrade has a limit of its own, longer than the start's, and running past
// it rolls back with a fixed reason that names the limit. The same upgrade
// then succeeds within the usual limit.
func TestPostgresUpgradePastItsTimeLimitChangesNothing(t *testing.T) {
	raw, db := postgresAtSchema(t, 5)
	slowUpgrade(t, db, "1")
	before := releasedRows(t, db, 5)
	usual := postgresUpgradeTimeout
	setLimits(t, postgresStartTimeout, 200*time.Millisecond)
	s, err := OpenPostgres(t.Context(), raw, 10)
	want := "cannot migrate PostgreSQL database from schema version 5 to 6: the upgrade did not finish within 200ms; the database is unchanged"
	if s != nil || err == nil || err.Error() != want {
		t.Fatalf("an upgrade past its limit returned %v, want %q", err, want)
	}
	assertNotUpgraded(t, db, before)

	// The upgrade outlasts the start's own limit, which it must not use.
	setLimits(t, 500*time.Millisecond, usual)
	s, err = OpenPostgres(t.Context(), raw, 10)
	if err != nil {
		t.Fatal("the upgrade failed within its usual limit", err)
	}
	defer s.DB.Close()
	var version int
	if err := s.DB.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&version); err != nil || version != currentSchema {
		t.Fatal("the upgrade did not reach the current schema", version, err)
	}
	if after := releasedRows(t, s.DB, 5); !reflect.DeepEqual(after, before) {
		t.Fatal("the upgrade changed stored rows")
	}
}

// A start with nothing to upgrade keeps the short limit: waiting for the schema
// lock, as behind another process's upgrade, ends at the start's limit rather
// than at the upgrade's, or at the 5-second lock_timeout. The upgrade's limit
// plays no part in it.
func TestPostgresStartWithNothingToUpgradeKeepsTheStartLimit(t *testing.T) {
	raw := testutil.PostgresURL(t)
	if raw == "" {
		t.Skip("set RSS_TEST_POSTGRES_URL to test PostgreSQL startup limits")
	}
	s, err := OpenPostgres(t.Context(), raw, 10)
	if err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	db, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal("cannot configure the PostgreSQL fixture")
	}
	defer db.Close()
	holder, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(t.Context(), "SELECT pg_advisory_lock("+postgresSchemaLock+"::bigint)"); err != nil {
		t.Fatal(err)
	}
	setLimits(t, 300*time.Millisecond, time.Hour)
	started := time.Now()
	s, err = OpenPostgres(t.Context(), raw, 10)
	waited := time.Since(started)
	// Release the lock at once for the next start and for any other test
	// package sharing this database.
	if _, err := holder.ExecContext(t.Context(), "SELECT pg_advisory_unlock("+postgresSchemaLock+"::bigint)"); err != nil {
		t.Fatal(err)
	}
	if s != nil || err == nil || !strings.HasPrefix(err.Error(), "cannot lock PostgreSQL schema initialization") {
		t.Fatal("a start behind a held schema lock did not stop at the lock", err)
	}
	if waited > 3*time.Second {
		t.Fatalf("a start with nothing to upgrade waited %v behind the schema lock, past its own limit", waited)
	}

	setLimits(t, 10*time.Second, time.Nanosecond)
	s, err = OpenPostgres(t.Context(), raw, 10)
	if err != nil {
		t.Fatal("a start with nothing to upgrade used the upgrade's limit", err)
	}
	s.DB.Close()
}

// The upgrade raises statement_timeout to its own limit for its transaction
// only, and keeps lock_timeout, so it cannot queue behind other locks; the
// connection returns to the pool with its usual settings. It logs when it
// starts and finishes, without the connection URL.
func TestPostgresUpgradeRaisesStatementTimeoutOnlyForItself(t *testing.T) {
	raw, db := postgresAtSchema(t, 5)
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE upgrade_probe(pid INTEGER, statement_timeout TEXT, lock_timeout TEXT);
CREATE FUNCTION probe_upgrade() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 INSERT INTO upgrade_probe SELECT pg_backend_pid(),
  (SELECT setting FROM pg_settings WHERE name='statement_timeout'), (SELECT setting FROM pg_settings WHERE name='lock_timeout');
 RETURN NEW;
END $$;
CREATE TRIGGER probe_upgrade BEFORE UPDATE ON schema_version FOR EACH ROW EXECUTE FUNCTION probe_upgrade()`); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	restore := slog.Default()
	defer slog.SetDefault(restore)
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo})))
	s, err := OpenPostgres(t.Context(), raw, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	var pid int
	var during, lock string
	if err := s.DB.QueryRowContext(t.Context(), "SELECT pid,statement_timeout,lock_timeout FROM upgrade_probe").Scan(&pid, &during, &lock); err != nil {
		t.Fatal("the upgrade did not run its last step", err)
	}
	if want := strconv.FormatInt(postgresUpgradeTimeout.Milliseconds(), 10); during != want || lock != "5000" {
		t.Fatalf("inside the upgrade statement_timeout=%sms lock_timeout=%sms, want %sms and 5000ms", during, lock, want)
	}
	// The pool holds only the connection that upgraded.
	conn, err := s.DB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var after int
	var setting string
	if err := conn.QueryRowContext(t.Context(), "SELECT pg_backend_pid(),setting FROM pg_settings WHERE name='statement_timeout'").Scan(&after, &setting); err != nil {
		t.Fatal(err)
	}
	if after != pid || setting != "30000" {
		t.Fatalf("after the upgrade connection %d has statement_timeout=%sms, want connection %d with 30000ms", after, setting, pid)
	}

	text := log.String()
	for _, want := range []string{
		`msg="PostgreSQL schema upgrade started" from_schema=5 to_schema=6 time_limit=` + postgresUpgradeTimeout.String(),
		`msg="PostgreSQL schema upgrade finished" from_schema=5 to_schema=6 duration=`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the upgrade log lacks %q:\n%s", want, text)
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if password, _ := u.User.Password(); strings.Contains(text, raw) || password != "" && strings.Contains(text, password) {
		t.Fatal("the upgrade log carries the connection URL")
	}
}
