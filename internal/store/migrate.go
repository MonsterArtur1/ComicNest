package store

import (
	"database/sql"
	"fmt"
)

// migrations is an append-only list; each entry runs in its own transaction
// and the applied version is recorded in schema_version.
var migrations = []string{
	// 1: initial schema — see docs/SPECIFICATION.md §5
	`
	CREATE TABLE series (
		id                  INTEGER PRIMARY KEY,
		name                TEXT NOT NULL,
		folder_path         TEXT UNIQUE,
		publisher           TEXT NOT NULL DEFAULT '',
		description         TEXT NOT NULL DEFAULT '',
		comicvine_volume_id INTEGER,
		metadata_locked     INTEGER NOT NULL DEFAULT 0,
		created_at          TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE issues (
		id                 INTEGER PRIMARY KEY,
		series_id          INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
		path               TEXT NOT NULL UNIQUE,
		file_size          INTEGER NOT NULL DEFAULT 0,
		file_missing       INTEGER NOT NULL DEFAULT 0,
		issue_number       TEXT NOT NULL DEFAULT '',
		title              TEXT NOT NULL DEFAULT '',
		summary            TEXT NOT NULL DEFAULT '',
		release_date       TEXT NOT NULL DEFAULT '',
		writer             TEXT NOT NULL DEFAULT '',
		artist             TEXT NOT NULL DEFAULT '',
		publisher          TEXT NOT NULL DEFAULT '',
		page_count         INTEGER NOT NULL DEFAULT 0,
		comicvine_issue_id INTEGER,
		metadata_source    TEXT NOT NULL DEFAULT 'filename',
		metadata_locked    INTEGER NOT NULL DEFAULT 0,
		has_comicinfo      INTEGER NOT NULL DEFAULT 0,
		cover_cached       INTEGER NOT NULL DEFAULT 0,
		created_at         TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at         TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE INDEX idx_issues_series ON issues(series_id);
	`,

	// 2: one-shot flag on series (single publications like tributes or
	// treasury editions that are not part of any series)
	`
	ALTER TABLE series ADD COLUMN one_shot INTEGER NOT NULL DEFAULT 0;
	`,
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}

	var current int
	err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current)
	if err != nil {
		return err
	}

	for i := current; i < len(migrations); i++ {
		version := i + 1
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %d: %w", version, err)
		}
	}
	return nil
}
