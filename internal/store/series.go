package store

import (
	"database/sql"
	"errors"
)

// Series is a comic series row plus aggregates used by list views.
type Series struct {
	ID                int64
	Name              string
	FolderPath        sql.NullString
	Publisher         string
	Description       string
	ComicVineVolumeID sql.NullInt64
	ComicVineURL      string // comicvine.gamespot.com page, set together with ComicVineVolumeID
	MetadataLocked    bool
	OneShot           bool // single publication, not part of any series
	CreatedAt         string
	UpdatedAt         string

	IssueCount   int   // number of issues in the series
	CoverIssueID int64 // first issue of the series (used for cover + one-shot links)

	// Reading aggregates over issues present on disk (list views only).
	IssuesPresent int    // issues whose file exists
	IssuesStarted int    // present issues with real reading progress (page 2+, or finished)
	IssuesRead    int    // present issues read to the last page
	PageCount     int    // total pages across present issues (list views only)
	ReleaseYear   string // earliest issue's release year, e.g. "2016" (list views only)
}

// IssuesUnread returns the number of present issues not yet read to the end.
func (sr *Series) IssuesUnread() int {
	return sr.IssuesPresent - sr.IssuesRead
}

// AllRead reports whether every downloadable issue has been read to the end.
func (sr *Series) AllRead() bool {
	return sr.IssuesPresent > 0 && sr.IssuesRead == sr.IssuesPresent
}

// InProgress reports whether reading has started but not finished.
func (sr *Series) InProgress() bool {
	return sr.IssuesStarted > 0 && !sr.AllRead()
}

