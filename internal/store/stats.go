package store

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
