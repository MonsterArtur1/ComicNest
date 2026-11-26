package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var recNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func recTS(daysAgo int) string { return recNow.AddDate(0, 0, -daysAgo).Format(recTimeLayout) }

func TestIssueWeight(t *testing.T) {
	fresh := recTS(0)
	if w := IssueWeight(10, 10, fresh, false, "", recNow); w != 1 {
		t.Errorf("finished = %v, want 1", w)
	}
	if w := IssueWeight(4, 10, fresh, false, "", recNow); w != 0.5 {
		t.Errorf("started = %v, want 0.5", w)
	}
	if w := IssueWeight(1, 10, fresh, false, "", recNow); w != 0 {
		t.Errorf("lone first page = %v, want 0", w)
	}
	if w := IssueWeight(0, 10, "", true, fresh, recNow); w != 1 {
		t.Errorf("favorite only = %v, want 1", w)
	}
	if w := IssueWeight(10, 10, fresh, true, fresh, recNow); w != 2 {
		t.Errorf("finished + favorite = %v, want 2", w)
	}
	// Half-life of 180 days, floor at 25%.
	if w := IssueWeight(10, 10, recTS(180), false, "", recNow); w < 0.49 || w > 0.51 {
		t.Errorf("180 days old = %v, want ~0.5", w)
	}
	if w := IssueWeight(10, 10, recTS(5000), false, "", recNow); w != recRecencyFloor {
		t.Errorf("ancient = %v, want the floor %v", w, recRecencyFloor)
	}
}

func TestScoreSeriesWeighsWritersOverArtists(t *testing.T) {
	p := NewProfile()
	p.AddIssue("Saga", []string{"Vaughan"}, []string{"Staples"}, []string{"Alana"}, "Image", 1)
	p.AddIssue("Saga", []string{"Vaughan"}, []string{"Staples"}, []string{"Alana"}, "Image", 1)

	byWriter := p.ScoreSeries(&SeriesInfo{Writers: []Tag{{Key: "vaughan", Name: "Vaughan"}}})
	byArtist := p.ScoreSeries(&SeriesInfo{Artists: []Tag{{Key: "staples", Name: "Staples"}}})
	if byWriter.Total <= byArtist.Total || byWriter.Writer != 6*sat(2) || byArtist.Artist != 3*sat(2) {
		t.Errorf("writer %+v vs artist %+v", byWriter, byArtist)
	}
	none := p.ScoreSeries(&SeriesInfo{Writers: []Tag{{Key: "nobody"}}})
	if none.Total != 0 {
		t.Errorf("unrelated series scored %v", none.Total)
	}
	// Publisher counts a little; a second matching writer adds half.
	pub := p.ScoreSeries(&SeriesInfo{Publisher: "image"})
	if pub.Publisher != recPtsPublisher*sat(2) {
		t.Errorf("publisher points = %v", pub.Publisher)
	}
	p.AddIssue("Other", []string{"Gillen"}, nil, nil, "", 2)
	two := p.ScoreSeries(&SeriesInfo{Writers: []Tag{{Key: "vaughan"}, {Key: "gillen"}}})
	want := 6*sat(2) + 0.5*6*sat(2) // both have weight 2
	if d := two.Writer - want; d > 1e-9 || d < -1e-9 {
		t.Errorf("two writers = %v, want %v", two.Writer, want)
	}
	if got := p.Writers["vaughan"].TopSeries(); got != "Saga" {
		t.Errorf("TopSeries = %q", got)
	}
}

func TestDiversify(t *testing.T) {
	var items []Recommendation
	for i, k := range []string{"a", "a", "a", "a", "b", "c"} {
		items = append(items, Recommendation{SeriesID: int64(i + 1), key: k})
	}
	got := diversify(items, 2, 5)
	var ids []int64
	for _, g := range got {
		ids = append(ids, g.SeriesID)
	}
	// a, a, b, c first (a is capped at 2), then the skipped ones fill the cap.
	want := []int64{1, 2, 5, 6, 3}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v, want %v", ids, want)
		}
	}
}

func recIndex(series ...*SeriesInfo) *SeriesIndex {
	idx := &SeriesIndex{Series: map[int64]*SeriesInfo{}}
	for _, s := range series {
		idx.Series[s.ID] = s
		idx.Order = append(idx.Order, s.ID)
	}
	return idx
}

