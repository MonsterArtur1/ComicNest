package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// openTestStore opens a fresh SQLite database in a temp dir.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestClearIssueComicVine(t *testing.T) {
	st := openTestStore(t)
	seriesID, err := st.FindOrCreateSeriesByFolder("Saga", "Saga")
	if err != nil {
		t.Fatal(err)
	}

	issue := &Issue{SeriesID: seriesID, Path: "Saga 001.cbz", IssueNumber: "1", MetadataSource: SourceFilename}
	if err := st.InsertIssue(issue); err != nil {
		t.Fatal(err)
	}
	issue.ComicVineIssueID = sql.NullInt64{Int64: 42, Valid: true}
	issue.ComicVineURL = "https://comicvine.gamespot.com/saga-1/4000-42/"
	issue.MetadataSource = SourceComicVine
	issue.Title = "Chapter One"
	if err := st.UpdateIssueMetadata(issue); err != nil {
		t.Fatal(err)
	}

	if err := st.ClearIssueComicVine(issue.ID); err != nil {
		t.Fatalf("ClearIssueComicVine: %v", err)
	}
	got, err := st.GetIssue(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ComicVineIssueID.Valid {
		t.Error("comicvine_issue_id should be cleared")
	}
	if got.ComicVineURL != "" {
		t.Errorf("comicvine_url should be cleared, got %q", got.ComicVineURL)
	}
	if got.MetadataSource != SourceFilename {
		t.Errorf("metadata_source = %q, want %q", got.MetadataSource, SourceFilename)
	}
	// Fields ComicVine wrote are left as they are — only the source tag and
	// id are removed.
	if got.Title != "Chapter One" {
		t.Errorf("title should survive unlinking, got %q", got.Title)
	}

	// An issue never matched to begin with is left alone (no-op, no error).
	other := &Issue{SeriesID: seriesID, Path: "Saga 002.cbz", IssueNumber: "2", MetadataSource: SourceFilename}
	if err := st.InsertIssue(other); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearIssueComicVine(other.ID); err != nil {
		t.Fatalf("ClearIssueComicVine on unmatched issue: %v", err)
	}
}

func TestClearSeriesComicVineVolume(t *testing.T) {
	st := openTestStore(t)
	seriesID, err := st.FindOrCreateSeriesByFolder("Saga", "Saga")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSeriesComicVineVolume(seriesID, 7, "https://comicvine.gamespot.com/saga/4050-7/"); err != nil {
		t.Fatal(err)
	}

	// A ComicVine-sourced, unlocked issue: rolled back by the cascade.
	unlocked := &Issue{SeriesID: seriesID, Path: "Saga 001.cbz", IssueNumber: "1", MetadataSource: SourceFilename}
	if err := st.InsertIssue(unlocked); err != nil {
		t.Fatal(err)
	}
	unlocked.ComicVineIssueID = sql.NullInt64{Int64: 100, Valid: true}
	unlocked.MetadataSource = SourceComicVine
	if err := st.UpdateIssueMetadata(unlocked); err != nil {
		t.Fatal(err)
	}

	// A ComicVine-sourced but locked issue: the user's own edit, left alone.
	locked := &Issue{SeriesID: seriesID, Path: "Saga 002.cbz", IssueNumber: "2", MetadataSource: SourceFilename}
	if err := st.InsertIssue(locked); err != nil {
		t.Fatal(err)
	}
	locked.ComicVineIssueID = sql.NullInt64{Int64: 101, Valid: true}
	locked.MetadataSource = SourceComicVine
	locked.MetadataLocked = true
	if err := st.UpdateIssueMetadata(locked); err != nil {
		t.Fatal(err)
	}

	if err := st.ClearSeriesComicVineVolume(seriesID); err != nil {
		t.Fatalf("ClearSeriesComicVineVolume: %v", err)
	}

	sr, err := st.GetSeries(seriesID)
	if err != nil {
		t.Fatal(err)
	}
	if sr.ComicVineVolumeID.Valid {
		t.Error("series comicvine_volume_id should be cleared")
	}
	if sr.ComicVineURL != "" {
		t.Errorf("series comicvine_url should be cleared, got %q", sr.ComicVineURL)
	}

	gotUnlocked, err := st.GetIssue(unlocked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotUnlocked.ComicVineIssueID.Valid || gotUnlocked.MetadataSource != SourceFilename {
		t.Errorf("unlocked issue should be rolled back, got id valid=%v source=%q",
			gotUnlocked.ComicVineIssueID.Valid, gotUnlocked.MetadataSource)
	}

	gotLocked, err := st.GetIssue(locked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !gotLocked.ComicVineIssueID.Valid || gotLocked.MetadataSource != SourceComicVine {
		t.Errorf("locked issue should be left untouched, got id valid=%v source=%q",
			gotLocked.ComicVineIssueID.Valid, gotLocked.MetadataSource)
	}
}
