package store

import "testing"

// TestFinishedAtTimes checks that only present-on-disk issues actually read
// to their last page count, scoped to the requesting user.
func TestFinishedAtTimes(t *testing.T) {
	st := openTestStore(t)
	seriesID, err := st.FindOrCreateSeriesByFolder("Saga", "Saga")
	if err != nil {
		t.Fatal(err)
	}

	finished := &Issue{SeriesID: seriesID, Path: "Saga 001.cbz", IssueNumber: "1",
		MetadataSource: SourceFilename, FilePages: 20}
	if err := st.InsertIssue(finished); err != nil {
		t.Fatal(err)
	}
	if err := st.SetReadingProgress("ania", finished.ID, 20); err != nil {
		t.Fatal(err)
	}

	inProgress := &Issue{SeriesID: seriesID, Path: "Saga 002.cbz", IssueNumber: "2",
		MetadataSource: SourceFilename, FilePages: 20}
	if err := st.InsertIssue(inProgress); err != nil {
		t.Fatal(err)
	}
	if err := st.SetReadingProgress("ania", inProgress.ID, 5); err != nil {
		t.Fatal(err)
	}

	// Finished, but the file has since disappeared — should not count.
	missing := &Issue{SeriesID: seriesID, Path: "Saga 003.cbz", IssueNumber: "3",
		MetadataSource: SourceFilename, FilePages: 20}
	if err := st.InsertIssue(missing); err != nil {
		t.Fatal(err)
	}
	if err := st.SetReadingProgress("ania", missing.ID, 20); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkIssuesMissing([]int64{missing.ID}); err != nil {
		t.Fatal(err)
	}

	// Finished by a different user — should not count for "ania".
	otherUsers := &Issue{SeriesID: seriesID, Path: "Saga 004.cbz", IssueNumber: "4",
		MetadataSource: SourceFilename, FilePages: 20}
	if err := st.InsertIssue(otherUsers); err != nil {
		t.Fatal(err)
	}
	if err := st.SetReadingProgress("bartek", otherUsers.ID, 20); err != nil {
		t.Fatal(err)
	}

	times, err := st.FinishedAtTimes("ania")
	if err != nil {
		t.Fatalf("FinishedAtTimes: %v", err)
	}
	if len(times) != 1 {
		t.Fatalf("FinishedAtTimes(ania) = %v, want exactly the one finished+present issue", times)
	}

	// bartek finished a different issue than ania — scoped independently.
	if times, err := st.FinishedAtTimes("bartek"); err != nil || len(times) != 1 {
		t.Errorf("FinishedAtTimes(bartek) = %v, %v, want exactly bartek's own finished issue", times, err)
	}
}
