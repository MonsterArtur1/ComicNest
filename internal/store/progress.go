package store

import (
	"database/sql"
	"errors"
	"strings"
)

// ReadingProgress is the last page a reader fetched for an issue.
type ReadingProgress struct {
	Page      int    // last page read, 1-based (OPDS-PSE convention)
	UpdatedAt string // SQLite datetime text, UTC
}

// The user argument throughout is the account name from config.yaml, or ""
// for the anonymous reader when no accounts are configured.

// SetReadingProgress records that user reached page (1-based). Readers
// stream pages in order (with some read-ahead), so the highest page seen so
// far is kept — jumping back to re-read does not erase progress.
func (s *Store) SetReadingProgress(user string, issueID int64, page int) error {
	_, err := s.db.Exec(`
		INSERT INTO reading_progress (user, issue_id, page, updated_at)
		VALUES (?, ?, ?, datetime('now'))
		ON CONFLICT(user, issue_id) DO UPDATE SET
			page = MAX(page, excluded.page),
			updated_at = excluded.updated_at`, user, issueID, page)
	return err
}

// ClearReadingProgress forgets the user's progress on the issue (unread).
func (s *Store) ClearReadingProgress(user string, issueID int64) error {
	_, err := s.db.Exec(`DELETE FROM reading_progress WHERE user = ? AND issue_id = ?`, user, issueID)
	return err
}

// GetReadingProgress returns the user's progress, or nil when never read.
func (s *Store) GetReadingProgress(user string, issueID int64) (*ReadingProgress, error) {
	var p ReadingProgress
	err := s.db.QueryRow(`
		SELECT page, updated_at FROM reading_progress WHERE user = ? AND issue_id = ?`,
		user, issueID).Scan(&p.Page, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ReadingProgressFor returns the user's progress for the given issues
// (absent = unread), in one query for catalog feeds and list views.
func (s *Store) ReadingProgressFor(user string, issueIDs []int64) (map[int64]ReadingProgress, error) {
	out := make(map[int64]ReadingProgress)
	if len(issueIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(issueIDs)+1)
	args = append(args, user)
	for _, id := range issueIDs {
		args = append(args, id)
	}
	rows, err := s.db.Query(`
		SELECT issue_id, page, updated_at FROM reading_progress
		WHERE user = ? AND issue_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(issueIDs)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p ReadingProgress
		if err := rows.Scan(&id, &p.Page, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}

// IssueInProgress is an issue a reader started but has not finished.
type IssueInProgress struct {
	IssueWithSeries
	Progress ReadingProgress
}

// ListIssuesInProgress returns the user's started-but-unfinished issues (last
// read page below the page total), most recently read first. Issues whose
// page total is unknown count as unfinished. A lone first page (opened and
// closed right away) does not count as "started" — it takes real progress
// (page 2+) to show up here.
func (s *Store) ListIssuesInProgress(user string, limit int) ([]IssueInProgress, error) {
	rows, err := s.db.Query(`
		SELECT `+issueWithSeriesColumns+`, rp.page, rp.updated_at
		FROM reading_progress rp
		JOIN issues i ON i.id = rp.issue_id
		JOIN series s ON s.id = i.series_id
		WHERE rp.user = ? AND i.file_missing = 0 AND rp.page > 1
		  AND (`+totalPagesExpr+` = 0 OR rp.page < `+totalPagesExpr+`)
		ORDER BY rp.updated_at DESC
		LIMIT ?`, user, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []IssueInProgress
	for rows.Next() {
		var r IssueInProgress
		if err := rows.Scan(append(r.fields(), &r.SeriesName, &r.Progress.Page, &r.Progress.UpdatedAt)...); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// totalPagesExpr picks the best known page total for an issue: the count
// read from the file itself when available, falling back to the metadata
// page count. Zero means the total is unknown.
const totalPagesExpr = `CASE WHEN i.file_pages > 0 THEN i.file_pages ELSE i.page_count END`

// CountReadIssues returns the number of present-on-disk issues the user has
// read to the end (page total known and reached).
func (s *Store) CountReadIssues(user string) (int, error) {
	var n int
	err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM issues i JOIN reading_progress rp ON rp.issue_id = i.id AND rp.user = ?
		WHERE i.file_missing = 0 AND `+totalPagesExpr+` > 0 AND rp.page >= `+totalPagesExpr,
		user).Scan(&n)
	return n, err
}

// ListReadIssues returns present-on-disk issues the user has read to the
// end, most recently finished first.
func (s *Store) ListReadIssues(user string, limit, offset int) ([]IssueWithSeries, error) {
	rows, err := s.db.Query(`
		SELECT `+issueWithSeriesColumns+`
		FROM issues i JOIN series s ON s.id = i.series_id
		JOIN reading_progress rp ON rp.issue_id = i.id AND rp.user = ?
		WHERE i.file_missing = 0 AND `+totalPagesExpr+` > 0 AND rp.page >= `+totalPagesExpr+`
		ORDER BY rp.updated_at DESC, i.id DESC
		LIMIT ? OFFSET ?`, user, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssuesWithSeries(rows)
}

// unreadCondition matches issues with no real reading progress: either no
// row at all, or a lone first page (opened and closed right away, which
// does not count as "started" unless that first page was also the last).
const unreadCondition = `(rp.issue_id IS NULL
	OR (rp.page <= 1 AND NOT (` + totalPagesExpr + ` > 0 AND rp.page >= ` + totalPagesExpr + `)))`

// CountUnreadIssues returns the number of present-on-disk issues the user
// has not meaningfully started (no progress, or only its first page seen).
func (s *Store) CountUnreadIssues(user string) (int, error) {
	var n int
	err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM issues i LEFT JOIN reading_progress rp ON rp.issue_id = i.id AND rp.user = ?
		WHERE i.file_missing = 0 AND `+unreadCondition,
		user).Scan(&n)
	return n, err
}

// ListUnreadIssues returns present-on-disk issues the user has not
// meaningfully started, newest first.
func (s *Store) ListUnreadIssues(user string, limit, offset int) ([]IssueWithSeries, error) {
	rows, err := s.db.Query(`
		SELECT `+issueWithSeriesColumns+`
		FROM issues i JOIN series s ON s.id = i.series_id
		LEFT JOIN reading_progress rp ON rp.issue_id = i.id AND rp.user = ?
		WHERE i.file_missing = 0 AND `+unreadCondition+`
		ORDER BY i.created_at DESC, i.id DESC
		LIMIT ? OFFSET ?`, user, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssuesWithSeries(rows)
}

// AdoptAnonymousProgress hands progress recorded before accounts existed
// (empty user name) to the given user, keeping the user's own rows where
// both exist. Returns how many rows moved.
func (s *Store) AdoptAnonymousProgress(user string) (int64, error) {
	if user == "" {
		return 0, nil
	}
	res, err := s.db.Exec(`UPDATE OR IGNORE reading_progress SET user = ? WHERE user = ''`, user)
	if err != nil {
		return 0, err
	}
	moved, _ := res.RowsAffected()
	// Rows that collided with the user's own progress stay anonymous; drop them.
	if _, err := s.db.Exec(`DELETE FROM reading_progress WHERE user = ''`); err != nil {
		return moved, err
	}
	return moved, nil
}
