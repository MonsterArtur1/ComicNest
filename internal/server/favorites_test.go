package server

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// TestIssueFavoriteToggle checks the issue-page "add to favorites" toggle
// and its flash message. There is no series-level favorite — only
// individual issues can be favorited.
func TestIssueFavoriteToggle(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()

	body := get(t, h, "/issues/1", nil).Body.String()
	if strings.Contains(body, "★ Favorited") {
		t.Error("issue should not start as a favorite")
	}

	rec := postForm(t, h, "/issues/1/favorite", "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/issues/1?msg=fav_ok" {
		t.Fatalf("favorite toggle: %d, Location=%q", rec.Code, rec.Header().Get("Location"))
	}
	if fav, err := srv.store.IsIssueFavorite("", 1); err != nil || !fav {
		t.Fatalf("IsIssueFavorite after toggle = %v, %v", fav, err)
	}
	body = get(t, h, "/issues/1?msg=fav_ok", nil).Body.String()
	if !strings.Contains(body, "★ Favorited") || !strings.Contains(body, "Added to favorites.") {
		t.Errorf("issue page should show the favorite state and flash:\n%s", body)
	}

	rec = postForm(t, h, "/issues/1/favorite", "")
	if rec.Header().Get("Location") != "/issues/1?msg=unfav_ok" {
		t.Fatalf("un-favorite toggle Location=%q", rec.Header().Get("Location"))
	}
	if fav, err := srv.store.IsIssueFavorite("", 1); err != nil || fav {
		t.Fatalf("IsIssueFavorite after un-toggle = %v, %v", fav, err)
	}
}

// TestSeriesIssueFavoriteFilter checks the issue-favorite toggle within a
// series page and its "favorite" issue-list filter.
func TestSeriesIssueFavoriteFilter(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()

	if rec := postForm(t, h, "/issues/1/favorite", ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("favorite issue 1: %d", rec.Code)
	}

	body := get(t, h, "/series/1?filter=favorite", nil).Body.String()
	if !strings.Contains(body, "/issues/1") {
		t.Errorf("favorite filter should list issue 1:\n%s", body)
	}
	if strings.Contains(body, `href="/issues/2"`) {
		t.Errorf("favorite filter should not list unfavorited issue 2:\n%s", body)
	}
}

// TestFavoriteIssueSurfacesSeriesWithFilterLink checks that favoriting a
// single issue (with no way to favorite the series itself) makes the series
// show up under the library page's "favorite" filter, and that clicking
// through from there lands on the series page already filtered to
// favorites — not the unfiltered issue list.
func TestFavoriteIssueSurfacesSeriesWithFilterLink(t *testing.T) {
	srv, cbz := newTestServer(t, false)
	h := srv.Handler()
	addSeries(t, srv.store, filepath.Dir(cbz), 1) // adds "S01" alongside "Saga"

	if rec := postForm(t, h, "/issues/1/favorite", ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("favorite issue 1: %d", rec.Code)
	}

	body := get(t, h, "/?filter=favorite", nil).Body.String()
	if got := titles(body); len(got) != 1 || got[0] != "Saga" {
		t.Errorf("favorite filter = %v, want only [Saga] (surfaced by its favorited issue)", got)
	}
	if !strings.Contains(body, `href="/series/1?filter=favorite"`) {
		t.Errorf("series card should link straight into the favorite-filtered series view:\n%s", body)
	}

	seriesBody := get(t, h, "/series/1?filter=favorite", nil).Body.String()
	if !strings.Contains(seriesBody, "/issues/1") {
		t.Errorf("favorite-filtered series view should list issue 1:\n%s", seriesBody)
	}
	if strings.Contains(seriesBody, `href="/issues/2"`) {
		t.Errorf("favorite-filtered series view should not list unfavorited issue 2:\n%s", seriesBody)
	}

	// Outside the favorite view, the series card must not force the filter.
	plainBody := get(t, h, "/", nil).Body.String()
	if strings.Contains(plainBody, `href="/series/1?filter=favorite"`) {
		t.Errorf("series card should not force the favorite filter outside the favorite view:\n%s", plainBody)
	}
}

// TestOPDSFavorites checks the "Favorites" feed and its link from the root
// navigation feed.
func TestOPDSFavorites(t *testing.T) {
	srv, _ := newTestServer(t, true)
	h := srv.Handler()

	body := assertXML(t, get(t, h, "/opds", nil))
	if !strings.Contains(body, `href="http://nas.local:8080/opds/favorites"`) {
		t.Errorf("root feed should link to /opds/favorites:\n%s", body)
	}

	body = assertXML(t, get(t, h, "/opds/favorites", nil))
	if strings.Contains(body, "<entry>") {
		t.Errorf("favorites feed should be empty before anything is favorited:\n%s", body)
	}

	if err := srv.store.SetIssueFavorite("", 1, true); err != nil {
		t.Fatal(err)
	}
	body = assertXML(t, get(t, h, "/opds/favorites", nil))
	if !strings.Contains(body, "/opds/issues/1/file") {
		t.Errorf("favorites feed should list the favorited issue:\n%s", body)
	}
}
