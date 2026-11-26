package store

import (
	"database/sql"
	"fmt"
	"reflect"
	"testing"
)

// addTestIssue inserts a present issue into a series named after name.
func addTestIssue(t *testing.T, st *Store, series, number string, mod func(*Issue)) *Issue {
	t.Helper()
	sid, err := st.FindOrCreateSeriesByFolder(series, series, "")
	if err != nil {
		t.Fatal(err)
	}
	is := &Issue{SeriesID: sid, Path: fmt.Sprintf("%s %s.cbz", series, number), IssueNumber: number,
		MetadataSource: SourceFilename, FilePages: 10}
	if mod != nil {
		mod(is)
	}
	if err := st.InsertIssue(is); err != nil {
		t.Fatal(err)
	}
	if is.ComicVineIssueID.Valid {
		if err := st.UpdateIssueMetadata(is); err != nil {
			t.Fatal(err)
		}
	}
	return is
}

func cvID(id int64) func(*Issue) {
	return func(i *Issue) { i.ComicVineIssueID = sql.NullInt64{Int64: id, Valid: true} }
}

func TestSplitHelpers(t *testing.T) {
	if got, want := SplitCredits(" A B, a b ,, C"), []string{"A B", "C"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SplitCredits = %q, want %q", got, want)
	}
	if got := SplitList("x;y\nX", ",;\n"); !reflect.DeepEqual(got, []string{"x", "y"}) {
		t.Errorf("SplitList = %q", got)
	}
	if NormalizeName("  Brian   K.  VAUGHAN ") != "brian k. vaughan" {
		t.Errorf("NormalizeName = %q", NormalizeName("  Brian   K.  VAUGHAN "))
	}
}

func TestOrderArcCVIssues(t *testing.T) {
	// A curated order (not ascending by id) is kept, dates or not.
	curated := []ArcCVIssue{{ComicVineIssueID: 30, CoverDate: "2015-01-01"}, {ComicVineIssueID: 10, CoverDate: "2016-01-01"}}
	if got := OrderArcCVIssues(curated); got[0].ComicVineIssueID != 30 {
		t.Errorf("curated order changed: %+v", got)
	}
	// Plain id order with dates for everything falls back to cover date.
	byID := []ArcCVIssue{
		{ComicVineIssueID: 1, CoverDate: "2016-03-01"},
		{ComicVineIssueID: 2, CoverDate: "2015-01-01"},
		{ComicVineIssueID: 3, CoverDate: "2015-01-01"},
	}
	got := OrderArcCVIssues(byID)
	if got[0].ComicVineIssueID != 2 || got[1].ComicVineIssueID != 3 || got[2].ComicVineIssueID != 1 {
		t.Errorf("date order = %+v", got)
	}
	if byID[0].ComicVineIssueID != 1 {
		t.Error("input slice was modified")
	}
	// A missing date makes the date order unusable: ComicVine's order stays.
	undated := []ArcCVIssue{{ComicVineIssueID: 1, CoverDate: "2016-03-01"}, {ComicVineIssueID: 2}}
	if got := OrderArcCVIssues(undated); got[0].ComicVineIssueID != 1 {
		t.Errorf("undated order changed: %+v", got)
	}
}