// GetSeries returns the series by id (with its issue count), or nil.
func (s *Store) GetSeries(id int64) (*Series, error) {
	var sr Series
	err := s.db.QueryRow(`
		SELECT s.id, s.name, s.folder_path, s.publisher, s.description,
		       s.comicvine_volume_id, s.comicvine_url, s.metadata_locked, s.one_shot,
		       s.created_at, s.updated_at,
		       (SELECT COUNT(*) FROM issues i WHERE i.series_id = s.id)
		FROM series s WHERE s.id = ?`, id).
		Scan(&sr.ID, &sr.Name, &sr.FolderPath, &sr.Publisher, &sr.Description,
			&sr.ComicVineVolumeID, &sr.ComicVineURL, &sr.MetadataLocked, &sr.OneShot,
			&sr.CreatedAt, &sr.UpdatedAt, &sr.IssueCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sr, nil
}

// UpdateSeriesManual saves a user edit of series metadata (including the
// one-shot flag) and locks the series against automated overwrites.
func (s *Store) UpdateSeriesManual(id int64, name, publisher, description string, oneShot bool) error {
	_, err := s.db.Exec(`
		UPDATE series SET name = ?, publisher = ?, description = ?, one_shot = ?,
			metadata_locked = 1, updated_at = datetime('now')
		WHERE id = ?`, name, publisher, description, oneShot, id)
	return err
}

// SetSeriesLocked toggles the series metadata lock.
func (s *Store) SetSeriesLocked(id int64, locked bool) error {
	_, err := s.db.Exec(`
		UPDATE series SET metadata_locked = ?, updated_at = datetime('now')
		WHERE id = ?`, locked, id)
	return err
}

// SetSeriesOneShot applies an automated one-shot signal (scan or ComicVine).
// The flag is also user-editable in the series form, so locked series keep
// the user's choice.
func (s *Store) SetSeriesOneShot(id int64, oneShot bool) error {
	_, err := s.db.Exec(`
		UPDATE series SET one_shot = ?, updated_at = datetime('now')
		WHERE id = ? AND metadata_locked = 0`, oneShot, id)
	return err
}

// ReconcileOneShots derives the one-shot flag from library contents after a
// scan: a lone unnumbered issue marks its series as a one-shot, while a
// series that gained a second issue stops being one. Scraped one-shots (one
// issue that received a number from ComicVine) are left untouched.
func (s *Store) ReconcileOneShots() error {
	if _, err := s.db.Exec(`
		UPDATE series SET one_shot = 1
		WHERE one_shot = 0 AND metadata_locked = 0 AND id IN (
			SELECT series_id FROM issues GROUP BY series_id
			HAVING COUNT(*) = 1 AND MAX(issue_number) = ''
		)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`
		UPDATE series SET one_shot = 0
		WHERE one_shot = 1 AND metadata_locked = 0 AND id IN (
			SELECT series_id FROM issues GROUP BY series_id
			HAVING COUNT(*) >= 2
		)`)
	return err
}

// SetSeriesComicVineVolume records the user's ComicVine volume match. The
// match itself is allowed even on locked series — the lock protects metadata
// text, not the mapping.
func (s *Store) SetSeriesComicVineVolume(id, volumeID int64, url string) error {
	_, err := s.db.Exec(`
		UPDATE series SET comicvine_volume_id = ?, comicvine_url = ?, updated_at = datetime('now')
		WHERE id = ?`, volumeID, url, id)
	return err
}

// ClearSeriesComicVineVolume removes the series' ComicVine volume match and
// rolls back its unlocked issues that came from that volume — their matched
// id and metadata source are reset, same as ClearIssueComicVine — so a
// future rematch starts clean instead of silently keeping stale ComicVine
// ids around. Locked issues (the user's own edits) are left untouched.
func (s *Store) ClearSeriesComicVineVolume(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		UPDATE issues SET comicvine_issue_id = NULL, comicvine_url = '',
			metadata_source = CASE WHEN has_comicinfo THEN ? ELSE ? END,
			updated_at = datetime('now')
		WHERE series_id = ? AND metadata_locked = 0 AND metadata_source = ?`,
		SourceComicInfo, SourceFilename, id, SourceComicVine); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE series SET comicvine_volume_id = NULL, comicvine_url = '', updated_at = datetime('now')
		WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// EnrichSeriesFromComicVine applies ComicVine volume data to an unlocked
// series: the name is taken over outright (the user explicitly picked this
// volume, and folder-derived names like "WalkingDead" should become the real
// title), publisher/description only fill fields that are still empty.
func (s *Store) EnrichSeriesFromComicVine(id int64, name, publisher, description string) error {
	_, err := s.db.Exec(`
		UPDATE series SET
			name        = CASE WHEN ? != '' THEN ? ELSE name END,
			publisher   = CASE WHEN publisher   = '' THEN ? ELSE publisher   END,
			description = CASE WHEN description = '' THEN ? ELSE description END,
			updated_at  = datetime('now')
		WHERE id = ? AND metadata_locked = 0`, name, name, publisher, description, id)
	return err
}

// MergeSeries moves every issue from one series into another and deletes the
// now-empty source series. Used when two library entries turn out to be the
// same series (e.g. two folders scraped separately, corrected to the same
// name). The target keeps its own metadata, filling only fields it still has
// empty from the source, and is never itself locked or renamed by the merge.
//
// The source's own folder (or, for a virtual series, its own name) is
// recorded as an alias to the target: a rescan resolves it there instead of
// finding nothing, recreating the deleted series, and silently undoing the
// merge. Aliases that already pointed at the source, from an earlier merge,
// are repointed to the target too, so chained merges keep working.
func (s *Store) MergeSeries(fromID, intoID int64) error {
	if fromID == intoID {
		return errors.New("cannot merge a series into itself")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		INSERT INTO series_folder_aliases (folder_path, series_id)
		SELECT folder_path, ? FROM series WHERE id = ? AND folder_path IS NOT NULL
		ON CONFLICT(folder_path) DO UPDATE SET series_id = excluded.series_id`,
		intoID, fromID); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO series_name_aliases (name, series_id)
		SELECT name, ? FROM series WHERE id = ? AND folder_path IS NULL
		ON CONFLICT(name) DO UPDATE SET series_id = excluded.series_id`,
		intoID, fromID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE series_folder_aliases SET series_id = ? WHERE series_id = ?`, intoID, fromID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE series_name_aliases SET series_id = ? WHERE series_id = ?`, intoID, fromID); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		UPDATE series SET
			publisher   = CASE WHEN publisher   = '' THEN (SELECT publisher FROM series WHERE id = ?) ELSE publisher END,
			description = CASE WHEN description = '' THEN (SELECT description FROM series WHERE id = ?) ELSE description END,
			updated_at  = datetime('now')
		WHERE id = ?`, fromID, fromID, intoID); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE issues SET series_id = ?, updated_at = datetime('now')
		WHERE series_id = ?`, intoID, fromID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM series WHERE id = ?`, fromID); err != nil {
		return err
	}
	return tx.Commit()
}

// FindOrCreateSeriesByFolder returns the series backed by the given library
// folder (relative path), creating it with the folder's base name when
// absent. A folder that used to back its own series, merged away since (see
// MergeSeries), resolves to the merge target instead of creating a fresh
// series and silently undoing the merge.
func (s *Store) FindOrCreateSeriesByFolder(folderPath, name string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM series WHERE folder_path = ?`, folderPath).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	err = s.db.QueryRow(`SELECT series_id FROM series_folder_aliases WHERE folder_path = ?`, folderPath).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO series (name, folder_path) VALUES (?, ?)`, name, folderPath)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FindOrCreateSeriesByName returns the "virtual" series (no backing folder)
// with the given name, creating it when absent. Name match is
// case-insensitive. A name that used to back its own virtual series, merged
// away since (see MergeSeries), resolves to the merge target instead of
// creating a fresh series and silently undoing the merge.
func (s *Store) FindOrCreateSeriesByName(name string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`
		SELECT id FROM series
		WHERE folder_path IS NULL AND name = ? COLLATE NOCASE`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	err = s.db.QueryRow(`SELECT series_id FROM series_name_aliases WHERE name = ?`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO series (name) VALUES (?)`, name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SeriesSort names a supported ordering for ListSeries. Each criterion has an
// ascending and descending variant; the bare value is whichever direction
// makes sense as the default (e.g. "recent" is newest-first).
type SeriesSort string

const (
	SeriesSortName      SeriesSort = "name"       // A→Z
	SeriesSortNameDesc  SeriesSort = "name_desc"  // Z→A
	SeriesSortRecent    SeriesSort = "recent"     // newest added first
	SeriesSortRecentAsc SeriesSort = "recent_asc" // oldest added first
	SeriesSortUnread    SeriesSort = "unread"     // most unread issues first
	SeriesSortUnreadAsc SeriesSort = "unread_asc" // fewest unread issues first
	SeriesSortPages     SeriesSort = "pages"      // most pages first
	SeriesSortPagesAsc  SeriesSort = "pages_asc"  // fewest pages first
	SeriesSortYear      SeriesSort = "year"       // most recent release year first
	SeriesSortYearAsc   SeriesSort = "year_asc"   // oldest release year first
)

// ParseSeriesSort maps a query value to a sort (unknown → name).
func ParseSeriesSort(v string) SeriesSort {
	switch s := SeriesSort(v); s {
	case SeriesSortName, SeriesSortNameDesc, SeriesSortRecent, SeriesSortRecentAsc,
		SeriesSortUnread, SeriesSortUnreadAsc, SeriesSortPages, SeriesSortPagesAsc,
		SeriesSortYear, SeriesSortYearAsc:
		return s
	}
	return SeriesSortName
}

// SeriesFilter narrows the series grid.
type SeriesFilter string

const (
	SeriesFilterAll     SeriesFilter = "all"
	SeriesFilterUnread  SeriesFilter = "unread"  // no reading progress at all
	SeriesFilterRead    SeriesFilter = "read"    // every present issue read to the end
	SeriesFilterReading SeriesFilter = "reading" // started, not all finished
	SeriesFilterNoCV    SeriesFilter = "nocv"    // some issue without ComicVine (or manual) metadata
	SeriesFilterMissing SeriesFilter = "missing" // some issue's file is gone
)

// ParseSeriesFilter maps a query value to a filter (unknown → all).
func ParseSeriesFilter(v string) SeriesFilter {
	switch f := SeriesFilter(v); f {
	case SeriesFilterUnread, SeriesFilterRead, SeriesFilterReading, SeriesFilterNoCV, SeriesFilterMissing:
		return f
	}
	return SeriesFilterAll
}

// having returns the HAVING clause for the filter, in terms of the aggregate
// aliases computed by ListSeries.
func (f SeriesFilter) having() string {
	switch f {
	case SeriesFilterUnread:
		return "HAVING started_cnt = 0"
	case SeriesFilterRead:
		return "HAVING present_cnt > 0 AND read_cnt = present_cnt"
	case SeriesFilterReading:
		return "HAVING started_cnt > 0 AND read_cnt < present_cnt"
	case SeriesFilterNoCV:
		return "HAVING SUM(i.metadata_source IN ('filename', 'comicinfo')) > 0"
	case SeriesFilterMissing:
		return "HAVING SUM(i.file_missing) > 0"
	}
	return ""
}

// ListSeries returns series that have at least one issue, optionally filtered
// by a case-insensitive substring matched against the series name/publisher
// or any of its issues' writer/artist/publisher, and a SeriesFilter, with the
// given user's reading aggregates filled in.
func (s *Store) ListSeries(user, nameFilter string, sort SeriesSort, filter SeriesFilter) ([]Series, error) {
	order := "s.name COLLATE NOCASE ASC"
	switch sort {
	case SeriesSortNameDesc:
		order = "s.name COLLATE NOCASE DESC"
	case SeriesSortRecent:
		order = "MAX(i.created_at) DESC"
	case SeriesSortRecentAsc:
		order = "MAX(i.created_at) ASC"
	case SeriesSortUnread:
		order = "(present_cnt - read_cnt) DESC"
	case SeriesSortUnreadAsc:
		order = "(present_cnt - read_cnt) ASC"
	case SeriesSortPages:
		order = "pages_total DESC"
	case SeriesSortPagesAsc:
		order = "pages_total ASC"
	case SeriesSortYear:
		order = "release_year = '', release_year DESC"
	case SeriesSortYearAsc:
		order = "release_year = '', release_year ASC"
	}

	// An issue counts as read when its progress reached the page total
	// (archive count, else metadata count); unknown totals never count.
	const total = `CASE WHEN i.file_pages > 0 THEN i.file_pages ELSE i.page_count END`
	// A lone first page (opened and closed right away) does not count as
	// "started" — only real progress (page 2+) or an outright finish does.
	const started = `rp.page IS NOT NULL AND (rp.page > 1 OR (` + total + ` > 0 AND rp.page >= ` + total + `))`

	// The cover endpoint falls back to a placeholder on its own, so the
	// representative issue is simply the series' first one.
	query := `
		SELECT s.id, s.name, s.folder_path, s.publisher, s.description,
		       s.comicvine_volume_id, s.metadata_locked, s.one_shot,
		       s.created_at, s.updated_at,
		       COUNT(i.id),
		       COALESCE((
		           SELECT i2.id FROM issues i2
		           WHERE i2.series_id = s.id
		           ORDER BY CAST(i2.issue_number AS REAL), i2.issue_number
		           LIMIT 1
		       ), 0),
		       COALESCE(SUM(i.file_missing = 0), 0) AS present_cnt,
		       COALESCE(SUM(i.file_missing = 0 AND ` + started + `), 0) AS started_cnt,
		       COALESCE(SUM(i.file_missing = 0 AND rp.page IS NOT NULL
		                    AND ` + total + ` > 0 AND rp.page >= ` + total + `), 0) AS read_cnt,
		       COALESCE(SUM(CASE WHEN i.file_missing = 0 THEN ` + total + ` ELSE 0 END), 0) AS pages_total,
		       COALESCE(MIN(NULLIF(substr(i.release_date, 1, 4), '')), '') AS release_year
		FROM series s
		JOIN issues i ON i.series_id = s.id
		LEFT JOIN reading_progress rp ON rp.issue_id = i.id AND rp.user = ?
		WHERE (? = '' OR s.name LIKE '%' || ? || '%' OR s.publisher LIKE '%' || ? || '%'
		       OR EXISTS (
		           SELECT 1 FROM issues i2 WHERE i2.series_id = s.id
		           AND (i2.writer LIKE '%' || ? || '%' OR i2.artist LIKE '%' || ? || '%' OR i2.publisher LIKE '%' || ? || '%')
		       ))
		GROUP BY s.id
		` + filter.having() + `
		ORDER BY ` + order

	rows, err := s.db.Query(query, user, nameFilter, nameFilter, nameFilter, nameFilter, nameFilter, nameFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Series
	for rows.Next() {
		var sr Series
		err := rows.Scan(&sr.ID, &sr.Name, &sr.FolderPath, &sr.Publisher, &sr.Description,
			&sr.ComicVineVolumeID, &sr.MetadataLocked, &sr.OneShot,
			&sr.CreatedAt, &sr.UpdatedAt, &sr.IssueCount, &sr.CoverIssueID,
			&sr.IssuesPresent, &sr.IssuesStarted, &sr.IssuesRead, &sr.PageCount, &sr.ReleaseYear)
		if err != nil {
			return nil, err
		}
		out = append(out, sr)
	}
	return out, rows.Err()
}
