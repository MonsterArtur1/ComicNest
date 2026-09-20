package store

import "database/sql"

// GetLibraryName returns the display-name override for a library (admin
// panel "Rename"), or "" if none is set.
func (s *Store) GetLibraryName(path string) (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM library_names WHERE path = ?`, path).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return name, nil
}

// SetLibraryName sets (name != "") or clears (name == "") a library's
// display-name override.
func (s *Store) SetLibraryName(path, name string) error {
	if name == "" {
		_, err := s.db.Exec(`DELETE FROM library_names WHERE path = ?`, path)
		return err
	}
	_, err := s.db.Exec(`
		INSERT INTO library_names (path, name) VALUES (?, ?)
		ON CONFLICT(path) DO UPDATE SET name = excluded.name`, path, name)
	return err
}
