package server

import (
	"encoding/xml"
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
)

// newTestServer builds a Server over a fresh SQLite database in a temp dir,
// seeded with one series holding two issues (one of them missing on disk).
func newTestServer(t *testing.T, opdsCfg config.OPDSConfig) (*Server, string) {
	t.Helper()
	dir := t.TempDir()

	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	seriesID, err := st.FindOrCreateSeriesByFolder("Saga", "Saga")
	if err != nil {
		t.Fatalf("create series: %v", err)
	}
	cbz := filepath.Join(dir, "Saga 055.cbz")
	if err := os.WriteFile(cbz, []byte("PK\x03\x04fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	present := &store.Issue{SeriesID: seriesID, Path: cbz, FileSize: 10, IssueNumber: "55",
		Title: "Chapter Fifty-Five", Writer: "Brian K. Vaughan, Fiona Staples",
		Publisher: "Image", ReleaseDate: "2020-09-01", MetadataSource: store.SourceFilename}
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
	cfg := config.Config{Port: 8080, Listen: "localhost", Library: dir, DataDir: dir, OPDS: opdsCfg}
	srv, err := New(cfg, st, cache, library.NewScanner(st, cache, dir), comicvine.New(""))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return srv, cbz
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
	srv, _ := newTestServer(t, config.OPDSConfig{})
	if rec := get(t, srv.Handler(), "/opds", nil); rec.Code != http.StatusNotFound {
		t.Errorf("/opds with OPDS disabled: got %d, want 404", rec.Code)
	}
}

func TestOPDSNavigation(t *testing.T) {
	srv, _ := newTestServer(t, config.OPDSConfig{Enabled: true})
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
	srv, _ := newTestServer(t, config.OPDSConfig{Enabled: true})
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
	srv, _ := newTestServer(t, config.OPDSConfig{Enabled: true})
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

func TestOPDSFileDownload(t *testing.T) {
	srv, cbz := newTestServer(t, config.OPDSConfig{Enabled: true})
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
	if rec.Body.String() != "PK\x03\x04fake" {
		t.Errorf("unexpected body %q", rec.Body.String())
	}
	if rec := get(t, srv.Handler(), "/opds/issues/2/file", nil); rec.Code != http.StatusNotFound {
		t.Errorf("missing file download: got %d, want 404", rec.Code)
	}
}

func TestOPDSBasicAuth(t *testing.T) {
	srv, _ := newTestServer(t, config.OPDSConfig{Enabled: true, Username: "artur", Password: "sekret"})
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
	// The web UI stays open regardless of OPDS credentials.
	if rec := get(t, h, "/issues/1/download", nil); rec.Code != http.StatusOK {
		t.Errorf("web download should not require OPDS auth: got %d", rec.Code)
	}
}

func TestPluralIssues(t *testing.T) {
	cases := map[int]string{1: "1 zeszyt", 2: "2 zeszyty", 4: "4 zeszyty", 5: "5 zeszytów",
		12: "12 zeszytów", 22: "22 zeszyty", 25: "25 zeszytów", 112: "112 zeszytów"}
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
