package store

import "testing"

func TestScanHistory(t *testing.T) {
	st := openTestStore(t)

	entries := []ScanHistoryEntry{
		{StartedAt: "2024-01-01 10:00:00", FinishedAt: "2024-01-01 10:00:05", Found: 3, Processed: 3, Missing: 0},
		{StartedAt: "2024-01-02 10:00:00", FinishedAt: "2024-01-02 10:00:07", Found: 4, Processed: 4, Missing: 1, CVUpdated: 2},
		{StartedAt: "2024-01-03 10:00:00", FinishedAt: "2024-01-03 10:00:09", Found: 5, Processed: 5, Missing: 0, CVUpdated: 1, CVFailed: 1, Err: "boom"},
	}
	for _, e := range entries {
		if err := st.RecordScanHistory(e); err != nil {
			t.Fatalf("RecordScanHistory: %v", err)
		}
	}

	got, err := st.ListScanHistory(10)
	if err != nil {
		t.Fatalf("ListScanHistory: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	// Newest first.
	if got[0].StartedAt != "2024-01-03 10:00:00" || got[0].Err != "boom" || got[0].CVUpdated != 1 || got[0].CVFailed != 1 {
		t.Errorf("got[0] = %+v, want the third (newest) entry", got[0])
	}
	if got[2].StartedAt != "2024-01-01 10:00:00" {
		t.Errorf("got[2] = %+v, want the first (oldest) entry", got[2])
	}

	limited, err := st.ListScanHistory(2)
	if err != nil {
		t.Fatalf("ListScanHistory(2): %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("got %d entries with limit 2, want 2", len(limited))
	}
	if limited[0].StartedAt != "2024-01-03 10:00:00" || limited[1].StartedAt != "2024-01-02 10:00:00" {
		t.Errorf("limited entries not newest-first: %+v", limited)
	}
}
