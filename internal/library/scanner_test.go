package library

import (
	"bytes"
	"database/sql"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"comicnest/internal/covers"
	"comicnest/internal/store"
)

// tinyPNG returns a 2x2 PNG so covers and page counting work on test archives.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{200, 30, 30, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// writeComic creates dir/name.cbz with one page and, when series != "", a
// ComicInfo.xml naming that series.
func writeComic(t *testing.T, dir, name, series, number string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"p001.png": tinyPNG(t)}
	if series != "" {
		files["ComicInfo.xml"] = []byte(fmt.Sprintf(
			`<?xml version="1.0"?><ComicInfo><Series>%s</Series><Number>%s</Number></ComicInfo>`, series, number))
	}
	return buildCBZ(t, dir, name, files)
}

// newTestScanner opens a fresh store and scanner over the given library root.
func newTestScanner(t *testing.T, root string) (*Scanner, *store.Store) {
	t.Helper()
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
	return NewScanner(st, covers.New(coversDir), root), st
}

// visibleSeries returns the series shown in the UI (those with issues).
func visibleSeries(t *testing.T, st *store.Store) []store.Series {
	t.Helper()
	series, err := st.ListSeries("", "", store.SeriesSortName, store.SeriesFilterAll)
	if err != nil {
		t.Fatal(err)
	}
	return series
}

// seriesMap returns visible series name → sorted issue file names and fails
// the test when two visible series share a name (the map would hide that).
func seriesMap(t *testing.T, st *store.Store) map[string][]string {
	t.Helper()
	out := make(map[string][]string)
	for _, s := range visibleSeries(t, st) {
		if _, dup := out[s.Name]; dup {
			t.Errorf("duplicate series name %q", s.Name)
		}
		issues, err := st.ListIssuesBySeries(s.ID, store.IssueFilterAll)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, i := range issues {
			names = append(names, filepath.Base(i.Path))
		}
		sort.Strings(names)
		out[s.Name] = names
	}
	return out
}

// assertSeries compares visible series (by row count, then by name → files).
func assertSeries(t *testing.T, st *store.Store, want map[string][]string) {
	t.Helper()
	if n := len(visibleSeries(t, st)); n != len(want) {
		t.Errorf("%d visible series, want %d: %v", n, len(want), seriesMap(t, st))
		return
	}
	got := seriesMap(t, st)
	for name, files := range want {
		if fmt.Sprint(got[name]) != fmt.Sprint(files) {
			t.Errorf("series %q = %v, want %v (all: %v)", name, got[name], files, got)
		}
	}
}

// seriesByName returns the visible series with that name, failing when absent.
func seriesByName(t *testing.T, st *store.Store, name string) store.Series {
	t.Helper()
	for _, s := range visibleSeries(t, st) {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no visible series %q", name)
	return store.Series{}
}

func TestScanSplitsFolderWithMixedComicInfoSeries(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Mad Max")
	writeComic(t, dir, "Mad Max - Fury Road (2015).cbz", "Mad Max: Fury Road", "1")
	writeComic(t, dir, "Mad Max - Fury Road - Max 001.cbz", "Mad Max: Fury Road: Max", "1")
	writeComic(t, dir, "Mad Max - Fury Road - Max 002.cbz", "mad max: fury road: max", "2") // case differs

	sc, st := newTestScanner(t, root)
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"Mad Max: Fury Road":      {"Mad Max - Fury Road (2015).cbz"},
		"Mad Max: Fury Road: Max": {"Mad Max - Fury Road - Max 001.cbz", "Mad Max - Fury Road - Max 002.cbz"},
	}
	assertSeries(t, st, want)

	// The folder series row still exists (hidden: no issues), the new ones are virtual.
	folder, err := st.FindOrCreateSeriesByFolder("Mad Max", "Mad Max")
	if err != nil {
		t.Fatal(err)
	}
	if issues, _ := st.ListIssuesBySeries(folder, store.IssueFilterAll); len(issues) != 0 {
		t.Errorf("folder series should be empty, has %d issues", len(issues))
	}

	// Rescan: idempotent, no duplicate series.
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	assertSeries(t, st, want)

	// A file added later to the folder lands in its ComicInfo series too.
	writeComic(t, dir, "Mad Max - Fury Road - Max 003.cbz", "Mad Max: Fury Road: Max", "3")
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	want["Mad Max: Fury Road: Max"] = append(want["Mad Max: Fury Road: Max"], "Mad Max - Fury Road - Max 003.cbz")
	assertSeries(t, st, want)
}

func TestScanKeepsFolderSeriesWhenComicInfoIsConsistent(t *testing.T) {
	root := t.TempDir()
	// Folder name differs from the ComicInfo series, but all files agree → folder wins.
	writeComic(t, filepath.Join(root, "Saga"), "Saga 001.cbz", "Saga (2012)", "1")
	writeComic(t, filepath.Join(root, "Saga"), "Saga 002.cbz", "Saga (2012)", "2")
	// Only some files carry ComicInfo → one series as well.
	writeComic(t, filepath.Join(root, "Batman"), "Batman 001.cbz", "Batman", "1")
	writeComic(t, filepath.Join(root, "Batman"), "Batman 002.cbz", "", "")
	// Root-level files keep taking their series from ComicInfo.
	writeComic(t, root, "Loose 001.cbz", "Loose Series", "1")

	sc, st := newTestScanner(t, root)
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	assertSeries(t, st, map[string][]string{
		"Saga":         {"Saga 001.cbz", "Saga 002.cbz"},
		"Batman":       {"Batman 001.cbz", "Batman 002.cbz"},
		"Loose Series": {"Loose 001.cbz"},
	})
}

