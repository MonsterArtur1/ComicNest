package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Characters: one row per identity, linked to issues through issue_characters
// (with the source that named them). The ComicVine id is the identity when
// known; ComicInfo names carry none, so they join the character of the same
// case-insensitive name, and a ComicInfo-only character later seen on
// ComicVine adopts the ComicVine id instead of becoming a duplicate.

// Character is one character identity.
type Character struct {
	ID          int64
	Name        string
	ComicVineID sql.NullInt64
}

// CharacterInput names a character an issue features.
type CharacterInput struct {
	Name        string
	ComicVineID int // 0 = unknown (ComicInfo)
}

func resolveCharacter(tx execer, in CharacterInput) (int64, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return 0, nil
	}
	key := NormalizeName(name)
	var id int64

	if in.ComicVineID > 0 {
		err := tx.QueryRow(`SELECT id FROM characters WHERE comicvine_id = ?`, in.ComicVineID).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		err = tx.QueryRow(`SELECT id FROM characters WHERE name_key = ? AND comicvine_id IS NULL ORDER BY id LIMIT 1`, key).Scan(&id)
		if err == nil {
			_, err = tx.Exec(`UPDATE characters SET comicvine_id = ?, name = ? WHERE id = ?`, in.ComicVineID, name, id)
			return id, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		res, err := tx.Exec(`INSERT INTO characters (name, name_key, comicvine_id) VALUES (?, ?, ?)`, name, key, in.ComicVineID)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}

	err := tx.QueryRow(`SELECT id FROM characters WHERE name_key = ? ORDER BY id LIMIT 1`, key).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := tx.Exec(`INSERT INTO characters (name, name_key) VALUES (?, ?)`, name, key)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// replaceIssueCharacters makes the issue's characters of the given source
// exactly the given ones.
func replaceIssueCharacters(tx execer, issueID int64, source string, chars []CharacterInput) error {
	if _, err := tx.Exec(`DELETE FROM issue_characters WHERE issue_id = ? AND source = ?`, issueID, source); err != nil {
		return err
	}
	for _, in := range chars {
		id, err := resolveCharacter(tx, in)
		if err != nil {
			return err
		}
		if id == 0 {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO issue_characters (issue_id, character_id, source) VALUES (?, ?, ?)`,
			issueID, id, source); err != nil {
			return err
		}
	}
	return nil
}

// CharacterCount is a character with the number of present issues featuring it.
type CharacterCount struct {
	Character
	Issues int
}

// ListCharacters returns every character featured in at least one present
// issue, most issues first, ties by name.
func (s *Store) ListCharacters() ([]CharacterCount, error) {
	rows, err := s.db.Query(`
		SELECT c.id, c.name, c.comicvine_id, COUNT(DISTINCT ic.issue_id) AS n
		FROM characters c
		JOIN issue_characters ic ON ic.character_id = c.id
		JOIN issues i ON i.id = ic.issue_id AND i.file_missing = 0
		GROUP BY c.id
		ORDER BY n DESC, c.name COLLATE NOCASE, c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CharacterCount
	for rows.Next() {
		var c CharacterCount
		if err := rows.Scan(&c.ID, &c.Name, &c.ComicVineID, &c.Issues); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCharacter returns the character by id, or nil.
func (s *Store) GetCharacter(id int64) (*Character, error) {
	var c Character
	err := s.db.QueryRow(`SELECT id, name, comicvine_id FROM characters WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.ComicVineID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListCharacterIssues returns the present issues featuring the character,
// grouped-friendly: by series name (then series id), then issue number.
func (s *Store) ListCharacterIssues(id int64) ([]IssueWithSeries, error) {
	rows, err := s.db.Query(`
		SELECT `+issueWithSeriesColumns+`
		FROM issues i JOIN series s ON s.id = i.series_id
		WHERE i.file_missing = 0
		  AND i.id IN (SELECT issue_id FROM issue_characters WHERE character_id = ?)
		ORDER BY s.name COLLATE NOCASE, s.id, i.issue_number = '', CAST(i.issue_number AS REAL),
			i.issue_number, i.title, i.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssuesWithSeries(rows)
}

// IssueCharacters returns the characters featured in an issue, by name.
func (s *Store) IssueCharacters(issueID int64) ([]Character, error) {
	rows, err := s.db.Query(`
		SELECT c.id, c.name, c.comicvine_id
		FROM characters c
		WHERE c.id IN (SELECT character_id FROM issue_characters WHERE issue_id = ?)
		ORDER BY c.name COLLATE NOCASE, c.id`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Character
	for rows.Next() {
		var c Character
		if err := rows.Scan(&c.ID, &c.Name, &c.ComicVineID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
