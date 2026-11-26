package store

import (
	"database/sql"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Story arcs: reading orders that span series. An arc comes from ComicInfo.xml
// (StoryArc / StoryArcNumber) and/or ComicVine (an issue's story_arc_credits;
// the arc's full issue list is stored in arc_cv_issues, including issues the
// user does not own, so the page can show the gaps). The two sources are
// merged by name: a ComicInfo arc later seen on ComicVine adopts the ComicVine
// id instead of becoming a duplicate.

// execer is what *sql.DB and *sql.Tx share.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// withTx runs fn in a transaction. With the store's single connection, fn
// must use tx only — never s.db — or it would wait for itself.
func (s *Store) withTx(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Arc is one story arc.
type Arc struct {
	ID           int64
	Name         string
	ComicVineID  sql.NullInt64
	Description  string
	Publisher    string
	ImageURL     string
	ComicVineURL string
	FetchedAt    sql.NullString // when the ComicVine issue list was last fetched (UTC), NULL = never
}

const arcColumns = `id, name, comicvine_id, description, publisher, image_url, comicvine_url, fetched_at`

func scanArc(row interface{ Scan(...any) error }) (*Arc, error) {
	var a Arc
	err := row.Scan(&a.ID, &a.Name, &a.ComicVineID, &a.Description, &a.Publisher, &a.ImageURL,
		&a.ComicVineURL, &a.FetchedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ArcInput names an arc an issue belongs to.
type ArcInput struct {
	Name        string
	ComicVineID int // 0 = unknown (ComicInfo)
	Number      int // position in the arc (ComicInfo StoryArcNumber), 0 = unknown
}

// resolveArc finds or creates the arc for an input and returns its id (0 for
// a nameless input).
func resolveArc(tx execer, in ArcInput) (int64, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return 0, nil
	}
	key := NormalizeName(name)
	var id int64

	if in.ComicVineID > 0 {
		err := tx.QueryRow(`SELECT id FROM arcs WHERE comicvine_id = ?`, in.ComicVineID).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		// A ComicInfo-only arc with the same name becomes this ComicVine arc.
		err = tx.QueryRow(`SELECT id FROM arcs WHERE name_key = ? AND comicvine_id IS NULL ORDER BY id LIMIT 1`, key).Scan(&id)
		if err == nil {
			_, err = tx.Exec(`UPDATE arcs SET comicvine_id = ?, name = ? WHERE id = ?`, in.ComicVineID, name, id)
			return id, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		res, err := tx.Exec(`INSERT INTO arcs (name, name_key, comicvine_id) VALUES (?, ?, ?)`, name, key, in.ComicVineID)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}

	err := tx.QueryRow(`SELECT id FROM arcs WHERE name_key = ? ORDER BY id LIMIT 1`, key).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := tx.Exec(`INSERT INTO arcs (name, name_key) VALUES (?, ?)`, name, key)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// replaceIssueArcs makes the issue's memberships of the given source exactly
// the given arcs and returns their ids.
func replaceIssueArcs(tx execer, issueID int64, source string, arcs []ArcInput) ([]int64, error) {
	if _, err := tx.Exec(`DELETE FROM issue_arcs WHERE issue_id = ? AND source = ?`, issueID, source); err != nil {
		return nil, err
	}
	var ids []int64
	for _, in := range arcs {
		id, err := resolveArc(tx, in)
		if err != nil {
			return nil, err
		}
		if id == 0 {
			continue
		}
		var number any
		if in.Number > 0 {
			number = in.Number
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO issue_arcs (issue_id, arc_id, arc_number, source) VALUES (?, ?, ?, ?)`,
			issueID, id, number, source); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ReplaceComicInfoExtras stores what the issue's ComicInfo.xml says about its
// characters and story arcs (replacing the earlier ComicInfo-sourced rows) and
// marks the file as inspected, so the scan does not re-read it for this.
func (s *Store) ReplaceComicInfoExtras(issueID int64, characters []string, arcs []ArcInput) error {
	inputs := make([]CharacterInput, len(characters))
	for i, c := range characters {
		inputs[i] = CharacterInput{Name: c}
	}
	return s.withTx(func(tx *sql.Tx) error {
		if err := replaceIssueCharacters(tx, issueID, SourceComicInfo, inputs); err != nil {
			return err
		}
		if _, err := replaceIssueArcs(tx, issueID, SourceComicInfo, arcs); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE issues SET comicinfo_extras = 1 WHERE id = ?`, issueID)
		return err
	})
}

// ReplaceComicVineExtras stores the issue's ComicVine character and story arc
// credits (replacing earlier ComicVine-sourced rows), stamps cv_extras_at and
// returns the ids of the issue's ComicVine arcs.
func (s *Store) ReplaceComicVineExtras(issueID int64, characters []CharacterInput, arcs []ArcInput) ([]int64, error) {
	var ids []int64
	err := s.withTx(func(tx *sql.Tx) error {
		if err := replaceIssueCharacters(tx, issueID, SourceComicVine, characters); err != nil {
			return err
		}
		var err error
		if ids, err = replaceIssueArcs(tx, issueID, SourceComicVine, arcs); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE issues SET cv_extras_at = datetime('now') WHERE id = ?`, issueID)
		return err
	})
	return ids, err
}

// ClearComicVineExtras forgets an issue's ComicVine-sourced characters and
// arcs (its ComicVine match was removed); ComicInfo-sourced ones stay.
func (s *Store) ClearComicVineExtras(issueID int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM issue_characters WHERE issue_id = ? AND source = 'comicvine'`,
			`DELETE FROM issue_arcs WHERE issue_id = ? AND source = 'comicvine'`,
			`UPDATE issues SET cv_extras_at = NULL WHERE id = ?`,
		} {
			if _, err := tx.Exec(q, issueID); err != nil {
				return err
			}
		}
		return nil
	})
}

// ComicInfoExtrasPending reports whether the issue's ComicInfo characters/arcs
// were never read (rows from before the feature, or an archive that could not
// be opened last time) — the scan then inspects the file.
func (s *Store) ComicInfoExtrasPending(issueID int64) (bool, error) {
	var read sql.NullInt64
	err := s.db.QueryRow(`SELECT comicinfo_extras FROM issues WHERE id = ?`, issueID).Scan(&read)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return !read.Valid, err
}

// ForgetComicInfoExtras puts an issue back to "ComicInfo characters/arcs not
// read yet" (the state of rows from before the feature), so the next scan
// reads the file again. The stored rows stay until then.
func (s *Store) ForgetComicInfoExtras(issueID int64) error {
	_, err := s.db.Exec(`UPDATE issues SET comicinfo_extras = NULL WHERE id = ?`, issueID)
	return err
}

// CVExtrasIssue is an issue whose ComicVine credits still have to be fetched.
type CVExtrasIssue struct {
	ID               int64
	ComicVineIssueID int64
	Path             string
}

// IssuesNeedingCVExtras returns present-on-disk issues that are matched to a
// ComicVine issue but whose characters/story arcs were never fetched — those
// scraped before this feature existed. Locked issues are included on purpose:
// credits are relational extras, not the editable metadata fields the lock
// protects.
func (s *Store) IssuesNeedingCVExtras() ([]CVExtrasIssue, error) {
	rows, err := s.db.Query(`
		SELECT id, comicvine_issue_id, path FROM issues
		WHERE comicvine_issue_id IS NOT NULL AND cv_extras_at IS NULL AND file_missing = 0
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CVExtrasIssue
	for rows.Next() {
		var i CVExtrasIssue
		if err := rows.Scan(&i.ID, &i.ComicVineIssueID, &i.Path); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// GetArc returns the arc by id, or nil.
func (s *Store) GetArc(id int64) (*Arc, error) {
	a, err := scanArc(s.db.QueryRow(`SELECT `+arcColumns+` FROM arcs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

// StaleArcs returns the ComicVine arcs that have catalogued members and whose
// issue list was never fetched or was fetched more than olderThanDays ago.
func (s *Store) StaleArcs(olderThanDays int) ([]Arc, error) {
	rows, err := s.db.Query(`
		SELECT `+arcColumns+` FROM arcs
		WHERE comicvine_id IS NOT NULL
		  AND (fetched_at IS NULL OR fetched_at < datetime('now', ?))
		  AND id IN (SELECT arc_id FROM issue_arcs)
		ORDER BY id`, "-"+strconv.Itoa(olderThanDays)+" days")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Arc
	for rows.Next() {
		a, err := scanArc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ArcDetails is what a ComicVine arc fetch says about the arc itself.
type ArcDetails struct {
	Name        string
	Description string
	Publisher   string
	ImageURL    string
	URL         string
}

// ArcCVIssue is one entry of an arc's ComicVine issue list.
type ArcCVIssue struct {
	ComicVineIssueID int64
	VolumeID         int64
	VolumeName       string
	IssueNumber      string
	Name             string
	CoverDate        string // "2006-01-02" or ""
}

// OrderArcCVIssues puts a fetched arc's issues in reading order. ComicVine's
// own order is kept — unless it is plain ascending issue-id order (database
// order, carrying no editorial information) and every issue has a cover date,
// in which case the arc is ordered by cover date (stable, so ties keep
// ComicVine's order).
func OrderArcCVIssues(in []ArcCVIssue) []ArcCVIssue {
	out := slices.Clone(in)
	if len(out) < 2 {
		return out
	}
	ascending, dated := true, true
	for i, is := range out {
		if i > 0 && is.ComicVineIssueID < out[i-1].ComicVineIssueID {
			ascending = false
		}
		if is.CoverDate == "" {
			dated = false
		}
	}
	if ascending && dated {
		slices.SortStableFunc(out, func(a, b ArcCVIssue) int { return strings.Compare(a.CoverDate, b.CoverDate) })
	}
	return out
}

// SaveArcCV stores a fetched ComicVine arc: its details and its issue list
// (positions follow the given order), stamping fetched_at.
func (s *Store) SaveArcCV(arcID int64, d ArcDetails, issues []ArcCVIssue) error {
	return s.withTx(func(tx *sql.Tx) error {
		if d.Name = strings.TrimSpace(d.Name); d.Name != "" {
			if _, err := tx.Exec(`UPDATE arcs SET name = ?, name_key = ? WHERE id = ?`, d.Name, NormalizeName(d.Name), arcID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`
			UPDATE arcs SET description = ?, publisher = ?, image_url = ?, comicvine_url = ?,
				fetched_at = datetime('now')
			WHERE id = ?`, d.Description, d.Publisher, d.ImageURL, d.URL, arcID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM arc_cv_issues WHERE arc_id = ?`, arcID); err != nil {
			return err
		}
		for pos, is := range issues {
			if _, err := tx.Exec(`
				INSERT OR REPLACE INTO arc_cv_issues (arc_id, comicvine_issue_id, position, volume_id,
					volume_name, issue_number, name, cover_date)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				arcID, is.ComicVineIssueID, pos+1, is.VolumeID, is.VolumeName, is.IssueNumber, is.Name, is.CoverDate); err != nil {
				return err
			}
		}
		return nil
	})
}

// ArcEntry is one place in an arc's reading order: an issue the library has,
// or a gap (Owned == false) known only from ComicVine.
type ArcEntry struct {
	Position int // 1-based
	Owned    bool
	Issue    *IssueWithSeries // set when Owned
	// ComicVine's description of the entry (empty for ComicInfo-only entries).
	CVIssueID   int64
	VolumeName  string
	IssueNumber string
	Name        string
	CoverDate   string
}

func (s *Store) arcCVRows(arcID int64) ([]ArcCVIssue, error) {
	rows, err := s.db.Query(`
		SELECT comicvine_issue_id, volume_id, volume_name, issue_number, name, cover_date
		FROM arc_cv_issues WHERE arc_id = ? ORDER BY position`, arcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArcCVIssue
	for rows.Next() {
		var c ArcCVIssue
		if err := rows.Scan(&c.ComicVineIssueID, &c.VolumeID, &c.VolumeName, &c.IssueNumber, &c.Name, &c.CoverDate); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// arcMember is a present issue recorded as a member of an arc.
type arcMember struct {
	IssueWithSeries
	Number int // StoryArcNumber, 0 = none
}

func (s *Store) arcMembers(arcID int64) ([]arcMember, error) {
	rows, err := s.db.Query(`
		SELECT `+issueWithSeriesColumns+`,
		       COALESCE((SELECT MIN(x.arc_number) FROM issue_arcs x WHERE x.arc_id = ? AND x.issue_id = i.id), 0)
		FROM issues i JOIN series s ON s.id = i.series_id
		WHERE i.file_missing = 0 AND i.id IN (SELECT issue_id FROM issue_arcs WHERE arc_id = ?)`,
		arcID, arcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []arcMember
	for rows.Next() {
		var m arcMember
		if err := rows.Scan(append(m.fields(), &m.SeriesName, &m.Number)...); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// presentIssuesByComicVineID returns the present-on-disk issues matched to any
// of the given ComicVine issue ids, lowest local id first.
func (s *Store) presentIssuesByComicVineID(ids []int64) ([]IssueWithSeries, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`
		SELECT `+issueWithSeriesColumns+`
		FROM issues i JOIN series s ON s.id = i.series_id
		WHERE i.file_missing = 0 AND i.comicvine_issue_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)
		ORDER BY i.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssuesWithSeries(rows)
}

// issueNumberValue is an issue number's numeric value for ordering (unparseable
// numbers sort last).
func issueNumberValue(n string) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
		return v
	}
	return 1e18
}

// ArcEntries returns an arc's reading order. With a fetched ComicVine issue
// list that is the list itself (owned issues matched by ComicVine issue id,
// the rest shown as gaps), followed by members the list does not know — files
// tagged with the arc in ComicInfo but not matched to ComicVine. Without a
// list the order is the ComicInfo one: StoryArcNumber first, then release
// date, then series name and issue number.
func (s *Store) ArcEntries(arcID int64) ([]ArcEntry, error) {
	cv, err := s.arcCVRows(arcID)
	if err != nil {
		return nil, err
	}
	members, err := s.arcMembers(arcID)
	if err != nil {
		return nil, err
	}
	memberNumber := make(map[int64]int, len(members))
	for _, m := range members {
		memberNumber[m.ID] = m.Number
	}

	owners := make(map[int64]*IssueWithSeries)
	if len(cv) > 0 {
		ids := make([]int64, len(cv))
		for i, c := range cv {
			ids[i] = c.ComicVineIssueID
		}
		found, err := s.presentIssuesByComicVineID(ids)
		if err != nil {
			return nil, err
		}
		for i := range found {
			if id := found[i].ComicVineIssueID.Int64; owners[id] == nil {
				owners[id] = &found[i]
			}
		}
	}

	var entries []ArcEntry
	used := make(map[int64]bool)
	for _, c := range cv {
		e := ArcEntry{CVIssueID: c.ComicVineIssueID, VolumeName: c.VolumeName, IssueNumber: c.IssueNumber,
			Name: c.Name, CoverDate: c.CoverDate}
		if is := owners[c.ComicVineIssueID]; is != nil {
			e.Owned, e.Issue = true, is
			used[is.ID] = true
		}
		entries = append(entries, e)
	}

	var rest []arcMember
	for _, m := range members {
		if !used[m.ID] {
			rest = append(rest, m)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool {
		a, b := rest[i], rest[j]
		if (a.Number > 0) != (b.Number > 0) {
			return a.Number > 0
		}
		if a.Number != b.Number {
			return a.Number < b.Number
		}
		if (a.ReleaseDate == "") != (b.ReleaseDate == "") {
			return a.ReleaseDate != ""
		}
		if a.ReleaseDate != b.ReleaseDate {
			return a.ReleaseDate < b.ReleaseDate
		}
		if an, bn := strings.ToLower(a.SeriesName), strings.ToLower(b.SeriesName); an != bn {
			return an < bn
		}
		return issueNumberValue(a.IssueNumber) < issueNumberValue(b.IssueNumber)
	})
	for i := range rest {
		is := rest[i].IssueWithSeries
		entries = append(entries, ArcEntry{Owned: true, Issue: &is, IssueNumber: is.IssueNumber, Name: is.Title,
			CoverDate: is.ReleaseDate})
	}

	for i := range entries {
		entries[i].Position = i + 1
	}
	return entries, nil
}

// ArcPosition places one issue inside an arc it belongs to.
type ArcPosition struct {
	Arc      Arc
	Position int // 1-based; 0 when the issue is not found in the order
	Total    int
	Next     *IssueWithSeries // the next owned issue after this one, nil at the end
}

// arcsOfIssue returns the ids of every arc the issue belongs to: by recorded
// membership or by its ComicVine issue id appearing in an arc's issue list.
func (s *Store) arcsOfIssue(issueID int64) ([]int64, error) {
	rows, err := s.db.Query(`
		SELECT arc_id FROM issue_arcs WHERE issue_id = ?
		UNION
		SELECT c.arc_id FROM arc_cv_issues c JOIN issues i ON i.comicvine_issue_id = c.comicvine_issue_id
		WHERE i.id = ?
		ORDER BY 1`, issueID, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ArcPositionsOfIssue lists the arcs an issue is part of, with its place in
// each reading order and the next owned issue.
func (s *Store) ArcPositionsOfIssue(issueID int64) ([]ArcPosition, error) {
	ids, err := s.arcsOfIssue(issueID)
	if err != nil {
		return nil, err
	}
	var out []ArcPosition
	for _, id := range ids {
		arc, err := s.GetArc(id)
		if err != nil {
			return nil, err
		}
		if arc == nil {
			continue
		}
		entries, err := s.ArcEntries(id)
		if err != nil {
			return nil, err
		}
		p := ArcPosition{Arc: *arc, Total: len(entries)}
		for i, e := range entries {
			if e.Owned && e.Issue.ID == issueID {
				p.Position = e.Position
				for _, n := range entries[i+1:] {
					if n.Owned {
						p.Next = n.Issue
						break
					}
				}
				break
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// ArcSummary is one row of the arcs index.
type ArcSummary struct {
	Arc
	Owned        int // present issues in the arc
	Total        int // entries in the reading order, owned or not
	Read         int // owned issues the user has finished
	CoverIssueID int64
}

// readExpr is true for an issue the user has read to the end (joined as
// `issues i LEFT JOIN reading_progress rp`).
const readExpr = `COALESCE(` + totalPagesExpr + ` > 0 AND rp.page >= ` + totalPagesExpr + `, 0)`

// ListArcs returns every arc with at least one owned issue, by name, with the
// user's reading counts. Three queries in all: the arcs, ComicVine entry counts,
// and every (arc, owned issue) pair.
func (s *Store) ListArcs(user string) ([]ArcSummary, error) {
	rows, err := s.db.Query(`SELECT ` + arcColumns + ` FROM arcs ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	var arcs []ArcSummary
	for rows.Next() {
		a, err := scanArc(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		arcs = append(arcs, ArcSummary{Arc: *a})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	index := make(map[int64]int, len(arcs))
	for i, a := range arcs {
		index[a.ID] = i
	}

	cvCounts, err := s.db.Query(`SELECT arc_id, COUNT(*) FROM arc_cv_issues GROUP BY arc_id`)
	if err != nil {
		return nil, err
	}
	for cvCounts.Next() {
		var id int64
		var n int
		if err := cvCounts.Scan(&id, &n); err != nil {
			cvCounts.Close()
			return nil, err
		}
		if i, ok := index[id]; ok {
			arcs[i].Total = n
		}
	}
	if err := cvCounts.Err(); err != nil {
		cvCounts.Close()
		return nil, err
	}
	cvCounts.Close()

	type tally struct {
		cvSeen   map[int64]bool // ComicVine issue ids matched to an owned issue
		issues   map[int64]bool // owned issue ids already counted
		coverPos int
	}
	tallies := make(map[int64]*tally, len(arcs))
	count := func(arcID, issueID, cvID int64, pos int, read bool) {
		i, ok := index[arcID]
		if !ok {
			return
		}
		t := tallies[arcID]
		if t == nil {
			t = &tally{cvSeen: map[int64]bool{}, issues: map[int64]bool{}, coverPos: 1 << 30}
			tallies[arcID] = t
		}
		if t.issues[issueID] || (cvID != 0 && t.cvSeen[cvID]) {
			return
		}
		t.issues[issueID] = true
		if cvID != 0 {
			t.cvSeen[cvID] = true
		}
		arcs[i].Owned++
		if read {
			arcs[i].Read++
		}
		if pos < t.coverPos {
			t.coverPos = pos
			arcs[i].CoverIssueID = issueID
		}
	}

	// Owned issues matched through ComicVine's issue lists first, so a member
	// that is also in the list is not counted twice.
	pairs, err := s.db.Query(`
		SELECT c.arc_id, i.id, c.comicvine_issue_id, c.position, `+readExpr+`
		FROM arc_cv_issues c
		JOIN issues i ON i.comicvine_issue_id = c.comicvine_issue_id AND i.file_missing = 0
		LEFT JOIN reading_progress rp ON rp.issue_id = i.id AND rp.user = ?
		ORDER BY c.arc_id, c.position, i.id`, user)
	if err != nil {
		return nil, err
	}
	for pairs.Next() {
		var arcID, issueID, cvID int64
		var pos int
		var read bool
		if err := pairs.Scan(&arcID, &issueID, &cvID, &pos, &read); err != nil {
			pairs.Close()
			return nil, err
		}
		count(arcID, issueID, cvID, pos, read)
	}
	if err := pairs.Err(); err != nil {
		pairs.Close()
		return nil, err
	}
	pairs.Close()

	members, err := s.db.Query(`
		SELECT ia.arc_id, i.id, COALESCE(MIN(ia.arc_number), 0), `+readExpr+`
		FROM issue_arcs ia
		JOIN issues i ON i.id = ia.issue_id AND i.file_missing = 0
		LEFT JOIN reading_progress rp ON rp.issue_id = i.id AND rp.user = ?
		GROUP BY ia.arc_id, i.id
		ORDER BY ia.arc_id, 3, i.id`, user)
	if err != nil {
		return nil, err
	}
	for members.Next() {
		var arcID, issueID int64
		var num int
		var read bool
		if err := members.Scan(&arcID, &issueID, &num, &read); err != nil {
			members.Close()
			return nil, err
		}
		// Members the ComicVine list does not cover count as extra entries
		// placed after it; the already-matched ones were counted above.
		count(arcID, issueID, 0, 1<<20+num, read)
	}
	if err := members.Err(); err != nil {
		members.Close()
		return nil, err
	}
	members.Close()

	out := arcs[:0]
	for _, a := range arcs {
		if a.Owned == 0 {
			continue
		}
		// Owned issues the ComicVine list does not cover (ComicInfo-only
		// members) lengthen the order beyond it.
		a.Total += a.Owned - len(tallies[a.ID].cvSeen)
		out = append(out, a)
	}
	return out, nil
}
