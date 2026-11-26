package store

import (
	"slices"
	"strings"
)

// Creators are not stored on their own: writers and artists live in the
// comma-separated issues.writer / issues.artist columns (ComicInfo, ComicVine,
// manual edits), so a creator page is computed from those. A creator's
// identity is NormalizeName of the name, which is also what the URL carries.

// Creator is one person with the number of present issues they have a credit on.
type Creator struct {
	Key     string // NormalizeName(Name), the identity used in URLs
	Name    string // the first spelling met
	Writer  int    // issues credited as writer
	Artist  int    // issues credited as artist
	Credits int    // issues with either credit (an issue counts once)
}

// ListCreators returns everyone credited on a present issue, most issues
// first, ties by name.
func (s *Store) ListCreators() ([]Creator, error) {
	rows, err := s.db.Query(`
		SELECT writer, artist FROM issues
		WHERE file_missing = 0 AND (writer != '' OR artist != '')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byKey := make(map[string]*Creator)
	var order []*Creator
	touch := func(name string) *Creator {
		key := NormalizeName(name)
		c := byKey[key]
		if c == nil {
			c = &Creator{Key: key, Name: name}
			byKey[key] = c
			order = append(order, c)
		}
		return c
	}
	for rows.Next() {
		var writer, artist string
		if err := rows.Scan(&writer, &artist); err != nil {
			return nil, err
		}
		inIssue := make(map[*Creator]bool)
		for _, w := range SplitCredits(writer) {
			c := touch(w)
			if !inIssue[c] {
				c.Credits++
				inIssue[c] = true
			}
			c.Writer++
		}
		for _, a := range SplitCredits(artist) {
			c := touch(a)
			if !inIssue[c] {
				c.Credits++
				inIssue[c] = true
			}
			c.Artist++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Creator, len(order))
	for i, c := range order {
		out[i] = *c
	}
	slices.SortFunc(out, func(a, b Creator) int {
		if a.Credits != b.Credits {
			return b.Credits - a.Credits
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, nil
}

// CreatorIssues is everything the library holds by one creator.
type CreatorIssues struct {
	Name    string // display name; "" when nobody by that key is credited
	Writer  []IssueWithSeries
	Artist  []IssueWithSeries
	Credits int // distinct issues
}

// ListCreatorIssues returns the present issues crediting the creator (key is
// a NormalizeName value) as writer and as artist, by series name then issue
// number.
func (s *Store) ListCreatorIssues(key string) (CreatorIssues, error) {
	var out CreatorIssues
	key = NormalizeName(key)
	if key == "" {
		return out, nil
	}
	rows, err := s.db.Query(`
		SELECT ` + issueWithSeriesColumns + `
		FROM issues i JOIN series s ON s.id = i.series_id
		WHERE i.file_missing = 0 AND (i.writer != '' OR i.artist != '')
		ORDER BY s.name COLLATE NOCASE, s.id, i.issue_number = '', CAST(i.issue_number AS REAL),
			i.issue_number, i.title, i.id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	all, err := scanIssuesWithSeries(rows)
	if err != nil {
		return out, err
	}

	credited := func(list, key string) (name string, ok bool) {
		for _, n := range SplitCredits(list) {
			if NormalizeName(n) == key {
				return n, true
			}
		}
		return "", false
	}
	for _, is := range all {
		w, isWriter := credited(is.Writer, key)
		a, isArtist := credited(is.Artist, key)
		if !isWriter && !isArtist {
			continue
		}
		if out.Name == "" {
			out.Name = w
			if !isWriter {
				out.Name = a
			}
		}
		if isWriter {
			out.Writer = append(out.Writer, is)
		}
		if isArtist {
			out.Artist = append(out.Artist, is)
		}
		out.Credits++
	}
	return out, nil
}
