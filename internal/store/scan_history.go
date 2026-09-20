package store

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
}

// RecordScanHistory appends one completed scan to the history table.
func (s *Store) RecordScanHistory(h ScanHistoryEntry) error {
	_, err := s.db.Exec(`
		INSERT INTO scan_history (started_at, finished_at, found, processed, missing, cv_updated, cv_failed, error, library)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.StartedAt, h.FinishedAt, h.Found, h.Processed, h.Missing, h.CVUpdated, h.CVFailed, h.Err, h.Library)
	return err
}

// ListScanHistory returns the most recent scans, newest first, capped at
// limit. library scopes the result to that library's own scans; "" returns
// every scan regardless of library.
func (s *Store) ListScanHistory(limit int, library string) ([]ScanHistoryEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, started_at, finished_at, found, processed, missing, cv_updated, cv_failed, error, library
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
		if err := rows.Scan(&h.ID, &h.StartedAt, &h.FinishedAt, &h.Found, &h.Processed, &h.Missing, &h.CVUpdated, &h.CVFailed, &h.Err, &h.Library); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
