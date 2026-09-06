package server

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"comicnest/internal/store"
)

func TestHandleMatchUnlink(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	post := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, target, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if err := srv.store.SetSeriesComicVineVolume(1, 7, "https://comicvine.gamespot.com/saga/4050-7/"); err != nil {
		t.Fatal(err)
	}
	issue, err := srv.store.GetIssue(1)
	if err != nil {
		t.Fatal(err)
	}
	issue.ComicVineIssueID = sql.NullInt64{Int64: 55, Valid: true}
	issue.ComicVineURL = "https://comicvine.gamespot.com/saga-1/4000-55/"
	issue.MetadataSource = store.SourceComicVine
	if err := srv.store.UpdateIssueMetadata(issue); err != nil {
		t.Fatal(err)
	}

	// The matched pages link out to ComicVine before unlinking (the series
	// action strip is fetched separately via HTMX; the issue page renders
	// its link inline).
	if body := get(t, h, "/series/1/scrape/status", nil).Body.String(); !strings.Contains(body, `href="https://comicvine.gamespot.com/saga/4050-7/"`) {
		t.Errorf("series action strip should link to the matched ComicVine volume:\n%s", body)
	}
	if body := get(t, h, "/issues/1", nil).Body.String(); !strings.Contains(body, `href="https://comicvine.gamespot.com/saga-1/4000-55/"`) {
		t.Errorf("issue page should link to the matched ComicVine issue:\n%s", body)
	}

	rec := post("/series/1/match/unlink")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/series/1" {
		t.Fatalf("unlink: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}

	// And stop linking once the match is gone.
	if body := get(t, h, "/series/1/scrape/status", nil).Body.String(); strings.Contains(body, "comicvine.gamespot.com") {
		t.Errorf("series action strip should no longer link to ComicVine:\n%s", body)
	}
	if body := get(t, h, "/issues/1", nil).Body.String(); strings.Contains(body, "comicvine.gamespot.com") {
		t.Errorf("issue page should no longer link to ComicVine:\n%s", body)
	}

	sr, err := srv.store.GetSeries(1)
	if err != nil {
		t.Fatal(err)
	}
	if sr.ComicVineVolumeID.Valid || sr.ComicVineURL != "" {
		t.Errorf("series should no longer be matched to a ComicVine volume, got valid=%v url=%q",
			sr.ComicVineVolumeID.Valid, sr.ComicVineURL)
	}
	updated, err := srv.store.GetIssue(1)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ComicVineIssueID.Valid || updated.ComicVineURL != "" || updated.MetadataSource != store.SourceFilename {
		t.Errorf("issue should be rolled back, got id valid=%v url=%q source=%q",
			updated.ComicVineIssueID.Valid, updated.ComicVineURL, updated.MetadataSource)
	}
}

func TestHandleIssueScrapeUnlink(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	post := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, target, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	issue, err := srv.store.GetIssue(1)
	if err != nil {
		t.Fatal(err)
	}
	issue.ComicVineIssueID = sql.NullInt64{Int64: 55, Valid: true}
	issue.ComicVineURL = "https://comicvine.gamespot.com/saga-1/4000-55/"
	issue.MetadataSource = store.SourceComicVine
	if err := srv.store.UpdateIssueMetadata(issue); err != nil {
		t.Fatal(err)
	}

	rec := post("/issues/1/scrape/unlink")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("scrape/unlink: got %d, want %d\n%s", rec.Code, http.StatusSeeOther, rec.Body)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "/issues/1") || !strings.Contains(loc, "msg=cv_unlinked") {
		t.Errorf("redirect location = %q", loc)
	}

	updated, err := srv.store.GetIssue(1)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ComicVineIssueID.Valid || updated.ComicVineURL != "" || updated.MetadataSource != store.SourceFilename {
		t.Errorf("issue should be rolled back, got id valid=%v url=%q source=%q",
			updated.ComicVineIssueID.Valid, updated.ComicVineURL, updated.MetadataSource)
	}
}