func TestScanBackfillsComicInfoSeriesForOldRows(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Mixed")
	a := writeComic(t, dir, "A 001.cbz", "Series A", "1")
	b := writeComic(t, dir, "B 001.cbz", "Series B", "1")
	plain := writeComic(t, dir, "C 001.cbz", "", "")

	sc, st := newTestScanner(t, root)

	// Simulate rows catalogued before the comicinfo_series column existed:
	// inserted directly, NULL in the new column, locked metadata on one of them.
	folder, err := st.FindOrCreateSeriesByFolder("Mixed", "Mixed")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{a, b, plain} {
		is := &store.Issue{SeriesID: folder, Path: p, MetadataSource: store.SourceManual, MetadataLocked: true}
		if err := st.InsertIssue(is); err != nil {
			t.Fatal(err)
		}
		if err := st.SetIssueLocked(is.ID, true); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.GetIssue(is.ID); got.ComicInfoSeries.Valid {
			t.Fatalf("precondition: comicinfo_series should be NULL, got %v", got.ComicInfoSeries)
		}
	}

	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	assertSeries(t, st, map[string][]string{
		"Series A": {"A 001.cbz"},
		"Series B": {"B 001.cbz"},
		"Mixed":    {"C 001.cbz"},
	})
	// The column is filled for every file, "" for the one without ComicInfo,
	// and locked metadata was left untouched (backfill only records the series).
	for p, want := range map[string]sql.NullString{
		a:     {String: "Series A", Valid: true},
		b:     {String: "Series B", Valid: true},
		plain: {String: "", Valid: true},
	} {
		is, err := st.GetIssueByPath(p)
		if err != nil || is == nil {
			t.Fatalf("issue %s: %v", p, err)
		}
		if is.ComicInfoSeries != want {
			t.Errorf("%s: comicinfo_series = %v, want %v", filepath.Base(p), is.ComicInfoSeries, want)
		}
		if is.MetadataSource != store.SourceManual || !is.MetadataLocked {
			t.Errorf("%s: locked manual metadata was overwritten: %s", filepath.Base(p), is.MetadataSource)
		}
	}
}

func TestScanSplitSurvivesRenameOfVirtualSeries(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Mad Max")
	writeComic(t, dir, "Mad Max - Fury Road (2015).cbz", "Mad Max: Fury Road", "1")
	writeComic(t, dir, "Mad Max - Fury Road - Max 001.cbz", "Mad Max: Fury Road: Max", "1")

	sc, st := newTestScanner(t, root)
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	// User renames (and thereby locks) the split-out series.
	max := seriesByName(t, st, "Mad Max: Fury Road: Max")
	if err := st.UpdateSeriesManual(max.ID, "Fury Road: Max (Vertigo)", "Vertigo", "", false); err != nil {
		t.Fatal(err)
	}
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"Mad Max: Fury Road":       {"Mad Max - Fury Road (2015).cbz"},
		"Fury Road: Max (Vertigo)": {"Mad Max - Fury Road - Max 001.cbz"},
	}
	assertSeries(t, st, want)
	if got := seriesByName(t, st, "Fury Road: Max (Vertigo)"); got.ID != max.ID {
		t.Errorf("renamed series was replaced: id %d → %d", max.ID, got.ID)
	}

	// A file added later with the same ComicInfo Series joins the renamed series.
	writeComic(t, dir, "Mad Max - Fury Road - Max 002.cbz", "Mad Max: Fury Road: Max", "2")
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	want["Fury Road: Max (Vertigo)"] = append(want["Fury Road: Max (Vertigo)"], "Mad Max - Fury Road - Max 002.cbz")
	assertSeries(t, st, want)
}

func TestScanSeriesEqualToFolderNameStays(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Mad Max")
	writeComic(t, dir, "Mad Max 001.cbz", "mad max", "1") // same as folder, other case
	writeComic(t, dir, "Mad Max - Fury Road 001.cbz", "Mad Max: Fury Road", "1")
	writeComic(t, dir, "Mad Max Extra.cbz", "", "")

	sc, st := newTestScanner(t, root)
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	assertSeries(t, st, map[string][]string{
		"Mad Max":            {"Mad Max 001.cbz", "Mad Max Extra.cbz"},
		"Mad Max: Fury Road": {"Mad Max - Fury Road 001.cbz"},
	})
	if s := seriesByName(t, st, "Mad Max"); !s.FolderPath.Valid {
		t.Error("\"Mad Max\" should be the folder series, not a virtual duplicate")
	}
}

func TestScanCorruptArchiveLeavesComicInfoSeriesNull(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Broken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "Broken 001.cbz")
	if err := os.WriteFile(bad, []byte("this is not a zip file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeComic(t, dir, "Broken 002.cbz", "", "")

	sc, st := newTestScanner(t, root)
	for pass := 1; pass <= 2; pass++ { // add, then refresh: both must keep NULL
		if err := sc.scan(); err != nil {
			t.Fatal(err)
		}
		is, err := st.GetIssueByPath(bad)
		if err != nil || is == nil {
			t.Fatalf("pass %d: corrupt file not catalogued: %v", pass, err)
		}
		if is.ComicInfoSeries.Valid {
			t.Errorf("pass %d: corrupt archive should leave comicinfo_series NULL, got %q", pass, is.ComicInfoSeries.String)
		}
		ok, _ := st.GetIssueByPath(filepath.Join(dir, "Broken 002.cbz"))
		if ok == nil || ok.ComicInfoSeries != (sql.NullString{String: "", Valid: true}) {
			t.Errorf("pass %d: readable archive without ComicInfo should record '': %+v", pass, ok.ComicInfoSeries)
		}
	}
}
