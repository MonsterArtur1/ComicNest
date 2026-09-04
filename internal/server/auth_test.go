package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"comicnest/internal/config"
)

// newAuthTestServer is newTestServer with two accounts and OPDS enabled.
func newAuthTestServer(t *testing.T) *Server {
	t.Helper()
	srv, _ := newTestServer(t, true)
	srv.cfg.Users = []config.User{{Name: "ania", Password: "a-pass"}, {Name: "bartek", Password: "b-pass"}}
	return srv
}

// login performs the form login and returns the session cookie.
func login(t *testing.T, h http.Handler, name, password string) *http.Cookie {
	t.Helper()
	rec := postForm(t, h, "/login", "name="+name+"&password="+password+"&next=/series/1")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login %s: %d\n%s", name, rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); loc != "/series/1" {
		t.Errorf("login should follow next=, got %q", loc)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Errorf("session cookie should be HttpOnly + SameSite=Strict: %+v", c)
			}
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

func asUser(c *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(c) }
}

func postAs(t *testing.T, h http.Handler, c *http.Cookie, target, form string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthGatesWebUI(t *testing.T) {
	srv := newAuthTestServer(t)
	h := srv.Handler()

	// Pages redirect to the login form; non-GET actions are refused outright.
	rec := get(t, h, "/series/1?filter=all", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?next=%2Fseries%2F1%3Ffilter%3Dall" {
		t.Errorf("anonymous page request: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := postForm(t, h, "/issues/1/read", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous POST: got %d, want 401", rec.Code)
	}
	// Login page and static assets stay reachable.
	if rec := get(t, h, "/login", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="password"`) {
		t.Errorf("login page: %d", rec.Code)
	}
	if rec := get(t, h, "/static/styles.css", nil); rec.Code != http.StatusOK {
		t.Errorf("static: %d", rec.Code)
	}

	// Wrong password → 401 with the form and message; unknown user the same.
	for _, form := range []string{"name=ania&password=wrong", "name=nobody&password=a-pass", ""} {
		rec := postForm(t, h, "/login", form)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Nieprawidłowa nazwa użytkownika lub hasło.") {
			t.Errorf("bad login %q: %d", form, rec.Code)
		}
	}

	// Correct login → session; pages show the user and a logout button.
	c := login(t, h, "ania", "a-pass")
	body := get(t, h, "/", asUser(c)).Body.String()
	if !strings.Contains(body, "👤 ania") || !strings.Contains(body, `action="/logout"`) {
		t.Errorf("home page should show the logged-in user:\n%s", body)
	}
	// Open redirects are neutralised.
	rec = postForm(t, h, "/login", "name=ania&password=a-pass&next=//evil.example/")
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("open redirect not blocked: %q", loc)
	}

	// Logout drops the session.
	rec = postAs(t, h, c, "/logout", "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := get(t, h, "/", asUser(c)); rec.Code != http.StatusSeeOther {
		t.Errorf("session should be gone after logout, got %d", rec.Code)
	}
}

func TestProgressIsPerUser(t *testing.T) {
	srv := newAuthTestServer(t)
	h := srv.Handler()
	ania := login(t, h, "ania", "a-pass")
	bartek := login(t, h, "bartek", "b-pass")

	// Ania reads two pages in the browser.
	if rec := postAs(t, h, ania, "/issues/1/progress", "page=2"); rec.Code != http.StatusNoContent {
		t.Fatalf("progress: %d", rec.Code)
	}
	if body := get(t, h, "/issues/1", asUser(ania)).Body.String(); !strings.Contains(body, "Czytaj dalej (str. 2)") {
		t.Errorf("ania should see her progress:\n%s", body)
	}
	if body := get(t, h, "/issues/1", asUser(bartek)).Body.String(); strings.Contains(body, "Przeczytano") {
		t.Errorf("bartek must not see ania's progress:\n%s", body)
	}
	if body := get(t, h, "/?filter=reading", asUser(bartek)).Body.String(); strings.Contains(body, `card-title">Saga`) {
		t.Errorf("bartek's 'reading' filter must be empty:\n%s", body)
	}

	// Bartek marks it read; ania's progress is untouched.
	postAs(t, h, bartek, "/issues/1/read", "")
	if body := get(t, h, "/", asUser(bartek)).Body.String(); !strings.Contains(body, `class="read-mark"`) {
		t.Errorf("bartek should see the read mark")
	}
	if body := get(t, h, "/", asUser(ania)).Body.String(); strings.Contains(body, `class="read-mark"`) {
		t.Errorf("ania must not see bartek's read mark")
	}
	if p, _ := srv.store.GetReadingProgress("ania", 1); p == nil || p.Page != 2 {
		t.Errorf("ania's progress changed: %+v", p)
	}

	// OPDS: Basic auth with the same accounts, progress on the same records.
	basic := func(u, p string) func(*http.Request) { return func(r *http.Request) { r.SetBasicAuth(u, p) } }
	if rec := get(t, h, "/opds", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("OPDS without credentials: %d", rec.Code)
	}
	if rec := get(t, h, "/opds", basic("ania", "wrong")); rec.Code != http.StatusUnauthorized {
		t.Errorf("OPDS with wrong password: %d", rec.Code)
	}
	body := assertXML(t, get(t, h, "/opds/series/1", basic("ania", "a-pass")))
	if !strings.Contains(body, `pse:lastRead="2"`) {
		t.Errorf("ania's OPDS feed should show her web progress:\n%s", body)
	}
	body = assertXML(t, get(t, h, "/opds/series/1", basic("bartek", "b-pass")))
	if !strings.Contains(body, `pse:lastRead="3"`) {
		t.Errorf("bartek's OPDS feed should show his (finished) progress:\n%s", body)
	}
	body = assertXML(t, get(t, h, "/opds/reading", basic("ania", "a-pass")))
	if !strings.Contains(body, "/opds/issues/1/file") {
		t.Errorf("ania's 'currently reading' should list the issue:\n%s", body)
	}
	body = assertXML(t, get(t, h, "/opds/reading", basic("bartek", "b-pass")))
	if strings.Contains(body, "<entry>") {
		t.Errorf("bartek finished it, his 'currently reading' should be empty:\n%s", body)
	}
	// Streaming a page through OPDS records progress for the Basic-auth user.
	get(t, h, "/opds/issues/1/pages/2", basic("ania", "a-pass"))
	if p, _ := srv.store.GetReadingProgress("ania", 1); p == nil || p.Page != 3 {
		t.Errorf("OPDS streaming should advance ania's progress: %+v", p)
	}
}

func TestAnonymousModeUnchanged(t *testing.T) {
	srv, _ := newTestServer(t, true)
	h := srv.Handler()
	if rec := get(t, h, "/", nil); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `action="/logout"`) {
		t.Errorf("without accounts the UI is open and shows no user box: %d", rec.Code)
	}
	if rec := get(t, h, "/login", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("login page without accounts should redirect home, got %d", rec.Code)
	}
	if rec := get(t, h, "/opds", nil); rec.Code != http.StatusOK {
		t.Errorf("OPDS without accounts is open: %d", rec.Code)
	}
}

