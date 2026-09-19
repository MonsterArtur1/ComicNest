package library

import (
	"os"
	"path/filepath"
	"testing"

	"comicnest/internal/covers"
	"comicnest/internal/store"
)

// TestScanDoesNotAffectOtherLibraries guards against a real bug: Store's
// AllIssuePaths spans every library sharing the database, so a scanner must
// scope it to its own root (see Scanner.underRoot) before deciding what's
// missing — otherwise scanning library A would see library B's issues as
// "not found" and wrongly mark them missing.
func TestScanDoesNotAffectOtherLibraries(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	coversDir := filepath.Join(dir, "covers")
	if err := os.MkdirAll(coversDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := covers.New(coversDir)

	rootA := filepath.Join(dir, "LibraryA")
	rootB := filepath.Join(dir, "LibraryB")
	writeComic(t, filepath.Join(rootA, "Saga"), "Saga 001.cbz", "Saga", "1")
	writeComic(t, filepath.Join(rootB, "Y"), "Y 001.cbz", "Y The Last Man", "1")

	scA := NewScanner(st, cache, rootA)
	scB := NewScanner(st, cache, rootB)

	if err := scA.scan(); err != nil {
		t.Fatalf("scan A (first): %v", err)
	}
	if err := scB.scan(); err != nil {
		t.Fatalf("scan B (first): %v", err)
	}

	// Rescanning library A alone must not touch library B's issue.
	if err := scA.scan(); err != nil {
		t.Fatalf("scan A (second): %v", err)
	}

	missing, err := st.CountMissingIssues()
	if err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatalf("CountMissingIssues() = %d after rescanning library A alone, want 0 (library B must be untouched)", missing)
	}

	series, err := st.ListSeries("", "", store.SeriesSortName, store.SeriesFilterAll, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 {
		t.Fatalf("ListSeries(all) = %d series, want 2 (Saga + Y)", len(series))
	}
}
