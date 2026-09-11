package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
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

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		if errors.Is(err, errPostgresEncoding) {
			return nil, errPostgresEncoding
		}
		return nil, errors.New("cannot connect to PostgreSQL; check connection settings and server availability")
	}
	if err := initializePostgres(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db, MaxItems: maxItems, postgres: true}, nil
}

func initializePostgres(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin PostgreSQL schema initialization")
	}
	defer tx.Rollback()
	// Serialize startup DDL even before the version table exists. This lock is
	// database-local and transaction-scoped, so a failed startup releases it.
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(32284341236986475::bigint)"); err != nil {
		return errors.New("cannot lock PostgreSQL schema initialization")
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version(singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK(singleton), version INTEGER NOT NULL)`); err != nil {
		return errors.New("cannot read PostgreSQL schema version; check database ownership and permissions")
	}
	var count, version int
	if err := tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(min(version),0) FROM schema_version").Scan(&count, &version); err != nil {
		return errors.New("cannot read PostgreSQL schema version")
	}
	if count > 1 || (count == 1 && (version < 1 || version > 4)) {
		return errors.New("unsupported PostgreSQL database schema version")
	}
	if count == 0 {
		// No IF NOT EXISTS here: unrelated or partially initialized tables must
		// cause rollback, rather than silently being adopted or overwritten.
		if _, err := tx.ExecContext(ctx, postgresSchema); err != nil {
			return errors.New("cannot initialize PostgreSQL tables; use an empty dedicated database with schema creation permission")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version(version) VALUES(4)"); err != nil {
			return errors.New("cannot record PostgreSQL schema version")
		}
	} else if version == 1 {
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
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit PostgreSQL schema initialization")
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
	for _, sentinel := range []error{sql.ErrNoRows, ErrStale, errPostgresEncoding, context.Canceled, context.DeadlineExceeded} {
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
