package store

import (
	"slices"
	"strconv"
	"strings"
)

// LibraryStats is a library-wide health snapshot for the admin panel.
// LockedIssueCount counts locked issues regardless of file_missing; every
// other issue-level field is scoped as its name says.
type LibraryStats struct {
	SeriesCount       int
	OneShotCount      int
	LockedSeriesCount int
	IssueCount        int   // present on disk (file_missing = 0)
	MissingCount      int   // file_missing = 1
	TotalSize         int64 // bytes, sum of file_size over present issues
	ComicVineCount    int   // present issues sourced from ComicVine
	NoMetadataCount   int   // present issues never enriched from ComicVine
	LockedIssueCount  int
}

// LibraryStats reports library-wide counts for the admin panel: one query
// over series (that have at least one issue, matching the library grid) and
// one over issues. library scopes the result to series stamped with that
// root path; "" means every library ("all").
func (s *Store) LibraryStats(library string) (LibraryStats, error) {
	var st LibraryStats
	err := s.db.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(one_shot), 0),
		       COALESCE(SUM(metadata_locked), 0)
		FROM series
		WHERE (? = '' OR library = ?) AND id IN (SELECT DISTINCT series_id FROM issues)`,
		library, library).
		Scan(&st.SeriesCount, &st.OneShotCount, &st.LockedSeriesCount)
	if err != nil {
		return LibraryStats{}, err
	}

	err = s.db.QueryRow(`
		SELECT COALESCE(SUM(i.file_missing = 0), 0),
		       COALESCE(SUM(i.file_missing = 1), 0),
		       COALESCE(SUM(CASE WHEN i.file_missing = 0 THEN i.file_size ELSE 0 END), 0),
		       COALESCE(SUM(i.file_missing = 0 AND i.metadata_source = 'comicvine'), 0),
		       COALESCE(SUM(i.file_missing = 0 AND i.metadata_source IN ('filename', 'comicinfo')), 0),
		       COALESCE(SUM(i.metadata_locked), 0)
		FROM issues i
		JOIN series s ON s.id = i.series_id
		WHERE (? = '' OR s.library = ?)`,
		library, library).
		Scan(&st.IssueCount, &st.MissingCount, &st.TotalSize,
			&st.ComicVineCount, &st.NoMetadataCount, &st.LockedIssueCount)
	if err != nil {
		return LibraryStats{}, err
	}
	return st, nil
}

// UserStats is one user's all-time reading summary for the "Your stats"
// page. Like the rest of the reading views it only counts present-on-disk
// issues, and "read" means the page total is known and reached.
type UserStats struct {
	IssuesRead      int
	InProgress      int // page 2+ reached, not finished (see ListIssuesInProgress)
	PagesRead       int // every page of finished issues plus the pages reached in the rest
	SeriesStarted   int // series with at least one issue opened
	SeriesCompleted int // series with every present issue read
	Favorites       int
	TopSeries       []NamedCount
	TopPublishers   []NamedCount
	TopWriters      []NamedCount
}

// NamedCount is one row of a "most read" ranking. ID is the series id for
// TopSeries and zero elsewhere.
type NamedCount struct {
	ID    int64
	Name  string
	Count int
}

// userStatsTopN is how many rows each "most read" ranking keeps.
const userStatsTopN = 5

// UserStats reports the user's reading summary: two aggregate queries plus
// one pass over the finished issues for the rankings (writer credits are a
// comma-separated list, which is easier to split in Go than in SQLite).
func (s *Store) UserStats(user string) (UserStats, error) {
	var st UserStats
	err := s.db.QueryRow(`
		SELECT COALESCE(SUM(done), 0),
		       COALESCE(SUM(NOT done AND page > 1), 0),
		       COALESCE(SUM(CASE WHEN done THEN total ELSE MIN(page, CASE WHEN total > 0 THEN total ELSE page END) END), 0)
		FROM (
			SELECT rp.page AS page, `+totalPagesExpr+` AS total,
			       (`+totalPagesExpr+` > 0 AND rp.page >= `+totalPagesExpr+`) AS done
			FROM issues i JOIN reading_progress rp ON rp.issue_id = i.id AND rp.user = ?
			WHERE i.file_missing = 0
		)`, user).
		Scan(&st.IssuesRead, &st.InProgress, &st.PagesRead)
	if err != nil {
		return UserStats{}, err
	}

	err = s.db.QueryRow(`
		SELECT COALESCE(SUM(opened > 0), 0), COALESCE(SUM(done = present), 0)
		FROM (
			SELECT COUNT(*) AS present,
			       COUNT(rp.issue_id) AS opened,
			       COALESCE(SUM(`+totalPagesExpr+` > 0 AND rp.page >= `+totalPagesExpr+`), 0) AS done
			FROM issues i LEFT JOIN reading_progress rp ON rp.issue_id = i.id AND rp.user = ?
			WHERE i.file_missing = 0
			GROUP BY i.series_id
		)`, user).
		Scan(&st.SeriesStarted, &st.SeriesCompleted)
	if err != nil {
		return UserStats{}, err
	}

	err = s.db.QueryRow(`
		SELECT COUNT(*) FROM issue_favorites f JOIN issues i ON i.id = f.issue_id
		WHERE f.user = ? AND i.file_missing = 0`, user).Scan(&st.Favorites)
	if err != nil {
		return UserStats{}, err
	}

	rows, err := s.db.Query(`
		SELECT s.id, s.name, COALESCE(NULLIF(i.publisher, ''), s.publisher), i.writer
		FROM issues i
		JOIN series s ON s.id = i.series_id
		JOIN reading_progress rp ON rp.issue_id = i.id AND rp.user = ?
		WHERE i.file_missing = 0 AND `+totalPagesExpr+` > 0 AND rp.page >= `+totalPagesExpr,
		user)
	if err != nil {
		return UserStats{}, err
	}
	defer rows.Close()
	series := newCounter()
	publishers := newCounter()
	writers := newCounter()
	for rows.Next() {
		var (
			id                      int64
			name, publisher, writer string
		)
		if err := rows.Scan(&id, &name, &publisher, &writer); err != nil {
			return UserStats{}, err
		}
		series.add(id, name)
		publishers.add(0, publisher)
		seen := make(map[string]bool)
		for _, w := range strings.Split(writer, ",") {
			if w = strings.TrimSpace(w); w != "" && !seen[strings.ToLower(w)] {
				seen[strings.ToLower(w)] = true
				writers.add(0, w)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return UserStats{}, err
	}
	st.TopSeries = series.top(userStatsTopN)
	st.TopPublishers = publishers.top(userStatsTopN)
	st.TopWriters = writers.top(userStatsTopN)
	return st, nil
}

// counter tallies NamedCounts keyed case-insensitively by name (or by ID
// when one is given, so two series sharing a name stay apart).
type counter struct {
	rows  []NamedCount
	index map[string]int
}

func newCounter() *counter { return &counter{index: make(map[string]int)} }

func (c *counter) add(id int64, name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	key := strings.ToLower(name)
	if id != 0 {
		key = strconv.FormatInt(id, 10)
	}
	if i, ok := c.index[key]; ok {
		c.rows[i].Count++
		return
	}
	c.index[key] = len(c.rows)
	c.rows = append(c.rows, NamedCount{ID: id, Name: name, Count: 1})
}

// top returns the n highest counts, ties broken by name.
func (c *counter) top(n int) []NamedCount {
	out := slices.Clone(c.rows)
	slices.SortFunc(out, func(a, b NamedCount) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
