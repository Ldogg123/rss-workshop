package store

import (
	"database/sql"
	"fmt"
)

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
		if count != 1 || version < 1 || version > 3 {
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
		}
	}
	return tx.Commit()
}
