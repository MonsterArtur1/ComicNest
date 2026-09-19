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

func TestListSeriesComicVineIssues(t *testing.T) {
	st := openTestStore(t)
	seriesID, err := st.FindOrCreateSeriesByFolder("Saga", "Saga", "")
	if err != nil {
		t.Fatal(err)
	}

	// A ComicVine-sourced, unlocked issue: returned for rollback.
	unlocked := &Issue{SeriesID: seriesID, Path: "Saga 001.cbz", IssueNumber: "1", MetadataSource: SourceFilename}
	if err := st.InsertIssue(unlocked); err != nil {
		t.Fatal(err)
	}
	unlocked.ComicVineIssueID = sql.NullInt64{Int64: 100, Valid: true}
	unlocked.MetadataSource = SourceComicVine
	if err := st.UpdateIssueMetadata(unlocked); err != nil {
		t.Fatal(err)
	}

	// A ComicVine-sourced but locked issue: the user's own edit, excluded.
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

	// An issue that was never matched: excluded.
	never := &Issue{SeriesID: seriesID, Path: "Saga 003.cbz", IssueNumber: "3", MetadataSource: SourceFilename}
	if err := st.InsertIssue(never); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListSeriesComicVineIssues(seriesID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != unlocked.ID {
		t.Fatalf("ListSeriesComicVineIssues = %+v, want just the unlocked issue", got)
	}
}

func TestClearSeriesComicVineVolume(t *testing.T) {
	st := openTestStore(t)
	seriesID, err := st.FindOrCreateSeriesByFolder("Saga", "Saga", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSeriesComicVineVolume(seriesID, 7, "https://comicvine.gamespot.com/saga/4050-7/"); err != nil {
		t.Fatal(err)
	}
	if err := st.EnrichSeriesFromComicVine(seriesID, "Saga", "Image", "A space opera."); err != nil {
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
	if sr.Publisher != "" || sr.Description != "" {
		t.Errorf("unlocked series' publisher/description should be cleared, got %q / %q", sr.Publisher, sr.Description)
	}
}

func TestClearSeriesComicVineVolumeLocked(t *testing.T) {
	st := openTestStore(t)
	seriesID, err := st.FindOrCreateSeriesByFolder("Saga", "Saga", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSeriesComicVineVolume(seriesID, 7, "https://comicvine.gamespot.com/saga/4050-7/"); err != nil {
		t.Fatal(err)
	}
	// A manual edit locks the series and sets its own publisher/description.
	if err := st.UpdateSeriesManual(seriesID, "Saga", "Image Comics", "My own summary.", false); err != nil {
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
		t.Error("series comicvine_volume_id should be cleared even when locked")
	}
	if sr.Publisher != "Image Comics" || sr.Description != "My own summary." {
		t.Errorf("locked series' publisher/description should survive, got %q / %q", sr.Publisher, sr.Description)
	}
}
