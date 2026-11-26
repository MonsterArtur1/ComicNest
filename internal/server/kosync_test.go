package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"comicnest/internal/kosync"
	"comicnest/internal/store"
)

// kosyncFixture is a test server with kosync routes mounted, one library issue
// (3 pages, id 1) hashed, and two accounts that have each logged in once.
type kosyncFixture struct {
	srv     *Server
	h       http.Handler
	issueID int64
	partial string
	byName  string
}

func newKosyncFixture(t *testing.T) *kosyncFixture {
	t.Helper()
	srv, cbz := newTestServer(t, false)
	srv.cfg.KosyncEnabled = true
	srv.kosyncRoutesRegistered = true
	srv.kosyncRoutes()

	issue, err := srv.store.GetIssue(1)
	if err != nil || issue == nil {
		t.Fatalf("seed issue: %v", err)
	}
	partial, err := kosync.PartialMD5(cbz)
	if err != nil {
		t.Fatal(err)
	}
	byName := kosync.FilenameMD5(cbz)
	if err := srv.store.SetIssueHashes(issue.ID, partial, byName, 10, 1); err != nil {
		t.Fatal(err)
	}

	mustCreateUser(t, srv.store, "alice", "secret", true)
	mustCreateUser(t, srv.store, "bob", "hunter2", false)
	f := &kosyncFixture{srv: srv, h: srv.Handler(), issueID: issue.ID, partial: partial, byName: byName}
	f.webLogin(t, "alice", "secret")
	f.webLogin(t, "bob", "hunter2")
	return f
}

func (f *kosyncFixture) webLogin(t *testing.T, name, pw string) {
	t.Helper()
	form := url.Values{"name": {name}, "password": {pw}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login %s: %d", name, rec.Code)
	}
}

func (f *kosyncFixture) do(method, path, user, pw, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Accept", "application/vnd.koreader.v1+json")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.Header.Set("x-auth-user", user)
		req.Header.Set("x-auth-key", kosync.PasswordKey(pw))
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *kosyncFixture) alice(method, path, body string) *httptest.ResponseRecorder {
	return f.do(method, path, "alice", "secret", body)
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("bad JSON %q: %v", rec.Body.String(), err)
	}
	return m
}

func putBody(doc, progress string, pct float64, deviceID string) string {
	b, _ := json.Marshal(map[string]any{"document": doc, "progress": progress,
		"percentage": pct, "device": "Kobo", "device_id": deviceID})
	return string(b)
}

func TestKosyncDisabledByDefault(t *testing.T) {
	srv, _ := newTestServer(t, false)
	for _, p := range []string{"/kosync/healthcheck", "/kosync/users/auth"} {
		if rec := get(t, srv.Handler(), p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s with kosync disabled: %d, want 404", p, rec.Code)
		}
	}
}

func TestKosyncHealthcheckNeedsNoAuth(t *testing.T) {
	f := newKosyncFixture(t)
	rec := f.do("GET", "/kosync/healthcheck", "", "", "")
	if rec.Code != 200 || decodeJSON(t, rec)["state"] != "OK" {
		t.Errorf("healthcheck: %d %s", rec.Code, rec.Body)
	}
}

