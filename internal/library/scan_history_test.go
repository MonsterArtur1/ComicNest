package library

import (
	"path/filepath"
	"testing"
	"time"
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

	history, err := st.ListScanHistory(10)
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
}