func TestBuildRecommendationsBuckets(t *testing.T) {
	vaughan := []Tag{{Key: "brian k. vaughan", Name: "Brian K. Vaughan"}}
	idx := recIndex(
		&SeriesInfo{ID: 1, Name: "Saga", Present: 3, Writers: vaughan},
		&SeriesInfo{ID: 2, Name: "Y", Present: 3, Writers: vaughan},
		&SeriesInfo{ID: 3, Name: "Ex Machina", Present: 3, Writers: vaughan, Library: "/b"},
		&SeriesInfo{ID: 4, Name: "Batman Annual", Present: 2, Characters: []Tag{{Key: "batman", Name: "Batman"}}},
		&SeriesInfo{ID: 5, Name: "Old Run", Present: 4},
		&SeriesInfo{ID: 6, Name: "Fresh Run", Present: 4},
		&SeriesInfo{ID: 7, Name: "Hidden", Present: 3, Writers: vaughan},
		&SeriesInfo{ID: 8, Name: "Crowd Pleaser", Present: 2},
	)
	p := NewProfile()
	p.AddIssue("Saga", []string{"Brian K. Vaughan"}, nil, []string{"Batman"}, "", 1)
	p.AddIssue("Saga", []string{"Brian K. Vaughan"}, nil, []string{"Batman"}, "", 1)
	in := recInput{
		idx: idx,
		progress: map[int64]seriesProgress{
			1: {Started: 3, Finished: 3, LastRead: recTS(1)},
			5: {Started: 2, Finished: 1, LastRead: recTS(90)},
			6: {Started: 2, Finished: 1, LastRead: recTS(3)},
		},
		dismissed: map[int64]bool{7: true},
		profile:   p,
		popular:   map[int64]int{8: 2, 2: 1},
		now:       recNow,
	}
	got := buildRecommendations(in)
	if got.Fallback {
		t.Fatal("unexpected fallback")
	}
	byKey := map[string]RecBucket{}
	for _, b := range got.Buckets {
		byKey[b.Key] = b
	}
	names := func(b RecBucket) string {
		var n []string
		for _, it := range b.Items {
			n = append(n, it.Name)
		}
		return strings.Join(n, "|")
	}
	if n := names(byKey[RecBucketWriters]); n != "Ex Machina|Y" && n != "Y|Ex Machina" {
		t.Errorf("writers bucket = %q (finished Saga, dismissed Hidden must be absent)", n)
	}
	if w := byKey[RecBucketWriters].Items[0]; !strings.Contains(w.Reason, "Because you read Saga — same writer: Brian K. Vaughan") {
		t.Errorf("writer reason = %q", w.Reason)
	}
	if n := names(byKey[RecBucketCharacters]); n != "Batman Annual" {
		t.Errorf("characters bucket = %q", n)
	}
	if r := byKey[RecBucketCharacters].Items[0].Reason; r != "Features Batman, whom you've read in 2 issues" {
		t.Errorf("character reason = %q", r)
	}
	// Only the series untouched for 4+ weeks is "unfinished".
	if n := names(byKey[RecBucketUnfinished]); n != "Old Run" {
		t.Errorf("unfinished bucket = %q", n)
	}
	if r := byKey[RecBucketUnfinished].Items[0].Reason; r != "You read 1 of 4 issues — last 3 months ago" {
		t.Errorf("unfinished reason = %q", r)
	}
	// Y is already in the writers bucket; only Crowd Pleaser is left for popular.
	if n := names(byKey[RecBucketPopular]); n != "Crowd Pleaser" {
		t.Errorf("popular bucket = %q", n)
	}
	if r := byKey[RecBucketPopular].Items[0].Reason; r != "Finished by 2 other readers on this server" {
		t.Errorf("popular reason = %q", r)
	}

	// Library scoping drops series of other libraries.
	in.library = "/a"
	for _, b := range buildRecommendations(in).Buckets {
		for _, it := range b.Items {
			if it.Name == "Ex Machina" {
				t.Errorf("series of library /b shown while /a is selected (bucket %s)", b.Key)
			}
		}
	}
}

func TestBuildRecommendationsFallbackAndCap(t *testing.T) {
	var all []*SeriesInfo
	for i := 1; i <= 20; i++ {
		all = append(all, &SeriesInfo{ID: int64(i), Name: strings.Repeat("S", i), Present: i})
	}
	got := buildRecommendations(recInput{idx: recIndex(all...), progress: map[int64]seriesProgress{},
		dismissed: map[int64]bool{}, profile: NewProfile(), now: recNow})
	if !got.Fallback || len(got.Buckets) != 1 || got.Buckets[0].Key != RecBucketStart {
		t.Fatalf("want the start fallback, got %+v", got)
	}
	items := got.Buckets[0].Items
	if len(items) != recBucketCap || items[0].SeriesID != 20 {
		t.Errorf("fallback: %d items, first %d; want %d, biggest first", len(items), items[0].SeriesID, recBucketCap)
	}
	// Nothing at all (every series dismissed) is an empty result, not an error.
	dismissed := map[int64]bool{}
	for _, s := range all {
		dismissed[s.ID] = true
	}
	got = buildRecommendations(recInput{idx: recIndex(all...), progress: map[int64]seriesProgress{},
		dismissed: dismissed, profile: NewProfile(), now: recNow})
	if got.Count() != 0 {
		t.Errorf("all dismissed but %d shown", got.Count())
	}
}

func newRecStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func finish(t *testing.T, st *Store, user string, is *Issue) {
	t.Helper()
	if err := st.SetReadingProgress(user, is.ID, 10); err != nil { // FilePages = 10
		t.Fatal(err)
	}
}

func recommend(t *testing.T, st *Store, user, lib string) *Recommendations {
	t.Helper()
	idx, err := st.LoadSeriesIndex()
	if err != nil {
		t.Fatal(err)
	}
	recs, err := st.Recommend(user, lib, idx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func bucketOf(r *Recommendations, key string) []Recommendation {
	for _, b := range r.Buckets {
		if b.Key == key {
			return b.Items
		}
	}
	return nil
}

func TestRecommendFromDatabase(t *testing.T) {
	st := newRecStore(t)
	writer := func(w string) func(*Issue) { return func(i *Issue) { i.Writer = w } }
	saga := addTestIssue(t, st, "Saga", "1", writer("Brian K. Vaughan"))
	addTestIssue(t, st, "Y", "1", writer("Brian K. Vaughan, Pia Guerra"))
	addTestIssue(t, st, "Other", "1", writer("Someone Else"))
	gone := addTestIssue(t, st, "Gone", "1", writer("Brian K. Vaughan"))
	if err := st.MarkIssuesMissing([]int64{gone.ID}); err != nil {
		t.Fatal(err)
	}
	finish(t, st, "ania", saga)

	recs := bucketOf(recommend(t, st, "ania", ""), RecBucketWriters)
	if len(recs) != 1 || recs[0].Name != "Y" || !strings.Contains(recs[0].Reason, "Saga") {
		t.Fatalf("ania's writers bucket = %+v (Gone is missing on disk, Other unrelated)", recs)
	}
	// A user with no history gets no taste-based rows — only what ania
	// finished, as an anonymous count.
	fresh := recommend(t, st, "bartek", "")
	pop := bucketOf(fresh, RecBucketPopular)
	if len(bucketOf(fresh, RecBucketWriters)) != 0 || len(pop) != 1 || pop[0].Name != "Saga" ||
		strings.Contains(pop[0].Reason, "ania") {
		t.Errorf("bartek (no history) = %+v", fresh)
	}

	// "Not interested" is per user and reversible.
	ySeries := recs[0].SeriesID
	if err := st.DismissRecommendation("ania", ySeries); err != nil {
		t.Fatal(err)
	}
	if got := bucketOf(recommend(t, st, "ania", ""), RecBucketWriters); len(got) != 0 {
		t.Errorf("dismissed series still recommended: %+v", got)
	}
	d, err := st.ListDismissedSeries("ania")
	if err != nil || len(d) != 1 || d[0].Name != "Y" {
		t.Errorf("dismissed list = %+v, %v", d, err)
	}
	if d, _ := st.ListDismissedSeries("bartek"); len(d) != 0 {
		t.Errorf("bartek sees ania's dismissals: %+v", d)
	}
	if err := st.RestoreRecommendation("ania", ySeries); err != nil {
		t.Fatal(err)
	}
	if got := bucketOf(recommend(t, st, "ania", ""), RecBucketWriters); len(got) != 1 {
		t.Errorf("restored series not recommended again: %+v", got)
	}

	// Library scoping.
	if err := st.stampSeriesLibrary(ySeries, "/lib-b"); err != nil {
		t.Fatal(err)
	}
	if got := bucketOf(recommend(t, st, "ania", "/lib-a"), RecBucketWriters); len(got) != 0 {
		t.Errorf("series of another library recommended: %+v", got)
	}
	if got := bucketOf(recommend(t, st, "ania", "/lib-b"), RecBucketWriters); len(got) != 1 {
		t.Errorf("series of the selected library missing: %+v", got)
	}
}

func TestRecommendFavoritesCharactersAndPopular(t *testing.T) {
	st := newRecStore(t)
	fav := addTestIssue(t, st, "Favorited", "1", nil)
	addTestIssue(t, st, "Guest Star", "1", nil)
	crowd1 := addTestIssue(t, st, "Crowd", "1", nil)
	crowd2 := addTestIssue(t, st, "Crowd", "2", nil)
	// Three issues the user favorited feature Batman, as does an unread series.
	fav2 := addTestIssue(t, st, "Favorited", "2", nil)
	fav3 := addTestIssue(t, st, "Favorited", "3", nil)
	guest := addTestIssue(t, st, "Guest Star", "2", nil)
	for _, is := range []*Issue{fav, fav2, fav3, guest} {
		if err := st.ReplaceComicInfoExtras(is.ID, []string{"Batman"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, is := range []*Issue{fav, fav2, fav3} {
		if err := st.SetIssueFavorite("ania", is.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	// bartek finishes the whole Crowd series.
	finish(t, st, "bartek", crowd1)
	finish(t, st, "bartek", crowd2)

	r := recommend(t, st, "ania", "")
	chars := bucketOf(r, RecBucketCharacters)
	// Favoriting is not reading, so "Favorited" is itself an unstarted
	// candidate next to "Guest Star"; both carry the same reason.
	var names []string
	for _, c := range chars {
		names = append(names, c.Name)
	}
	if len(chars) == 0 || chars[0].Name == "" || !strings.Contains(chars[0].Reason, "Batman") {
		t.Fatalf("characters bucket = %v", names)
	}
	if !strings.Contains(chars[0].Reason, "in 3 issues") {
		t.Errorf("reason should count the favorited issues: %q", chars[0].Reason)
	}
	pop := bucketOf(r, RecBucketPopular)
	if len(pop) != 1 || pop[0].Name != "Crowd" || pop[0].Reason != "Finished by 1 other reader on this server" {
		t.Errorf("popular = %+v", pop)
	}
	// bartek's own finished series is not popular for bartek.
	if got := bucketOf(recommend(t, st, "bartek", ""), RecBucketPopular); len(got) != 0 {
		t.Errorf("own series shown as popular: %+v", got)
	}
}

func TestRecommendArcContinuation(t *testing.T) {
	st := newRecStore(t)
	a := addTestIssue(t, st, "Tie-in A", "1", nil)
	b := addTestIssue(t, st, "Main Book", "7", nil)
	c := addTestIssue(t, st, "Tie-in C", "3", nil)
	for i, is := range []*Issue{a, b, c} {
		if err := st.ReplaceComicInfoExtras(is.ID, nil, []ArcInput{{Name: "Big Event", Number: i + 1}}); err != nil {
			t.Fatal(err)
		}
	}
	finish(t, st, "ania", a)

	arcs := bucketOf(recommend(t, st, "ania", ""), RecBucketArcs)
	if len(arcs) != 1 {
		t.Fatalf("arcs bucket = %+v", arcs)
	}
	got := arcs[0]
	if got.IssueID != b.ID || got.Name != "Main Book #7" || got.Reason != "Next in Big Event — 1 of 3 owned issues read" {
		t.Errorf("arc recommendation = %+v", got)
	}
	// Once the arc is finished it disappears.
	finish(t, st, "ania", b)
	finish(t, st, "ania", c)
	if arcs := bucketOf(recommend(t, st, "ania", ""), RecBucketArcs); len(arcs) != 0 {
		t.Errorf("finished arc still offered: %+v", arcs)
	}
}

func TestSimilarSeries(t *testing.T) {
	st := newRecStore(t)
	mod := func(w string) func(*Issue) { return func(i *Issue) { i.Writer = w } }
	a := addTestIssue(t, st, "Saga", "1", mod("Vaughan"))
	addTestIssue(t, st, "Y", "1", mod("Vaughan"))
	d := addTestIssue(t, st, "Unrelated", "1", mod("Nobody"))
	e := addTestIssue(t, st, "Arc Mate", "1", mod("Third"))
	for _, is := range []*Issue{a, e} {
		if err := st.ReplaceComicInfoExtras(is.ID, nil, []ArcInput{{Name: "Shared Arc"}}); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := st.LoadSeriesIndex()
	if err != nil {
		t.Fatal(err)
	}
	got := SimilarSeries(idx, a.SeriesID, "", 10)
	if len(got) != 2 || got[0].Name != "Arc Mate" || got[1].Name != "Y" {
		t.Fatalf("similar = %+v", got)
	}
	if !strings.Contains(got[0].Reason, "Same story arc: Shared Arc") || !strings.Contains(got[1].Reason, "Same writer: Vaughan") {
		t.Errorf("reasons = %q / %q", got[0].Reason, got[1].Reason)
	}
	for _, g := range got {
		if g.SeriesID == d.SeriesID || g.SeriesID == a.SeriesID {
			t.Errorf("unrelated or self series listed: %+v", g)
		}
	}
	if got := SimilarSeries(idx, a.SeriesID, "/elsewhere", 10); len(got) != 0 {
		t.Errorf("library scope ignored: %+v", got)
	}
}
