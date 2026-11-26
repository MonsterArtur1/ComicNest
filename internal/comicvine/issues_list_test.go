package comicvine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRecentIssuesBatchesVolumes(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/issues/" {
			t.Errorf("path = %q", r.URL.Path)
		}
		q := r.URL.Query()
		filter := q.Get("filter")
		if !strings.HasSuffix(filter, ",store_date:2026-11-01|2026-12-31") || !strings.HasPrefix(filter, "volume:") {
			t.Errorf("filter = %q", filter)
		}
		if !strings.Contains(q.Get("field_list"), "store_date") {
			t.Errorf("field_list = %q", q.Get("field_list"))
		}
		ids := strings.Split(strings.TrimPrefix(strings.Split(filter, ",")[0], "volume:"), "|")
		if len(ids) > volumeBatch {
			t.Errorf("batch of %d volume ids exceeds %d", len(ids), volumeBatch)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"error":"OK","status_code":1,"results":[
			{"id":900,"name":"Hello","issue_number":"7","cover_date":"2026-12-01","store_date":"2026-11-26 00:00:00","site_detail_url":"http://cv/900","volume":{"id":%s}}]}`, ids[0])
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	vols := make([]int, 45) // 45 ids → two requests
	for i := range vols {
		vols[i] = 100 + i
	}
	got, err := c.RecentIssues(vols, "2026-11-01", "2026-12-31")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(got) != 2 {
		t.Fatalf("calls=%d issues=%d", calls.Load(), len(got))
	}
	if got[0].VolumeID != 100 || got[1].VolumeID != 140 || got[0].StoreDate != "2026-11-26" ||
		got[0].IssueNumber != "7" || got[0].URL != "http://cv/900" {
		t.Errorf("decoded: %+v", got)
	}
}

func TestGetVolumeIssuesPaginates(t *testing.T) {
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("filter") != "volume:42" {
			t.Errorf("filter = %q", q.Get("filter"))
		}
		offsets = append(offsets, q.Get("offset"))
		n := 100
		if q.Get("offset") == "100" {
			n = 3
		}
		var items []string
		for i := 0; i < n; i++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"issue_number":"%d","name":"","cover_date":"","store_date":""}`, i+1, i+1))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"error":"OK","status_code":1,"results":[%s]}`, strings.Join(items, ","))
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv).GetVolumeIssues(42)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 103 || strings.Join(offsets, ",") != "0,100" {
		t.Fatalf("issues=%d offsets=%v", len(got), offsets)
	}
	if got[0].VolumeID != 42 { // no volume object in the answer: defaults to the asked volume
		t.Errorf("volume id: %+v", got[0])
	}
}

func TestListIssuesNoKeyAndErrors(t *testing.T) {
	if _, err := New("").RecentIssues([]int{1}, "a", "b"); err != ErrNoKey {
		t.Errorf("RecentIssues no key: %v", err)
	}
	if _, err := New("").GetVolumeIssues(1); err != ErrNoKey {
		t.Errorf("GetVolumeIssues no key: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"error":"Rate limit","status_code":107,"results":[]}`)
	}))
	defer srv.Close()
	if _, err := newTestClient(t, srv).RecentIssues([]int{1}, "a", "b"); err == nil || !strings.Contains(err.Error(), "Rate limit") {
		t.Errorf("api error: %v", err)
	}
}
