package store

import (
	"database/sql"
	"errors"
)

// Metadata source ranks, lowest to highest priority. A scan may overwrite
// metadata whose source rank is lower than or equal to the incoming one,
// unless the issue is locked (see docs/SPECIFICATION.md §5).
const (
	SourceFilename  = "filename"
	SourceComicInfo = "comicinfo"
	SourceComicVine = "comicvine"
	SourceManual    = "manual"
)

var sourceRank = map[string]int{
	SourceFilename:  0,
	SourceComicInfo: 1,
	SourceComicVine: 2,
	SourceManual:    3,
}

// SourceRank returns the priority of a metadata source (unknown = -1).
func SourceRank(source string) int {
	if r, ok := sourceRank[source]; ok {
		return r
	}
	return -1
}

// Issue is a single comic file row.
type Issue struct {
	ID               int64
	SeriesID         int64
	Path             string
	FileSize         int64
	FileMissing      bool
	IssueNumber      string
	Title            string
	Summary          string
	ReleaseDate      string
	Writer           string
	Artist           string
	Publisher        string
	PageCount        int // from metadata (ComicInfo), may be 0 or wrong
	FilePages        int // image entries actually inside the archive (0 = not counted yet)
	ComicVineIssueID sql.NullInt64
	MetadataSource   string
	MetadataLocked   bool
	HasComicInfo     bool
	// ComicInfoSeries is the raw <Series> of the file's ComicInfo.xml: invalid
	// (NULL) = never inspected, "" = none. Drives the mixed-folder split.
	ComicInfoSeries sql.NullString
	CoverCached     bool
	CreatedAt       string
	UpdatedAt       string
}

const issueColumns = `id, series_id, path, file_size, file_missing, issue_number,
	title, summary, release_date, writer, artist, publisher, page_count, file_pages,
	comicvine_issue_id, metadata_source, metadata_locked, has_comicinfo,
	comicinfo_series, cover_cached, created_at, updated_at`

// fields returns scan targets for issueColumns, in order.
func (i *Issue) fields() []any {
	return []any{&i.ID, &i.SeriesID, &i.Path, &i.FileSize, &i.FileMissing,
		&i.IssueNumber, &i.Title, &i.Summary, &i.ReleaseDate, &i.Writer,
		&i.Artist, &i.Publisher, &i.PageCount, &i.FilePages, &i.ComicVineIssueID,
		&i.MetadataSource, &i.MetadataLocked, &i.HasComicInfo, &i.ComicInfoSeries,
		&i.CoverCached, &i.CreatedAt, &i.UpdatedAt}
}

func scanIssue(row interface{ Scan(...any) error }) (*Issue, error) {
	var i Issue
	if err := row.Scan(i.fields()...); err != nil {
		return nil, err
	}
	return &i, nil
}

// TotalPages is the page count to trust for reading: the archive's actual
// image count when known, otherwise the metadata figure.
func (i *Issue) TotalPages() int {
	if i.FilePages > 0 {
		return i.FilePages
	}
	return i.PageCount
}

// IssueFilter narrows issue lists in the UI.
type IssueFilter string

const (
	IssueFilterAll       IssueFilter = "all"
	IssueFilterNoMeta    IssueFilter = "nometa"    // only filename-derived metadata
	IssueFilterComicVine IssueFilter = "comicvine" // scraped from ComicVine
	IssueFilterMissing   IssueFilter = "missing"   // file gone from disk
)

func (f IssueFilter) where() string {
	switch f {
	case IssueFilterNoMeta:
		return `AND metadata_source = 'filename'`
	case IssueFilterComicVine:
		return `AND metadata_source = 'comicvine'`
	case IssueFilterMissing:
		return `AND file_missing = 1`
	}
	return ""
}

