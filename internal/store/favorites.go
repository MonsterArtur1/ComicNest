package store

import "strings"

// The user argument throughout is the account name from config.yaml, or ""
// for the anonymous reader when no accounts are configured — same convention
// as reading progress (see progress.go).
//
// Only individual issues can be favorited — there is no series-level
// favorite. A series shows up under the "favorite" filter/star on the
// library page when it contains at least one favorited issue (see
// Store.ListSeries).

// SetIssueFavorite marks or unmarks an issue as one of user's favorites.
func (s *Store) SetIssueFavorite(user string, issueID int64, favorite bool) error {
	if !favorite {
		_, err := s.db.Exec(`DELETE FROM issue_favorites WHERE user = ? AND issue_id = ?`, user, issueID)
		return err
	}
	_, err := s.db.Exec(`
		INSERT INTO issue_favorites (user, issue_id) VALUES (?, ?)
		ON CONFLICT(user, issue_id) DO NOTHING`, user, issueID)
	return err
}

// IsIssueFavorite reports whether user has favorited the issue.
func (s *Store) IsIssueFavorite(user string, issueID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM issue_favorites WHERE user = ? AND issue_id = ?`, user, issueID).Scan(&n)
	return n > 0, err
}

// IssueFavoritesFor returns which of the given issues user has favorited,
// for list views that batch this in one query.
func (s *Store) IssueFavoritesFor(user string, issueIDs []int64) (map[int64]bool, error) {
	out := make(map[int64]bool)
	if len(issueIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(issueIDs)+1)
	args = append(args, user)
	for _, id := range issueIDs {
		args = append(args, id)
	}
	rows, err := s.db.Query(`
		SELECT issue_id FROM issue_favorites
		WHERE user = ? AND issue_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(issueIDs)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ListFavoriteIssuesBySeries returns the user's favorited issues within a
// series, same ordering as ListIssuesBySeries.
func (s *Store) ListFavoriteIssuesBySeries(user string, seriesID int64) ([]Issue, error) {
	rows, err := s.db.Query(`
		SELECT i.id, i.series_id, i.path, i.file_size, i.file_missing, i.issue_number,
			i.title, i.summary, i.release_date, i.writer, i.artist, i.publisher,
			i.page_count, i.file_pages, i.comicvine_issue_id, i.comicvine_url, i.metadata_source,
			i.metadata_locked, i.has_comicinfo, i.comicinfo_series, i.cover_cached,
			i.created_at, i.updated_at
		FROM issues i
		JOIN issue_favorites f ON f.issue_id = i.id AND f.user = ?
		WHERE i.series_id = ?
		ORDER BY i.issue_number = '', CAST(i.issue_number AS REAL), i.issue_number, i.title`,
		user, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

// CountFavoriteIssues returns the number of present-on-disk issues user has
// favorited.
func (s *Store) CountFavoriteIssues(user string) (int, error) {
	var n int
	err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM issues i JOIN issue_favorites f ON f.issue_id = i.id AND f.user = ?
		WHERE i.file_missing = 0`, user).Scan(&n)
	return n, err
}

// ListFavoriteIssues returns present-on-disk issues user has favorited, most
// recently favorited first.
func (s *Store) ListFavoriteIssues(user string, limit, offset int) ([]IssueWithSeries, error) {
	rows, err := s.db.Query(`
		SELECT `+issueWithSeriesColumns+`
		FROM issues i JOIN series s ON s.id = i.series_id
		JOIN issue_favorites f ON f.issue_id = i.id AND f.user = ?
		WHERE i.file_missing = 0
		ORDER BY f.created_at DESC, i.id DESC
		LIMIT ? OFFSET ?`, user, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssuesWithSeries(rows)
}
