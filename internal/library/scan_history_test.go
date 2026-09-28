package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"comicnest/internal/store"
)

// TestScanRecordsHistory checks that a full Start()/run() cycle (not the bare
// sc.scan() call the other scanner tests use) appends one row to the store's
// scan history, matching the counts from the seeded test library.
func TestScanRecordsHistory(t *testing.T) {
	root := t.TempDir()
	writeComic(t, filepath.Join(root, "Saga"), "Saga 001.cbz", "Saga (2012)", "1")
	writeComic(t, filepath.Join(root, "Saga"), "Saga 002.cbz", "Saga (2012)", "2")

	sc, st := newTestScanner(t, root)
	if err := sc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sc.Status().Finished {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sc.Status().Finished {
		t.Fatal("scan did not finish within 2s")
	}

	history, err := st.ListScanHistory(10, "")
	if err != nil {
		t.Fatalf("ListScanHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("got %d scan history rows, want 1: %+v", len(history), history)
	}
	h := history[0]
	if h.Found != 2 || h.Processed != 2 || h.Missing != 0 {
		t.Errorf("history row = %+v, want Found=2 Processed=2 Missing=0", h)
	}
	if h.Err != "" {
		t.Errorf("history row Err = %q, want empty", h.Err)
	}
	if !h.Detailed || h.Added != 2 || h.AddedBytes <= 0 || h.SeriesAdded != 1 || h.ProblemCount != 0 {
		t.Errorf("history row = %+v, want Detailed, Added=2, AddedBytes>0, SeriesAdded=1, no problems", h)
	}
}

// runScan starts a scan and waits for it (and its history row) to finish.
func runScan(t *testing.T, sc *Scanner, st *store.Store) store.ScanHistoryEntry {
	t.Helper()
	if err := sc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for sc.Status().Running {
		if time.Now().After(deadline) {
			t.Fatal("scan did not finish within 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	history, err := st.ListScanHistory(1, "")
	if err != nil || len(history) == 0 {
		t.Fatalf("ListScanHistory: %v, %d rows", err, len(history))
	}
	return history[0]
}

// TestScanHistoryChanges checks the per-scan change counters across rescans:
// newly missing files are told apart from already-missing ones, files that
// come back count as restored, and a broken archive is catalogued but listed
// as a problem with its library-relative path.
func TestScanHistoryChanges(t *testing.T) {
	root := t.TempDir()
	one := writeComic(t, filepath.Join(root, "Saga"), "Saga 001.cbz", "Saga (2012)", "1")
	writeComic(t, filepath.Join(root, "Saga"), "Saga 002.cbz", "Saga (2012)", "2")
	sc, st := newTestScanner(t, root)
	runScan(t, sc, st)

	// Second scan: one file gone, one broken file new.
	saved, err := os.ReadFile(one)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(one); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Saga", "Saga 003.cbz"), []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := runScan(t, sc, st)
	if h.Added != 1 || h.NewlyMissing != 1 || h.Missing != 1 || h.Restored != 0 || h.SeriesAdded != 0 {
		t.Errorf("second scan = %+v, want Added=1 NewlyMissing=1 Missing=1 Restored=0 SeriesAdded=0", h)
	}
	if h.ProblemCount == 0 || len(h.Problems) != h.ProblemCount {
		t.Fatalf("second scan problems = %d %+v, want some, all kept", h.ProblemCount, h.Problems)
	}
	for _, p := range h.Problems {
		if p.Path != "Saga/Saga 003.cbz" || !strings.HasSuffix(p.Message, "zip: not a valid zip file") ||
			strings.Contains(p.Message, root) || strings.Contains(p.Message, " from:") || strings.Contains(p.Message, " in:") {
			t.Errorf("problem = %+v, want the broken file (relative path) with a path-free message", p)
		}
	}

	// Third scan: nothing changed — the file stays missing but isn't new news.
	h = runScan(t, sc, st)
	if h.Added != 0 || h.NewlyMissing != 0 || h.Missing != 1 {
		t.Errorf("third scan = %+v, want Added=0 NewlyMissing=0 Missing=1", h)
	}

	// Fourth scan: the missing file is back.
	if err := os.WriteFile(one, saved, 0o644); err != nil {
		t.Fatal(err)
	}
	h = runScan(t, sc, st)
	if h.Restored != 1 || h.Added != 0 || h.Missing != 0 {
		t.Errorf("fourth scan = %+v, want Restored=1 Added=0 Missing=0", h)
	}
}
