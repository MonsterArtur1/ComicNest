package store

import (
	"database/sql"
	"errors"
)

// User is an account: the same credentials log into the web UI and
// authenticate OPDS readers (internal/server/auth.go). Passwords are never
// stored here — only the hash the server layer computes (bcrypt).
type User struct {
	ID           int64
	Name         string
	PasswordHash string
	IsAdmin      bool
	LastLoginAt  sql.NullString
	CreatedAt    string
	UpdatedAt    string
}

const userColumns = `id, name, password_hash, is_admin, last_login_at, created_at, updated_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Name, &u.PasswordHash, &u.IsAdmin, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser inserts a new account. name must be unique (case-sensitive,
// matching HTTP Basic auth and session lookups).
func (s *Store) CreateUser(name, passwordHash string, isAdmin bool) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO users (name, password_hash, is_admin) VALUES (?, ?, ?)`,
		name, passwordHash, isAdmin)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetUser returns the account by id, or nil.
func (s *Store) GetUser(id int64) (*User, error) {
	u, err := scanUser(s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// GetUserByName returns the account with the given name, or nil.
func (s *Store) GetUserByName(name string) (*User, error) {
	u, err := scanUser(s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// ListUsers returns every account, alphabetically by name.
func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT ` + userColumns + ` FROM users ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// CountUsers returns the number of accounts. Zero means no account has ever
// been created — the app runs open, with the anonymous visitor treated as an
// admin so they can create the first one.
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CountAdmins returns the number of admin accounts, so the last one can be
// protected from demotion or deletion.
func (s *Store) CountAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&n)
	return n, err
}

// SetUserPassword replaces the account's password hash.
func (s *Store) SetUserPassword(id int64, passwordHash string) error {
	_, err := s.db.Exec(`
		UPDATE users SET password_hash = ?, updated_at = datetime('now') WHERE id = ?`,
		passwordHash, id)
	return err
}

// SetUserAdmin toggles the account's admin flag.
func (s *Store) SetUserAdmin(id int64, isAdmin bool) error {
	_, err := s.db.Exec(`
		UPDATE users SET is_admin = ?, updated_at = datetime('now') WHERE id = ?`,
		isAdmin, id)
	return err
}

// DeleteUser removes the account. Its reading progress rows stay (keyed by
// name, not id) — a future account with the same name would inherit them,
// same as the pre-account anonymous reader today.
func (s *Store) DeleteUser(id int64) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

// TouchUserLogin records a successful login (web session or OPDS Basic
// auth), for the admin panel's "last seen" column.
func (s *Store) TouchUserLogin(name string) error {
	_, err := s.db.Exec(`
		UPDATE users SET last_login_at = datetime('now') WHERE name = ?`, name)
	return err
}
