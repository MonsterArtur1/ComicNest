package store

import (
	"database/sql"
	"errors"
)

// Guided-view panel cache (page_panels). See internal/server/panels.go.

// PageFileState identifies the archive state and detection algorithm a
// cached panel list was computed for.
type PageFileState struct {
	Size    int64 // archive size in bytes
	MTime   int64 // archive modification time, unix seconds
	Version int   // detector version and options, panels.Options.CacheVersion
}

// GetPagePanels returns the cached panel JSON of an issue page (0-based) for
// the given reading order. ok is false when nothing is cached or the cached
// row was computed for another file state or algorithm version.
func (s *Store) GetPagePanels(issueID int64, page int, rtl bool, st PageFileState) (panels string, ok bool, err error) {
	var cached PageFileState
	err = s.db.QueryRow(`
		SELECT file_size, mtime, version, panels FROM page_panels
		WHERE issue_id = ? AND page = ? AND rtl = ?`, issueID, page, rtl).
		Scan(&cached.Size, &cached.MTime, &cached.Version, &panels)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if cached != st {
		return "", false, nil
	}
	return panels, true, nil
}

// SetPagePanels stores (or replaces) the panel JSON of an issue page, and
// drops the issue's rows computed for another file state or version (the
// file changed, or detection improved: they would never be served again).
func (s *Store) SetPagePanels(issueID int64, page int, rtl bool, st PageFileState, panels string) error {
	if _, err := s.db.Exec(`
		DELETE FROM page_panels
		WHERE issue_id = ? AND (file_size != ? OR mtime != ? OR version != ?)`,
		issueID, st.Size, st.MTime, st.Version); err != nil {
		return err
	}
	_, err := s.db.Exec(`
		INSERT INTO page_panels (issue_id, page, rtl, file_size, mtime, version, panels)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(issue_id, page, rtl) DO UPDATE SET
			file_size = excluded.file_size, mtime = excluded.mtime,
			version = excluded.version, panels = excluded.panels`,
		issueID, page, rtl, st.Size, st.MTime, st.Version, panels)
	return err
}
