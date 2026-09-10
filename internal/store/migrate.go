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

	// 3: page streaming (OPDS-PSE) — the real number of image pages inside
	// the archive (page_count is metadata and may be missing or wrong) and
	// per-issue reading progress reported by streaming readers.
	`
	ALTER TABLE issues ADD COLUMN file_pages INTEGER NOT NULL DEFAULT 0;

	CREATE TABLE reading_progress (
		issue_id   INTEGER PRIMARY KEY REFERENCES issues(id) ON DELETE CASCADE,
		page       INTEGER NOT NULL,
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	`,

	// 4: user accounts — progress is kept per user name (accounts live in
	// config.yaml, not in the database). Existing rows become the anonymous
	// reader's ('') and are handed to the first configured user at startup
	// (see Store.AdoptAnonymousProgress).
	`
	CREATE TABLE reading_progress_v4 (
		user       TEXT NOT NULL DEFAULT '',
		issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
		page       INTEGER NOT NULL,
		updated_at TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (user, issue_id)
	);
	INSERT INTO reading_progress_v4 (user, issue_id, page, updated_at)
		SELECT '', issue_id, page, updated_at FROM reading_progress;
	DROP TABLE reading_progress;
	ALTER TABLE reading_progress_v4 RENAME TO reading_progress;
	CREATE INDEX idx_progress_issue ON reading_progress(issue_id);
	`,

	// 5: the Series value from the file's ComicInfo.xml, kept separately from
	// the (editable) series row so the scanner can split a folder whose files
	// belong to different series. NULL = not inspected yet (rows from before
	// this migration, backfilled by the next scan), '' = no ComicInfo/Series.
	`
	ALTER TABLE issues ADD COLUMN comicinfo_series TEXT;
	`,

	// 6: the ComicVine site page for a matched volume/issue, so the UI can
	// link straight to it instead of just showing the numeric id.
	`
	ALTER TABLE series ADD COLUMN comicvine_url TEXT NOT NULL DEFAULT '';
	ALTER TABLE issues ADD COLUMN comicvine_url TEXT NOT NULL DEFAULT '';
	`,

	// 7: aliases remembering how a series merged away (MergeSeries) used to be
	// found — by its own library folder, or by its own name when it was a
	// virtual (folder-less) series. Without this, a rescan re-resolves that
	// folder/name to nothing, recreates the deleted series from scratch, and
	// silently undoes the merge (see Store.FindOrCreateSeriesByFolder/Name).
	`
	CREATE TABLE series_folder_aliases (
		folder_path TEXT PRIMARY KEY,
		series_id   INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE
	);
	CREATE TABLE series_name_aliases (
		name        TEXT PRIMARY KEY COLLATE NOCASE,
		series_id   INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE
	);
	`,

	// 8: user accounts move from config.yaml into the database, with a real
	// password hash and an admin flag (see internal/server/admin.go). Zero
	// rows here means "no accounts yet" — the app runs open, with the visitor
	// treated as an anonymous admin so they can create the first account.
	`
	CREATE TABLE users (
		id            INTEGER PRIMARY KEY,
		name          TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		is_admin      INTEGER NOT NULL DEFAULT 0,
		last_login_at TEXT,
		created_at    TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
	);
	`,

	// 9: one row per completed scan, so the admin panel can show a history
	// instead of just the current/last status (which the in-memory Status
	// struct forgets on restart).
	`
	CREATE TABLE scan_history (
		id          INTEGER PRIMARY KEY,
		started_at  TEXT NOT NULL,
		finished_at TEXT NOT NULL,
		found       INTEGER NOT NULL DEFAULT 0,
		processed   INTEGER NOT NULL DEFAULT 0,
		missing     INTEGER NOT NULL DEFAULT 0,
		cv_updated  INTEGER NOT NULL DEFAULT 0,
		cv_failed   INTEGER NOT NULL DEFAULT 0,
		error       TEXT NOT NULL DEFAULT ''
	);
	`,

	// 10: per-user favorites on series and on individual issues, kept in their
	// own tables (same shape as reading_progress) so the anonymous reader and
	// each account keep separate lists.
	`
	CREATE TABLE series_favorites (
		user       TEXT NOT NULL,
		series_id  INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (user, series_id)
	);
	CREATE TABLE issue_favorites (
		user       TEXT NOT NULL,
		issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (user, issue_id)
	);
	CREATE INDEX idx_issue_favorites_issue ON issue_favorites(issue_id);
	`,

	// 11: series can no longer be favorited on their own — only individual
	// issues. A series still shows up under the "favorite" filter/star when
	// it contains a favorited issue, computed from issue_favorites alone
	// (see Store.ListSeries).
	`
	DROP TABLE series_favorites;
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
