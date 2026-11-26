package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"comicnest/internal/comicvine"
	"comicnest/internal/store"
)

// cvMock is a ComicVine API stand-in: volume 7 holds issue "55" (cv id 101);
// issue 101 features Spider-Man and Venom and belongs to arc 9 ("Big Event"),
// which lists issues 101, 102 and 103 (102 and 103 are not in the library).
type cvMock struct {
	srv                     *httptest.Server
	issueCalls, arcCalls    atomic.Int32
	detailCalls, volumeHits atomic.Int32
}

func newCVMock(t *testing.T) *cvMock {
	t.Helper()
	m := &cvMock{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok := func(results string) { fmt.Fprintf(w, `{"error":"OK","status_code":1,"results":%s}`, results) }
		switch r.URL.Path {
		case "/volume/4050-7/":
			m.volumeHits.Add(1)
			ok(`{"id":7,"name":"Saga","issues":[{"id":101,"issue_number":"55"}]}`)
		case "/issue/4000-101/":
			m.issueCalls.Add(1)
			ok(`{"id":101,"name":"Chapter","issue_number":"55",
				"character_credits":[{"id":1443,"name":"Spider-Man"},{"id":2,"name":"Venom"}],
				"story_arc_credits":[{"id":9,"name":"Big Event"}]}`)
		case "/story_arc/4045-9/":
			m.arcCalls.Add(1)
			ok(`{"id":9,"name":"Big Event","description":"<p>Everything changes.</p>","publisher":{"name":"Marvel"},
				"issues":[{"id":101},{"id":102},{"id":103}]}`)
		case "/issues/":
			m.detailCalls.Add(1)
			ok(`[{"id":101,"issue_number":"55","volume":{"id":7,"name":"Saga"},"cover_date":"2020-09-01"},
				{"id":102,"issue_number":"56","volume":{"id":7,"name":"Saga"},"cover_date":"2020-10-01"},
				{"id":103,"issue_number":"2","volume":{"id":8,"name":"Delta Force"},"cover_date":"2020-11-01"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// withCV points the server's ComicVine client at the mock.
func (m *cvMock) withCV(srv *Server) {
	srv.cv = comicvine.NewWithBaseURL("test-key", m.srv.URL, time.Millisecond)
}

func post(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func body(t *testing.T, h http.Handler, target string) string {
	t.Helper()
	rec := get(t, h, target, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d\n%s", target, rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func mustContain(t *testing.T, what, page string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(page, w) {
			t.Errorf("%s: missing %q in:\n%s", what, w, page)
		}
	}
}

func TestSingleIssueScrapeSavesCharactersAndArcs(t *testing.T) {
	srv, _ := newTestServer(t, false)
	mock := newCVMock(t)
	mock.withCV(srv)
	h := srv.Handler()
	if err := srv.store.SetSeriesComicVineVolume(1, 7, ""); err != nil {
		t.Fatal(err)
	}

	rec := post(t, h, "/issues/1/scrape")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "msg=cv_ok") {
		t.Fatalf("scrape: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	if mock.arcCalls.Load() != 1 {
		t.Errorf("arc fetched %d times, want 1", mock.arcCalls.Load())
	}

	// The issue page links its arc (with position), characters and credits.
	page := body(t, h, "/issues/1")
	mustContain(t, "issue page", page,
		`Part of: <a href="/arcs/1">Big Event</a>`, "#1 of 3",
		`<a href="/characters/1">Spider-Man</a>`, `<a href="/characters/2">Venom</a>`,
		`<a href="/creators/brian%20k.%20vaughan">Brian K. Vaughan</a>`,
		`<a href="/creators/fiona%20staples">Fiona Staples</a>`)

	// The arc page: owned entry, two gaps with their volume and number.
	page = body(t, h, "/arcs/1")
	mustContain(t, "arc page", page, "Big Event", "owned 1 of 3", "read 0 of 3", "Everything changes.",
		"Start reading", `href="/issues/1/read"`, "Delta Force", "#56", "#2", "missing from your library")
	if n := strings.Count(page, "missing from your library"); n != 2 {
		t.Errorf("%d gaps shown, want 2", n)
	}

	// The index shows the arc with the owned/total counts.
	mustContain(t, "arcs index", body(t, h, "/arcs"), "Big Event", "own 1 of 3")
	if strings.Contains(body(t, h, "/arcs?q=zzz"), "Big Event") {
		t.Error("filter should hide non-matching arcs")
	}

	// Characters.
	chars := body(t, h, "/characters")
	mustContain(t, "characters index", chars, "Spider-Man", "Venom")
	filtered := body(t, h, "/characters?q=ven")
	mustContain(t, "filtered characters", filtered, "Venom")
	if strings.Contains(filtered, "Spider-Man") {
		t.Error("character filter did not narrow the list")
	}
	mustContain(t, "character page", body(t, h, "/characters/1"), "Spider-Man", "1 issue", `href="/series/1"`, `href="/issues/1"`)

	// A second scrape of the same issue does not refetch the arc.
	post(t, h, "/issues/1/scrape")
	if mock.arcCalls.Load() != 1 {
		t.Errorf("arc refetched on a second scrape (%d calls)", mock.arcCalls.Load())
	}
}

func TestArcRefreshHandler(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	// An arc from ComicInfo only, and one with a ComicVine id.
	if err := srv.store.ReplaceComicInfoExtras(1, nil, []store.ArcInput{{Name: "Local"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.ReplaceComicVineExtras(1, nil, []store.ArcInput{{Name: "Big Event", ComicVineID: 9}}); err != nil {
		t.Fatal(err)
	}

	// No API key: refused, and the button is disabled with a hint.
	if loc := post(t, h, "/arcs/2/refresh").Header().Get("Location"); !strings.Contains(loc, "msg=nokey") {
		t.Errorf("no key: redirect %q", loc)
	}
	mustContain(t, "arc page without a key", body(t, h, "/arcs/2"), `disabled title="Set comicvine_api_key`)

	mock := newCVMock(t)
	mock.withCV(srv)
	if loc := post(t, h, "/arcs/1/refresh").Header().Get("Location"); !strings.Contains(loc, "msg=nolink") {
		t.Errorf("ComicInfo-only arc: redirect %q", loc)
	}
	rec := post(t, h, "/arcs/2/refresh")
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !strings.Contains(loc, "/arcs/2") || !strings.Contains(loc, "msg=refreshed") {
		t.Errorf("refresh: %d -> %q", rec.Code, loc)
	}
	if mock.arcCalls.Load() != 1 {
		t.Errorf("arc calls = %d, want 1", mock.arcCalls.Load())
	}
	post(t, h, "/arcs/2/refresh") // an explicit refresh always fetches again
	if mock.arcCalls.Load() != 2 {
		t.Errorf("arc calls after a second refresh = %d, want 2", mock.arcCalls.Load())
	}
	mustContain(t, "arc page", body(t, h, "/arcs/2?msg=refreshed"), "Reading order refreshed from ComicVine.", "Marvel")
	if rec := get(t, h, "/arcs/999", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown arc: %d", rec.Code)
	}
}

func TestBackfillFillsIssuesScrapedBeforeTheFeature(t *testing.T) {
	srv, _ := newTestServer(t, false)
	mock := newCVMock(t)
	mock.withCV(srv)
	h := srv.Handler()

	// An issue already matched to ComicVine and locked (scraped long ago), with
	// no characters/arcs recorded.
	if err := srv.store.SetSeriesComicVineVolume(1, 7, ""); err != nil {
		t.Fatal(err)
	}
	issue, _ := srv.store.GetIssue(1)
	issue.ComicVineIssueID.Int64, issue.ComicVineIssueID.Valid = 101, true
	issue.MetadataSource = store.SourceComicVine
	issue.MetadataLocked = true
	issue.Summary = "kept as is"
	if err := srv.store.UpdateIssueMetadata(issue); err != nil {
		t.Fatal(err)
	}

	var lastDone, lastTotal int
	srv.autoScrapeCV(func(done, total, updated, failed int) { lastDone, lastTotal = done, total })
	if lastDone != 1 || lastTotal != 1 {
		t.Errorf("progress = %d/%d, want 1/1", lastDone, lastTotal)
	}
	if mock.issueCalls.Load() != 1 || mock.arcCalls.Load() != 1 {
		t.Errorf("calls: issue %d, arc %d, want 1 each", mock.issueCalls.Load(), mock.arcCalls.Load())
	}
	mustContain(t, "issue page", body(t, h, "/issues/1"), "Big Event", "Spider-Man")
	if got, _ := srv.store.GetIssue(1); got.Summary != "kept as is" || !got.MetadataLocked {
		t.Errorf("backfill touched the locked metadata: %+v", got)
	}

	// Nothing left to backfill and the arc is fresh: a second scan makes no
	// ComicVine requests at all.
	before := mock.issueCalls.Load() + mock.arcCalls.Load() + mock.detailCalls.Load() + mock.volumeHits.Load()
	srv.autoScrapeCV(func(int, int, int, int) {})
	after := mock.issueCalls.Load() + mock.arcCalls.Load() + mock.detailCalls.Load() + mock.volumeHits.Load()
	if before != after {
		t.Errorf("second pass made %d request(s), want none", after-before)
	}
}

func TestContinueReadingLinksNextUnreadOwnedIssue(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	sid := int64(1)
	second := &store.Issue{SeriesID: sid, Path: filepath.Join(srv.cfg.Library, "Saga 057.cbz"), IssueNumber: "57",
		MetadataSource: store.SourceFilename, FilePages: 3}
	writeTestCBZ(t, second.Path, 3)
	if err := srv.store.InsertIssue(second); err != nil {
		t.Fatal(err)
	}
	// ComicInfo-only arc: issue 1 is part 1, the new one part 2.
	if err := srv.store.ReplaceComicInfoExtras(second.ID, nil, []store.ArcInput{{Name: "Local Arc", Number: 2}}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.ReplaceComicInfoExtras(1, nil, []store.ArcInput{{Name: "Local Arc", Number: 1}}); err != nil {
		t.Fatal(err)
	}

	page := body(t, h, "/arcs/1")
	mustContain(t, "unread arc", page, "owned 2 of 2", "read 0 of 2", "Start reading", `href="/issues/1/read"`)

	if err := srv.store.SetReadingProgress("", 1, 3); err != nil { // finish part 1
		t.Fatal(err)
	}
	page = body(t, h, "/arcs/1")
	mustContain(t, "arc after part 1", page, "read 1 of 2", "Continue reading", fmt.Sprintf(`href="/issues/%d/read"`, second.ID))
	if strings.Contains(page, "Start reading") {
		t.Error("should say Continue once something is read")
	}
	// Part 1 now offers "next in arc".
	mustContain(t, "issue page", body(t, h, "/issues/1"), "Next in arc", fmt.Sprintf(`href="/issues/%d"`, second.ID), "#1 of 2")

	if err := srv.store.SetReadingProgress("", second.ID, 3); err != nil {
		t.Fatal(err)
	}
	page = body(t, h, "/arcs/1")
	mustContain(t, "finished arc", page, "read 2 of 2", "Everything you own from this arc is read.")
	if strings.Contains(page, "Continue reading") {
		t.Error("no continue button once everything is read")
	}
}

func TestCreatorPages(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()

	mustContain(t, "creators index", body(t, h, "/creators"), "Brian K. Vaughan", "Fiona Staples", `href="/creators/brian%20k.%20vaughan"`)
	filtered := body(t, h, "/creators?q=fiona")
	mustContain(t, "filtered", filtered, "Fiona Staples")
	if strings.Contains(filtered, "Brian") {
		t.Error("creator filter did not narrow the list")
	}

	page := body(t, h, "/creators/brian%20k.%20vaughan")
	mustContain(t, "creator page", page, "Brian K. Vaughan", "Writer", `href="/series/1"`, `href="/issues/1"`, "Chapter Fifty-Five")
	if strings.Contains(page, "Artist") {
		t.Error("a writer-only creator should have no artist section")
	}
	// Any spelling that normalizes the same reaches the page.
	mustContain(t, "other spelling", body(t, h, "/creators/Brian%20K.%20%20VAUGHAN"), "Brian K. Vaughan")
	if rec := get(t, h, "/creators/nobody", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown creator: %d", rec.Code)
	}

	// The stats ranking links to the creator once the issue is read.
	if err := srv.store.SetReadingProgress("", 1, 3); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "stats", body(t, h, "/stats"), `href="/creators/brian%20k.%20vaughan"`)
}

func TestCreatorNameWithSlashAndAccents(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	issue, _ := srv.store.GetIssue(1)
	issue.Writer = "AC/DC Écrivain"
	if err := srv.store.UpdateIssueMetadata(issue); err != nil {
		t.Fatal(err)
	}
	link := creatorURL("AC/DC Écrivain")
	if strings.Contains(strings.TrimPrefix(link, "/creators/"), "/") {
		t.Fatalf("slash must be escaped in %q", link)
	}
	mustContain(t, "creator page", body(t, h, link), "AC/DC Écrivain")
}

func TestNavLinksAndEmptyPages(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	page := body(t, h, "/")
	mustContain(t, "home", page, `href="/arcs"`, `href="/characters"`, `href="/creators"`)
	mustContain(t, "empty arcs", body(t, h, "/arcs"), "No story arcs yet")
	mustContain(t, "empty characters", body(t, h, "/characters"), "No characters yet")
	if rec := get(t, h, "/characters/42", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown character: %d", rec.Code)
	}
	if rec := get(t, h, "/arcs/x", nil); rec.Code != http.StatusNotFound {
		t.Errorf("bad arc id: %d", rec.Code)
	}
}