func TestAdoptAnonymousProgress(t *testing.T) {
	srv, _ := newTestServer(t, false)
	st := srv.store
	if err := st.SetReadingProgress("", 1, 2); err != nil {
		t.Fatal(err)
	}
	moved, err := st.AdoptAnonymousProgress("ania")
	if err != nil || moved != 1 {
		t.Fatalf("adopt: moved=%d err=%v", moved, err)
	}
	if p, _ := st.GetReadingProgress("ania", 1); p == nil || p.Page != 2 {
		t.Errorf("ania should own the old progress: %+v", p)
	}
	if p, _ := st.GetReadingProgress("", 1); p != nil {
		t.Errorf("anonymous row should be gone: %+v", p)
	}
	// A collision keeps the user's own row and drops the anonymous one.
	st.SetReadingProgress("", 1, 3)
	if moved, err := st.AdoptAnonymousProgress("ania"); err != nil || moved != 0 {
		t.Errorf("collision: moved=%d err=%v", moved, err)
	}
	if p, _ := st.GetReadingProgress("ania", 1); p == nil || p.Page != 2 {
		t.Errorf("ania's row must win the collision: %+v", p)
	}
	if p, _ := st.GetReadingProgress("", 1); p != nil {
		t.Errorf("anonymous leftover should be deleted: %+v", p)
	}
}
