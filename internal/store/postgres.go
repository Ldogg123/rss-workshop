package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed postgres_schema.sql
var postgresSchema string

var errPostgresEncoding = errors.New("PostgreSQL requires UTF8 server and client encoding; use a UTF8 database")

// OpenPostgres opens a dedicated application database (or explicit search_path
// schema). It initializes new storage transactionally and never converts an
// existing SQLite database. Only one application process per database is
// supported; row locking protects edits within that process, not scheduling
// across multiple application instances.
func OpenPostgres(ctx context.Context, databaseURL string, maxItems int) (*Store, error) {
	u, err := url.Parse(databaseURL)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" || strings.Trim(u.Path, "/") == "" || u.Fragment != "" {
		return nil, errors.New("invalid PostgreSQL connection URL")
	}
	// pgx ends the credentials at the first @, as PostgreSQL's client library
	// does, while net/url ends them at the last. An unencoded @ in a user name or
	// password therefore names a host that cannot exist, and startup would blame
	// the server instead of the URL.
	_, rest, _ := strings.Cut(databaseURL, "://")
	if end := strings.IndexAny(rest, "/?#"); end >= 0 && strings.Count(rest[:end], "@") > 1 {
		return nil, errors.New("invalid PostgreSQL connection URL: percent-encode @ in the user name or password as %40")
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		// ParseConfig errors can contain the complete URL, including passwords.
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}
	if config.ConnectTimeout == 0 || config.ConnectTimeout > 5*time.Second {
		config.ConnectTimeout = 5 * time.Second
	}
	config.RuntimeParams["application_name"] = "rss-workshop"
	// pgx sends Go strings as UTF-8. A connection or role setting must not
	// reinterpret those bytes, and legacy server encodings cannot store all
	// extracted text. Check every new pooled connection before sending SQL.
	config.RuntimeParams["client_encoding"] = "UTF8"
	validateConnect := config.ValidateConnect
	config.ValidateConnect = func(ctx context.Context, conn *pgconn.PgConn) error {
		if conn.ParameterStatus("server_encoding") != "UTF8" || conn.ParameterStatus("client_encoding") != "UTF8" {
			return errPostgresEncoding
		}
		if validateConnect != nil {
			return validateConnect(ctx, conn)
		}
		return nil
	}
	config.RuntimeParams["statement_timeout"] = "30000"
	config.RuntimeParams["lock_timeout"] = "5000"
	config.RuntimeParams["idle_in_transaction_session_timeout"] = "30000"
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	start, cancel := context.WithTimeout(ctx, postgresStartTimeout)
	defer cancel()
	if err := db.PingContext(start); err != nil {
		db.Close()
		if errors.Is(err, errPostgresEncoding) {
			return nil, errPostgresEncoding
		}
		return nil, errors.New("cannot connect to PostgreSQL; check connection settings and server availability")
	}
	version, err := initializePostgres(start, db)
	if err == nil && version < currentSchema {
		err = upgradePostgres(ctx, db)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db, MaxItems: maxItems, postgres: true}, nil
}

// postgresStartTimeout bounds connecting and every start with nothing to
// upgrade, so a stuck server stops startup within seconds. An upgrade can read
// whole tables, as schema 6 reads items, which on slow storage with a cold
// cache can take minutes, so it gets postgresUpgradeTimeout, with its
// statement_timeout raised to match. The readiness check fails meanwhile, but
// a container's restart policy acts only when the process exits, so this
// limit is what ends an upgrade on a server that stopped responding; a stop
// signal cancels it sooner. They are variables only so tests can shorten them.
var (
	postgresStartTimeout   = 10 * time.Second
	postgresUpgradeTimeout = 10 * time.Minute
)

// initializePostgres creates the tables of an empty database and returns the
// schema version it found. A version below currentSchema needs upgradePostgres.
func initializePostgres(ctx context.Context, db *sql.DB) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, errors.New("cannot begin PostgreSQL schema initialization")
	}
	defer tx.Rollback()
	count, version, err := lockPostgresSchema(ctx, tx)
	if err != nil || count == 1 && version < currentSchema {
		// An older schema is upgraded in a transaction of its own, under its own
		// limit; this one ends without a change.
		return version, err
	}
	if count == 1 {
		if err := tx.Commit(); err != nil {
			return 0, errors.New("cannot commit PostgreSQL schema initialization")
		}
		return version, nil
	}
	// No IF NOT EXISTS here: unrelated or partially initialized tables must
	// cause rollback, rather than silently being adopted or overwritten.
	if _, err := tx.ExecContext(ctx, postgresSchema); err != nil {
		return 0, errors.New("cannot initialize PostgreSQL tables; use an empty dedicated database with schema creation permission")
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version(version) VALUES($1)", currentSchema); err != nil {
		return 0, errors.New("cannot record PostgreSQL schema version")
	}
	if err := tx.Commit(); err != nil {
		return 0, errors.New("cannot commit PostgreSQL schema initialization")
	}
	return currentSchema, nil
}

