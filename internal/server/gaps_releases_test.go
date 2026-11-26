package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"comicnest/internal/comicvine"
	"comicnest/internal/store"
)

// addNumbered adds present issues with the given numbers to a new series.
func addNumbered(t *testing.T, st *store.Store, name string, nums ...string) int64 {
	t.Helper()
	id, err := st.FindOrCreateSeriesByFolder(name, name, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nums {
		if err := st.InsertIssue(&store.Issue{SeriesID: id, Path: name + "-" + n + ".cbz", IssueNumber: n, MetadataSource: store.SourceFilename}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestGapsPagesAndFilter(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	addNumbered(t, srv.store, "Holey", "1", "2", "3", "4", "5", "9")
	addNumbered(t, srv.store, "Whole", "1", "2", "3")

	body := get(t, h, "/gaps", nil).Body.String()
	if !strings.Contains(body, "Holey") || !strings.Contains(body, "Missing: #6–8") || !strings.Contains(body, "owned 6 of 9") {
		t.Errorf("/gaps:\n%s", body)
	}
	if strings.Contains(body, "Whole") {
		t.Error("a complete series must not be listed")
	}

	txt := get(t, h, "/gaps.txt", nil)
	if ct := txt.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content type %q", ct)
	}
	if !strings.Contains(txt.Body.String(), "Holey: #6-8\n") {
		t.Errorf("/gaps.txt: %q", txt.Body.String())
	}

	home := get(t, h, "/?filter=gaps", nil).Body.String()
	if !strings.Contains(home, "Holey") || strings.Contains(home, "Whole") || !strings.Contains(home, "3 missing") {
		t.Errorf("gaps filter:\n%s", home)
	}
	if !strings.Contains(get(t, h, "/", nil).Body.String(), "filter=gaps") {
		t.Error("home should offer the has-gaps filter")
	}

	series := get(t, h, "/series/2", nil).Body.String() // Holey
	if !strings.Contains(series, "Missing: #6–8") || !strings.Contains(series, "Owned 6 of 9") {
		t.Errorf("series page:\n%s", series)
	}
}

func TestSeriesPageCVGapsAndVanished(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	// Series 1 holds #55 (present) and #56 (file gone).
	srv.store.SetSeriesComicVineVolume(1, 7, "")
	if err := srv.store.UpsertVolumeIssues([]store.CVIssue{
		{VolumeID: 7, CVIssueID: 1, IssueNumber: "54", Name: "Before", StoreDate: "2020-01-01"},
		{VolumeID: 7, CVIssueID: 2, IssueNumber: "55"},
		{VolumeID: 7, CVIssueID: 3, IssueNumber: "56", Name: "Gone"},
	}, 7, true); err != nil {
		t.Fatal(err)
	}
	body := get(t, h, "/series/1", nil).Body.String()
	for _, want := range []string{"Owned 1 of 3", "Missing: #54, #56", "file disappeared: #56", "per ComicVine", "Before", "2020-01-01"} {
		if !strings.Contains(body, want) {
			t.Errorf("series page missing %q", want)
		}
	}
}

func TestFollowAndReleasesPages(t *testing.T) {
	var asked []string
	cvSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RawQuery)
		q := r.URL.Query()
		today := time.Now().UTC()
		d := func(n int) string { return today.AddDate(0, 0, n).Format("2006-01-02") }
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(q.Get("filter"), "volume:7,") { // the release check
			fmt.Fprintf(w, `{"error":"OK","status_code":1,"results":[
				{"id":11,"name":"Fresh","issue_number":"55","store_date":%q,"volume":{"id":7}},
				{"id":12,"name":"Soon","issue_number":"57","store_date":%q,"volume":{"id":7}}]}`, d(-1), d(3))
			return
		}
		fmt.Fprint(w, `{"error":"OK","status_code":1,"results":[]}`)
	}))
	defer cvSrv.Close()

	srv, _ := newTestServer(t, false)
	srv.cv = comicvine.NewWithBaseURL("key", cvSrv.URL, time.Millisecond)
	h := srv.Handler()

	// Not matched: no follow button, no releases.
	if strings.Contains(get(t, h, "/series/1", nil).Body.String(), "/series/1/follow") {
		t.Error("unmatched series must not offer Follow")
	}
	srv.store.SetSeriesComicVineVolume(1, 7, "")
	body := get(t, h, "/series/1", nil).Body.String()
	if !strings.Contains(body, `action="/series/1/follow"`) || !strings.Contains(body, "Follow") {
		t.Errorf("matched series should offer Follow:\n%s", body)
	}

	// Nothing followed yet: the release check asks nobody.
	srv.releases.tryStart()
	srv.runReleaseCheck()
	if len(asked) != 0 {
		t.Fatalf("no followed volumes, yet asked %v", asked)
	}

	// Explicit follow, then the check batches the volume.
	rec := postForm(t, h, "/series/1/follow", "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/series/1" {
		t.Fatalf("follow: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(get(t, h, "/series/1", nil).Body.String(), "Following") {
		t.Error("series page should show Following")
	}
	if !srv.releases.tryStart() {
		t.Fatal("job should be free again")
	}
	srv.runReleaseCheck()
	if len(asked) != 1 || !strings.Contains(asked[0], "store_date") {
		t.Fatalf("release check requests: %v", asked)
	}
	if st := srv.releases.status(); st.Err != "" || st.Releases != 2 || !st.Finished {
		t.Fatalf("job status: %+v", st)
	}
	if _, ok, _ := srv.store.MetaTime(store.MetaReleasesChecked); !ok {
		t.Error("a clean check must stamp the time")
	}

	rel := get(t, h, "/releases", nil).Body.String()
	for _, want := range []string{"Fresh", "Soon", "in library ✓", "Check for new releases"} {
		if !strings.Contains(rel, want) {
			t.Errorf("/releases missing %q:\n%s", want, rel)
		}
	}
	// The home page strip shows them too; #55 is on disk, #57 not.
	home := get(t, h, "/", nil).Body.String()
	if !strings.Contains(home, "releases-strip") || !strings.Contains(home, "Fresh") {
		t.Errorf("home strip:\n%s", home)
	}

	// Unfollow removes them again.
	postForm(t, h, "/series/1/follow", "")
	if strings.Contains(get(t, h, "/releases", nil).Body.String(), "Fresh") {
		t.Error("unfollowed series must not list releases")
	}
	if strings.Contains(get(t, h, "/", nil).Body.String(), "releases-strip") {
		t.Error("empty strip must be hidden")
	}
}

func TestReleasesWithoutKey(t *testing.T) {
	srv, _ := newTestServer(t, false) // comicvine.New("")
	h := srv.Handler()
	rel := get(t, h, "/releases", nil)
	if rel.Code != http.StatusOK || !strings.Contains(rel.Body.String(), "comicvine_api_key") {
		t.Errorf("/releases without a key should show the hint:\n%s", rel.Body)
	}
	if strings.Contains(get(t, h, "/", nil).Body.String(), "releases-strip") {
		t.Error("home strip must be hidden without a key")
	}
	// The button does nothing without a key, but redirects politely.
	rec := postForm(t, h, "/releases/check", "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/releases" {
		t.Errorf("check: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestReleaseJobSingleFlight(t *testing.T) {
	j := &releaseJob{}
	if !j.tryStart() || j.tryStart() {
		t.Fatal("second start while running must be refused")
	}
	j.finish(1, 2, nil)
	if st := j.status(); st.Running || !st.Finished || st.CheckedAt == "" {
		t.Errorf("after finish: %+v", st)
	}
	if !j.tryStart() {
		t.Error("job should be reusable after finishing")
	}
}

func TestSyncVolumeAndScanPhase(t *testing.T) {
	var paths []string
	cvSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.Query().Get("filter"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error":"OK","status_code":1,"results":[
			{"id":1,"issue_number":"55","store_date":"2020-01-01"},{"id":2,"issue_number":"57","name":"Next","store_date":"2020-02-01"}]}`)
	}))
	defer cvSrv.Close()

	srv, _ := newTestServer(t, false)
	srv.cv = comicvine.NewWithBaseURL("key", cvSrv.URL, time.Millisecond)
	srv.store.SetSeriesComicVineVolume(1, 7, "")

	srv.syncCVData() // stale (never synced) volume 7 + the first release check (nobody follows)
	if len(paths) != 1 || paths[0] != "/issues/?volume:7" {
		t.Fatalf("requests: %v", paths)
	}
	g, err := srv.store.SeriesGaps(1, "2026-11-26")
	if err != nil || g == nil || !g.FromComicVine() || g.RangeText() != "#57" {
		t.Fatalf("gaps after sync: %+v %v", g, err)
	}
	srv.syncCVData() // fresh now, and the release check ran today: no more requests
	if len(paths) != 1 {
		t.Errorf("fresh cache and daily stamp should avoid requests: %v", paths)
	}
}
