package server

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"comicnest/internal/comicvine"
	"comicnest/internal/config"
	"comicnest/internal/covers"
	"comicnest/internal/library"
	"comicnest/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// mustCreateUser creates an account directly in the store (bypassing the
// admin panel HTTP handlers, which is fine for tests that only need the
// account to exist to exercise login/OPDS/admin-gating behavior).
func mustCreateUser(t *testing.T, st *store.Store, name, password string, isAdmin bool) *store.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateUser(name, string(hash), isAdmin)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.GetUser(id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// newTestServer builds a Server over a fresh SQLite database in a temp dir,
// seeded with one series holding two issues (one of them missing on disk).
func newTestServer(t *testing.T, opdsEnabled bool) (*Server, string) {
	t.Helper()
	dir := t.TempDir()

	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	seriesID, err := st.FindOrCreateSeriesByFolder("Saga", "Saga", dir)
	if err != nil {
		t.Fatalf("create series: %v", err)
	}
	cbz := filepath.Join(dir, "Saga 055.cbz")
	writeTestCBZ(t, cbz, 3)
	present := &store.Issue{SeriesID: seriesID, Path: cbz, FileSize: 10, IssueNumber: "55",
		Title: "Chapter Fifty-Five", Writer: "Brian K. Vaughan, Fiona Staples",
		Publisher: "Image", ReleaseDate: "2020-09-01", MetadataSource: store.SourceFilename,
		FilePages: 3}
	if err := st.InsertIssue(present); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	gone := &store.Issue{SeriesID: seriesID, Path: filepath.Join(dir, "Saga 056.cbr"),
		IssueNumber: "56", MetadataSource: store.SourceFilename}
	if err := st.InsertIssue(gone); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	if err := st.MarkIssuesMissing([]int64{gone.ID}); err != nil {
		t.Fatalf("mark missing: %v", err)
	}

	coversDir := filepath.Join(dir, "covers")
	if err := os.MkdirAll(coversDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := covers.New(coversDir)
	cfg := config.Config{Port: 8080, Listen: "localhost", Library: dir, DataDir: dir, OPDSEnabled: opdsEnabled}
	configPath := filepath.Join(dir, "config.yaml")
	scanners := map[string]*library.Scanner{dir: library.NewScanner(st, cache, dir)}
	srv, err := New(cfg, configPath, st, cache, scanners, comicvine.New(""))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return srv, cbz
}

// writeTestCBZ creates a CBZ holding n tiny PNG pages (4x4, distinct colours).
func writeTestCBZ(t *testing.T, path string, n int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for p := range n {
		img := image.NewRGBA(image.Rect(0, 0, 4, 4))
		draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{uint8(p * 60), 0, 0, 255}}, image.Point{}, draw.Src)
		w, err := zw.Create(fmt.Sprintf("page%03d.png", p+1))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(w, img); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, h http.Handler, target string, auth func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = "nas.local:8080"
	if auth != nil {
		auth(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertXML(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	body := rec.Body.String()
	dec := xml.NewDecoder(strings.NewReader(body))
	for {
		if _, err := dec.Token(); err != nil {
			if err.Error() == "EOF" {
				return body
			}
			t.Fatalf("response is not well-formed XML: %v\n%s", err, body)
		}
	}
}

func TestOPDSDisabledByDefault(t *testing.T) {
	srv, _ := newTestServer(t, false)
	if rec := get(t, srv.Handler(), "/opds", nil); rec.Code != http.StatusNotFound {
		t.Errorf("/opds with OPDS disabled: got %d, want 404", rec.Code)
	}
}

func TestOPDSNavigation(t *testing.T) {
	srv, _ := newTestServer(t, true)
	h := srv.Handler()

	rec := get(t, h, "/opds", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/opds: %d\n%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/atom+xml;profile=opds-catalog;kind=navigation") {
		t.Errorf("root content type = %q", ct)
	}
	body := assertXML(t, rec)
	for _, want := range []string{
		`href="http://nas.local:8080/opds/series"`,
		`href="http://nas.local:8080/opds/recent"`,
		`rel="search" href="http://nas.local:8080/opds/opensearch.xml"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("root feed missing %q\n%s", want, body)
		}
	}

	rec = get(t, h, "/opds/series", nil)
	body = assertXML(t, rec)
	if !strings.Contains(body, `<title>Saga</title>`) ||
		!strings.Contains(body, `href="http://nas.local:8080/opds/series/1"`) {
		t.Errorf("series list feed lacks the Saga entry:\n%s", body)
	}
	if !strings.Contains(body, `<opensearch:totalResults>1</opensearch:totalResults>`) {
		t.Errorf("series list feed lacks totalResults:\n%s", body)
	}
}

func TestOPDSSeriesAcquisitionFeed(t *testing.T) {
	srv, _ := newTestServer(t, true)
	rec := get(t, srv.Handler(), "/opds/series/1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/opds/series/1: %d\n%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/atom+xml;profile=opds-catalog;kind=acquisition") {
		t.Errorf("series content type = %q", ct)
	}
	body := assertXML(t, rec)
	for _, want := range []string{
		`<title>Saga #55 – Chapter Fifty-Five</title>`,
		`rel="http://opds-spec.org/acquisition" href="http://nas.local:8080/opds/issues/1/file" type="application/vnd.comicbook+zip"`,
		`<name>Brian K. Vaughan</name>`,
		`<name>Fiona Staples</name>`,
		`<dc:publisher>Image</dc:publisher>`,
		`<dc:issued>2020-09-01</dc:issued>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("series feed missing %q\n%s", want, body)
		}
	}
	// The issue whose file vanished must not be offered for download.
	if strings.Contains(body, "#56") || strings.Contains(body, "/opds/issues/2/file") {
		t.Errorf("missing-file issue leaked into the feed:\n%s", body)
	}
	// No cover cached → no image links (rather than a link to the placeholder).
	if strings.Contains(body, "opds-spec.org/image") {
		t.Errorf("issue without cached cover should have no image links:\n%s", body)
	}

	if rec := get(t, srv.Handler(), "/opds/series/999", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown series: got %d, want 404", rec.Code)
	}
}

func TestOPDSRecentAndSearch(t *testing.T) {
	srv, _ := newTestServer(t, true)
	h := srv.Handler()

	body := assertXML(t, get(t, h, "/opds/recent", nil))
	if !strings.Contains(body, "/opds/issues/1/file") || strings.Contains(body, "/opds/issues/2/file") {
		t.Errorf("recent feed should list only the present issue:\n%s", body)
	}

	// Series name matches even though the issue title does not.
	body = assertXML(t, get(t, h, "/opds/search?q=saga", nil))
	if !strings.Contains(body, "/opds/issues/1/file") {
		t.Errorf("search by series name found nothing:\n%s", body)
	}
	body = assertXML(t, get(t, h, "/opds/search?q=nothing-here", nil))
	if strings.Contains(body, "<entry>") {
		t.Errorf("search with no hits should return an empty feed:\n%s", body)
	}

	rec := get(t, h, "/opds/opensearch.xml", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `template="http://nas.local:8080/opds/search?q={searchTerms}"`) {
		t.Errorf("opensearch description: %d\n%s", rec.Code, rec.Body)
	}
}

func TestOPDSReadUnread(t *testing.T) {
	srv, _ := newTestServer(t, true)
	h := srv.Handler()

	body := assertXML(t, get(t, h, "/opds", nil))
	for _, want := range []string{
		`href="http://nas.local:8080/opds/unread"`,
		`href="http://nas.local:8080/opds/read"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("root feed missing %q\n%s", want, body)
		}
	}

	// Never opened: shows up as unread, not as read.
	body = assertXML(t, get(t, h, "/opds/unread", nil))
	if !strings.Contains(body, "/opds/issues/1/file") {
		t.Errorf("unread feed should list the untouched issue:\n%s", body)
	}
	body = assertXML(t, get(t, h, "/opds/read", nil))
	if strings.Contains(body, "<entry>") {
		t.Errorf("read feed should be empty before anything is read:\n%s", body)
	}

	// Only the first page seen (opened and closed right away) still counts
	// as unread, not as read or in progress.
	if err := srv.store.SetReadingProgress("", 1, 1); err != nil {
		t.Fatalf("SetReadingProgress: %v", err)
	}
	body = assertXML(t, get(t, h, "/opds/unread", nil))
	if !strings.Contains(body, "/opds/issues/1/file") {
		t.Errorf("unread feed should still list an issue with only its first page seen:\n%s", body)
	}
	body = assertXML(t, get(t, h, "/opds/reading", nil))
	if strings.Contains(body, "<entry>") {
		t.Errorf("reading feed should not list an issue with only its first page seen:\n%s", body)
	}

	// Finish the issue (3 pages) and it moves from unread to read.
	if err := srv.store.SetReadingProgress("", 1, 3); err != nil {
		t.Fatalf("SetReadingProgress: %v", err)
	}
	body = assertXML(t, get(t, h, "/opds/read", nil))
	if !strings.Contains(body, "/opds/issues/1/file") {
		t.Errorf("read feed should list the finished issue:\n%s", body)
	}
	body = assertXML(t, get(t, h, "/opds/unread", nil))
	if strings.Contains(body, "<entry>") {
		t.Errorf("unread feed should be empty once the only issue is read:\n%s", body)
	}
}

func TestOPDSFileDownload(t *testing.T) {
	srv, cbz := newTestServer(t, true)
	rec := get(t, srv.Handler(), "/opds/issues/1/file", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("download: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.comicbook+zip" {
		t.Errorf("download content type = %q", ct)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), filepath.Base(cbz)) {
		t.Errorf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	want, err := os.ReadFile(cbz)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != string(want) {
		t.Errorf("download body differs from the file on disk")
	}
	if rec := get(t, srv.Handler(), "/opds/issues/2/file", nil); rec.Code != http.StatusNotFound {
		t.Errorf("missing file download: got %d, want 404", rec.Code)
	}
}

func TestOPDSPageStreaming(t *testing.T) {
	srv, _ := newTestServer(t, true)
	h := srv.Handler()

	// The series feed advertises page streaming with the real page count and
	// no progress yet.
	body := assertXML(t, get(t, h, "/opds/series/1", nil))
	stream := `rel="http://vaemendis.net/opds-pse/stream" href="http://nas.local:8080/opds/issues/1/pages/{pageNumber}?width={maxWidth}" type="image/jpeg" pse:count="3"`
	if !strings.Contains(body, stream+">") && !strings.Contains(body, stream+"<") {
		t.Errorf("series feed lacks the page streaming link:\n%s", body)
	}
	if strings.Contains(body, "pse:lastRead") {
		t.Errorf("unread issue should carry no lastRead:\n%s", body)
	}
	if !strings.Contains(body, `xmlns:pse="http://vaemendis.net/opds-pse/ns"`) {
		t.Errorf("feed lacks the pse namespace declaration:\n%s", body)
	}

	// Nothing is being read yet.
	body = assertXML(t, get(t, h, "/opds/reading", nil))
	if strings.Contains(body, "<entry>") {
		t.Errorf("reading feed should start empty:\n%s", body)
	}

	// Page 0 streams as the original PNG.
	rec := get(t, h, "/opds/issues/1/pages/0", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("page 0: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if _, err := png.Decode(rec.Body); err != nil {
		t.Errorf("page 0 is not a valid PNG: %v", err)
	}

	// ?width= scales and re-encodes as JPEG.
	rec = get(t, h, "/opds/issues/1/pages/1?width=2", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("page 1 resized: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if cfg, _, err := image.DecodeConfig(rec.Body); err != nil || cfg.Width != 2 {
		t.Errorf("resized page: width=%d err=%v", cfg.Width, err)
	}

	// Fetching page index 1 means 2 pages read → progress shows in feeds and
	// the issue is listed as currently being read.
	body = assertXML(t, get(t, h, "/opds/series/1", nil))
	if !strings.Contains(body, `pse:count="3" pse:lastRead="2" pse:lastReadDate="`) {
		t.Errorf("series feed lacks progress after streaming:\n%s", body)
	}
	body = assertXML(t, get(t, h, "/opds/reading", nil))
	if !strings.Contains(body, "/opds/issues/1/file") || !strings.Contains(body, `pse:lastRead="2"`) {
		t.Errorf("reading feed should list the started issue:\n%s", body)
	}

	// Going back to page 0 does not lower the progress.
	get(t, h, "/opds/issues/1/pages/0", nil)
	body = assertXML(t, get(t, h, "/opds/series/1", nil))
	if !strings.Contains(body, `pse:lastRead="2"`) {
		t.Errorf("re-reading an earlier page must not lower progress:\n%s", body)
	}

	// Reaching the last page finishes the issue: it leaves "currently reading".
	if rec := get(t, h, "/opds/issues/1/pages/2", nil); rec.Code != http.StatusOK {
		t.Fatalf("last page: %d", rec.Code)
	}
	body = assertXML(t, get(t, h, "/opds/reading", nil))
	if strings.Contains(body, "<entry>") {
		t.Errorf("finished issue must leave the reading feed:\n%s", body)
	}

	// Out of range and missing files are 404s.
	if rec := get(t, h, "/opds/issues/1/pages/3", nil); rec.Code != http.StatusNotFound {
		t.Errorf("page past the end: got %d, want 404", rec.Code)
	}
	if rec := get(t, h, "/opds/issues/2/pages/0", nil); rec.Code != http.StatusNotFound {
		t.Errorf("missing file page: got %d, want 404", rec.Code)
	}
}

func TestWebShowsReadingProgress(t *testing.T) {
	srv, _ := newTestServer(t, true)
	h := srv.Handler()

	// Unread: no progress markup anywhere.
	if body := get(t, h, "/series/1", nil).Body.String(); strings.Contains(body, `class="progress`) {
		t.Errorf("unread issue should show no progress bar:\n%s", body)
	}
	if body := get(t, h, "/issues/1", nil).Body.String(); strings.Contains(body, "<dt>Read</dt>") {
		t.Errorf("unread issue should show no read count:\n%s", body)
	}

	// Stream page index 1 → 2 of 3 pages read.
	if rec := get(t, h, "/opds/issues/1/pages/1", nil); rec.Code != http.StatusOK {
		t.Fatalf("stream page: %d", rec.Code)
	}
	body := get(t, h, "/series/1", nil).Body.String()
	if !strings.Contains(body, `class="progress "`) || !strings.Contains(body, `style="width: 66%"`) ||
		!strings.Contains(body, "read: p. 2 of 3 (66%)") {
		t.Errorf("series list lacks the progress bar:\n%s", body)
	}
	body = get(t, h, "/issues/1", nil).Body.String()
	if !strings.Contains(body, "<dt>Read</dt><dd>2 of 3 pages (66%)</dd>") ||
		!strings.Contains(body, "Reading") {
		t.Errorf("issue page lacks the read count:\n%s", body)
	}

	// Last page → finished.
	get(t, h, "/opds/issues/1/pages/2", nil)
	body = get(t, h, "/issues/1", nil).Body.String()
	if !strings.Contains(body, "<strong>Read</strong>") || !strings.Contains(body, `class="progress progress-lg done"`) {
		t.Errorf("finished issue should be marked as read:\n%s", body)
	}
}

func TestHomeFiltersAndReadMark(t *testing.T) {
	srv, _ := newTestServer(t, true)
	h := srv.Handler()
	lists := func(filter string) bool {
		body := get(t, h, "/?filter="+filter, nil).Body.String()
		return strings.Contains(body, `<span class="card-title">Saga</span>`)
	}
	hasMark := func() bool {
		return strings.Contains(get(t, h, "/", nil).Body.String(), `class="read-mark"`)
	}

	// A second series whose issues are all present and untouched: every
	// aggregate over it is computed from NULL progress rows only.
	batmanID, err := srv.store.FindOrCreateSeriesByFolder("Batman", "Batman", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.InsertIssue(&store.Issue{SeriesID: batmanID, Path: filepath.Join(t.TempDir(), "Batman 1.cbz"),
		IssueNumber: "1", FilePages: 20, MetadataSource: store.SourceComicVine}); err != nil {
		t.Fatal(err)
	}
	batmanListed := func(filter string) bool {
		return strings.Contains(get(t, h, "/?filter="+filter, nil).Body.String(), `<span class="card-title">Batman</span>`)
	}
	if rec := get(t, h, "/", nil); rec.Code != http.StatusOK {
		t.Fatalf("home page: %d", rec.Code)
	}
	if !batmanListed("all") || !batmanListed("unread") || batmanListed("nocv") || batmanListed("missing") {
		t.Error("untouched ComicVine-sourced series should be listed under 'all' and 'unread' only")
	}

	// Nothing read yet.
	if hasMark() {
		t.Error("unread series must not carry the read mark")
	}
	if !lists("unread") || lists("reading") || lists("read") {
		t.Error("unread series should be listed only under 'unread'")
	}
	// Independent of reading: Saga has a filename-sourced issue and a missing file.
	if !lists("nocv") || !lists("missing") {
		t.Error("series should match the 'nocv' and 'missing' filters")
	}
	if !lists("all") || !lists("bogus") {
		t.Error("'all' and unknown filters should list everything")
	}

	// Opening and immediately closing (just the first page) must not count
	// as "started" — it should still read as unread.
	get(t, h, "/opds/issues/1/pages/0", nil)
	if !lists("unread") || lists("reading") || lists("read") || hasMark() {
		t.Error("a series with only its first page opened should still be listed as unread")
	}

	// Real progress (a second page) → in progress.
	get(t, h, "/opds/issues/1/pages/1", nil)
	if lists("unread") || !lists("reading") || lists("read") || hasMark() {
		t.Error("started series should be listed only under 'reading', without the read mark")
	}

	// Finish the only present issue (the missing one does not count) → read.
	get(t, h, "/opds/issues/1/pages/2", nil)
	if lists("unread") || lists("reading") || !lists("read") || !hasMark() {
		t.Error("finished series should be listed under 'read' with the read mark")
	}

	// The sort selector keeps the active filter.
	body := get(t, h, "/?filter=read&sort=recent", nil).Body.String()
	if !strings.Contains(body, `<input type="hidden" name="filter" value="read">`) ||
		!strings.Contains(body, `class="chip active" href="/?sort=recent&amp;filter=read"`) {
		t.Errorf("filter/sort state not preserved in the page head:\n%s", body)
	}
}

func TestMarkReadUnread(t *testing.T) {
	srv, _ := newTestServer(t, true)
	h := srv.Handler()
	post := func(target, form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Mark read from the series list → progress = page total, back to the list.
	rec := post("/issues/1/read", "next=/series/1?filter=all")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/series/1?filter=all" {
		t.Fatalf("mark read: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	body := get(t, h, "/issues/1", nil).Body.String()
	if !strings.Contains(body, "<dt>Read</dt><dd>3 of 3 pages (100%)</dd>") ||
		!strings.Contains(body, "Mark as unread") || strings.Contains(body, "Mark as read") {
		t.Errorf("issue page after mark read:\n%s", body)
	}
	if !strings.Contains(get(t, h, "/", nil).Body.String(), `class="read-mark"`) {
		t.Error("home grid should show the read mark after marking read")
	}
	body = get(t, h, "/series/1", nil).Body.String()
	if !strings.Contains(body, "↺ unread") || strings.Contains(body, "✓ read") {
		t.Errorf("series list should offer 'unread' for a finished issue:\n%s", body)
	}

	// Mark unread without "next" → issue page with a flash.
	rec = post("/issues/1/unread", "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/issues/1?msg=unread_ok" {
		t.Fatalf("mark unread: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	body = get(t, h, "/issues/1?msg=unread_ok", nil).Body.String()
	if strings.Contains(body, "<dt>Read</dt>") || !strings.Contains(body, "Issue marked as unread.") {
		t.Errorf("issue page after mark unread:\n%s", body)
	}
	if p, _ := srv.store.GetReadingProgress("", 1); p != nil {
		t.Errorf("progress should be gone, got %+v", p)
	}

	// Off-site "next" values are ignored.
	rec = post("/issues/1/read", "next=//evil.example/x")
	if loc := rec.Header().Get("Location"); loc != "/issues/1?msg=read_ok" {
		t.Errorf("open redirect not blocked: %q", loc)
	}

	// Missing file with no known page count cannot be marked read.
	rec = post("/issues/2/read", "")
	if loc := rec.Header().Get("Location"); loc != "/issues/2?msg=read_nopages" {
		t.Errorf("issue without pages: %q", loc)
	}
}

func TestOPDSNoStreamingForPDF(t *testing.T) {
	e := opdsIssueEntry("http://h", store.Issue{ID: 9, Path: "x/Comic.pdf", PageCount: 40}, "Comic", nil)
	for _, l := range e.Links {
		if l.Rel == "http://vaemendis.net/opds-pse/stream" {
			t.Errorf("PDF issues must not advertise page streaming: %+v", l)
		}
	}
	e = opdsIssueEntry("http://h", store.Issue{ID: 9, Path: "x/Comic.cbr", PageCount: 40}, "Comic", nil)
	found := false
	for _, l := range e.Links {
		if l.Rel == "http://vaemendis.net/opds-pse/stream" && l.PageCount == 40 {
			found = true
		}
	}
	if !found {
		t.Errorf("CBR with metadata page count should advertise streaming: %+v", e.Links)
	}
}

func TestOPDSBasicAuth(t *testing.T) {
	srv, _ := newTestServer(t, true)
	mustCreateUser(t, srv.store, "artur", "sekret", false)
	h := srv.Handler()

	for _, path := range []string{"/opds", "/opds/series/1", "/opds/issues/1/file", "/opds/issues/1/cover"} {
		rec := get(t, h, path, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without credentials: got %d, want 401", path, rec.Code)
		}
		if !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Basic ") {
			t.Errorf("%s: missing WWW-Authenticate challenge", path)
		}
	}
	wrong := func(r *http.Request) { r.SetBasicAuth("artur", "zle") }
	if rec := get(t, h, "/opds", wrong); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: got %d, want 401", rec.Code)
	}
	right := func(r *http.Request) { r.SetBasicAuth("artur", "sekret") }
	if rec := get(t, h, "/opds", right); rec.Code != http.StatusOK {
		t.Errorf("correct credentials: got %d, want 200", rec.Code)
	}
	// The web UI uses the same accounts, via the login form: without a session
	// it redirects to /login instead of answering a Basic challenge.
	rec := get(t, h, "/issues/1/download", nil)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login?next=") {
		t.Errorf("web download without a session: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestPluralIssues(t *testing.T) {
	cases := map[int]string{1: "1 issue", 2: "2 issues", 4: "4 issues", 5: "5 issues",
		12: "12 issues", 22: "22 issues", 25: "25 issues", 112: "112 issues"}
	for n, want := range cases {
		if got := pluralIssues(n); got != want {
			t.Errorf("pluralIssues(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestComicMediaType(t *testing.T) {
	cases := map[string]string{
		"a/b.cbz": "application/vnd.comicbook+zip", "B.CBR": "application/vnd.comicbook-rar",
		"x.pdf": "application/pdf", "x.zip": "application/octet-stream",
	}
	for path, want := range cases {
		if got := comicMediaType(path); got != want {
			t.Errorf("comicMediaType(%q) = %q, want %q", path, got, want)
		}
	}
}
