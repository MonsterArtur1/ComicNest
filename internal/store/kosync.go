package store

import (
	"database/sql"
	"errors"
)

// KOReader progress sync (kosync) storage. See internal/server/kosync.go.

// SetUserKosyncKey stores the md5-of-password verifier KOReader authenticates
// with. A no-op when the stored value already matches, so it is cheap to call
// on every login.
func (s *Store) SetUserKosyncKey(name, key string) error {
	_, err := s.db.Exec(`UPDATE users SET kosync_key = ? WHERE name = ? AND kosync_key IS NOT ?`, key, name, key)
	return err
}

// ClearUserKosyncKey forgets the account's verifier (sync disabled for it
// until it logs in again while kosync is enabled).
func (s *Store) ClearUserKosyncKey(id int64) error {
	_, err := s.db.Exec(`UPDATE users SET kosync_key = NULL WHERE id = ?`, id)
	return err
}

// GetUserKosyncKey returns the account's verifier, or "" when the account
// does not exist or has none.
func (s *Store) GetUserKosyncKey(name string) (string, error) {
	var k sql.NullString
	err := s.db.QueryRow(`SELECT kosync_key FROM users WHERE name = ?`, name).Scan(&k)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return k.String, err
}

// DocHashState is what the scanner needs to decide whether an issue's
// KOReader hashes are current.
type DocHashState struct {
	IssueID int64
	Hashed  bool  // a row in issue_hashes exists
	Size    int64 // file size / mtime (unix seconds) the hashes were computed at
	MTime   int64
}

// GetDocHashState looks up the issue at path; ok is false when there is none.
func (s *Store) GetDocHashState(path string) (st DocHashState, ok bool, err error) {
	var size, mtime sql.NullInt64
	err = s.db.QueryRow(`
		SELECT i.id, h.file_size, h.mtime
		FROM issues i LEFT JOIN issue_hashes h ON h.issue_id = i.id
		WHERE i.path = ?`, path).Scan(&st.IssueID, &size, &mtime)
	if errors.Is(err, sql.ErrNoRows) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	st.Hashed = size.Valid
	st.Size, st.MTime = size.Int64, mtime.Int64
	return st, true, nil
}

// SetIssueHashes stores (or replaces) the issue's KOReader document ids.
func (s *Store) SetIssueHashes(issueID int64, partial, filename string, size, mtime int64) error {
	_, err := s.db.Exec(`
		INSERT INTO issue_hashes (issue_id, md5_partial, md5_filename, file_size, mtime)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(issue_id) DO UPDATE SET
			md5_partial = excluded.md5_partial, md5_filename = excluded.md5_filename,
			file_size = excluded.file_size, mtime = excluded.mtime`,
		issueID, partial, filename, size, mtime)
	return err
}

// FindIssueByDocument returns the issue KOReader's document id refers to —
// matching either the content hash or the file-name hash — preferring files
// present on disk. Nil when no library issue has that id.
func (s *Store) FindIssueByDocument(document string) (*Issue, error) {
	row := s.db.QueryRow(`SELECT `+issueColumns+` FROM issues WHERE id = (
		SELECT i.id FROM issues i JOIN issue_hashes h ON h.issue_id = i.id
		WHERE h.md5_partial = ?1 OR h.md5_filename = ?1
		ORDER BY i.file_missing, (h.md5_partial = ?1) DESC, i.id
		LIMIT 1)`, document)
	i, err := scanIssue(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return i, err
}

// KosyncProgress is a position pushed by a KOReader device.
type KosyncProgress struct {
	Document   string
	Progress   string
	Percentage float64
	Device     string
	DeviceID   string
	Timestamp  int64 // unix seconds
}

// SaveKosyncProgress stores the user's latest position for the document.
func (s *Store) SaveKosyncProgress(user string, p KosyncProgress) error {
	_, err := s.db.Exec(`
		INSERT INTO kosync_progress (user, document, progress, percentage, device, device_id, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user, document) DO UPDATE SET
			progress = excluded.progress, percentage = excluded.percentage,
			device = excluded.device, device_id = excluded.device_id,
			timestamp = excluded.timestamp`,
		user, p.Document, p.Progress, p.Percentage, p.Device, p.DeviceID, p.Timestamp)
	return err
}

// GetKosyncProgress returns the user's last pushed position, or nil.
func (s *Store) GetKosyncProgress(user, document string) (*KosyncProgress, error) {
	p := KosyncProgress{Document: document}
	err := s.db.QueryRow(`
		SELECT progress, percentage, device, device_id, timestamp
		FROM kosync_progress WHERE user = ? AND document = ?`, user, document).
		Scan(&p.Progress, &p.Percentage, &p.Device, &p.DeviceID, &p.Timestamp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
