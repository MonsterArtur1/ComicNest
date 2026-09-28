package store

import (
	"encoding/json"
	"log"
)

// MaxScanProblems caps how many per-file problems one scan history row keeps
// (ScanHistoryEntry.ProblemCount still holds the real total), so a library
// full of broken archives can't bloat the table.
const MaxScanProblems = 100

// ScanProblem is one file a scan could not fully handle — the scan carries on
// past it, but the admin panel lists it so it isn't only in the log.
type ScanProblem struct {
	Path    string `json:"path"`    // relative to the library root when possible
	Stage   string `json:"stage"`   // "add", "refresh", "comicinfo", "cover", "walk"
	Message string `json:"message"` // the underlying error text
}

// ScanHistoryEntry is one completed scan, shown in the admin panel's scan
// history table. StartedAt/FinishedAt are raw DB text (UTC, "2006-01-02
// 15:04:05"), formatted by the caller the same way as Series.CreatedAt or
// User.LastLoginAt elsewhere.
type ScanHistoryEntry struct {
	ID         int64
	StartedAt  string
	FinishedAt string
	Found      int
	Processed  int
	Missing    int
	CVUpdated  int
	CVFailed   int
	Err        string
	// Library is the root path of the library this scan covered, or "" for
	// a combined "scan all" run recorded before per-library history existed.
	Library string

	// Detailed is false for rows recorded before migration 14: the change
	// counters below are then unknown, not zero.
	Detailed     bool
	Added        int   // files catalogued for the first time
	AddedBytes   int64 // their combined size on disk
	Restored     int   // previously missing files that reappeared
	NewlyMissing int   // files that went missing during this scan
	SeriesAdded  int   // series created by this scan
	ProblemCount int   // total per-file problems (Problems is capped)
	Problems     []ScanProblem
}

// RecordScanHistory appends one completed scan to the history table.
func (s *Store) RecordScanHistory(h ScanHistoryEntry) error {
	problems := ""
	if len(h.Problems) > 0 {
		if len(h.Problems) > MaxScanProblems {
			h.Problems = h.Problems[:MaxScanProblems]
		}
		raw, err := json.Marshal(h.Problems)
		if err != nil {
			return err
		}
		problems = string(raw)
	}
	if h.ProblemCount < len(h.Problems) {
		h.ProblemCount = len(h.Problems)
	}
	_, err := s.db.Exec(`
		INSERT INTO scan_history (started_at, finished_at, found, processed, missing, cv_updated, cv_failed, error, library,
		                          detailed, added, added_bytes, restored, newly_missing, series_added, problem_count, problems)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.StartedAt, h.FinishedAt, h.Found, h.Processed, h.Missing, h.CVUpdated, h.CVFailed, h.Err, h.Library,
		h.Detailed, h.Added, h.AddedBytes, h.Restored, h.NewlyMissing, h.SeriesAdded, h.ProblemCount, problems)
	return err
}

// ListScanHistory returns the most recent scans, newest first, capped at
// limit. library scopes the result to that library's own scans; "" returns
// every scan regardless of library.
func (s *Store) ListScanHistory(limit int, library string) ([]ScanHistoryEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, started_at, finished_at, found, processed, missing, cv_updated, cv_failed, error, library,
		       detailed, added, added_bytes, restored, newly_missing, series_added, problem_count, problems
		FROM scan_history
		WHERE ? = '' OR library = ?
		ORDER BY id DESC
		LIMIT ?`, library, library, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ScanHistoryEntry
	for rows.Next() {
		var h ScanHistoryEntry
		var problems string
		if err := rows.Scan(&h.ID, &h.StartedAt, &h.FinishedAt, &h.Found, &h.Processed, &h.Missing, &h.CVUpdated, &h.CVFailed, &h.Err, &h.Library,
			&h.Detailed, &h.Added, &h.AddedBytes, &h.Restored, &h.NewlyMissing, &h.SeriesAdded, &h.ProblemCount, &problems); err != nil {
			return nil, err
		}
		if problems != "" {
			// A corrupt list only loses the details; the count still shows.
			if err := json.Unmarshal([]byte(problems), &h.Problems); err != nil {
				log.Printf("scan history %d: decoding problems: %v", h.ID, err)
			}
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// MissingIssueIDs returns the ids of issues currently flagged as missing, so
// a scan can tell files that just disappeared from ones already gone, and
// spot the ones that came back.
func (s *Store) MissingIssueIDs() (map[int64]bool, error) {
	rows, err := s.db.Query(`SELECT id FROM issues WHERE file_missing = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// MaxSeriesID returns the highest series id (0 when there are none). Paired
// with CountSeriesAfter it tells a scan how many series it created.
func (s *Store) MaxSeriesID() (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM series`).Scan(&id)
	return id, err
}

// CountSeriesAfter counts the series of library whose id is above afterID —
// i.e. those created since MaxSeriesID returned it.
func (s *Store) CountSeriesAfter(afterID int64, library string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM series WHERE id > ? AND library = ?`, afterID, library).Scan(&n)
	return n, err
}
