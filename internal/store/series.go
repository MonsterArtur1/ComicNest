package store

import "database/sql"

// Series is a comic series row plus aggregates used by list views.
type Series struct {
	ID                int64
	Name              string
	FolderPath        sql.NullString
	Publisher         string
	Description       string
	ComicVineVolumeID sql.NullInt64
	MetadataLocked    bool
	OneShot           bool // single publication, not part of any series
	CreatedAt         string
	UpdatedAt         string

	IssueCount   int   // number of issues in the series
	CoverIssueID int64 // first issue of the series (used for cover + one-shot links)
}

// GetSeries returns the series by id (with its issue count), or nil.
func (s *Store) GetSeries(id int64) (*Series, error) {
	var sr Series
	err := s.db.QueryRow(`
		SELECT s.id, s.name, s.folder_path, s.publisher, s.description,
		       s.comicvine_volume_id, s.metadata_locked, s.one_shot,
		       s.created_at, s.updated_at,
		       (SELECT COUNT(*) FROM issues i WHERE i.series_id = s.id)
		FROM series s WHERE s.id = ?`, id).
		Scan(&sr.ID, &sr.Name, &sr.FolderPath, &sr.Publisher, &sr.Description,
			&sr.ComicVineVolumeID, &sr.MetadataLocked, &sr.OneShot,
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
func (s *Store) SetSeriesComicVineVolume(id, volumeID int64) error {
	_, err := s.db.Exec(`
		UPDATE series SET comicvine_volume_id = ?, updated_at = datetime('now')
		WHERE id = ?`, volumeID, id)
	return err
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

// FindOrCreateSeriesByFolder returns the series backed by the given library
// folder (relative path), creating it with the folder's base name when absent.
func (s *Store) FindOrCreateSeriesByFolder(folderPath, name string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM series WHERE folder_path = ?`, folderPath).Scan(&id)
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
// with the given name, creating it when absent. Name match is case-insensitive.
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
	res, err := s.db.Exec(`INSERT INTO series (name) VALUES (?)`, name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SeriesSort names a supported ordering for ListSeries.
type SeriesSort string

const (
	SeriesSortName   SeriesSort = "name"
	SeriesSortRecent SeriesSort = "recent"
)

// ListSeries returns series that have at least one issue, optionally filtered
// by a case-insensitive name substring.
func (s *Store) ListSeries(nameFilter string, sort SeriesSort) ([]Series, error) {
	order := "s.name COLLATE NOCASE ASC"
	if sort == SeriesSortRecent {
		order = "MAX(i.created_at) DESC"
	}

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
		       ), 0)
		FROM series s
		JOIN issues i ON i.series_id = s.id
		WHERE (? = '' OR s.name LIKE '%' || ? || '%')
		GROUP BY s.id
		ORDER BY ` + order

	rows, err := s.db.Query(query, nameFilter, nameFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Series
	for rows.Next() {
		var sr Series
		err := rows.Scan(&sr.ID, &sr.Name, &sr.FolderPath, &sr.Publisher, &sr.Description,
			&sr.ComicVineVolumeID, &sr.MetadataLocked, &sr.OneShot,
			&sr.CreatedAt, &sr.UpdatedAt, &sr.IssueCount, &sr.CoverIssueID)
		if err != nil {
			return nil, err
		}
		out = append(out, sr)
	}
	return out, rows.Err()
}
