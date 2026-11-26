package library

import (
	"os"
	"testing"
	"time"

	"comicnest/internal/kosync"
)

func TestScanHashesIssuesForKOReader(t *testing.T) {
	root := t.TempDir()
	path := writeComic(t, root, "Saga 001.cbz", "Saga", "1")
	sc, st := newTestScanner(t, root)
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}

	partial, err := kosync.PartialMD5(path)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := st.FindIssueByDocument(partial)
	if err != nil || issue == nil || issue.Path != path {
		t.Fatalf("lookup by content hash: %+v, %v", issue, err)
	}
	if byName, _ := st.FindIssueByDocument(kosync.FilenameMD5(path)); byName == nil || byName.ID != issue.ID {
		t.Errorf("lookup by filename hash: %+v", byName)
	}

	// Backfill: an issue without hashes (as after the upgrade) gets them on the next scan.
	if _, ok, _ := st.GetDocHashState(path); !ok {
		t.Fatal("issue not found")
	}
	if err := st.SetIssueHashes(issue.ID, "stale", "stale", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.FindIssueByDocument(partial); got == nil {
		t.Error("stale hashes not recomputed after size/mtime mismatch")
	}

	// A changed file is re-hashed.
	data, _ := os.ReadFile(path)
	data = append(data, make([]byte, 100)...) // zip trailing junk is harmless
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, time.Now(), time.Now().Add(time.Minute))
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	newPartial, _ := kosync.PartialMD5(path)
	if newPartial == partial {
		t.Fatal("appended bytes did not change the sampled content")
	}
	if got, _ := st.FindIssueByDocument(newPartial); got == nil || got.ID != issue.ID {
		t.Error("changed file not re-hashed")
	}
	if old, _ := st.FindIssueByDocument(partial); old != nil {
		t.Error("old content hash still resolves")
	}
}