// postgresSchemaLock is the advisory lock key that serializes startups.
const postgresSchemaLock = "32284341236986475"

// lockPostgresSchema takes the schema lock for tx and reads the version: count
// is 0 for an empty database and 1 for a supported schema.
func lockPostgresSchema(ctx context.Context, tx *sql.Tx) (count, version int, err error) {
	// Serialize startup DDL even before the version table exists. This lock is
	// database-local and transaction-scoped, so a failed startup releases it.
	// lock_timeout still ends the wait after 5 seconds, so a second process
	// stops rather than queue behind an upgrade that can take minutes.
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock("+postgresSchemaLock+"::bigint)"); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			return 0, 0, errors.New("cannot lock PostgreSQL schema initialization: another connection is initializing or upgrading this database; run one application process per database")
		}
		return 0, 0, errors.New("cannot lock PostgreSQL schema initialization")
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version(singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK(singleton), version INTEGER NOT NULL)`); err != nil {
		return 0, 0, errors.New("cannot read PostgreSQL schema version; check database ownership and permissions")
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(min(version),0) FROM schema_version").Scan(&count, &version); err != nil {
		return 0, 0, errors.New("cannot read PostgreSQL schema version")
	}
	if count > 1 || (count == 1 && (version < 1 || version > currentSchema)) {
		return 0, 0, errors.New("unsupported PostgreSQL database schema version")
	}
	return count, version, nil
}

// upgradePostgres migrates a supported older schema to the current one in a
// single transaction, which ctx can cancel and postgresUpgradeTimeout ends.
func upgradePostgres(ctx context.Context, db *sql.DB) error {
	limit := postgresUpgradeTimeout
	upgrade, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	tx, err := db.BeginTx(upgrade, nil)
	if err != nil {
		return errors.New("cannot begin PostgreSQL schema upgrade")
	}
	defer tx.Rollback()
	// Read again under the lock: another process may have upgraded since.
	count, version, err := lockPostgresSchema(upgrade, tx)
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("unsupported PostgreSQL database schema version")
	}
	if version == currentSchema {
		return nil
	}
	// The connection's 30-second statement_timeout would end a long step first.
	// set_config with true is SET LOCAL: the limit ends with this transaction.
	var pid int
	if err := tx.QueryRowContext(upgrade, "SELECT pg_backend_pid(),set_config('statement_timeout',$1,true)", strconv.FormatInt(max(limit.Milliseconds(), 1), 10)).Scan(&pid, new(string)); err != nil {
		return errors.New("cannot set the PostgreSQL schema upgrade time limit")
	}
	// When the limit passes or startup is interrupted, the driver drops the
	// connection and asks the server to cancel the statement, but from a
	// goroutine that a process exiting on this error does not wait for.
	// Without that request PostgreSQL goes on running the statement, holding
	// this upgrade's locks, until it ends or reaches statement_timeout, so
	// send one here and wait for it: the upgrade rolls back before the exit.
	canceled := make(chan struct{})
	stop := context.AfterFunc(upgrade, func() {
		defer close(canceled)
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = db.ExecContext(cancelCtx, "SELECT pg_cancel_backend($1)", pid)
	})
	defer func() {
		if !stop() {
			<-canceled
		}
	}()
	// The app cannot run pg_dump, so unlike SQLite nothing is copied first.
	slog.Warn("upgrading PostgreSQL database schema without an automatic backup", "from_schema", version, "to_schema", currentSchema,
		"note", "an older release cannot open the upgraded database; keep a pg_dump from before this upgrade to roll back")
	slog.Info("PostgreSQL schema upgrade started", "from_schema", version, "to_schema", currentSchema, "time_limit", limit.String())
	started := time.Now()
	if err := migratePostgres(upgrade, tx, version); err != nil {
		reason := ""
		switch {
		case ctx.Err() != nil:
			reason = "startup was interrupted"
		case upgrade.Err() != nil || time.Since(started) >= limit:
			reason = "the upgrade did not finish within " + describeLimit(limit)
		default:
			return err
		}
		return fmt.Errorf("cannot migrate PostgreSQL database from schema version %d to %d: %s; the database is unchanged", version, currentSchema, reason)
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit PostgreSQL schema upgrade")
	}
	slog.Info("PostgreSQL schema upgrade finished", "from_schema", version, "to_schema", currentSchema, "duration", time.Since(started).Round(time.Millisecond).String())
	return nil
}

// describeLimit names a time limit in an error an operator reads.
func describeLimit(d time.Duration) string {
	switch {
	case d == time.Minute:
		return "1 minute"
	case d > time.Minute && d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + " minutes"
	}
	return d.String()
}

// migratePostgres takes every step from version to currentSchema in tx.
func migratePostgres(ctx context.Context, tx *sql.Tx, version int) error {
	if version == 1 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE runs ADD COLUMN diagnostics TEXT NOT NULL DEFAULT ''; UPDATE schema_version SET version=2`); err != nil {
			return errors.New("cannot migrate PostgreSQL database from schema version 1 to 2")
		}
		version = 2
	}
	if version == 2 {
		// Recipe JSON gains filters; older binaries must reject this database
		// rather than ignore the rules while refreshing its feeds.
		if _, err := tx.ExecContext(ctx, "UPDATE schema_version SET version=3"); err != nil {
			return errors.New("cannot migrate PostgreSQL database from schema version 2 to 3")
		}
		version = 3
	}
	if version == 3 {
		// Article bodies are stored beside the list-page teaser rather than
		// replacing it, so re-extracting the list page each refresh keeps
		// updating late-published images without discarding fetched content.
		if _, err := tx.ExecContext(ctx, `ALTER TABLE items ADD COLUMN content_full TEXT NOT NULL DEFAULT ''; UPDATE schema_version SET version=4`); err != nil {
			return errors.New("cannot migrate PostgreSQL database from schema version 3 to 4")
		}
		version = 4
	}
	if version == 4 {
		// Library filters are shared by reference. The version gate also stops
		// an older application from refreshing linked feeds without their rules.
		if _, err := tx.ExecContext(ctx, schemaFivePostgres+"UPDATE schema_version SET version=5"); err != nil {
			return errors.New("cannot migrate PostgreSQL database from schema version 4 to 5")
		}
		version = 5
	}
	if version == 5 {
		// See schemaSixSQLite: last_new starts at the newest stored story, and
		// the version gate keeps older applications from ignoring 1.3 recipes.
		// It reads all of items, the one step whose time grows with the library.
		if _, err := tx.ExecContext(ctx, schemaSixPostgres+"UPDATE schema_version SET version=6"); err != nil {
			return errors.New("cannot migrate PostgreSQL database from schema version 5 to 6")
		}
	}
	return nil
}

// bind translates only this package's fixed SQL, where every question mark is
// a parameter. User-provided strings must always remain separate arguments.
func (s *Store) bind(query string) string {
	if !s.postgres {
		return query
	}
	n := 0
	var out strings.Builder
	for _, r := range query {
		if r == '?' {
			n++
			out.WriteString("$" + strconv.Itoa(n))
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// Driver/server error details can contain connection settings or row values.
// Keep useful error identities and SQLSTATE, without exposing the raw error.
func (s *Store) cleanError(err *error) {
	if !s.postgres || *err == nil {
		return
	}
	var bad *InvalidError
	if errors.As(*err, &bad) {
		*err = bad
		return
	}
	for _, sentinel := range []error{sql.ErrNoRows, ErrStale, ErrDeleted, ErrFilterInUse, errPostgresEncoding, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(*err, sentinel) {
			*err = sentinel
			return
		}
	}
	var pgErr *pgconn.PgError
	if errors.As(*err, &pgErr) && len(pgErr.Code) == 5 && strings.IndexFunc(pgErr.Code, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z') }) == -1 {
		*err = fmt.Errorf("PostgreSQL operation failed (SQLSTATE %s)", pgErr.Code)
		return
	}
	*err = errors.New("PostgreSQL operation failed")
}
