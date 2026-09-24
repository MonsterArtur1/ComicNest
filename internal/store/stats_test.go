package store

import "testing"

// TestLibraryStats builds a small library covering every counted case
// (locked, one-shot, ComicVine-sourced, missing) and checks the aggregates
// LibraryStats reports.
func TestLibraryStats(t *testing.T) {
	st := openTestStore(t)

	regular, err := st.FindOrCreateSeriesByFolder("Saga", "Saga", "")
	if err != nil {
		t.Fatal(err)
	}
	locked, err := st.FindOrCreateSeriesByFolder("Locke & Key", "Locke & Key", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSeriesLocked(locked, true); err != nil {
		t.Fatal(err)
	}
	oneShot, err := st.FindOrCreateSeriesByFolder("Deadpool Killogy", "Deadpool Killogy", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSeriesOneShot(oneShot, true); err != nil {
		t.Fatal(err)
	}
	// An empty series (no issues) must not be counted.
	if _, err := st.FindOrCreateSeriesByFolder("Empty", "Empty", ""); err != nil {
		t.Fatal(err)
	}

	present := &Issue{SeriesID: regular, Path: "Saga/Saga 001.cbz", FileSize: 100,
		IssueNumber: "1", MetadataSource: SourceFilename}
	if err := st.InsertIssue(present); err != nil {
		t.Fatal(err)
	}
	cv := &Issue{SeriesID: regular, Path: "Saga/Saga 002.cbz", FileSize: 200,
		IssueNumber: "2", MetadataSource: SourceComicVine}
	if err := st.InsertIssue(cv); err != nil {
		t.Fatal(err)
	}
	lockedIssue := &Issue{SeriesID: locked, Path: "Locke & Key/Locke & Key 001.cbz", FileSize: 50,
		IssueNumber: "1", MetadataSource: SourceManual}
	if err := st.InsertIssue(lockedIssue); err != nil {
		t.Fatal(err)
	}
	if err := st.SetIssueLocked(lockedIssue.ID, true); err != nil {
		t.Fatal(err)
	}
	oneShotIssue := &Issue{SeriesID: oneShot, Path: "Deadpool Killogy/Deadpool Killogy.cbz", FileSize: 300,
		MetadataSource: SourceComicInfo}
	if err := st.InsertIssue(oneShotIssue); err != nil {
		t.Fatal(err)
	}
	missing := &Issue{SeriesID: regular, Path: "Saga/Saga 003.cbz", FileSize: 999,
		IssueNumber: "3", MetadataSource: SourceFilename}
	if err := st.InsertIssue(missing); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkIssuesMissing([]int64{missing.ID}); err != nil {
		t.Fatal(err)
	}

	stats, err := st.LibraryStats("")
	if err != nil {
		t.Fatalf("LibraryStats: %v", err)
	}

	want := LibraryStats{
		SeriesCount:       3, // regular, locked, oneShot — "Empty" excluded
		OneShotCount:      1,
		LockedSeriesCount: 1,
		IssueCount:        4, // present, cv, lockedIssue, oneShotIssue
		MissingCount:      1,
		TotalSize:         100 + 200 + 50 + 300,
		ComicVineCount:    1,
		NoMetadataCount:   2, // present (filename) + oneShotIssue (comicinfo)
		LockedIssueCount:  1,
	}
	if stats != want {
		t.Errorf("LibraryStats() = %+v, want %+v", stats, want)
	}
}

// TestLibraryStatsScoping checks that LibraryStats("") aggregates every
// library while LibraryStats(path) counts only the series stamped with that
// library's root path (see Store.FindOrCreateSeriesBy*).
func TestLibraryStatsScoping(t *testing.T) {
	st := openTestStore(t)

	a, err := st.FindOrCreateSeriesByFolder("A-Series", "A-Series", "/libA")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertIssue(&Issue{SeriesID: a, Path: "/libA/A-Series/1.cbz", FileSize: 10, IssueNumber: "1"}); err != nil {
		t.Fatal(err)
	}
	b, err := st.FindOrCreateSeriesByFolder("B-Series", "B-Series", "/libB")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertIssue(&Issue{SeriesID: b, Path: "/libB/B-Series/1.cbz", FileSize: 20, IssueNumber: "1"}); err != nil {
		t.Fatal(err)
	}

	statsA, err := st.LibraryStats("/libA")
	if err != nil {
		t.Fatal(err)
	}
	if statsA.SeriesCount != 1 || statsA.IssueCount != 1 || statsA.TotalSize != 10 {
		t.Errorf("LibraryStats(/libA) = %+v, want just A's series", statsA)
	}

	all, err := st.LibraryStats("")
	if err != nil {
		t.Fatal(err)
	}
	if all.SeriesCount != 2 || all.IssueCount != 2 || all.TotalSize != 30 {
		t.Errorf("LibraryStats(\"\") = %+v, want both libraries combined", all)
	}
}

func TestUserStats(t *testing.T) {
	st := openTestStore(t)
	add := func(series, path, writer string, pages int) *Issue {
		t.Helper()
		id, err := st.FindOrCreateSeriesByFolder(series, series, "")
		if err != nil {
			t.Fatal(err)
		}
		is := &Issue{SeriesID: id, Path: path, Writer: writer, Publisher: "Image",
			MetadataSource: SourceFilename, FilePages: pages}
		if err := st.InsertIssue(is); err != nil {
			t.Fatal(err)
		}
		return is
	}
	progress := func(user string, is *Issue, page int) {
		t.Helper()
		if err := st.SetReadingProgress(user, is.ID, page); err != nil {
			t.Fatal(err)
		}
	}

	saga1 := add("Saga", "Saga 001.cbz", "Brian K. Vaughan, Fiona Staples", 20)
	saga2 := add("Saga", "Saga 002.cbz", "Brian K. Vaughan", 20)
	saga3 := add("Saga", "Saga 003.cbz", "Brian K. Vaughan", 20)
	paper := add("Paper Girls", "Paper Girls 001.cbz", "brian k. vaughan", 10)
	add("Monstress", "Monstress 001.cbz", "Marjorie Liu", 30) // never opened
	gone := add("Monstress", "Monstress 002.cbz", "Marjorie Liu", 30)

	progress("ania", saga1, 20)
	progress("ania", saga2, 20)
	progress("ania", saga3, 5) // in progress
	progress("ania", paper, 10)
	progress("ania", gone, 30) // finished, but the file is gone — ignored
	if err := st.MarkIssuesMissing([]int64{gone.ID}); err != nil {
		t.Fatal(err)
	}
	progress("bartek", saga3, 20) // someone else's progress — ignored
	if err := st.SetIssueFavorite("ania", saga1.ID, true); err != nil {
		t.Fatal(err)
	}

	got, err := st.UserStats("ania")
	if err != nil {
		t.Fatalf("UserStats: %v", err)
	}
	if got.IssuesRead != 3 || got.InProgress != 1 || got.PagesRead != 55 ||
		got.SeriesStarted != 2 || got.SeriesCompleted != 1 || got.Favorites != 1 {
		t.Errorf("UserStats counts = %+v, want read 3, in progress 1, pages 55, series 2 started / 1 completed, 1 favorite", got)
	}
	if len(got.TopSeries) != 2 || got.TopSeries[0].Name != "Saga" || got.TopSeries[0].Count != 2 || got.TopSeries[0].ID != saga1.SeriesID {
		t.Errorf("TopSeries = %+v, want Saga (2) first", got.TopSeries)
	}
	if len(got.TopPublishers) != 1 || got.TopPublishers[0] != (NamedCount{Name: "Image", Count: 3}) {
		t.Errorf("TopPublishers = %+v, want just Image (3)", got.TopPublishers)
	}
	wantWriters := []NamedCount{{Name: "Brian K. Vaughan", Count: 3}, {Name: "Fiona Staples", Count: 1}}
	if len(got.TopWriters) != 2 || got.TopWriters[0] != wantWriters[0] || got.TopWriters[1] != wantWriters[1] {
		t.Errorf("TopWriters = %+v, want %+v (credits split on commas, merged case-insensitively)", got.TopWriters, wantWriters)
	}

	empty, err := st.UserStats("nobody")
	if err != nil || empty.IssuesRead != 0 || empty.SeriesStarted != 0 || len(empty.TopSeries) != 0 {
		t.Errorf("UserStats(nobody) = %+v, %v, want all zero", empty, err)
	}
}
