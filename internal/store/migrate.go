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

	// 12: multi-library support. Every series is stamped with the root path
	// of the library it was scanned from (see Store.FindOrCreateSeriesBy*),
	// so the admin panel and main menu can scope stats/listings to one
	// library or "all". '' means not yet stamped (pre-upgrade rows) — they
	// get their real value the next time their library is scanned; until
	// then they still show up correctly under "all libraries".
	`
	ALTER TABLE series ADD COLUMN library TEXT NOT NULL DEFAULT '';
	CREATE INDEX idx_series_library ON series(library);
	ALTER TABLE scan_history ADD COLUMN library TEXT NOT NULL DEFAULT '';
	`,

	// 13: a library's display-name override (admin panel "Rename"), keyed by
	// its root path — a runtime preference, not a config.yaml setting, so
	// renaming needs no restart and no file edit either (see
	// Store.SetLibraryName / Server.libraryName).
	`
	CREATE TABLE library_names (
		path TEXT PRIMARY KEY,
		name TEXT NOT NULL
	);
	`,

	// 14: richer scan history — what a scan actually changed (new files and
	// their size, files that came back, newly missing ones, new series) and
	// the per-file problems it ran into (JSON list, capped; problem_count is
	// the uncapped total). detailed = 0 marks rows recorded before this, whose
	// zeros mean "unknown" rather than "nothing changed".
	`
	ALTER TABLE scan_history ADD COLUMN detailed      INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE scan_history ADD COLUMN added         INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE scan_history ADD COLUMN added_bytes   INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE scan_history ADD COLUMN restored      INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE scan_history ADD COLUMN newly_missing INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE scan_history ADD COLUMN series_added  INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE scan_history ADD COLUMN problem_count INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE scan_history ADD COLUMN problems      TEXT NOT NULL DEFAULT '';
	`,

	// 15: a log of individual page reads, for the page-level half of the
	// "Your stats" page (reading_progress only keeps the furthest page per
	// issue, so it can't say when pages were read). One row per user, issue,
	// page and UTC day — re-reading a page on another day counts again,
	// flipping back and forth within one day doesn't (see
	// Store.RecordPageRead). read_at is the first time that day.
	`
	CREATE TABLE page_reads (
		user     TEXT NOT NULL,
		issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
		page     INTEGER NOT NULL,
		day      TEXT NOT NULL,
		read_at  TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (user, issue_id, page, day)
	);
	CREATE INDEX idx_page_reads_user ON page_reads(user, read_at);
	CREATE INDEX idx_page_reads_issue ON page_reads(issue_id);
	`,

	// 16: KOReader progress sync (kosync, internal/server/kosync.go).
	// users.kosync_key is the md5 hex of the account password — what KOReader
	// sends as x-auth-key — stored only while kosync is enabled, refreshed on
	// each login/password change (NULL = sync not enabled for the account yet).
	// issue_hashes holds KOReader's two document ids per issue (partial md5 of
	// the content, md5 of the file name) plus the size/mtime they were computed
	// at. kosync_progress is the last position each user's KOReader pushed per
	// document id, whether or not the document is in the library.
	`
	ALTER TABLE users ADD COLUMN kosync_key TEXT;
	CREATE TABLE issue_hashes (
		issue_id     INTEGER PRIMARY KEY REFERENCES issues(id) ON DELETE CASCADE,
		md5_partial  TEXT NOT NULL,
		md5_filename TEXT NOT NULL,
		file_size    INTEGER NOT NULL,
		mtime        INTEGER NOT NULL
	);
	CREATE INDEX idx_issue_hashes_partial ON issue_hashes(md5_partial);
	CREATE INDEX idx_issue_hashes_filename ON issue_hashes(md5_filename);
	CREATE TABLE kosync_progress (
		user       TEXT NOT NULL,
		document   TEXT NOT NULL,
		progress   TEXT NOT NULL,
		percentage REAL NOT NULL,
		device     TEXT NOT NULL,
		device_id  TEXT NOT NULL,
		timestamp  INTEGER NOT NULL,
		PRIMARY KEY (user, document)
	);
	`,

	// 17: cached ComicVine issue lists for gap detection and
	// the new-releases feed, per-user explicit follow/unfollow of series, and a
	// tiny key/value table for the daily release-check stamp. A volume with a
	// cv_volume_sync row has had its full issue list fetched (gap detection
	// trusts only those); the release check may add partial rows without one.
	`
	CREATE TABLE cv_volume_issues (
		volume_id    INTEGER NOT NULL,
		cv_issue_id  INTEGER NOT NULL,
		issue_number TEXT NOT NULL DEFAULT '',
		name         TEXT NOT NULL DEFAULT '',
		cover_date   TEXT NOT NULL DEFAULT '',
		store_date   TEXT NOT NULL DEFAULT '',
		site_url     TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (volume_id, cv_issue_id)
	);
	CREATE INDEX idx_cv_volume_issues_store ON cv_volume_issues(store_date);
	CREATE TABLE cv_volume_sync (
		volume_id INTEGER PRIMARY KEY,
		synced_at TEXT NOT NULL
	);
	CREATE TABLE series_follows (
		user      TEXT NOT NULL,
		series_id INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
		follow    INTEGER NOT NULL,
		PRIMARY KEY (user, series_id)
	);
	CREATE INDEX idx_series_follows_series ON series_follows(series_id);
	CREATE TABLE app_meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	`,

	// 18: story arcs / reading orders and characters. See
	// store/arcs.go and store/characters.go.
	//   issues.comicinfo_extras  NULL = ComicInfo characters/arcs not read yet
	//                            (the next scan inspects the file, like
	//                            comicinfo_series); 1 = read.
	//   issues.cv_extras_at      NULL = ComicVine character/arc credits not
	//                            fetched yet (the scan's ComicVine phase
	//                            backfills issues that already have a
	//                            comicvine_issue_id).
	//   arcs / arc_cv_issues     an arc and its full ComicVine issue list in
	//                            reading order (owned or not); fetched_at NULL =
	//                            list not fetched, refetched after ~30 days.
	//   issue_arcs               membership of a catalogued issue (source
	//                            'comicinfo' or 'comicvine'; arc_number is
	//                            ComicInfo's StoryArcNumber, NULL when absent).
	//   characters / issue_characters  one row per character identity (the
	//                            ComicVine id when known, else the
	//                            case-insensitive name).
	`
	ALTER TABLE issues ADD COLUMN comicinfo_extras INTEGER;
	ALTER TABLE issues ADD COLUMN cv_extras_at TEXT;

	CREATE TABLE arcs (
		id            INTEGER PRIMARY KEY,
		name          TEXT NOT NULL,
		name_key      TEXT NOT NULL,
		comicvine_id  INTEGER UNIQUE,
		description   TEXT NOT NULL DEFAULT '',
		publisher     TEXT NOT NULL DEFAULT '',
		image_url     TEXT NOT NULL DEFAULT '',
		comicvine_url TEXT NOT NULL DEFAULT '',
		fetched_at    TEXT
	);
	CREATE INDEX idx_arcs_name_key ON arcs(name_key);

	CREATE TABLE arc_cv_issues (
		arc_id             INTEGER NOT NULL REFERENCES arcs(id) ON DELETE CASCADE,
		comicvine_issue_id INTEGER NOT NULL,
		position           INTEGER NOT NULL,
		volume_id          INTEGER NOT NULL DEFAULT 0,
		volume_name        TEXT NOT NULL DEFAULT '',
		issue_number       TEXT NOT NULL DEFAULT '',
		name               TEXT NOT NULL DEFAULT '',
		cover_date         TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (arc_id, comicvine_issue_id)
	);
	CREATE INDEX idx_arc_cv_issues_issue ON arc_cv_issues(comicvine_issue_id);

	CREATE TABLE issue_arcs (
		issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
		arc_id     INTEGER NOT NULL REFERENCES arcs(id) ON DELETE CASCADE,
		arc_number INTEGER,
		source     TEXT NOT NULL,
		PRIMARY KEY (issue_id, arc_id, source)
	);
	CREATE INDEX idx_issue_arcs_arc ON issue_arcs(arc_id);

	CREATE TABLE characters (
		id           INTEGER PRIMARY KEY,
		name         TEXT NOT NULL,
		name_key     TEXT NOT NULL,
		comicvine_id INTEGER UNIQUE
	);
	CREATE INDEX idx_characters_name_key ON characters(name_key);

	CREATE TABLE issue_characters (
		issue_id     INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
		character_id INTEGER NOT NULL REFERENCES characters(id) ON DELETE CASCADE,
		source       TEXT NOT NULL,
		PRIMARY KEY (issue_id, character_id, source)
	);
	CREATE INDEX idx_issue_characters_character ON issue_characters(character_id);
	`,

	// 19: "not interested" marks of the recommendations
	// (store/recommend.go), per user.
	`
	CREATE TABLE recommendation_dismissals (
		user       TEXT NOT NULL,
		series_id  INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (user, series_id)
	);
	`,

	// 20: guided view — cached panel rectangles per
	// issue page (internal/panels), as the JSON list the reader receives.
	// file_size/mtime are the archive's state when the page was analysed and
	// version the detection algorithm's (panels.Version); a row that no longer
	// matches either is recomputed on the next request (see
	// Store.GetPagePanels). rtl = the manga (right-to-left) reading order.
	`
	CREATE TABLE page_panels (
		issue_id  INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
		page      INTEGER NOT NULL,
		rtl       INTEGER NOT NULL DEFAULT 0,
		file_size INTEGER NOT NULL,
		mtime     INTEGER NOT NULL,
		version   INTEGER NOT NULL,
		panels    TEXT NOT NULL,
		PRIMARY KEY (issue_id, page, rtl)
	);
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