// ListIssuesBySeries returns a series' issues sorted by issue number
// (numerically where possible, unnumbered entries last).
func (s *Store) ListIssuesBySeries(seriesID int64, filter IssueFilter) ([]Issue, error) {
	rows, err := s.db.Query(`
		SELECT `+issueColumns+` FROM issues
		WHERE series_id = ? `+filter.where()+`
		ORDER BY issue_number = '', CAST(issue_number AS REAL), issue_number, title`,
		seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

// IssueWithSeries is an issue joined with its series name, for search results.
type IssueWithSeries struct {
	Issue
	SeriesName string
}

// issueWithSeriesColumns is the select list for IssueWithSeries queries over
// `issues i JOIN series s`.
const issueWithSeriesColumns = `i.id, i.series_id, i.path, i.file_size, i.file_missing,
	i.issue_number, i.title, i.summary, i.release_date, i.writer, i.artist, i.publisher,
	i.page_count, i.file_pages, i.comicvine_issue_id, i.metadata_source, i.metadata_locked,
	i.has_comicinfo, i.comicinfo_series, i.cover_cached, i.created_at, i.updated_at, s.name`

func scanIssuesWithSeries(rows *sql.Rows) ([]IssueWithSeries, error) {
	var out []IssueWithSeries
	for rows.Next() {
		var i IssueWithSeries
		if err := rows.Scan(append(i.fields(), &i.SeriesName)...); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// SearchIssues finds issues whose title or number matches the query.
func (s *Store) SearchIssues(q string) ([]IssueWithSeries, error) {
	rows, err := s.db.Query(`
		SELECT `+issueWithSeriesColumns+`
		FROM issues i JOIN series s ON s.id = i.series_id
		WHERE i.title LIKE '%' || ? || '%' OR i.issue_number LIKE '%' || ? || '%'
		ORDER BY s.name COLLATE NOCASE, CAST(i.issue_number AS REAL)
		LIMIT 100`, q, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssuesWithSeries(rows)
}

// SearchIssuesBroad finds present-on-disk issues whose series name, title or
// number matches the query — one flat result list for clients (OPDS readers)
// that cannot show series and issues separately.
func (s *Store) SearchIssuesBroad(q string, limit int) ([]IssueWithSeries, error) {
	rows, err := s.db.Query(`
		SELECT `+issueWithSeriesColumns+`
		FROM issues i JOIN series s ON s.id = i.series_id
		WHERE i.file_missing = 0
		  AND (s.name LIKE '%' || ? || '%'
		       OR i.title LIKE '%' || ? || '%'
		       OR i.issue_number LIKE '%' || ? || '%')
		ORDER BY s.name COLLATE NOCASE, i.issue_number = '',
			CAST(i.issue_number AS REAL), i.issue_number, i.title
		LIMIT ?`, q, q, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssuesWithSeries(rows)
}

// ListRecentIssues returns present-on-disk issues, newest first (by the time
// the scanner added them), for "recently added" views.
func (s *Store) ListRecentIssues(limit, offset int) ([]IssueWithSeries, error) {
	rows, err := s.db.Query(`
		SELECT `+issueWithSeriesColumns+`
		FROM issues i JOIN series s ON s.id = i.series_id
		WHERE i.file_missing = 0
		ORDER BY i.created_at DESC, i.id DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssuesWithSeries(rows)
}

// CountIssues returns the number of issues present on disk.
func (s *Store) CountIssues() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM issues WHERE file_missing = 0`).Scan(&n)
	return n, err
}

// IssuesNeedingComicVine returns unlocked, present-on-disk issues that lack
// ComicVine metadata but belong to a series matched to a ComicVine volume,
// grouped by that volume id.
func (s *Store) IssuesNeedingComicVine() (map[int64][]Issue, error) {
	rows, err := s.db.Query(`
		SELECT i.id, i.series_id, i.path, i.file_size, i.file_missing, i.issue_number,
			i.title, i.summary, i.release_date, i.writer, i.artist, i.publisher,
			i.page_count, i.file_pages, i.comicvine_issue_id, i.metadata_source,
			i.metadata_locked, i.has_comicinfo, i.comicinfo_series, i.cover_cached,
			i.created_at, i.updated_at, s.comicvine_volume_id
		FROM issues i JOIN series s ON s.id = i.series_id
		WHERE s.comicvine_volume_id IS NOT NULL
		  AND i.metadata_locked = 0
		  AND i.metadata_source != 'comicvine'
		  AND i.file_missing = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64][]Issue)
	for rows.Next() {
		var i Issue
		var volumeID int64
		if err := rows.Scan(append(i.fields(), &volumeID)...); err != nil {
			return nil, err
		}
		out[volumeID] = append(out[volumeID], i)
	}
	return out, rows.Err()
}

// GetIssue returns the issue by id, or nil when it does not exist.
func (s *Store) GetIssue(id int64) (*Issue, error) {
	i, err := scanIssue(s.db.QueryRow(`SELECT `+issueColumns+` FROM issues WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return i, err
}

// GetIssueByPath returns the issue with the given file path, or nil.
func (s *Store) GetIssueByPath(path string) (*Issue, error) {
	i, err := scanIssue(s.db.QueryRow(`SELECT `+issueColumns+` FROM issues WHERE path = ?`, path))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return i, err
}

// InsertIssue inserts a new issue and fills in its ID.
func (s *Store) InsertIssue(i *Issue) error {
	res, err := s.db.Exec(`
		INSERT INTO issues (series_id, path, file_size, issue_number, title,
			summary, release_date, writer, artist, publisher, page_count, file_pages,
			metadata_source, has_comicinfo, comicinfo_series)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		i.SeriesID, i.Path, i.FileSize, i.IssueNumber, i.Title,
		i.Summary, i.ReleaseDate, i.Writer, i.Artist, i.Publisher, i.PageCount, i.FilePages,
		i.MetadataSource, i.HasComicInfo, i.ComicInfoSeries)
	if err != nil {
		return err
	}
	i.ID, err = res.LastInsertId()
	return err
}

// UpdateIssueMetadata rewrites the metadata fields of an existing issue.
func (s *Store) UpdateIssueMetadata(i *Issue) error {
	_, err := s.db.Exec(`
		UPDATE issues SET issue_number = ?, title = ?, summary = ?,
			release_date = ?, writer = ?, artist = ?, publisher = ?,
			page_count = ?, comicvine_issue_id = ?, metadata_source = ?,
			metadata_locked = ?, has_comicinfo = ?, updated_at = datetime('now')
		WHERE id = ?`,
		i.IssueNumber, i.Title, i.Summary, i.ReleaseDate, i.Writer, i.Artist,
		i.Publisher, i.PageCount, i.ComicVineIssueID, i.MetadataSource,
		i.MetadataLocked, i.HasComicInfo, i.ID)
	return err
}

// ClearIssueComicVine undoes a single issue's ComicVine match: the matched
// id is forgotten and the metadata source falls back to what the file
// itself carries (ComicInfo.xml when present, else the filename), so a
// future scan or scrape can pick the issue up again. Fields ComicVine wrote
// (title, summary, credits...) are left as they are — only the "this came
// from ComicVine" tag and id are removed, mirroring how unlocking keeps data
// while lifting the write-protection.
func (s *Store) ClearIssueComicVine(id int64) error {
	_, err := s.db.Exec(`
		UPDATE issues SET comicvine_issue_id = NULL,
			metadata_source = CASE WHEN has_comicinfo THEN ? ELSE ? END,
			updated_at = datetime('now')
		WHERE id = ? AND metadata_source = ?`,
		SourceComicInfo, SourceFilename, id, SourceComicVine)
	return err
}

// SetIssueLocked toggles the issue metadata lock. Unlocking keeps the manual
// data but lets a future explicit scrape overwrite it.
func (s *Store) SetIssueLocked(id int64, locked bool) error {
	_, err := s.db.Exec(`
		UPDATE issues SET metadata_locked = ?, updated_at = datetime('now')
		WHERE id = ?`, locked, id)
	return err
}

// TouchIssueFile refreshes file facts after a scan saw the file on disk.
func (s *Store) TouchIssueFile(id int64, size int64) error {
	_, err := s.db.Exec(`
		UPDATE issues SET file_size = ?, file_missing = 0, updated_at = datetime('now')
		WHERE id = ?`, size, id)
	return err
}

// SetIssueFilePages records the number of image pages found in the archive.
func (s *Store) SetIssueFilePages(id int64, pages int) error {
	_, err := s.db.Exec(`UPDATE issues SET file_pages = ? WHERE id = ?`, pages, id)
	return err
}

// SetIssueComicInfoSeries records the raw ComicInfo <Series> value ("" when
// the file has none), marking the file as inspected.
func (s *Store) SetIssueComicInfoSeries(id int64, series string) error {
	_, err := s.db.Exec(`UPDATE issues SET comicinfo_series = ? WHERE id = ?`, series, id)
	return err
}

// SetIssueSeries moves an issue to another series (used by the scanner when a
// folder turns out to hold several series).
func (s *Store) SetIssueSeries(issueID, seriesID int64) error {
	_, err := s.db.Exec(`
		UPDATE issues SET series_id = ?, updated_at = datetime('now')
		WHERE id = ?`, seriesID, issueID)
	return err
}

// IssueSeriesRef is the minimal view the scanner needs to regroup issues.
type IssueSeriesRef struct {
	ID              int64
	Path            string
	SeriesID        int64
	SeriesFolder    sql.NullString // series.folder_path (NULL = virtual series)
	ComicInfoSeries sql.NullString
}

// ListIssueSeriesRefs returns every present-on-disk issue with its current
// series (and that series' folder) and ComicInfo series value.
func (s *Store) ListIssueSeriesRefs() ([]IssueSeriesRef, error) {
	rows, err := s.db.Query(`
		SELECT i.id, i.path, i.series_id, s.folder_path, i.comicinfo_series
		FROM issues i JOIN series s ON s.id = i.series_id
		WHERE i.file_missing = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []IssueSeriesRef
	for rows.Next() {
		var r IssueSeriesRef
		if err := rows.Scan(&r.ID, &r.Path, &r.SeriesID, &r.SeriesFolder, &r.ComicInfoSeries); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetCoverCached records whether a thumbnail exists for the issue.
func (s *Store) SetCoverCached(id int64, cached bool) error {
	_, err := s.db.Exec(`UPDATE issues SET cover_cached = ? WHERE id = ?`, cached, id)
	return err
}

// DeleteIssue removes an issue record permanently (used for records whose
// file disappeared from the library). Empty series vanish from lists on
// their own (list queries join on issues).
func (s *Store) DeleteIssue(id int64) error {
	_, err := s.db.Exec(`DELETE FROM issues WHERE id = ?`, id)
	return err
}

// AllIssuePaths returns path → id for every issue, used by the scanner to
// detect files that disappeared from the library.
func (s *Store) AllIssuePaths() (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT path, id FROM issues`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var path string
		var id int64
		if err := rows.Scan(&path, &id); err != nil {
			return nil, err
		}
		out[path] = id
	}
	return out, rows.Err()
}

// AllIssueIDs returns the set of existing issue ids (used to garbage-collect
// orphaned cover thumbnails).
func (s *Store) AllIssueIDs() (map[int64]bool, error) {
	rows, err := s.db.Query(`SELECT id FROM issues`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// MarkIssuesMissing flags the given issues as missing from disk.
func (s *Store) MarkIssuesMissing(ids []int64) error {
	for _, id := range ids {
		if _, err := s.db.Exec(`
			UPDATE issues SET file_missing = 1, updated_at = datetime('now')
			WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}
