package store

import "testing"

// TestListSeriesLibraryScoping checks that ListSeries scopes to one library
// when given its root path, and returns every library's series when given ""
// ("all libraries").
func TestListSeriesLibraryScoping(t *testing.T) {
	st := openTestStore(t)

	a, err := st.FindOrCreateSeriesByFolder("A-Series", "A-Series", "/libA")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertIssue(&Issue{SeriesID: a, Path: "/libA/A-Series/1.cbz", IssueNumber: "1"}); err != nil {
		t.Fatal(err)
	}
	b, err := st.FindOrCreateSeriesByFolder("B-Series", "B-Series", "/libB")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertIssue(&Issue{SeriesID: b, Path: "/libB/B-Series/1.cbz", IssueNumber: "1"}); err != nil {
		t.Fatal(err)
	}

	onlyA, err := st.ListSeries("", "", SeriesSortName, SeriesFilterAll, "/libA")
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyA) != 1 || onlyA[0].ID != a {
		t.Errorf("ListSeries(library=/libA) = %+v, want just A-Series", onlyA)
	}

	both, err := st.ListSeries("", "", SeriesSortName, SeriesFilterAll, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(both) != 2 {
		t.Errorf("ListSeries(library=\"\") returned %d series, want 2", len(both))
	}
}

// TestMarkOrphanedLibrariesMissing checks that removing a library from the
// configured list flags its issues missing, leaves a still-configured
// library's issues alone, and never touches an unstamped (”) series — and
// that an empty configured list (nothing configured at all) is a no-op
// rather than flagging everything.
func TestMarkOrphanedLibrariesMissing(t *testing.T) {
	st := openTestStore(t)

	kept, err := st.FindOrCreateSeriesByFolder("Kept", "Kept", "/libA")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertIssue(&Issue{SeriesID: kept, Path: "/libA/Kept/1.cbz", IssueNumber: "1"}); err != nil {
		t.Fatal(err)
	}
	removed, err := st.FindOrCreateSeriesByFolder("Removed", "Removed", "/libB")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertIssue(&Issue{SeriesID: removed, Path: "/libB/Removed/1.cbz", IssueNumber: "1"}); err != nil {
		t.Fatal(err)
	}
	unstamped, err := st.FindOrCreateSeriesByName("Unstamped", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertIssue(&Issue{SeriesID: unstamped, Path: "/somewhere/1.cbz", IssueNumber: "1"}); err != nil {
		t.Fatal(err)
	}

	// Nothing configured at all: a no-op, not a mass "missing" flag.
	if n, err := st.MarkOrphanedLibrariesMissing(nil); err != nil || n != 0 {
		t.Fatalf("MarkOrphanedLibrariesMissing(nil) = %d, %v, want 0, nil", n, err)
	}

	// Only /libA is configured now: /libB's issue is orphaned, /libA's and
	// the unstamped one are not.
	n, err := st.MarkOrphanedLibrariesMissing([]string{"/libA"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("MarkOrphanedLibrariesMissing([/libA]) flagged %d issue(s), want 1", n)
	}

	missing, err := st.CountMissingIssues()
	if err != nil {
		t.Fatal(err)
	}
	if missing != 1 {
		t.Fatalf("CountMissingIssues() = %d, want 1", missing)
	}

	all, err := st.ListSeries("", "", SeriesSortName, SeriesFilterAll, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, sr := range all {
		switch sr.ID {
		case kept:
			if sr.IssuesPresent != 1 {
				t.Errorf("Kept series should still be present, got IssuesPresent=%d", sr.IssuesPresent)
			}
		case removed:
			if sr.IssuesPresent != 0 {
				t.Errorf("Removed series' issue should now be missing, got IssuesPresent=%d", sr.IssuesPresent)
			}
		case unstamped:
			if sr.IssuesPresent != 1 {
				t.Errorf("Unstamped series should be untouched, got IssuesPresent=%d", sr.IssuesPresent)
			}
		}
	}

	// Idempotent: running it again flags nothing new.
	if n, err := st.MarkOrphanedLibrariesMissing([]string{"/libA"}); err != nil || n != 0 {
		t.Errorf("second MarkOrphanedLibrariesMissing([/libA]) = %d, %v, want 0, nil", n, err)
	}
}
