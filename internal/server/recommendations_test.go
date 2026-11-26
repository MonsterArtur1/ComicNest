package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"comicnest/internal/store"
)

// recServer is the auth test server (series 1 "Saga", writer Brian K. Vaughan)
// plus a second series "Y" with the same writer, with recommendation caching
// off. It returns the handler, the id of Y and a login cookie per user.
func recServer(t *testing.T) (*Server, http.Handler, int64, map[string]*http.Cookie) {
	t.Helper()
	srv := newAuthTestServer(t)
	srv.recCache.ttl = -1
	h := srv.Handler()
	yID := addRecSeries(t, srv, "Y", "Brian K. Vaughan", srv.libraries[0].Path)
	return srv, h, yID, map[string]*http.Cookie{
		"ania":   login(t, h, "ania", "a-pass"),
		"bartek": login(t, h, "bartek", "b-pass"),
	}
}

func addRecSeries(t *testing.T, srv *Server, name, writer, lib string) int64 {
	t.Helper()
	sid, err := srv.store.FindOrCreateSeriesByFolder(name, name, lib)
	if err != nil {
		t.Fatal(err)
	}
	is := &store.Issue{SeriesID: sid, Path: name + " 001.cbz", IssueNumber: "1", Writer: writer,
		MetadataSource: store.SourceFilename, FilePages: 3}
	if err := srv.store.InsertIssue(is); err != nil {
		t.Fatal(err)
	}
	return sid
}

