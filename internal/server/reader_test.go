package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"comicnest/internal/store"
)

func postForm(t *testing.T, h http.Handler, target, form string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestReaderPageAndProgress(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()

	// Fresh issue: opens at page 1 with the real page count, no neighbours.
	rec := get(t, h, "/issues/1/read", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reader: %d\n%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-total="3"`, `data-start="1"`, `data-prev="0"`, `data-next="0"`,
		`<script src="/static/reader.js" defer></script>`, `Saga #55 – Chapter Fifty-Five`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("reader page missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "Next issue") || strings.Contains(body, "Previous issue") {
		t.Errorf("no neighbouring issues exist, yet the bar links to one:\n%s", body)
	}
	if strings.Contains(body, "End of issue") {
		t.Errorf("the end-of-issue popup was removed on purpose:\n%s", body)
	}

	// The reader loads pages without recording progress …
	if rec := get(t, h, "/issues/1/pages/1?track=0", nil); rec.Code != http.StatusOK {
		t.Fatalf("page via web route: %d", rec.Code)
	}
	if p, _ := srv.store.GetReadingProgress("", 1); p != nil {
		t.Errorf("track=0 must not record progress, got %+v", p)
	}
	// … and reports it explicitly.
	if rec := postForm(t, h, "/issues/1/progress", "page=2"); rec.Code != http.StatusNoContent {
		t.Fatalf("progress: %d %s", rec.Code, rec.Body)
	}
	if p, _ := srv.store.GetReadingProgress("", 1); p == nil || p.Page != 2 {
		t.Fatalf("progress not stored: %+v", p)
	}
	// Resume from the recorded page; ?page= overrides.
	if body := get(t, h, "/issues/1/read", nil).Body.String(); !strings.Contains(body, `data-start="2"`) {
		t.Errorf("reader should resume at page 2:\n%s", body)
	}
	if body := get(t, h, "/issues/1/read?page=3", nil).Body.String(); !strings.Contains(body, `data-start="3"`) {
		t.Errorf("?page= should override the resume point:\n%s", body)
	}
	// Same record as OPDS: the catalog shows lastRead="2" and the issue page
	// offers "Continue".
	srv2, _ := newTestServer(t, true)
	postForm(t, srv2.Handler(), "/issues/1/progress", "page=2")
	if body := get(t, srv2.Handler(), "/opds/series/1", nil).Body.String(); !strings.Contains(body, `pse:lastRead="2"`) {
		t.Errorf("OPDS feed should reflect web progress:\n%s", body)
	}
	if body := get(t, h, "/issues/1", nil).Body.String(); !strings.Contains(body, "Continue (p. 2)") {
		t.Errorf("issue page should offer to continue:\n%s", body)
	}

	// Finishing: reaching the last page marks it read; the reader then
	// starts over from page 1 and progress never goes backwards.
	postForm(t, h, "/issues/1/progress", "page=3")
	if body := get(t, h, "/issues/1/read", nil).Body.String(); !strings.Contains(body, `data-start="1"`) {
		t.Errorf("finished issue should restart at page 1:\n%s", body)
	}
	postForm(t, h, "/issues/1/progress", "page=1")
	if p, _ := srv.store.GetReadingProgress("", 1); p == nil || p.Page != 3 {
		t.Errorf("progress must keep the furthest page, got %+v", p)
	}
	if body := get(t, h, "/issues/1", nil).Body.String(); !strings.Contains(body, "Read again") {
		t.Errorf("finished issue page should offer to reread:\n%s", body)
	}

	// Validation.
	for _, form := range []string{"page=0", "page=4", "page=x", ""} {
		if rec := postForm(t, h, "/issues/1/progress", form); rec.Code != http.StatusBadRequest {
			t.Errorf("progress %q: got %d, want 400", form, rec.Code)
		}
	}
	// Missing file → no reader.
	if rec := get(t, h, "/issues/2/read", nil); rec.Code != http.StatusNotFound {
		t.Errorf("reader for a missing file: got %d, want 404", rec.Code)
	}
}

func TestReaderNeighboursAndPDF(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	dir := t.TempDir()

	// Add a readable issue #57 and a PDF #58 to Saga (series 1).
	cbz := filepath.Join(dir, "Saga 057.cbz")
	writeTestCBZ(t, cbz, 2)
	i57 := &store.Issue{SeriesID: 1, Path: cbz, IssueNumber: "57", FilePages: 2, MetadataSource: store.SourceFilename}
	if err := srv.store.InsertIssue(i57); err != nil {
		t.Fatal(err)
	}
	pdf := filepath.Join(dir, "Saga 058.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	i58 := &store.Issue{SeriesID: 1, Path: pdf, IssueNumber: "58", PageCount: 30, MetadataSource: store.SourceFilename}
	if err := srv.store.InsertIssue(i58); err != nil {
		t.Fatal(err)
	}

	// #55 → next is #57 (#56 is missing on disk, #58 is a PDF).
	body := get(t, h, "/issues/1/read", nil).Body.String()
	if !strings.Contains(body, `data-next="`+itoa(i57.ID)+`"`) || !strings.Contains(body, `href="/issues/`+itoa(i57.ID)+`/read" title="Next issue"`) {
		t.Errorf("#55 should point at #57 as next:\n%s", body)
	}
	body = get(t, h, "/issues/"+itoa(i57.ID)+"/read", nil).Body.String()
	if !strings.Contains(body, `data-prev="1"`) || !strings.Contains(body, `data-next="0"`) ||
		!strings.Contains(body, `href="/issues/1/read" title="Previous issue"`) {
		t.Errorf("#57 should point back at #55 and have no next:\n%s", body)
	}

	// PDFs cannot be read in the browser and get no "Read" button.
	if rec := get(t, h, "/issues/"+itoa(i58.ID)+"/read", nil); rec.Code != http.StatusNotFound {
		t.Errorf("reader for a PDF: got %d, want 404", rec.Code)
	}
	body = get(t, h, "/issues/"+itoa(i58.ID), nil).Body.String()
	if strings.Contains(body, `href="/issues/`+itoa(i58.ID)+`/read"`) || !strings.Contains(body, `class="btn btn-primary" href="/issues/`+itoa(i58.ID)+`/download"`) {
		t.Errorf("PDF issue page should offer download as the primary action:\n%s", body)
	}
	body = get(t, h, "/series/1", nil).Body.String()
	if strings.Count(body, ">Read</a>") != 2 {
		t.Errorf("series list should show 'Read' for the two CBZ issues only:\n%s", body)
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
