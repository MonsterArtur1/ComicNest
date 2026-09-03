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

// SetReadingProgress records that the reader reached page (1-based). Readers
// stream pages in order (with some read-ahead), so the highest page seen so
// far is kept — jumping back to re-read does not erase progress.
func (s *Store) SetReadingProgress(issueID int64, page int) error {
	_, err := s.db.Exec(`
		INSERT INTO reading_progress (issue_id, page, updated_at)
		VALUES (?, ?, datetime('now'))
		ON CONFLICT(issue_id) DO UPDATE SET
			page = MAX(page, excluded.page),
			updated_at = excluded.updated_at`, issueID, page)
	return err
}

// GetReadingProgress returns the issue's progress, or nil when never read.
func (s *Store) GetReadingProgress(issueID int64) (*ReadingProgress, error) {
	var p ReadingProgress
	err := s.db.QueryRow(`SELECT page, updated_at FROM reading_progress WHERE issue_id = ?`, issueID).
		Scan(&p.Page, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ReadingProgressFor returns progress for the given issues (absent = unread),
// in one query for catalog feeds.
func (s *Store) ReadingProgressFor(issueIDs []int64) (map[int64]ReadingProgress, error) {
	out := make(map[int64]ReadingProgress)
	if len(issueIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(issueIDs))
	for i, id := range issueIDs {
		args[i] = id
	}
	rows, err := s.db.Query(`
		SELECT issue_id, page, updated_at FROM reading_progress
		WHERE issue_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(issueIDs)), ",")+`)`, args...)
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

// ListIssuesInProgress returns started-but-unfinished issues (last read page
// below the page total), most recently read first. Issues whose page total is
// unknown count as unfinished.
func (s *Store) ListIssuesInProgress(limit int) ([]IssueInProgress, error) {
	rows, err := s.db.Query(`
		SELECT `+issueWithSeriesColumns+`, rp.page, rp.updated_at
		FROM reading_progress rp
		JOIN issues i ON i.id = rp.issue_id
		JOIN series s ON s.id = i.series_id
		WHERE i.file_missing = 0
		  AND (CASE WHEN i.file_pages > 0 THEN i.file_pages ELSE i.page_count END = 0
		       OR rp.page < CASE WHEN i.file_pages > 0 THEN i.file_pages ELSE i.page_count END)
		ORDER BY rp.updated_at DESC
		LIMIT ?`, limit)
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
