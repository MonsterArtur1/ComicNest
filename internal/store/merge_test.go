package store

import "testing"

// TestMergeSeriesFolderSurvivesRescan reproduces the bug where merging two
// folder-backed series looked fine until the next library scan: since
// MergeSeries deleted the source series row outright, a rescan's
// FindOrCreateSeriesByFolder found no row for that folder anymore and
// created a brand new series, silently undoing the merge.
func TestMergeSeriesFolderSurvivesRescan(t *testing.T) {
	st := openTestStore(t)

	fromID, err := st.FindOrCreateSeriesByFolder("Mad Max Fury Road", "Mad Max Fury Road", "")
	if err != nil {
		t.Fatal(err)
	}
	intoID, err := st.FindOrCreateSeriesByFolder("Mad Max", "Mad Max", "")
	if err != nil {
		t.Fatal(err)
	}
	issue := &Issue{SeriesID: fromID, Path: "Mad Max Fury Road/Mad Max Fury Road 001.cbz", IssueNumber: "1"}
	if err := st.InsertIssue(issue); err != nil {
		t.Fatal(err)
	}

	if err := st.MergeSeries(fromID, intoID); err != nil {
		t.Fatalf("MergeSeries: %v", err)
	}

	// A rescan re-resolves the same folder — it must land back on intoID,
	// not recreate a series for the now-deleted fromID.
	resolved, err := st.FindOrCreateSeriesByFolder("Mad Max Fury Road", "Mad Max Fury Road", "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != intoID {
		t.Errorf("rescan resolved folder to series %d, want the merge target %d (merge was undone)", resolved, intoID)
	}

	got, err := st.GetIssue(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SeriesID != intoID {
		t.Errorf("issue series = %d, want %d", got.SeriesID, intoID)
	}

	if sr, err := st.GetSeries(fromID); err != nil || sr != nil {
		t.Errorf("source series should stay gone, got %+v (err %v)", sr, err)
	}
}

// TestMergeSeriesNameSurvivesRescan is the same scenario for two virtual
// (folder-less) series, resolved by name instead of by folder.
func TestMergeSeriesNameSurvivesRescan(t *testing.T) {
	st := openTestStore(t)

	fromID, err := st.FindOrCreateSeriesByName("The Boys: Dear Becky", "")
	if err != nil {
		t.Fatal(err)
	}
	intoID, err := st.FindOrCreateSeriesByName("The Boys Dear Becky", "")
	if err != nil {
		t.Fatal(err)
	}
	issue := &Issue{SeriesID: fromID, Path: "The Boys Dear Becky 01.cbz", IssueNumber: "1"}
	if err := st.InsertIssue(issue); err != nil {
		t.Fatal(err)
	}

	if err := st.MergeSeries(fromID, intoID); err != nil {
		t.Fatalf("MergeSeries: %v", err)
	}

	resolved, err := st.FindOrCreateSeriesByName("The Boys: Dear Becky", "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != intoID {
		t.Errorf("rescan resolved name to series %d, want the merge target %d (merge was undone)", resolved, intoID)
	}
}

// TestMergeSeriesChainedAliasesFollow checks that merging C into B after A
// was already merged into B keeps A's alias pointing at whatever B is now
// merged into.
func TestMergeSeriesChainedAliasesFollow(t *testing.T) {
	st := openTestStore(t)

	a, err := st.FindOrCreateSeriesByFolder("A", "A", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.FindOrCreateSeriesByFolder("B", "B", "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.FindOrCreateSeriesByFolder("C", "C", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := st.MergeSeries(a, b); err != nil {
		t.Fatalf("merge A into B: %v", err)
	}
	if err := st.MergeSeries(b, c); err != nil {
		t.Fatalf("merge B into C: %v", err)
	}

	resolved, err := st.FindOrCreateSeriesByFolder("A", "A", "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != c {
		t.Errorf("folder A resolved to %d, want the final target %d", resolved, c)
	}
	resolvedB, err := st.FindOrCreateSeriesByFolder("B", "B", "")
	if err != nil {
		t.Fatal(err)
	}
	if resolvedB != c {
		t.Errorf("folder B resolved to %d, want the final target %d", resolvedB, c)
	}
}
