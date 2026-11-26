package store

import (
	"database/sql"
	"time"
)

// Following and new releases.
//
// A user follows a series when it is matched to a ComicVine volume and either
// they chose to follow it (series_follows.follow = 1) or — with no explicit
// choice — they have started reading it (any reading_progress row). An
// explicit unfollow (follow = 0) overrides the implicit rule.

// followedExpr is a SQL predicate over series alias s and a user parameter
// (bound twice by the callers): "this user follows s".
const followedExpr = `
	s.comicvine_volume_id IS NOT NULL AND (
		EXISTS (SELECT 1 FROM series_follows f WHERE f.series_id = s.id AND f.user = ? AND f.follow = 1)
		OR (
			NOT EXISTS (SELECT 1 FROM series_follows f WHERE f.series_id = s.id AND f.user = ?)
			AND EXISTS (SELECT 1 FROM reading_progress rp JOIN issues i ON i.id = rp.issue_id
			            WHERE i.series_id = s.id AND rp.user = ?)
		)
	)`

// FollowState reports whether user follows the series and whether that is an
// explicit choice (false = implied by reading progress, or not followed).
func (s *Store) FollowState(user string, seriesID int64) (following, explicit bool, err error) {
	var vol sql.NullInt64
	if err = s.db.QueryRow(`SELECT comicvine_volume_id FROM series WHERE id = ?`, seriesID).Scan(&vol); err != nil {
		if err == sql.ErrNoRows {
			return false, false, nil
		}
		return false, false, err
	}
	if !vol.Valid {
		return false, false, nil
	}
	var f int
	err = s.db.QueryRow(`SELECT follow FROM series_follows WHERE user = ? AND series_id = ?`, user, seriesID).Scan(&f)
	if err == nil {
		return f == 1, true, nil
	}
	if err != sql.ErrNoRows {
		return false, false, err
	}
	err = s.db.QueryRow(`
		SELECT EXISTS (SELECT 1 FROM reading_progress rp JOIN issues i ON i.id = rp.issue_id
		               WHERE i.series_id = ? AND rp.user = ?)`, seriesID, user).Scan(&following)
	return following, false, err
}

// SetSeriesFollow records the user's explicit choice for a series.
func (s *Store) SetSeriesFollow(user string, seriesID int64, follow bool) error {
	_, err := s.db.Exec(`
		INSERT INTO series_follows (user, series_id, follow) VALUES (?, ?, ?)
		ON CONFLICT(user, series_id) DO UPDATE SET follow = excluded.follow`, user, seriesID, follow)
	return err
}

// FollowedVolumeIDs lists the ComicVine volumes any user follows — the set the
// release check asks ComicVine about (release data is shared between users).
func (s *Store) FollowedVolumeIDs() ([]int64, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT s.comicvine_volume_id FROM series s
		WHERE s.comicvine_volume_id IS NOT NULL AND (
			EXISTS (SELECT 1 FROM series_follows f WHERE f.series_id = s.id AND f.follow = 1)
			OR EXISTS (SELECT 1 FROM reading_progress rp JOIN issues i ON i.id = rp.issue_id
			           WHERE i.series_id = s.id
			             AND NOT EXISTS (SELECT 1 FROM series_follows f
			                             WHERE f.series_id = s.id AND f.user = rp.user AND f.follow = 0))
		)
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Release is a ComicVine issue of a series the user follows.
type Release struct {
	SeriesID    int64
	SeriesName  string
	CVIssueID   int64
	IssueNumber string
	Name        string
	StoreDate   string // YYYY-MM-DD
	URL         string
	InLibrary   bool // the issue is on disk
}

// ListReleases returns releases with a store date in [from, to] (inclusive,
// "YYYY-MM-DD") for the series user follows, oldest first. library scopes to
// one library ("" = all).
func (s *Store) ListReleases(user, from, to, library string) ([]Release, error) {
	rows, err := s.db.Query(`
		SELECT s.id, s.name, c.cv_issue_id, c.issue_number, c.name, c.store_date, c.site_url,
		       EXISTS (
		           SELECT 1 FROM issues i WHERE i.series_id = s.id AND i.file_missing = 0 AND (
		               i.comicvine_issue_id = c.cv_issue_id
		               OR (TRIM(c.issue_number) != '' AND TRIM(i.issue_number) != ''
		                   AND LOWER(LTRIM(TRIM(i.issue_number), '0')) = LOWER(LTRIM(TRIM(c.issue_number), '0')))
		           )
		       )
		FROM cv_volume_issues c
		JOIN series s ON s.comicvine_volume_id = c.volume_id
		WHERE c.store_date != '' AND c.store_date >= ? AND c.store_date <= ?
		  AND (? = '' OR s.library = ?)
		  AND (`+followedExpr+`)
		ORDER BY c.store_date, s.name COLLATE NOCASE, CAST(c.issue_number AS REAL)`,
		from, to, library, library, user, user, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Release
	for rows.Next() {
		var r Release
		if err := rows.Scan(&r.SeriesID, &r.SeriesName, &r.CVIssueID, &r.IssueNumber, &r.Name,
			&r.StoreDate, &r.URL, &r.InLibrary); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Meta keys of app_meta.
const MetaReleasesChecked = "releases_checked_at"

// GetMeta returns an app_meta value ("" when unset).
func (s *Store) GetMeta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM app_meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetMeta stores an app_meta value.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO app_meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// MetaTime parses a timestamp stored with SetMeta(key, MetaTimeString(t)).
func (s *Store) MetaTime(key string) (time.Time, bool, error) {
	v, err := s.GetMeta(key)
	if err != nil || v == "" {
		return time.Time{}, false, err
	}
	t, err := time.Parse("2006-01-02 15:04:05", v)
	if err != nil {
		return time.Time{}, false, nil
	}
	return t, true, nil
}

// MetaTimeString formats t the way MetaTime reads it back (UTC).
func MetaTimeString(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }
