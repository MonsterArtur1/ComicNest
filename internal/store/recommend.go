package store

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Recommendations from the user's own library ("What to read next"): no
// external service, everything is derived from what is on disk, what the user
// read/favorited and the credits/characters/arcs of those issues.
//
// The pipeline is a few aggregate queries (LoadSeriesIndex, Recommend) plus
// pure Go scoring (Profile, ScoreSeries, buildRecommendations), so the scoring
// is unit-testable without a database and the cost does not grow with the
// number of series shown.
//
// Scoring model (all numbers are the constants below):
//
//  1. Every issue the user touched gets a weight: finished 1.0, started
//     (page 2+) 0.5, favorited +1.0 (on top). Progress and favorites decay with
//     age: weight * 0.5^(age/180 days), but never below 25% of the base, so
//     old favorites still count.
//  2. Weights accumulate into a Profile: per writer, artist, character
//     (and publisher), plus the series the weight came from (for the reason).
//  3. An unstarted candidate series scores the sum of
//     writer points (6 * sat(affinity); best writer + 1/2 of the 2nd + 1/4 of
//     the 3rd), artist points (3 * sat, same combination), character points
//     (3 * sat, same combination) and publisher points (1 * sat), where
//     sat(x) = x / (x + 2) maps an affinity of any size into 0..1, so one
//     huge fan base cannot drown everything else and writers weigh twice as
//     much as artists.
//  4. A candidate goes to the "writers" bucket when writer+artist points are
//     at least as large as the character points and reach recMinScore, else
//     to "characters" when those reach recMinScore. Each bucket is sorted by
//     score and diversified: at most recPerKey series per top creator or
//     character come first, the rest only fill the cap (recBucketCap).
//  5. Arc, unfinished-series and popular buckets have their own simple
//     orderings, described at their builders.
const (
	recWeightFinished = 1.0
	recWeightStarted  = 0.5
	recWeightFavorite = 1.0
	recHalfLifeDays   = 180.0
	recRecencyFloor   = 0.25
	recSatK           = 2.0

	recPtsWriter    = 6.0
	recPtsArtist    = 3.0
	recPtsCharacter = 3.0
	recPtsPublisher = 1.0

	recMinScore  = 1.5 // lowest creator/character points worth recommending
	recPerKey    = 3   // series per top creator/character before the rest only fill up
	recBucketCap = 12  // series per bucket

	recAbandonedDays   = 28  // a started series untouched this long goes to "unfinished"
	recPopularMinShare = 0.5 // another reader "finished" a series when they read at least this share of it
	recMaxArcProbe     = 3 * recBucketCap

	recTimeLayout = "2006-01-02 15:04:05"
)

// Bucket keys.
const (
	RecBucketWriters    = "writers"
	RecBucketCharacters = "characters"
	RecBucketArcs       = "arcs"
	RecBucketUnfinished = "unfinished"
	RecBucketPopular    = "popular"
	RecBucketStart      = "start" // empty-profile fallback
)

// Tag is a person, character or arc by identity (Key) with a display name.
type Tag struct {
	Key  string
	Name string
}

// SeriesInfo is what the recommender knows about one series with present
// issues; it is independent of any user.
type SeriesInfo struct {
	ID           int64
	Name         string
	Library      string
	Publisher    string
	OneShot      bool
	Present      int
	CoverIssueID int64
	Writers      []Tag
	Artists      []Tag
	Characters   []Tag
	Arcs         []Tag
}

// SeriesIndex lists every series with a present issue, by name.
type SeriesIndex struct {
	Series map[int64]*SeriesInfo
	Order  []int64
}

// queryEach runs a query and calls fn per row, closing the rows before it
// returns (the store has a single connection, so callers must not nest).
func (s *Store) queryEach(query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// tagSet collects distinct tags (by Key) per series in first-seen order.
type tagSet map[int64]map[string]bool

func (ts tagSet) add(dst *[]Tag, seriesID int64, name string) {
	key := NormalizeName(name)
	if key == "" {
		return
	}
	m := ts[seriesID]
	if m == nil {
		m = map[string]bool{}
		ts[seriesID] = m
	}
	if m[key] {
		return
	}
	m[key] = true
	*dst = append(*dst, Tag{Key: key, Name: strings.TrimSpace(name)})
}

// LoadSeriesIndex reads the library-wide, user-independent part of the
// recommender: series, and their writers, artists, characters and arcs
// (four queries). Callers may cache the result for a few minutes.
func (s *Store) LoadSeriesIndex() (*SeriesIndex, error) {
	idx := &SeriesIndex{Series: map[int64]*SeriesInfo{}}
	err := s.queryEach(`
		SELECT s.id, s.name, s.library, s.publisher, s.one_shot, COUNT(i.id),
		       COALESCE((SELECT i2.id FROM issues i2 WHERE i2.series_id = s.id AND i2.file_missing = 0
		                 ORDER BY CAST(i2.issue_number AS REAL), i2.issue_number LIMIT 1), 0)
		FROM series s JOIN issues i ON i.series_id = s.id AND i.file_missing = 0
		GROUP BY s.id
		ORDER BY s.name COLLATE NOCASE, s.id`, nil, func(r *sql.Rows) error {
		var si SeriesInfo
		if err := r.Scan(&si.ID, &si.Name, &si.Library, &si.Publisher, &si.OneShot, &si.Present, &si.CoverIssueID); err != nil {
			return err
		}
		idx.Series[si.ID] = &si
		idx.Order = append(idx.Order, si.ID)
		return nil
	})
	if err != nil {
		return nil, err
	}

	writers, artists := tagSet{}, tagSet{}
	err = s.queryEach(`SELECT series_id, writer, artist FROM issues
		WHERE file_missing = 0 AND (writer != '' OR artist != '')`, nil, func(r *sql.Rows) error {
		var id int64
		var writer, artist string
		if err := r.Scan(&id, &writer, &artist); err != nil {
			return err
		}
		si := idx.Series[id]
		if si == nil {
			return nil
		}
		for _, w := range SplitCredits(writer) {
			writers.add(&si.Writers, id, w)
		}
		for _, a := range SplitCredits(artist) {
			artists.add(&si.Artists, id, a)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	chars := tagSet{}
	err = s.queryEach(`
		SELECT DISTINCT i.series_id, c.name
		FROM issue_characters ic
		JOIN issues i ON i.id = ic.issue_id AND i.file_missing = 0
		JOIN characters c ON c.id = ic.character_id
		ORDER BY i.series_id, c.name COLLATE NOCASE`, nil, func(r *sql.Rows) error {
		var id int64
		var name string
		if err := r.Scan(&id, &name); err != nil {
			return err
		}
		if si := idx.Series[id]; si != nil {
			chars.add(&si.Characters, id, name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	err = s.queryEach(`
		SELECT x.series_id, a.id, a.name FROM (
			SELECT i.series_id, ia.arc_id FROM issue_arcs ia
			JOIN issues i ON i.id = ia.issue_id AND i.file_missing = 0
			UNION
			SELECT i.series_id, c.arc_id FROM arc_cv_issues c
			JOIN issues i ON i.comicvine_issue_id = c.comicvine_issue_id AND i.file_missing = 0
		) x JOIN arcs a ON a.id = x.arc_id
		ORDER BY x.series_id, a.id`, nil, func(r *sql.Rows) error {
		var id, arcID int64
		var name string
		if err := r.Scan(&id, &arcID, &name); err != nil {
			return err
		}
		if si := idx.Series[id]; si != nil {
			si.Arcs = append(si.Arcs, Tag{Key: strconv.FormatInt(arcID, 10), Name: name})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return idx, nil
}

// --- profile and scoring (pure) ---

// Affinity is how much the user has read of one person or character.
type Affinity struct {
	Name     string
	Weight   float64
	Issues   int                // weighted issues featuring it
	bySeries map[string]float64 // weight per series name
}

// TopSeries is the series the affinity mostly came from ("" if unknown).
func (a *Affinity) TopSeries() string {
	best, bestW := "", 0.0
	for name, w := range a.bySeries {
		if w > bestW || (w == bestW && name < best) {
			best, bestW = name, w
		}
	}
	return best
}

// Profile is the user's taste, accumulated from weighted issues.
type Profile struct {
	Writers    map[string]*Affinity
	Artists    map[string]*Affinity
	Characters map[string]*Affinity
	Publishers map[string]float64
	Issues     int // issues that carried weight
}

// NewProfile returns an empty profile.
func NewProfile() *Profile {
	return &Profile{Writers: map[string]*Affinity{}, Artists: map[string]*Affinity{},
		Characters: map[string]*Affinity{}, Publishers: map[string]float64{}}
}

func addAffinity(m map[string]*Affinity, name, seriesName string, w float64) {
	key := NormalizeName(name)
	if key == "" {
		return
	}
	a := m[key]
	if a == nil {
		a = &Affinity{Name: strings.TrimSpace(name), bySeries: map[string]float64{}}
		m[key] = a
	}
	a.Weight += w
	a.Issues++
	a.bySeries[seriesName] += w
}

// AddIssue adds one weighted issue (weight 0 is ignored).
func (p *Profile) AddIssue(seriesName string, writers, artists, characters []string, publisher string, weight float64) {
	if weight <= 0 {
		return
	}
	p.Issues++
	for _, w := range writers {
		addAffinity(p.Writers, w, seriesName, weight)
	}
	for _, a := range artists {
		addAffinity(p.Artists, a, seriesName, weight)
	}
	for _, c := range characters {
		addAffinity(p.Characters, c, seriesName, weight)
	}
	if k := NormalizeName(publisher); k != "" {
		p.Publishers[k] += weight
	}
}

func recency(ts string, now time.Time) float64 {
	t, err := time.Parse(recTimeLayout, ts)
	if err != nil {
		return 1
	}
	days := math.Max(0, now.Sub(t).Hours()/24)
	return math.Max(recRecencyFloor, math.Pow(0.5, days/recHalfLifeDays))
}

// IssueWeight is the taste weight of one issue: page is the user's last page
// (0 = no progress row), total the page total (0 = unknown), readAt/favAt the
// SQLite timestamps of the progress and the favorite.
func IssueWeight(page, total int, readAt string, fav bool, favAt string, now time.Time) float64 {
	w := 0.0
	switch {
	case total > 0 && page >= total:
		w = recWeightFinished
	case page > 1:
		w = recWeightStarted
	}
	if w > 0 {
		w *= recency(readAt, now)
	}
	if fav {
		w += recWeightFavorite * recency(favAt, now)
	}
	return w
}

func sat(x float64) float64 { return x / (x + recSatK) }

// Match is one shared person/character with the points it earned.
type Match struct {
	Tag    Tag
	Aff    *Affinity
	Points float64
}

func matches(aff map[string]*Affinity, tags []Tag, pts float64) []Match {
	var out []Match
	for _, t := range tags {
		if a := aff[t.Key]; a != nil {
			out = append(out, Match{Tag: t, Aff: a, Points: pts * sat(a.Weight)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Points != out[j].Points {
			return out[i].Points > out[j].Points
		}
		return out[i].Tag.Key < out[j].Tag.Key
	})
	return out
}

// combine adds the best match fully, the second by half, the third by a
// quarter (matches sorted best first); further ones add nothing.
func combine(ms []Match) float64 {
	total, f := 0.0, 1.0
	for i := 0; i < len(ms) && i < 3; i++ {
		total += ms[i].Points * f
		f /= 2
	}
	return total
}

// SeriesScore is the explainable result of ScoreSeries.
type SeriesScore struct {
	Total, Writer, Artist, Character, Publisher float64
	Writers, Artists, Characters                []Match
}

// ScoreSeries scores an unstarted series against the profile.
func (p *Profile) ScoreSeries(si *SeriesInfo) SeriesScore {
	var sc SeriesScore
	sc.Writers = matches(p.Writers, si.Writers, recPtsWriter)
	sc.Artists = matches(p.Artists, si.Artists, recPtsArtist)
	sc.Characters = matches(p.Characters, si.Characters, recPtsCharacter)
	sc.Writer, sc.Artist, sc.Character = combine(sc.Writers), combine(sc.Artists), combine(sc.Characters)
	if k := NormalizeName(si.Publisher); k != "" {
		sc.Publisher = recPtsPublisher * sat(p.Publishers[k])
	}
	sc.Total = sc.Writer + sc.Artist + sc.Character + sc.Publisher
	return sc
}

// --- output types ---

// Recommendation is one tile: a series, or (arcs) the next issue of an arc.
type Recommendation struct {
	SeriesID     int64
	IssueID      int64 // set when the tile is an issue (arc continuation)
	Name         string
	OneShot      bool
	CoverIssueID int64
	Reason       string
	Score        float64
	Dismissable  bool
	key          string // diversification key
}

// RecBucket is a titled row of recommendations.
type RecBucket struct {
	Key   string
	Title string
	Items []Recommendation
}

// Recommendations is everything shown on /recommendations.
type Recommendations struct {
	Buckets  []RecBucket
	Fallback bool // no signal: Buckets holds the "start with these" row
}

// Count returns the number of tiles over all buckets.
func (r *Recommendations) Count() int {
	n := 0
	for _, b := range r.Buckets {
		n += len(b.Items)
	}
	return n
}

// seriesProgress is one user's reading aggregate of a series.
type seriesProgress struct {
	Started  int // present issues with real progress (page 2+ or finished)
	Finished int
	LastRead string
}

// recInput is everything buildRecommendations needs.
type recInput struct {
	idx       *SeriesIndex
	library   string
	progress  map[int64]seriesProgress
	dismissed map[int64]bool
	profile   *Profile
	popular   map[int64]int // series id -> other readers who finished it
	arcs      []Recommendation
	now       time.Time
}

func sortRecs(items []Recommendation) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name); an != bn {
			return an < bn
		}
		return a.SeriesID < b.SeriesID
	})
}

// diversify takes items (best first) up to limit: first at most perKey per
// key, then the skipped ones in order to fill what is left.
func diversify(items []Recommendation, perKey, limit int) []Recommendation {
	var out, skipped []Recommendation
	count := map[string]int{}
	for _, it := range items {
		if len(out) == limit {
			break
		}
		if it.key != "" && count[it.key] >= perKey {
			skipped = append(skipped, it)
			continue
		}
		count[it.key]++
		out = append(out, it)
	}
	for _, it := range skipped {
		if len(out) == limit {
			break
		}
		out = append(out, it)
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func humanAgo(now time.Time, ts string) string {
	t, err := time.Parse(recTimeLayout, ts)
	if err != nil {
		return "a while ago"
	}
	days := int(now.Sub(t).Hours() / 24)
	switch {
	case days < 14:
		return fmt.Sprintf("%d days ago", max(days, 1))
	case days < 60:
		return fmt.Sprintf("%d weeks ago", days/7)
	}
	return fmt.Sprintf("%d months ago", days/30)
}

// buildRecommendations assembles the buckets from already-loaded data. Pure.
func buildRecommendations(in recInput) *Recommendations {
	var writers, chars, unfinished, popular, start []Recommendation
	for _, id := range in.idx.Order {
		si := in.idx.Series[id]
		if si.Present == 0 || in.dismissed[id] || (in.library != "" && si.Library != in.library) {
			continue
		}
		pr := in.progress[id]
		if pr.Started > 0 {
			// Started but not finished, and left alone for a while.
			if pr.Finished >= si.Present {
				continue
			}
			last, err := time.Parse(recTimeLayout, pr.LastRead)
			if err == nil && in.now.Sub(last) < recAbandonedDays*24*time.Hour {
				continue
			}
			unfinished = append(unfinished, Recommendation{
				SeriesID: id, Name: si.Name, OneShot: si.OneShot, CoverIssueID: si.CoverIssueID, Dismissable: true,
				Score: float64(pr.Finished) / float64(si.Present),
				Reason: fmt.Sprintf("You read %d of %d %s — last %s", pr.Finished, si.Present,
					plural(si.Present, "issue", "issues"), humanAgo(in.now, pr.LastRead)),
			})
			continue
		}

		rec := Recommendation{SeriesID: id, Name: si.Name, OneShot: si.OneShot, CoverIssueID: si.CoverIssueID, Dismissable: true}
		start = append(start, Recommendation{SeriesID: id, Name: si.Name, OneShot: si.OneShot, CoverIssueID: si.CoverIssueID,
			Dismissable: true, Score: float64(si.Present),
			Reason: fmt.Sprintf("%d %s in your library", si.Present, plural(si.Present, "issue", "issues"))})

		sc := in.profile.ScoreSeries(si)
		switch creators := sc.Writer + sc.Artist; {
		case creators >= recMinScore && creators >= sc.Character:
			rec.Score = sc.Total
			m, role := sc.Artists, "artist"
			if len(sc.Writers) > 0 {
				m, role = sc.Writers, "writer"
			}
			top := m[0]
			rec.key = role + ":" + top.Tag.Key
			if from := top.Aff.TopSeries(); from != "" {
				rec.Reason = fmt.Sprintf("Because you read %s — same %s: %s", from, role, top.Tag.Name)
			} else {
				rec.Reason = fmt.Sprintf("Same %s as issues you read: %s", role, top.Tag.Name)
			}
			writers = append(writers, rec)
		case sc.Character >= recMinScore:
			rec.Score = sc.Total
			top := sc.Characters[0]
			rec.key = top.Tag.Key
			rec.Reason = fmt.Sprintf("Features %s, whom you've read in %d %s", top.Tag.Name, top.Aff.Issues,
				plural(top.Aff.Issues, "issue", "issues"))
			chars = append(chars, rec)
		}
	}

	var out Recommendations
	shown := map[int64]bool{}
	add := func(key, title string, items []Recommendation, perKey int) {
		sortRecs(items)
		items = diversify(items, perKey, recBucketCap)
		if len(items) == 0 {
			return
		}
		for _, it := range items {
			shown[it.SeriesID] = true
		}
		out.Buckets = append(out.Buckets, RecBucket{Key: key, Title: title, Items: items})
	}
	add(RecBucketWriters, "More from writers you like", writers, recPerKey)
	add(RecBucketCharacters, "Characters you follow", chars, recPerKey)
	if len(in.arcs) > 0 {
		items := in.arcs
		if len(items) > recBucketCap {
			items = items[:recBucketCap]
		}
		out.Buckets = append(out.Buckets, RecBucket{Key: RecBucketArcs, Title: "Continue a story arc", Items: items})
	}
	// Unfinished series: the more of it was read, the closer it is to done.
	add(RecBucketUnfinished, "Unfinished series", unfinished, 0)

	// Popular on this server: unstarted series other readers finished, most
	// readers first; only the count is shown, never who.
	for _, id := range in.idx.Order {
		n := in.popular[id]
		si := in.idx.Series[id]
		if n == 0 || shown[id] || in.progress[id].Started > 0 || in.dismissed[id] ||
			(in.library != "" && si.Library != in.library) {
			continue
		}
		popular = append(popular, Recommendation{SeriesID: id, Name: si.Name, OneShot: si.OneShot,
			CoverIssueID: si.CoverIssueID, Dismissable: true, Score: float64(n),
			Reason: fmt.Sprintf("Finished by %d other %s on this server", n, plural(n, "reader", "readers"))})
	}
	add(RecBucketPopular, "Popular on this server", popular, 0)

	if len(out.Buckets) == 0 {
		out.Fallback = true
		items := start
		sortRecs(items)
		if len(items) > recBucketCap {
			items = items[:recBucketCap]
		}
		if len(items) > 0 {
			out.Buckets = []RecBucket{{Key: RecBucketStart, Title: "Start with these", Items: items}}
		}
	}
	return &out
}

// --- loading the user's side ---

func (s *Store) userSeriesProgress(user string) (map[int64]seriesProgress, error) {
	out := map[int64]seriesProgress{}
	err := s.queryEach(`
		SELECT i.series_id,
		       SUM(rp.page > 1 OR `+readExpr+`), SUM(`+readExpr+`), MAX(rp.updated_at)
		FROM issues i JOIN reading_progress rp ON rp.issue_id = i.id AND rp.user = ?
		WHERE i.file_missing = 0
		GROUP BY i.series_id`, []any{user}, func(r *sql.Rows) error {
		var id int64
		var p seriesProgress
		if err := r.Scan(&id, &p.Started, &p.Finished, &p.LastRead); err != nil {
			return err
		}
		out[id] = p
		return nil
	})
	return out, err
}

// userProfile builds the taste profile: every present issue the user has
// progress on or favorited, with its credits and characters (two queries).
func (s *Store) userProfile(user string, idx *SeriesIndex, now time.Time) (*Profile, error) {
	type row struct {
		id             int64
		series         string
		writer, artist string
		publisher      string
		weight         float64
		characters     []string
	}
	var rows []*row
	byID := map[int64]*row{}
	err := s.queryEach(`
		SELECT i.id, s.name, i.writer, i.artist, COALESCE(NULLIF(i.publisher, ''), s.publisher),
		       COALESCE(rp.page, 0), `+totalPagesExpr+`, COALESCE(rp.updated_at, ''),
		       f.issue_id IS NOT NULL, COALESCE(f.created_at, '')
		FROM issues i JOIN series s ON s.id = i.series_id
		LEFT JOIN reading_progress rp ON rp.issue_id = i.id AND rp.user = ?
		LEFT JOIN issue_favorites f ON f.issue_id = i.id AND f.user = ?
		WHERE i.file_missing = 0 AND (rp.issue_id IS NOT NULL OR f.issue_id IS NOT NULL)`,
		[]any{user, user}, func(r *sql.Rows) error {
			var x row
			var page, total int
			var readAt, favAt string
			var fav bool
			if err := r.Scan(&x.id, &x.series, &x.writer, &x.artist, &x.publisher, &page, &total, &readAt, &fav, &favAt); err != nil {
				return err
			}
			if x.weight = IssueWeight(page, total, readAt, fav, favAt, now); x.weight > 0 {
				rows = append(rows, &x)
				byID[x.id] = &x
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		err = s.queryEach(`
			SELECT DISTINCT ic.issue_id, c.name
			FROM issue_characters ic JOIN characters c ON c.id = ic.character_id
			WHERE ic.issue_id IN (
				SELECT i.id FROM issues i
				WHERE i.file_missing = 0 AND (
					EXISTS (SELECT 1 FROM reading_progress rp WHERE rp.issue_id = i.id AND rp.user = ?)
					OR EXISTS (SELECT 1 FROM issue_favorites f WHERE f.issue_id = i.id AND f.user = ?)))`,
			[]any{user, user}, func(r *sql.Rows) error {
				var id int64
				var name string
				if err := r.Scan(&id, &name); err != nil {
					return err
				}
				if x := byID[id]; x != nil {
					x.characters = append(x.characters, name)
				}
				return nil
			})
		if err != nil {
			return nil, err
		}
	}
	p := NewProfile()
	for _, x := range rows {
		p.AddIssue(x.series, SplitCredits(x.writer), SplitCredits(x.artist), x.characters, x.publisher, x.weight)
	}
	return p, nil
}

// otherReaders counts, per series, the other users who finished at least
// recPopularMinShare of its present issues.
func (s *Store) otherReaders(user string, idx *SeriesIndex) (map[int64]int, error) {
	out := map[int64]int{}
	err := s.queryEach(`
		SELECT i.series_id, COUNT(*)
		FROM issues i JOIN reading_progress rp ON rp.issue_id = i.id AND rp.user != ? AND rp.user != ''
		WHERE i.file_missing = 0 AND `+totalPagesExpr+` > 0 AND rp.page >= `+totalPagesExpr+`
		GROUP BY i.series_id, rp.user`, []any{user}, func(r *sql.Rows) error {
		var id int64
		var n int
		if err := r.Scan(&id, &n); err != nil {
			return err
		}
		if si := idx.Series[id]; si != nil && float64(n) >= recPopularMinShare*float64(si.Present) {
			out[id]++
		}
		return nil
	})
	return out, err
}

// arcRecommendations lists, for arcs the user has partly read (at least one
// finished issue, not all owned ones), the next unread owned issue after the
// last finished one. ListArcs is three queries; only the best few arcs are
// then expanded (ArcEntries), so the cost is bounded by recMaxArcProbe.
func (s *Store) arcRecommendations(user, library string, idx *SeriesIndex) ([]Recommendation, error) {
	arcs, err := s.ListArcs(user)
	if err != nil {
		return nil, err
	}
	var partial []ArcSummary
	for _, a := range arcs {
		if a.Read >= 1 && a.Read < a.Owned {
			partial = append(partial, a)
		}
	}
	sort.SliceStable(partial, func(i, j int) bool {
		fi, fj := float64(partial[i].Read)/float64(partial[i].Owned), float64(partial[j].Read)/float64(partial[j].Owned)
		if fi != fj {
			return fi > fj
		}
		return strings.ToLower(partial[i].Name) < strings.ToLower(partial[j].Name)
	})
	if len(partial) > recMaxArcProbe {
		partial = partial[:recMaxArcProbe]
	}

	var out []Recommendation
	for _, a := range partial {
		if len(out) == recBucketCap {
			break
		}
		entries, err := s.ArcEntries(a.ID)
		if err != nil {
			return nil, err
		}
		var ids []int64
		for _, e := range entries {
			if e.Owned {
				ids = append(ids, e.Issue.ID)
			}
		}
		prog, err := s.ReadingProgressFor(user, ids)
		if err != nil {
			return nil, err
		}
		finished := func(is *IssueWithSeries) bool {
			p, ok := prog[is.ID]
			total := is.FilePages
			if total == 0 {
				total = is.PageCount
			}
			return ok && total > 0 && p.Page >= total
		}
		lastFinished := -1
		for i, e := range entries {
			if e.Owned && finished(e.Issue) {
				lastFinished = i
			}
		}
		var next *IssueWithSeries
		for pass := 0; pass < 2 && next == nil; pass++ {
			from := lastFinished + 1
			if pass == 1 {
				from = 0
			}
			for _, e := range entries[from:] {
				if e.Owned && !finished(e.Issue) {
					next = e.Issue
					break
				}
			}
		}
		if next == nil {
			continue
		}
		si := idx.Series[next.SeriesID]
		if si == nil || (library != "" && si.Library != library) {
			continue
		}
		name := next.SeriesName
		if next.IssueNumber != "" {
			name += " #" + next.IssueNumber
		}
		out = append(out, Recommendation{
			SeriesID: next.SeriesID, IssueID: next.ID, Name: name, CoverIssueID: next.ID,
			Score:  float64(a.Read) / float64(a.Owned),
			Reason: fmt.Sprintf("Next in %s — %d of %d owned issues read", a.Name, a.Read, a.Owned),
		})
	}
	return out, nil
}

// Recommend computes the user's recommendations over the (cacheable) index.
// library scopes candidates to one library root ("" = all).
func (s *Store) Recommend(user, library string, idx *SeriesIndex, now time.Time) (*Recommendations, error) {
	progress, err := s.userSeriesProgress(user)
	if err != nil {
		return nil, err
	}
	dismissed, err := s.dismissedSet(user)
	if err != nil {
		return nil, err
	}
	profile, err := s.userProfile(user, idx, now)
	if err != nil {
		return nil, err
	}
	popular, err := s.otherReaders(user, idx)
	if err != nil {
		return nil, err
	}
	arcs, err := s.arcRecommendations(user, library, idx)
	if err != nil {
		return nil, err
	}
	return buildRecommendations(recInput{idx: idx, library: library, progress: progress, dismissed: dismissed,
		profile: profile, popular: popular, arcs: arcs, now: now}), nil
}

// --- "not interested" ---

func (s *Store) dismissedSet(user string) (map[int64]bool, error) {
	out := map[int64]bool{}
	err := s.queryEach(`SELECT series_id FROM recommendation_dismissals WHERE user = ?`, []any{user}, func(r *sql.Rows) error {
		var id int64
		if err := r.Scan(&id); err != nil {
			return err
		}
		out[id] = true
		return nil
	})
	return out, err
}

// DismissRecommendation hides a series from the user's recommendations.
func (s *Store) DismissRecommendation(user string, seriesID int64) error {
	_, err := s.db.Exec(`
		INSERT INTO recommendation_dismissals (user, series_id) VALUES (?, ?)
		ON CONFLICT(user, series_id) DO NOTHING`, user, seriesID)
	return err
}

// RestoreRecommendation undoes DismissRecommendation.
func (s *Store) RestoreRecommendation(user string, seriesID int64) error {
	_, err := s.db.Exec(`DELETE FROM recommendation_dismissals WHERE user = ? AND series_id = ?`, user, seriesID)
	return err
}

// DismissedSeries is a series the user hid from recommendations.
type DismissedSeries struct {
	ID   int64
	Name string
}

// ListDismissedSeries returns the user's hidden series by name.
func (s *Store) ListDismissedSeries(user string) ([]DismissedSeries, error) {
	var out []DismissedSeries
	err := s.queryEach(`
		SELECT s.id, s.name FROM recommendation_dismissals d JOIN series s ON s.id = d.series_id
		WHERE d.user = ? ORDER BY s.name COLLATE NOCASE, s.id`, []any{user}, func(r *sql.Rows) error {
		var d DismissedSeries
		if err := r.Scan(&d.ID, &d.Name); err != nil {
			return err
		}
		out = append(out, d)
		return nil
	})
	return out, err
}

// --- "more like this" (series page) ---

// Points of SimilarSeries: shared writer 3, artist 1.5, arc 4, characters 0.5
// each (only from two shared on, at most six count), same publisher 0.5 (never
// enough on its own).
const (
	simWriter    = 3.0
	simArtist    = 1.5
	simArc       = 4.0
	simCharacter = 0.5
	simPublisher = 0.5
)

func sharedTags(base map[string]bool, tags []Tag) []Tag {
	var out []Tag
	for _, t := range tags {
		if base[t.Key] {
			out = append(out, t)
		}
	}
	return out
}

func tagKeys(tags []Tag) map[string]bool {
	m := make(map[string]bool, len(tags))
	for _, t := range tags {
		m[t.Key] = true
	}
	return m
}

func tagNames(tags []Tag, n int) string {
	var names []string
	for i, t := range tags {
		if i == n {
			break
		}
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}

// SimilarSeries returns up to limit series sharing writers, artists, arcs or
// characters with the given one, best first. It does not depend on the user.
func SimilarSeries(idx *SeriesIndex, seriesID int64, library string, limit int) []Recommendation {
	base := idx.Series[seriesID]
	if base == nil {
		return nil
	}
	w, a, c, arcs := tagKeys(base.Writers), tagKeys(base.Artists), tagKeys(base.Characters), tagKeys(base.Arcs)
	var out []Recommendation
	for _, id := range idx.Order {
		si := idx.Series[id]
		if id == seriesID || si.Present == 0 || (library != "" && si.Library != library) {
			continue
		}
		sw, sa, sarc := sharedTags(w, si.Writers), sharedTags(a, si.Artists), sharedTags(arcs, si.Arcs)
		sc := sharedTags(c, si.Characters)
		score := simWriter*float64(len(sw)) + simArtist*float64(len(sa)) + simArc*float64(len(sarc))
		if len(sc) >= 2 {
			score += simCharacter * float64(min(len(sc), 6))
		} else {
			sc = nil
		}
		if score == 0 {
			continue
		}
		if base.Publisher != "" && strings.EqualFold(base.Publisher, si.Publisher) {
			score += simPublisher
		}
		var reasons []string
		if len(sarc) > 0 {
			reasons = append(reasons, "same story arc: "+tagNames(sarc, 1))
		}
		if len(sw) > 0 {
			reasons = append(reasons, plural(len(sw), "same writer: ", "same writers: ")+tagNames(sw, 2))
		}
		if len(sa) > 0 && len(reasons) < 2 {
			reasons = append(reasons, plural(len(sa), "same artist: ", "same artists: ")+tagNames(sa, 2))
		}
		if len(sc) > 0 && len(reasons) < 2 {
			reasons = append(reasons, "shared characters: "+tagNames(sc, 3))
		}
		out = append(out, Recommendation{SeriesID: id, Name: si.Name, OneShot: si.OneShot, CoverIssueID: si.CoverIssueID,
			Score: score, Reason: strings.ToUpper(reasons[0][:1]) + reasons[0][1:] + joinRest(reasons[1:])})
	}
	sortRecs(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func joinRest(rest []string) string {
	if len(rest) == 0 {
		return ""
	}
	return " · " + strings.Join(rest, " · ")
}