func TestKosyncRegisterRefused(t *testing.T) {
	f := newKosyncFixture(t)
	rec := f.do("POST", "/kosync/users/create", "", "", `{"username":"eve","password":"x"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("register: %d, want 403", rec.Code)
	}
	if !strings.Contains(decodeJSON(t, rec)["message"].(string), "Registration is disabled") {
		t.Errorf("register message: %s", rec.Body)
	}
	if u, _ := f.srv.store.GetUserByName("eve"); u != nil {
		t.Error("registration created an account")
	}
}

func TestKosyncAuth(t *testing.T) {
	f := newKosyncFixture(t)
	if rec := f.alice("GET", "/kosync/users/auth", ""); rec.Code != 200 || decodeJSON(t, rec)["authorized"] != "OK" {
		t.Errorf("valid auth: %d %s", rec.Code, rec.Body)
	}
	// uppercase hex key is accepted too
	req := httptest.NewRequest("GET", "/kosync/users/auth", nil)
	req.Header.Set("x-auth-user", "alice")
	req.Header.Set("x-auth-key", strings.ToUpper(kosync.PasswordKey("secret")))
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("uppercase key: %d", rec.Code)
	}

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"wrong password": f.do("GET", "/kosync/users/auth", "alice", "nope", ""),
		"unknown user":   f.do("GET", "/kosync/users/auth", "mallory", "secret", ""),
		"no headers":     f.do("GET", "/kosync/users/auth", "", "", ""),
		"other user key": f.do("GET", "/kosync/users/auth", "alice", "hunter2", ""),
	} {
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, rec.Code)
		}
		if decodeJSON(t, rec)["message"] != "Unauthorized" {
			t.Errorf("%s: body %s", name, rec.Body)
		}
	}
	// Every protected endpoint rejects bad credentials.
	for _, c := range [][2]string{{"PUT", "/kosync/syncs/progress"}, {"GET", "/kosync/syncs/progress/abc"}} {
		if rec := f.do(c[0], c[1], "alice", "wrong", putBody("abc", "1", 0.1, "d")); rec.Code != 401 {
			t.Errorf("%s %s with bad auth: %d", c[0], c[1], rec.Code)
		}
	}
}

func TestKosyncNoKeyUntilLoginAfterEnable(t *testing.T) {
	srv, _ := newTestServer(t, false)
	mustCreateUser(t, srv.store, "carol", "pw", false) // created while kosync was off
	srv.cfg.KosyncEnabled = true
	srv.kosyncRoutesRegistered = true
	srv.kosyncRoutes()
	f := &kosyncFixture{srv: srv, h: srv.Handler()}

	if rec := f.do("GET", "/kosync/users/auth", "carol", "pw", ""); rec.Code != 401 {
		t.Errorf("before first login: %d, want 401", rec.Code)
	}
	f.webLogin(t, "carol", "pw")
	if rec := f.do("GET", "/kosync/users/auth", "carol", "pw", ""); rec.Code != 200 {
		t.Errorf("after login: %d, want 200", rec.Code)
	}
}

func TestKosyncKeyNotStoredWhenDisabled(t *testing.T) {
	srv, _ := newTestServer(t, false)
	mustCreateUser(t, srv.store, "dave", "pw", false)
	f := &kosyncFixture{srv: srv, h: srv.Handler()}
	f.webLogin(t, "dave", "pw")
	if k, _ := srv.store.GetUserKosyncKey("dave"); k != "" {
		t.Errorf("md5 verifier stored while kosync is disabled: %q", k)
	}
}

func TestKosyncPasswordResetRefreshesKey(t *testing.T) {
	f := newKosyncFixture(t)
	bob, _ := f.srv.store.GetUserByName("bob")
	form := url.Values{"password": {"newpw"}, "password_confirm": {"newpw"}}
	req := httptest.NewRequest("POST", "/admin/users/"+strconv.FormatInt(bob.ID, 10)+"/password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(f.sessionFor(t, "alice", "secret"))
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reset password: %d", rec.Code)
	}
	if rec := f.do("GET", "/kosync/users/auth", "bob", "hunter2", ""); rec.Code != 401 {
		t.Errorf("old password: %d, want 401", rec.Code)
	}
	if rec := f.do("GET", "/kosync/users/auth", "bob", "newpw", ""); rec.Code != 200 {
		t.Errorf("new password: %d, want 200", rec.Code)
	}
}

func (f *kosyncFixture) sessionFor(t *testing.T, name, pw string) *http.Cookie {
	t.Helper()
	form := url.Values{"name": {name}, "password": {pw}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestKosyncPutValidation(t *testing.T) {
	f := newKosyncFixture(t)
	for name, body := range map[string]string{
		"empty":         ``,
		"not json":      `nope`,
		"no document":   `{"progress":"1","percentage":0.1}`,
		"no progress":   `{"document":"abc","percentage":0.1}`,
		"no percentage": `{"document":"abc","progress":"1"}`,
	} {
		rec := f.alice("PUT", "/kosync/syncs/progress", body)
		if rec.Code != http.StatusForbidden || decodeJSON(t, rec)["message"] != "Invalid request" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestKosyncUnknownDocument(t *testing.T) {
	f := newKosyncFixture(t)
	doc := strings.Repeat("a", 32)

	rec := f.alice("GET", "/kosync/syncs/progress/"+doc, "")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("unknown GET: %d %q", rec.Code, rec.Body)
	}

	before := time.Now().Unix()
	rec = f.alice("PUT", "/kosync/syncs/progress", putBody(doc, "42", 0.42, "dev-1"))
	if rec.Code != 200 {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	put := decodeJSON(t, rec)
	if put["document"] != doc || int64(put["timestamp"].(float64)) < before {
		t.Errorf("PUT response: %v", put)
	}

	got := decodeJSON(t, f.alice("GET", "/kosync/syncs/progress/"+doc, ""))
	if got["document"] != doc || got["progress"] != "42" || got["percentage"] != 0.42 ||
		got["device"] != "Kobo" || got["device_id"] != "dev-1" || got["timestamp"] != put["timestamp"] {
		t.Errorf("GET after PUT: %v", got)
	}

	// Newer push replaces it.
	f.alice("PUT", "/kosync/syncs/progress", putBody(doc, "50", 0.5, "dev-2"))
	got = decodeJSON(t, f.alice("GET", "/kosync/syncs/progress/"+doc, ""))
	if got["progress"] != "50" || got["device_id"] != "dev-2" {
		t.Errorf("GET after second PUT: %v", got)
	}
}

func TestKosyncUsersAreSeparate(t *testing.T) {
	f := newKosyncFixture(t)
	doc := strings.Repeat("b", 32)
	f.alice("PUT", "/kosync/syncs/progress", putBody(doc, "7", 0.07, "a"))
	if rec := f.do("GET", "/kosync/syncs/progress/"+doc, "bob", "hunter2", ""); strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Errorf("bob sees alice's progress: %s", rec.Body)
	}
	// And for a library issue.
	f.alice("PUT", "/kosync/syncs/progress", putBody(f.partial, "2", 0.66, "a"))
	if p, _ := f.srv.store.GetReadingProgress("bob", f.issueID); p != nil {
		t.Errorf("bob got reading progress: %+v", p)
	}
	if p, _ := f.srv.store.GetReadingProgress("alice", f.issueID); p == nil || p.Page != 2 {
		t.Errorf("alice progress: %+v", p)
	}
}

func TestKosyncPutMapsToReadingProgress(t *testing.T) {
	f := newKosyncFixture(t) // issue has 3 pages
	st := f.srv.store

	if rec := f.alice("PUT", "/kosync/syncs/progress", putBody(f.partial, "2", 2.0/3, "k1")); rec.Code != 200 {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	p, _ := st.GetReadingProgress("alice", f.issueID)
	if p == nil || p.Page != 2 {
		t.Fatalf("progress after page 2: %+v", p)
	}
	times, _ := st.PageReadTimes("alice")
	if len(times) != 1 {
		t.Errorf("page reads logged: %d, want 1", len(times))
	}

	// Filename-matching document id maps to the same issue; last page finishes it.
	if rec := f.alice("PUT", "/kosync/syncs/progress", putBody(f.byName, "3", 1, "k1")); rec.Code != 200 {
		t.Fatalf("PUT by filename: %d", rec.Code)
	}
	p, _ = st.GetReadingProgress("alice", f.issueID)
	if p == nil || p.Page != 3 {
		t.Fatalf("progress after last page: %+v", p)
	}
	if n, _ := st.CountReadIssues("alice"); n != 1 {
		t.Errorf("finished issues: %d, want 1", n)
	}

	// A page beyond the total is clamped.
	f.do("PUT", "/kosync/syncs/progress", "bob", "hunter2", putBody(f.partial, "99", 1, "k2"))
	if p, _ := st.GetReadingProgress("bob", f.issueID); p == nil || p.Page != 3 {
		t.Errorf("clamped progress: %+v", p)
	}
}

func TestKosyncGetReturnsNewerComicNestProgress(t *testing.T) {
	f := newKosyncFixture(t)
	st := f.srv.store

	// KOReader pushed page 1 an hour ago... then the web reader moved on.
	save := func(ts int64) {
		t.Helper()
		err := st.SaveKosyncProgress("alice", store.KosyncProgress{Document: f.partial, Progress: "1",
			Percentage: 0.33, Device: "Kobo", DeviceID: "k1", Timestamp: ts})
		if err != nil {
			t.Fatal(err)
		}
	}
	save(time.Now().Add(-time.Hour).Unix())
	if err := st.SetReadingProgress("alice", f.issueID, 2); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON(t, f.alice("GET", "/kosync/syncs/progress/"+f.partial, ""))
	if got["progress"] != "2" || got["device"] != "ComicNest" || got["device_id"] != "comicnest" {
		t.Fatalf("want ComicNest progress, got %v", got)
	}
	if pct := got["percentage"].(float64); pct < 0.66 || pct > 0.67 {
		t.Errorf("percentage = %v, want 2/3", pct)
	}

	// A later KOReader push wins again.
	save(time.Now().Add(time.Hour).Unix())
	got = decodeJSON(t, f.alice("GET", "/kosync/syncs/progress/"+f.partial, ""))
	if got["progress"] != "1" || got["device"] != "Kobo" {
		t.Errorf("want KOReader progress, got %v", got)
	}
}

func TestKosyncGetFromWebReaderOnly(t *testing.T) {
	f := newKosyncFixture(t)
	// Nothing read yet: empty.
	if rec := f.alice("GET", "/kosync/syncs/progress/"+f.partial, ""); strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Errorf("unread: %s", rec.Body)
	}
	// Only a lone first page: still empty.
	f.srv.store.SetReadingProgress("alice", f.issueID, 1)
	if rec := f.alice("GET", "/kosync/syncs/progress/"+f.partial, ""); strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Errorf("first page only: %s", rec.Body)
	}
	f.srv.store.SetReadingProgress("alice", f.issueID, 3)
	got := decodeJSON(t, f.alice("GET", "/kosync/syncs/progress/"+f.byName, ""))
	if got["progress"] != "3" || got["percentage"] != 1.0 {
		t.Errorf("web progress via filename id: %v", got)
	}
}

func TestKosyncOpenWithoutAccounts(t *testing.T) {
	srv, _ := newTestServer(t, false)
	srv.kosyncRoutesRegistered = true
	srv.kosyncRoutes()
	f := &kosyncFixture{srv: srv, h: srv.Handler()}
	if rec := f.do("GET", "/kosync/users/auth", "", "", ""); rec.Code != 200 {
		t.Errorf("open auth: %d", rec.Code)
	}
}

func TestKosyncPage(t *testing.T) {
	cases := []struct {
		progress string
		pct      float64
		total    int
		want     int
	}{
		{"5", 0.5, 10, 5},
		{"12", 1, 10, 10},          // clamped
		{"10", 1, 10, 10},          // last page
		{"9", 0.9, 10, 9},          // not finished
		{"", 0.5, 10, 5},           // derived from percentage
		{"/body/x", 0.996, 10, 10}, // xpointer at the end
		{"/body/x", 0.001, 10, 1},
		{"abc", 0, 10, 0},
		{"7", 0.7, 0, 7}, // unknown total: trust the page
	}
	for _, c := range cases {
		if got := kosyncPage(c.progress, c.pct, c.total); got != c.want {
			t.Errorf("kosyncPage(%q, %v, %d) = %d, want %d", c.progress, c.pct, c.total, got, c.want)
		}
	}
}