func page(t *testing.T, h http.Handler, c *http.Cookie, target string, extra ...*http.Cookie) string {
	t.Helper()
	rec := get(t, h, target, func(r *http.Request) {
		r.AddCookie(c)
		for _, e := range extra {
			r.AddCookie(e)
		}
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d\n%s", target, rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func TestRecommendationsPageIsPerUser(t *testing.T) {
	srv, h, yID, c := recServer(t)
	// ania finishes Saga #1.
	if err := srv.store.SetReadingProgress("ania", 1, 3); err != nil {
		t.Fatal(err)
	}

	ania := page(t, h, c["ania"], "/recommendations")
	mustContain(t, "ania's page", ania, "More from writers you like",
		"Because you read Saga — same writer: Brian K. Vaughan",
		fmt.Sprintf(`href="/series/%d"`, yID), "Not interested", `href="/recommendations"`)
	if strings.Contains(ania, "Finished by") {
		t.Errorf("ania finished Saga herself, it must not be 'popular':\n%s", ania)
	}

	// bartek has read nothing: no taste rows; Saga shows up as popular with an
	// anonymous count — never the name of who read it.
	bartek := page(t, h, c["bartek"], "/recommendations")
	mustContain(t, "bartek's page", bartek, "Popular on this server", "Finished by 1 other reader on this server")
	for _, s := range []string{"More from writers you like", "Because you read", "ania"} {
		if strings.Contains(bartek, s) {
			t.Errorf("bartek's page leaks or mis-scores %q:\n%s", s, bartek)
		}
	}
}

func TestRecommendationsDismissAndRestore(t *testing.T) {
	srv, h, yID, c := recServer(t)
	srv.store.SetReadingProgress("ania", 1, 3)
	form := "series=" + fmt.Sprint(yID)

	rec := postAs(t, h, c["ania"], "/recommendations/dismiss", form)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/recommendations" {
		t.Fatalf("dismiss: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	body := page(t, h, c["ania"], "/recommendations")
	if strings.Contains(body, "More from writers you like") {
		t.Errorf("dismissed series still recommended:\n%s", body)
	}
	mustContain(t, "dismissed list", body, "Hidden from recommendations", `action="/recommendations/restore"`)

	// The dismissal is ania's alone.
	if b := page(t, h, c["bartek"], "/recommendations"); strings.Contains(b, "Hidden from recommendations") {
		t.Errorf("bartek sees ania's dismissed list:\n%s", b)
	}
	// Dismissing from elsewhere returns there (open redirects are refused).
	rec = postAs(t, h, c["ania"], "/recommendations/dismiss", form+"&next="+url.QueryEscape("/?sort=name"))
	if loc := rec.Header().Get("Location"); loc != "/?sort=name" {
		t.Errorf("next= ignored: %q", loc)
	}
	rec = postAs(t, h, c["ania"], "/recommendations/dismiss", form+"&next=//evil.example/")
	if loc := rec.Header().Get("Location"); loc != "/recommendations" {
		t.Errorf("open redirect: %q", loc)
	}

	rec = postAs(t, h, c["ania"], "/recommendations/restore", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("restore: %d", rec.Code)
	}
	body = page(t, h, c["ania"], "/recommendations")
	mustContain(t, "after restore", body, "More from writers you like")
	if strings.Contains(body, "Hidden from recommendations") {
		t.Errorf("restored series still listed as hidden:\n%s", body)
	}

	// Unknown or malformed series ids are rejected.
	for _, f := range []string{"series=999", "series=abc", ""} {
		if rec := postAs(t, h, c["ania"], "/recommendations/dismiss", f); rec.Code != http.StatusNotFound {
			t.Errorf("dismiss %q: %d, want 404", f, rec.Code)
		}
	}
}

func TestRecommendationsEmptyAndStartStates(t *testing.T) {
	srv, h, yID, c := recServer(t)
	// bartek has no history and nobody read anything: "start with these".
	body := page(t, h, c["bartek"], "/recommendations")
	mustContain(t, "start state", body, "Start with these", "Nothing to go on yet", "issue in your library", "Saga")

	// Everything dismissed: a friendly empty state, with the way back.
	for _, id := range []int64{1, yID} {
		postAs(t, h, c["bartek"], "/recommendations/dismiss", fmt.Sprintf("series=%d", id))
	}
	body = page(t, h, c["bartek"], "/recommendations")
	mustContain(t, "empty state", body, "No recommendations right now", "Hidden from recommendations", "Restore")
	_ = srv
}

func TestRecommendationsRespectLibrarySelector(t *testing.T) {
	srv, h, _, c := recServer(t)
	srv.store.SetReadingProgress("ania", 1, 3)
	other := addRecSeries(t, srv, "Elsewhere", "Brian K. Vaughan", "/some/other/library")

	// All libraries: both the series in the configured library and the other.
	all := page(t, h, c["ania"], "/recommendations")
	mustContain(t, "all libraries", all, fmt.Sprintf(`href="/series/%d"`, other))

	// Library 0 selected (cookie): the foreign series is out.
	lib := &http.Cookie{Name: libraryCookie, Value: "0"}
	scoped := page(t, h, c["ania"], "/recommendations", lib)
	if strings.Contains(scoped, fmt.Sprintf(`href="/series/%d"`, other)) {
		t.Errorf("series of another library recommended under the library selector:\n%s", scoped)
	}
	mustContain(t, "scoped", scoped, "More from writers you like")
}

func TestHomeForYouStrip(t *testing.T) {
	srv, h, _, c := recServer(t)
	srv.store.SetReadingProgress("ania", 1, 3)

	home := page(t, h, c["ania"], "/")
	mustContain(t, "home", home, `class="foryou-strip"`, "Because you read Saga", `href="/recommendations"`)
	if strings.Contains(home, `action="/recommendations/dismiss"`) {
		t.Error("home strip should not carry dismiss buttons")
	}
	// Like the releases strip: first page, unfiltered only.
	if f := page(t, h, c["ania"], "/?filter=unread"); strings.Contains(f, "foryou-strip") {
		t.Error("strip shown on a filtered library page")
	}
	// No taste, no strip (the "start with these" fallback lives on /recommendations).
	if b := page(t, h, c["bartek"], "/"); strings.Contains(b, "foryou-strip") && strings.Contains(b, "Start with") {
		t.Errorf("fallback leaked onto the home page:\n%s", b)
	}
}

func TestSeriesPageMoreLikeThis(t *testing.T) {
	srv, h, yID, c := recServer(t)
	addRecSeries(t, srv, "Unrelated", "Somebody Else", srv.libraries[0].Path)

	body := page(t, h, c["bartek"], "/series/1")
	mustContain(t, "series page", body, "More like this", fmt.Sprintf(`href="/series/%d"`, yID), "Same writer: Brian K. Vaughan")
	if strings.Contains(body, ">Unrelated<") {
		t.Errorf("unrelated series listed under More like this:\n%s", body)
	}
	// A series nothing resembles has no such section.
	var lone int64
	lone = addRecSeries(t, srv, "Lonely", "Nobody Alike", srv.libraries[0].Path)
	if b := page(t, h, c["bartek"], fmt.Sprintf("/series/%d", lone)); strings.Contains(b, "More like this") {
		t.Errorf("empty More like this shown:\n%s", b)
	}
}

func TestRecommendationsRequireLogin(t *testing.T) {
	_, h, _, _ := recServer(t)
	if rec := get(t, h, "/recommendations", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("anonymous GET: %d", rec.Code)
	}
	if rec := postForm(t, h, "/recommendations/dismiss", "series=1"); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous dismiss: %d", rec.Code)
	}
}
