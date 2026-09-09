package library

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"comicnest/internal/covers"
	"comicnest/internal/store"
)

// TestResetIssueMetadataFilenameOnly checks that a bad ComicVine match is
// fully forgotten: every field it wrote reverts to what the filename alone
// says, same as a freshly scanned file with no ComicInfo.xml.
func TestResetIssueMetadataFilenameOnly(t *testing.T) {
	dir := t.TempDir()
	path := writeComic(t, dir, "Saga 001.cbz", "", "")

	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	seriesID, err := st.FindOrCreateSeriesByFolder("Saga", "Saga")
	if err != nil {
		t.Fatal(err)
	}

	issue := &store.Issue{
		SeriesID: seriesID, Path: path, IssueNumber: "1", Title: "A Bad Match",
		Summary: "Wrong summary entirely.", Writer: "Wrong Writer", Artist: "Wrong Artist",
		Publisher: "Wrong Publisher", PageCount: 99, FilePages: 1,
		ComicVineIssueID: sql.NullInt64{Int64: 4242, Valid: true},
		ComicVineURL:     "https://comicvine.gamespot.com/wrong-series/4000-4242/",
		MetadataSource:   store.SourceComicVine,
	}
	if err := st.InsertIssue(issue); err != nil {
		t.Fatal(err)
	}
	// InsertIssue doesn't persist the ComicVine columns; write them the way a
	// real scrape would.
	if err := st.UpdateIssueMetadata(issue); err != nil {
		t.Fatal(err)
	}

	coversDir := filepath.Join(t.TempDir(), "covers")
	if err := os.MkdirAll(coversDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := covers.New(coversDir)

	if err := ResetIssueMetadata(st, cache, issue); err != nil {
		t.Fatalf("ResetIssueMetadata: %v", err)
	}

	if issue.ComicVineIssueID.Valid || issue.ComicVineURL != "" {
		t.Errorf("ComicVine link should be cleared, got id=%v url=%q", issue.ComicVineIssueID, issue.ComicVineURL)
	}
	if issue.MetadataSource != store.SourceFilename {
		t.Errorf("MetadataSource = %q, want %q", issue.MetadataSource, store.SourceFilename)
	}
	if issue.Summary != "" || issue.Writer != "" || issue.Artist != "" || issue.Publisher != "" {
		t.Errorf("ComicVine-only fields should be blanked, got summary=%q writer=%q artist=%q publisher=%q",
			issue.Summary, issue.Writer, issue.Artist, issue.Publisher)
	}
	if issue.IssueNumber != "001" {
		t.Errorf("IssueNumber = %q, want %q (from the filename)", issue.IssueNumber, "001")
	}
	if !cache.Has(issue.ID) {
		t.Error("cover should have been re-extracted from the archive")
	}

	got, err := st.GetIssue(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ComicVineIssueID.Valid || got.Summary != "" || got.MetadataSource != store.SourceFilename {
		t.Errorf("reset should be persisted, got %+v", got)
	}
}

// TestResetIssueMetadataFallsBackToComicInfo checks that when the archive
// carries its own ComicInfo.xml, the reset lands on that instead of the bare
// filename — the same priority a fresh scan would give it.
func TestResetIssueMetadataFallsBackToComicInfo(t *testing.T) {
	dir := t.TempDir()
	path := writeComic(t, dir, "Saga 001.cbz", "Saga", "1")

	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	seriesID, err := st.FindOrCreateSeriesByFolder("Saga", "Saga")
	if err != nil {
		t.Fatal(err)
	}

	issue := &store.Issue{
		SeriesID: seriesID, Path: path, IssueNumber: "1", Title: "A Bad Match",
		ComicVineIssueID: sql.NullInt64{Int64: 4242, Valid: true},
		MetadataSource:   store.SourceComicVine,
	}
	if err := st.InsertIssue(issue); err != nil {
		t.Fatal(err)
	}

	coversDir := filepath.Join(t.TempDir(), "covers")
	if err := os.MkdirAll(coversDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := covers.New(coversDir)

	if err := ResetIssueMetadata(st, cache, issue); err != nil {
		t.Fatalf("ResetIssueMetadata: %v", err)
	}
	if issue.MetadataSource != store.SourceComicInfo {
		t.Errorf("MetadataSource = %q, want %q (ComicInfo.xml is present)", issue.MetadataSource, store.SourceComicInfo)
	}
	if !issue.HasComicInfo {
		t.Error("HasComicInfo should be true")
	}
}
