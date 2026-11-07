package store

import (
	"database/sql"
	"fmt"
)

// currentSchema is the schema this application creates and upgrades to.
const currentSchema = 6

// initializeSQLite applies supported upgrades in one transaction. Fresh
// schemas fail on existing unrelated tables; failed or future-version opens
// never create, replace, or partially migrate the application's tables.
func initializeSQLite(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='schema_version')").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := tx.Exec(schema); err != nil {
			return fmt.Errorf("cannot initialize SQLite tables: %w", err)
		}
	} else {
		var count, version int
		if err := tx.QueryRow("SELECT count(*),COALESCE(min(version),0) FROM schema_version").Scan(&count, &version); err != nil {
			return fmt.Errorf("cannot read SQLite database schema version: %w", err)
		}
		if count != 1 || version < 1 || version > currentSchema {
			return fmt.Errorf("unsupported database schema version")
		}
		if version == 1 {
			if _, err := tx.Exec(`ALTER TABLE runs ADD COLUMN diagnostics TEXT NOT NULL DEFAULT '';
CREATE INDEX runs_feed_history ON runs(feed_id,id DESC);
UPDATE schema_version SET version=2`); err != nil {
				return fmt.Errorf("cannot migrate SQLite database from schema version 1 to 2: %w", err)
			}
			version = 2
		}
		if version == 2 {
			// Filters live in recipe JSON. The version gate prevents older
			// applications from silently ignoring those rules on refresh.
			if _, err := tx.Exec("UPDATE schema_version SET version=3"); err != nil {
				return fmt.Errorf("cannot migrate SQLite database from schema version 2 to 3: %w", err)
			}
			version = 3
		}
		if version == 3 {
			// Article bodies are stored beside the list-page teaser rather than
			// replacing it, so re-extracting the list page each refresh keeps
			// updating late-published images without discarding fetched content.
			if _, err := tx.Exec(`ALTER TABLE items ADD COLUMN content_full TEXT NOT NULL DEFAULT '';
UPDATE schema_version SET version=4`); err != nil {
				return fmt.Errorf("cannot migrate SQLite database from schema version 3 to 4: %w", err)
			}
			version = 4
		}
		if version == 4 {
			// Library filters are shared by reference. The version gate also stops
			// an older application from refreshing linked feeds without their rules.
			// last_changed records when published output last changed; see
			// schemaFiveSQLite for why it starts where it does.
			if _, err := tx.Exec(schemaFiveSQLite + "UPDATE schema_version SET version=5"); err != nil {
				return fmt.Errorf("cannot migrate SQLite database from schema version 4 to 5: %w", err)
			}
			version = 5
		}
		if version == 5 {
			// See schemaSixSQLite for what last_new records and why the version
			// gate matters even to an application that never reads it.
			if _, err := tx.Exec(schemaSixSQLite + "UPDATE schema_version SET version=6"); err != nil {
				return fmt.Errorf("cannot migrate SQLite database from schema version 5 to 6: %w", err)
			}
		}
	}
	return tx.Commit()
}

// Keep these in step with the matching tables in schema.sql and
// postgres_schema.sql; migration tests compare upgraded and fresh databases.
//
// last_changed is when a feed's or story's published output last changed,
// replacing the observation times (last_success, last_seen) that moved on
// every refresh. A feed starts from last_success and a story from 0, which
// renders as its last_seen, so the upgrade itself changes no published byte;
// stories take their value lazily at their next merge rather than rewriting
// every row, article bodies included, inside this transaction.
const schemaFiveSQLite = `ALTER TABLE feeds ADD COLUMN last_changed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE items ADD COLUMN last_changed INTEGER NOT NULL DEFAULT 0;
UPDATE feeds SET last_changed=last_success;
CREATE TABLE filters (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, rules TEXT NOT NULL
);
CREATE TABLE feed_filters (
 feed_id TEXT NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
 filter_id TEXT NOT NULL REFERENCES filters(id),
 position INTEGER NOT NULL,
 PRIMARY KEY(feed_id,filter_id)
);
CREATE INDEX feed_filters_filter ON feed_filters(filter_id);
`

const schemaFivePostgres = `ALTER TABLE feeds ADD COLUMN last_changed BIGINT NOT NULL DEFAULT 0;
ALTER TABLE items ADD COLUMN last_changed BIGINT NOT NULL DEFAULT 0;
UPDATE feeds SET last_changed=last_success;
CREATE TABLE filters (
 id TEXT COLLATE "C" PRIMARY KEY, name TEXT COLLATE "C" NOT NULL, rules TEXT NOT NULL
);
CREATE TABLE feed_filters (
 feed_id TEXT NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
 filter_id TEXT NOT NULL REFERENCES filters(id),
 position INTEGER NOT NULL,
 PRIMARY KEY(feed_id,filter_id)
);
CREATE INDEX feed_filters_filter ON feed_filters(filter_id);
`

// Keep these in step with the feeds table in schema.sql and
// postgres_schema.sql; ALTER TABLE appends the column, so the fresh schemas
// list it last too.
//
// last_new is when a stored refresh last inserted a story its feed did not
// have stored, and that retention kept; 0 means none, or none known. Nothing
// remembers deleted rows, so a story that retention or history pruning removed
// counts again if its source still lists it. It is dashboard and metrics
// state: reader output never reads it, so neither this upgrade nor a refresh
// that moves it changes a published byte. Existing feeds start at their newest
// stored story's first_seen, which is what that rule would have recorded for
// every story still stored.
//
// The version gate matters as much as the column. It stops 1.2 and older from
// refreshing feeds whose recipes carry 1.3 fields they would silently ignore.
// Content cleanup's remove rules and accept_language change published output:
// without the gate an older application would publish uncleaned pages, or
// pages in the source's default language, and keep the article bodies it
// fetched that way, as schema 3 prevents for filters. quiet_days and the feed
// recipe type would not need the gate alone, because 1.2 only drops the first
// and refuses to refresh the second.
const schemaSixSQLite = `ALTER TABLE feeds ADD COLUMN last_new INTEGER NOT NULL DEFAULT 0;
UPDATE feeds SET last_new=COALESCE((SELECT max(first_seen) FROM items WHERE items.feed_id=feeds.id),0);
`

const schemaSixPostgres = `ALTER TABLE feeds ADD COLUMN last_new BIGINT NOT NULL DEFAULT 0;
UPDATE feeds SET last_new=m.v FROM (SELECT feed_id,max(first_seen) AS v FROM items GROUP BY feed_id) AS m WHERE m.feed_id=feeds.id;
`