func TestArcMergeAndReadingOrder(t *testing.T) {
	st := openTestStore(t)
	a := addTestIssue(t, st, "Alpha", "1", cvID(101))
	b := addTestIssue(t, st, "Beta", "5", cvID(102))
	c := addTestIssue(t, st, "Gamma", "9", nil) // ComicInfo-only member

	// ComicInfo names the arc first; ComicVine later adopts the same arc.
	if err := st.ReplaceComicInfoExtras(c.ID, nil, []ArcInput{{Name: "Big Event", Number: 3}}); err != nil {
		t.Fatal(err)
	}
	arcIDs, err := st.ReplaceComicVineExtras(a.ID, nil, []ArcInput{{Name: "big event", ComicVineID: 55}})
	if err != nil {
		t.Fatal(err)
	}
	if len(arcIDs) != 1 {
		t.Fatalf("arc ids = %v", arcIDs)
	}
	if _, err := st.ReplaceComicVineExtras(b.ID, nil, []ArcInput{{Name: "Big Event", ComicVineID: 55}}); err != nil {
		t.Fatal(err)
	}
	arc, err := st.GetArc(arcIDs[0])
	if err != nil || arc == nil {
		t.Fatalf("GetArc: %v %v", arc, err)
	}
	if arc.Name != "big event" || arc.ComicVineID.Int64 != 55 {
		t.Errorf("arc = %+v, want it merged with the ComicVine id", arc)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM arcs`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("arcs = %d (%v), want 1", n, err)
	}

	// Not fetched yet: stale, and the order is the ComicInfo one (members only).
	if stale, err := st.StaleArcs(30); err != nil || len(stale) != 1 {
		t.Fatalf("StaleArcs = %v, %v", stale, err)
	}
	entries, err := st.ArcEntries(arc.ID)
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries = %v, %v", entries, err)
	}
	if entries[0].Issue.ID != c.ID { // the numbered ComicInfo member comes first
		t.Errorf("first entry = issue %d, want the StoryArcNumber 3 member", entries[0].Issue.ID)
	}

	// Fetch the ComicVine list: a gap (103) between owned 102 and 101.
	err = st.SaveArcCV(arc.ID, ArcDetails{Name: "Big Event", Description: "Desc", Publisher: "Marvel"}, []ArcCVIssue{
		{ComicVineIssueID: 102, VolumeName: "Beta", IssueNumber: "5"},
		{ComicVineIssueID: 103, VolumeName: "Delta", IssueNumber: "2", CoverDate: "2015-02-01"},
		{ComicVineIssueID: 101, VolumeName: "Alpha", IssueNumber: "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stale, _ := st.StaleArcs(30); len(stale) != 0 {
		t.Errorf("a just-fetched arc is stale: %v", stale)
	}
	if arc, _ = st.GetArc(arc.ID); arc.Name != "Big Event" || arc.Publisher != "Marvel" || !arc.FetchedAt.Valid {
		t.Errorf("arc after fetch = %+v", arc)
	}
	entries, err = st.ArcEntries(arc.ID)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		pos   int
		owned bool
		vol   string
	}
	var got []row
	for _, e := range entries {
		got = append(got, row{e.Position, e.Owned, e.VolumeName})
	}
	want := []row{{1, true, "Beta"}, {2, false, "Delta"}, {3, true, "Alpha"}, {4, true, ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %+v, want %+v", got, want)
	}
	if entries[3].Issue.ID != c.ID {
		t.Errorf("the unmatched ComicInfo member should follow the list, got issue %d", entries[3].Issue.ID)
	}

	// Position of an issue in its arc, with the next owned one.
	pos, err := st.ArcPositionsOfIssue(b.ID)
	if err != nil || len(pos) != 1 {
		t.Fatalf("positions = %v, %v", pos, err)
	}
	if pos[0].Position != 1 || pos[0].Total != 4 || pos[0].Next == nil || pos[0].Next.ID != a.ID {
		t.Errorf("position of b = %+v", pos[0])
	}

	// Index: 3 owned of 4, nothing read, cover from the first owned entry.
	list, err := st.ListArcs("u")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListArcs = %v, %v", list, err)
	}
	if l := list[0]; l.Owned != 3 || l.Total != 4 || l.Read != 0 || l.CoverIssueID != b.ID {
		t.Errorf("summary = %+v", l)
	}
	if err := st.SetReadingProgress("u", a.ID, 10); err != nil { // a has 10 pages: finished
		t.Fatal(err)
	}
	if list, _ = st.ListArcs("u"); list[0].Read != 1 {
		t.Errorf("read = %d, want 1", list[0].Read)
	}
	if list, _ = st.ListArcs("other"); list[0].Read != 0 {
		t.Errorf("another user's read = %d, want 0 (read state is per user)", list[0].Read)
	}

	// A missing file is a gap, not an owned entry.
	if err := st.MarkIssuesMissing([]int64{b.ID}); err != nil {
		t.Fatal(err)
	}
	entries, _ = st.ArcEntries(arc.ID)
	if entries[0].Owned {
		t.Error("an issue whose file is missing must show as a gap")
	}
	if list, _ = st.ListArcs("u"); list[0].Owned != 2 || list[0].Total != 4 {
		t.Errorf("summary with a missing file = %+v", list[0])
	}
}

func TestArcRefreshReplacesAndClearKeepsComicInfo(t *testing.T) {
	st := openTestStore(t)
	is := addTestIssue(t, st, "Alpha", "1", cvID(7))

	if err := st.ReplaceComicInfoExtras(is.ID, []string{"Spider-Man"}, []ArcInput{{Name: "Local Arc"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReplaceComicVineExtras(is.ID, []CharacterInput{{Name: "Venom", ComicVineID: 2}}, []ArcInput{{Name: "CV Arc", ComicVineID: 9}}); err != nil {
		t.Fatal(err)
	}
	if need, _ := st.IssuesNeedingCVExtras(); len(need) != 0 {
		t.Errorf("IssuesNeedingCVExtras after fetch = %v", need)
	}
	if err := st.ClearComicVineExtras(is.ID); err != nil {
		t.Fatal(err)
	}
	chars, _ := st.IssueCharacters(is.ID)
	if len(chars) != 1 || chars[0].Name != "Spider-Man" {
		t.Errorf("characters after clear = %+v, want only the ComicInfo one", chars)
	}
	pos, _ := st.ArcPositionsOfIssue(is.ID)
	if len(pos) != 1 || pos[0].Arc.Name != "Local Arc" {
		t.Errorf("arcs after clear = %+v", pos)
	}
	if need, _ := st.IssuesNeedingCVExtras(); len(need) != 1 || need[0].ID != is.ID || need[0].ComicVineIssueID != 7 {
		t.Errorf("IssuesNeedingCVExtras after clear = %v", need)
	}
}

func TestComicInfoExtrasPending(t *testing.T) {
	st := openTestStore(t)
	is := addTestIssue(t, st, "Alpha", "1", nil)
	if p, err := st.ComicInfoExtrasPending(is.ID); err != nil || !p {
		t.Fatalf("a new row is pending: %v %v", p, err)
	}
	if err := st.ReplaceComicInfoExtras(is.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.ComicInfoExtrasPending(is.ID); p {
		t.Error("still pending after an (empty) read")
	}
}

func TestBackfillQueryIncludesLockedAndSkipsMissing(t *testing.T) {
	st := openTestStore(t)
	locked := addTestIssue(t, st, "Alpha", "1", func(i *Issue) { cvID(1)(i); i.MetadataLocked = true })
	gone := addTestIssue(t, st, "Alpha", "2", cvID(2))
	addTestIssue(t, st, "Alpha", "3", nil) // never matched
	if err := st.MarkIssuesMissing([]int64{gone.ID}); err != nil {
		t.Fatal(err)
	}
	need, err := st.IssuesNeedingCVExtras()
	if err != nil || len(need) != 1 || need[0].ID != locked.ID {
		t.Errorf("IssuesNeedingCVExtras = %v, %v", need, err)
	}
}

func TestCharacterIdentity(t *testing.T) {
	st := openTestStore(t)
	i1 := addTestIssue(t, st, "Alpha", "1", nil)
	i2 := addTestIssue(t, st, "Alpha", "2", nil)
	i3 := addTestIssue(t, st, "Beta", "1", nil)

	// ComicInfo spellings merge case-insensitively...
	if err := st.ReplaceComicInfoExtras(i1.ID, []string{"Spider-Man", "Venom"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceComicInfoExtras(i2.ID, []string{"spider-man"}, nil); err != nil {
		t.Fatal(err)
	}
	// ...and ComicVine's id is adopted by the same-named ComicInfo character.
	if _, err := st.ReplaceComicVineExtras(i3.ID, []CharacterInput{{Name: "Spider-Man", ComicVineID: 1443}, {Name: "Carnage", ComicVineID: 9}}, nil); err != nil {
		t.Fatal(err)
	}
	// A ComicVine character is found again by id even when the spelling differs.
	if _, err := st.ReplaceComicVineExtras(i1.ID, []CharacterInput{{Name: "Spiderman", ComicVineID: 1443}}, nil); err != nil {
		t.Fatal(err)
	}

	list, err := st.ListCharacters()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, c := range list {
		counts[c.Name] = c.Issues
	}
	// Spider-Man: issues 1 (ComicInfo + CV), 2 and 3 = 3 distinct issues, one row.
	if len(list) != 3 || list[0].ComicVineID.Int64 != 1443 || list[0].Issues != 3 {
		t.Fatalf("characters = %+v", list)
	}
	if counts["Venom"] != 1 || counts["Carnage"] != 1 {
		t.Errorf("counts = %v", counts)
	}

	issues, err := st.ListCharacterIssues(list[0].ID)
	if err != nil || len(issues) != 3 {
		t.Fatalf("issues = %v, %v", issues, err)
	}
	if issues[0].SeriesName != "Alpha" || issues[2].SeriesName != "Beta" {
		t.Errorf("not grouped by series: %+v", issues)
	}

	// Missing files drop out of the counts.
	if err := st.MarkIssuesMissing([]int64{i3.ID}); err != nil {
		t.Fatal(err)
	}
	list, _ = st.ListCharacters()
	if list[0].Issues != 2 {
		t.Errorf("count with a missing file = %d, want 2", list[0].Issues)
	}
	if c, err := st.GetCharacter(999); err != nil || c != nil {
		t.Errorf("GetCharacter(unknown) = %v, %v", c, err)
	}
}

func TestCreators(t *testing.T) {
	st := openTestStore(t)
	addTestIssue(t, st, "Saga", "1", func(i *Issue) {
		i.Writer = "Brian K. Vaughan, Fiona Staples"
		i.Artist = "Fiona Staples"
	})
	addTestIssue(t, st, "Saga", "2", func(i *Issue) { i.Writer = "brian k.  vaughan"; i.Artist = "Fiona Staples" })
	addTestIssue(t, st, "Paper Girls", "1", func(i *Issue) { i.Writer = "Brian K. Vaughan"; i.Artist = "Cliff Chiang" })
	gone := addTestIssue(t, st, "Paper Girls", "2", func(i *Issue) { i.Writer = "Brian K. Vaughan" })
	if err := st.MarkIssuesMissing([]int64{gone.ID}); err != nil {
		t.Fatal(err)
	}

	list, err := st.ListCreators()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("creators = %+v", list)
	}
	bkv := list[0]
	if bkv.Key != "brian k. vaughan" || bkv.Name != "Brian K. Vaughan" || bkv.Writer != 3 || bkv.Artist != 0 || bkv.Credits != 3 {
		t.Errorf("top creator = %+v", bkv)
	}
	if fs := list[1]; fs.Key != "fiona staples" || fs.Writer != 1 || fs.Artist != 2 || fs.Credits != 2 {
		t.Errorf("Fiona = %+v (an issue crediting both roles counts once)", fs)
	}

	ci, err := st.ListCreatorIssues("Fiona  STAPLES")
	if err != nil {
		t.Fatal(err)
	}
	if ci.Name != "Fiona Staples" || len(ci.Writer) != 1 || len(ci.Artist) != 2 || ci.Credits != 2 {
		t.Errorf("Fiona's issues = name %q, %d writer, %d artist, %d credits", ci.Name, len(ci.Writer), len(ci.Artist), ci.Credits)
	}
	if none, _ := st.ListCreatorIssues("nobody"); none.Name != "" || none.Credits != 0 {
		t.Errorf("unknown creator = %+v", none)
	}
	if none, _ := st.ListCreatorIssues("  "); none.Credits != 0 {
		t.Errorf("blank key = %+v", none)
	}
}
