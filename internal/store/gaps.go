package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Gap detection ("what am I missing?").
//
// The rule, in one place: only plain integer issue numbers take part. "055"
// and "55" are the same issue; "1.5", "12a", "Annual 1", "½" or an empty
// number never create a fake gap and never fill one. Issues flagged
// file_missing do not count as owned, but are reported as "vanished" (we used
// to have them) rather than "never owned".
//
//   - Series matched to a ComicVine volume whose issue list is cached
//     (cv_volume_issues) are compared against that list: every integer-numbered
//     ComicVine issue that has already been released (store date, else cover
//     date, not in the future) and is not on disk is missing.
//   - Other series are inferred: between the lowest and the highest integer
//     number owned, every number not owned is missing. Numbers below the lowest
//     owned one are unknowable and never reported. One-shots, series whose
//     numbers are mostly not integers (< 80% of the issues), and series so
//     sparse that the "range" is probably not issue numbering (more than
//     2*owned+20 missing — years, volume-relative numbering) are skipped.

// GapSource says where a series' expected issue list came from.
type GapSource string

const (
	GapSourceComicVine GapSource = "comicvine"
	GapSourceInferred  GapSource = "inferred"
)

// OwnedIssue is the part of an issue row gap detection needs.
type OwnedIssue struct {
	Number      string
	FileMissing bool
}

// CVIssue is one cached ComicVine issue of a volume.
type CVIssue struct {
	VolumeID    int64
	CVIssueID   int64
	IssueNumber string
	Name        string
	CoverDate   string
	StoreDate   string
	URL         string
}

// Date is the date to show/compare: the store (on-sale) date, else the cover date.
func (c CVIssue) Date() string {
	if c.StoreDate != "" {
		return c.StoreDate
	}
	return c.CoverDate
}

// MissingIssue is one issue number the user does not have.
type MissingIssue struct {
	Number   int
	Name     string // ComicVine title, "" when inferred
	Date     string // ComicVine store/cover date, "" when inferred
	Vanished bool   // true: a catalog record exists but its file disappeared
}

// Gaps is the result of gap detection for one series.
type Gaps struct {
	Source  GapSource
	Owned   int // expected issues present on disk
	Total   int // expected issues (owned + missing)
	Missing []MissingIssue
}

// MissingCount is the number of missing issues.
func (g *Gaps) MissingCount() int { return len(g.Missing) }

// VanishedCount is how many missing issues have a record whose file is gone.
func (g *Gaps) VanishedCount() int {
	n := 0
	for _, m := range g.Missing {
		if m.Vanished {
			n++
		}
	}
	return n
}

// RangeText renders the missing numbers compactly: "#12–14, #20".
func (g *Gaps) RangeText() string { return FormatRanges(g.numbers(false), "–") }

// RangeTextASCII is RangeText with a plain hyphen, for the text export.
func (g *Gaps) RangeTextASCII() string { return FormatRanges(g.numbers(false), "-") }

// VanishedText renders only the numbers whose file disappeared.
func (g *Gaps) VanishedText() string { return FormatRanges(g.numbers(true), "–") }

// FromComicVine reports whether the expected list came from ComicVine.
func (g *Gaps) FromComicVine() bool { return g.Source == GapSourceComicVine }

func (g *Gaps) numbers(vanishedOnly bool) []int {
	var out []int
	for _, m := range g.Missing {
		if !vanishedOnly || m.Vanished {
			out = append(out, m.Number)
		}
	}
	return out
}

// ParseIssueInt parses a plain integer issue number ("55", "055", "#12").
// Anything else — decimals, letters, annuals, empty — is not an integer
// number and returns ok = false.
func ParseIssueInt(s string) (int, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	if s == "" || len(s) > 6 { // 6 digits: no real series has 1,000,000 issues
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// FormatRanges renders sorted-or-not integers as "#12–14, #20" (sep joins the
// ends of a run; a run of two is "#12, #13").
func FormatRanges(nums []int, sep string) string {
	if len(nums) == 0 {
		return ""
	}
	sorted := append([]int(nil), nums...)
	sort.Ints(sorted)
	var parts []string
	flush := func(from, to int) {
		switch {
		case from == to:
			parts = append(parts, fmt.Sprintf("#%d", from))
		case to == from+1:
			parts = append(parts, fmt.Sprintf("#%d, #%d", from, to))
		default:
			parts = append(parts, fmt.Sprintf("#%d%s%d", from, sep, to))
		}
	}
	from, prev := sorted[0], sorted[0]
	for _, n := range sorted[1:] {
		if n == prev {
			continue
		}
		if n != prev+1 {
			flush(from, prev)
			from = n
		}
		prev = n
	}
	flush(from, prev)
	return strings.Join(parts, ", ")
}

// ownedState indexes owned issues by integer number: true = present on disk,
// false = only vanished records.
func ownedState(owned []OwnedIssue) map[int]bool {
	state := make(map[int]bool)
	for _, o := range owned {
		n, ok := ParseIssueInt(o.Number)
		if !ok {
			continue
		}
		if present, seen := state[n]; !seen || (!present && !o.FileMissing) {
			state[n] = !o.FileMissing
		}
	}
	return state
}

// ComputeCVGaps compares the on-disk issues with a ComicVine volume's issue
// list. today is "YYYY-MM-DD": issues dated after it (upcoming) are not
// missing yet. Returns nil when the volume list holds no integer-numbered,
// released issue (nothing to compare against).
func ComputeCVGaps(cv []CVIssue, owned []OwnedIssue, today string) *Gaps {
	state := ownedState(owned)
	type exp struct {
		name, date string
	}
	expected := make(map[int]exp)
	for _, c := range cv {
		n, ok := ParseIssueInt(c.IssueNumber)
		if !ok {
			continue
		}
		if d := c.Date(); d != "" && d > today {
			continue
		}
		// Duplicate numbers in a volume (reprints, variants): keep the first
		// with a name or date, deterministic by arrival order.
		if prev, dup := expected[n]; dup && (prev.name != "" || prev.date != "") {
			continue
		}
		expected[n] = exp{name: c.Name, date: c.Date()}
	}
	if len(expected) == 0 {
		return nil
	}
	g := &Gaps{Source: GapSourceComicVine, Total: len(expected)}
	nums := make([]int, 0, len(expected))
	for n := range expected {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	for _, n := range nums {
		present, seen := state[n]
		if present {
			g.Owned++
			continue
		}
		g.Missing = append(g.Missing, MissingIssue{Number: n, Name: expected[n].name, Date: expected[n].date, Vanished: seen})
	}
	return g
}

// ComputeInferredGaps infers gaps from the integer numbers alone (see the
// package comment above for the rule). Returns nil when the series is not a
// candidate.
func ComputeInferredGaps(owned []OwnedIssue) *Gaps {
	if len(owned) == 0 {
		return nil
	}
	state := ownedState(owned)
	ints := 0
	for _, o := range owned {
		if _, ok := ParseIssueInt(o.Number); ok {
			ints++
		}
	}
	if len(state) < 2 || ints*5 < len(owned)*4 { // < 80% integer numbers
		return nil
	}
	lo, hi := -1, -1
	for n := range state {
		if lo == -1 || n < lo {
			lo = n
		}
		if n > hi {
			hi = n
		}
	}
	g := &Gaps{Source: GapSourceInferred, Total: hi - lo + 1}
	if g.Total > 5000 {
		return nil
	}
	for n := lo; n <= hi; n++ {
		present, seen := state[n]
		if present {
			g.Owned++
		} else {
			g.Missing = append(g.Missing, MissingIssue{Number: n, Vanished: seen})
		}
	}
	if len(g.Missing) > 2*g.Owned+20 {
		return nil // implausibly sparse: probably not issue numbering
	}
	return g
}

// SeriesGap is one series with its gaps, for the /gaps page and the filter.
type SeriesGap struct {
	SeriesID     int64
	Name         string
	Library      string
	CoverIssueID int64
	Gaps         *Gaps
}

const cvSyncMaxAge = 7 * 24 * time.Hour

// cvIssuesByVolume loads the cached issues of every fully synced volume
// (volumeIDs nil) or just of the given ones.
func (s *Store) cvIssuesByVolume(volumeIDs map[int64]bool) (map[int64][]CVIssue, error) {
	rows, err := s.db.Query(`
		SELECT c.volume_id, c.cv_issue_id, c.issue_number, c.name, c.cover_date, c.store_date, c.site_url
		FROM cv_volume_issues c
		WHERE c.volume_id IN (SELECT volume_id FROM cv_volume_sync)
		ORDER BY c.volume_id, c.cv_issue_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]CVIssue)
	for rows.Next() {
		var c CVIssue
		if err := rows.Scan(&c.VolumeID, &c.CVIssueID, &c.IssueNumber, &c.Name, &c.CoverDate, &c.StoreDate, &c.URL); err != nil {
			return nil, err
		}
		if volumeIDs == nil || volumeIDs[c.VolumeID] {
			out[c.VolumeID] = append(out[c.VolumeID], c)
		}
	}
	return out, rows.Err()
}

// SeriesGaps computes the gaps of one series, or nil when it has none to
// report or is not a candidate (one-shot, nothing numbered, ...). A non-nil
// result may still have no missing issues (a complete run: "owned 42 of 42").
func (s *Store) SeriesGaps(seriesID int64, today string) (*Gaps, error) {
	var vol sql.NullInt64
	var oneShot bool
	err := s.db.QueryRow(`SELECT comicvine_volume_id, one_shot FROM series WHERE id = ?`, seriesID).Scan(&vol, &oneShot)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if oneShot {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT issue_number, file_missing FROM issues WHERE series_id = ?`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var owned []OwnedIssue
	for rows.Next() {
		var o OwnedIssue
		if err := rows.Scan(&o.Number, &o.FileMissing); err != nil {
			return nil, err
		}
		owned = append(owned, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if vol.Valid {
		// Only a fully synced volume's list is complete enough to compare with
		// (the release check also caches a few partial rows).
		cvAll, err := s.cvIssuesByVolume(map[int64]bool{vol.Int64: true})
		if err != nil {
			return nil, err
		}
		if g := ComputeCVGaps(cvAll[vol.Int64], owned, today); g != nil {
			return g, nil
		}
	}
	return ComputeInferredGaps(owned), nil
}

// ListSeriesGaps computes gaps for every series (scoped to library, "" = all)
// and returns those with at least one missing issue, most missing first.
func (s *Store) ListSeriesGaps(library, today string) ([]SeriesGap, error) {
	type seriesRow struct {
		id    int64
		name  string
		lib   string
		vol   sql.NullInt64
		cover int64
	}
	srows, err := s.db.Query(`
		SELECT s.id, s.name, s.library, s.comicvine_volume_id,
		       COALESCE((SELECT i2.id FROM issues i2 WHERE i2.series_id = s.id
		                 ORDER BY CAST(i2.issue_number AS REAL), i2.issue_number LIMIT 1), 0)
		FROM series s
		WHERE s.one_shot = 0 AND (? = '' OR s.library = ?)
		  AND EXISTS (SELECT 1 FROM issues i WHERE i.series_id = s.id)`, library, library)
	if err != nil {
		return nil, err
	}
	var series []seriesRow
	for srows.Next() {
		var r seriesRow
		if err := srows.Scan(&r.id, &r.name, &r.lib, &r.vol, &r.cover); err != nil {
			srows.Close()
			return nil, err
		}
		series = append(series, r)
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return nil, err
	}

	irows, err := s.db.Query(`SELECT series_id, issue_number, file_missing FROM issues`)
	if err != nil {
		return nil, err
	}
	bySeries := make(map[int64][]OwnedIssue)
	for irows.Next() {
		var id int64
		var o OwnedIssue
		if err := irows.Scan(&id, &o.Number, &o.FileMissing); err != nil {
			irows.Close()
			return nil, err
		}
		bySeries[id] = append(bySeries[id], o)
	}
	irows.Close()
	if err := irows.Err(); err != nil {
		return nil, err
	}

	cv, err := s.cvIssuesByVolume(nil)
	if err != nil {
		return nil, err
	}

	var out []SeriesGap
	for _, r := range series {
		var g *Gaps
		if r.vol.Valid {
			if list := cv[r.vol.Int64]; len(list) > 0 {
				g = ComputeCVGaps(list, bySeries[r.id], today)
			}
		}
		if g == nil {
			g = ComputeInferredGaps(bySeries[r.id])
		}
		if g == nil || g.MissingCount() == 0 {
			continue
		}
		out = append(out, SeriesGap{SeriesID: r.id, Name: r.name, Library: r.lib, CoverIssueID: r.cover, Gaps: g})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := out[i].Gaps.MissingCount(), out[j].Gaps.MissingCount(); a != b {
			return a > b
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// --- ComicVine issue cache ---

// VolumeIssues returns the cached ComicVine issues of a volume.
func (s *Store) VolumeIssues(volumeID int64) ([]CVIssue, error) {
	rows, err := s.db.Query(`
		SELECT volume_id, cv_issue_id, issue_number, name, cover_date, store_date, site_url
		FROM cv_volume_issues WHERE volume_id = ? ORDER BY cv_issue_id`, volumeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CVIssue
	for rows.Next() {
		var c CVIssue
		if err := rows.Scan(&c.VolumeID, &c.CVIssueID, &c.IssueNumber, &c.Name, &c.CoverDate, &c.StoreDate, &c.URL); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpsertVolumeIssues merges ComicVine issues into the cache. An empty date or
// URL never overwrites a known one. When fullSync is set the volume's
// cv_volume_sync stamp is refreshed too — only a full list fetch may do that,
// since gap detection trusts a stamped volume's list to be complete.
func (s *Store) UpsertVolumeIssues(issues []CVIssue, syncedVolumeID int64, fullSync bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range issues {
		if _, err := tx.Exec(`
			INSERT INTO cv_volume_issues (volume_id, cv_issue_id, issue_number, name, cover_date, store_date, site_url)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(volume_id, cv_issue_id) DO UPDATE SET
				issue_number = excluded.issue_number,
				name         = excluded.name,
				cover_date   = CASE WHEN excluded.cover_date != '' THEN excluded.cover_date ELSE cover_date END,
				store_date   = CASE WHEN excluded.store_date != '' THEN excluded.store_date ELSE store_date END,
				site_url     = CASE WHEN excluded.site_url   != '' THEN excluded.site_url   ELSE site_url   END`,
			c.VolumeID, c.CVIssueID, c.IssueNumber, c.Name, c.CoverDate, c.StoreDate, c.URL); err != nil {
			return err
		}
	}
	if fullSync {
		if _, err := tx.Exec(`
			INSERT INTO cv_volume_sync (volume_id, synced_at) VALUES (?, datetime('now'))
			ON CONFLICT(volume_id) DO UPDATE SET synced_at = excluded.synced_at`, syncedVolumeID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// VolumeSyncedWithin reports whether the volume's issue list was fully
// fetched less than maxAge ago.
func (s *Store) VolumeSyncedWithin(volumeID int64, maxAge time.Duration) (bool, error) {
	var synced string
	err := s.db.QueryRow(`SELECT synced_at FROM cv_volume_sync WHERE volume_id = ?`, volumeID).Scan(&synced)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	t, err := time.Parse("2006-01-02 15:04:05", synced)
	if err != nil {
		return false, nil
	}
	return time.Since(t) < maxAge, nil
}

// StaleVolumeIDs lists ComicVine volumes of matched series whose issue list
// was never fetched or is older than maxAge (0 = the default, a week), never-
// fetched first, at most limit of them.
func (s *Store) StaleVolumeIDs(maxAge time.Duration, limit int) ([]int64, error) {
	if maxAge <= 0 {
		maxAge = cvSyncMaxAge
	}
	cutoff := time.Now().UTC().Add(-maxAge).Format("2006-01-02 15:04:05")
	rows, err := s.db.Query(`
		SELECT s.comicvine_volume_id
		FROM series s LEFT JOIN cv_volume_sync v ON v.volume_id = s.comicvine_volume_id
		WHERE s.comicvine_volume_id IS NOT NULL AND (v.volume_id IS NULL OR v.synced_at < ?)
		  AND EXISTS (SELECT 1 FROM issues i WHERE i.series_id = s.id)
		GROUP BY s.comicvine_volume_id
		ORDER BY MIN(v.synced_at IS NOT NULL), MIN(v.synced_at)
		LIMIT ?`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
