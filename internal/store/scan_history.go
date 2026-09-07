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
}

// RecordScanHistory appends one completed scan to the history table.
func (s *Store) RecordScanHistory(h ScanHistoryEntry) error {
	_, err := s.db.Exec(`
		INSERT INTO scan_history (started_at, finished_at, found, processed, missing, cv_updated, cv_failed, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		h.StartedAt, h.FinishedAt, h.Found, h.Processed, h.Missing, h.CVUpdated, h.CVFailed, h.Err)
	return err
}

// ListScanHistory returns the most recent scans, newest first, capped at limit.
func (s *Store) ListScanHistory(limit int) ([]ScanHistoryEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, started_at, finished_at, found, processed, missing, cv_updated, cv_failed, error
		FROM scan_history
		ORDER BY id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ScanHistoryEntry
	for rows.Next() {
		var h ScanHistoryEntry
		if err := rows.Scan(&h.ID, &h.StartedAt, &h.FinishedAt, &h.Found, &h.Processed, &h.Missing, &h.CVUpdated, &h.CVFailed, &h.Err); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
