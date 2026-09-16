package store

import (
	"database/sql"
	"fmt"
)

// currentSchema is the schema this application creates and upgrades to.
const currentSchema = 5

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
