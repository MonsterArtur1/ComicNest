package server

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestAdminCreateUserFirstAccountIsAdmin(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()

	// Anonymous (no accounts yet) can create the first account, even without
	// checking "is_admin" — it becomes an admin regardless.
	rec := postForm(t, h, "/admin/users", "name=ania&password=secret&password_confirm=secret")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin" {
		t.Fatalf("create first user: %d -> %q\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	u, err := srv.store.GetUserByName("ania")
	if err != nil || u == nil {
		t.Fatalf("ania not created: %v", err)
	}
	if !u.IsAdmin {
		t.Error("the first account must be admin regardless of the checkbox")
	}

	// Auth is now enforced: the admin panel needs a login.
	if rec := get(t, h, "/admin", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("admin panel should require login now: %d", rec.Code)
	}
	c := login(t, h, "ania", "secret")
	body := get(t, h, "/admin", asUser(c)).Body.String()
	if !strings.Contains(body, "ania") {
		t.Errorf("admin panel should list ania:\n%s", body)
	}
}

func TestAdminCreateUserValidation(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	mustCreateUser(t, srv.store, "admin", "adminpass", true)
	c := login(t, h, "admin", "adminpass")
	post := func(form string) string {
		return postAs(t, h, c, "/admin/users", form).Body.String()
	}

	if body := post("name=&password=x&password_confirm=x"); !strings.Contains(body, "cannot be empty") {
		t.Errorf("empty name should error:\n%s", body)
	}
	if body := post("name=x%3Ay&password=x&password_confirm=x"); !strings.Contains(body, ":") {
		t.Errorf("colon in name should error:\n%s", body)
	}
	if body := post("name=nowy&password=&password_confirm="); !strings.Contains(body, "Password cannot be empty") {
		t.Errorf("empty password should error:\n%s", body)
	}
	if body := post("name=nowy&password=a&password_confirm=b"); !strings.Contains(body, "do not match") {
		t.Errorf("mismatched passwords should error:\n%s", body)
	}
	if body := post("name=admin&password=x&password_confirm=x"); !strings.Contains(body, "already exists") {
		t.Errorf("duplicate name should error:\n%s", body)
	}

	// A second, non-admin account: the checkbox is respected once it's not
	// the first account.
	postAs(t, h, c, "/admin/users", "name=reader&password=x&password_confirm=x")
	u, err := srv.store.GetUserByName("reader")
	if err != nil || u == nil {
		t.Fatalf("reader not created: %v", err)
	}
	if u.IsAdmin {
		t.Error("second account should not be admin unless the checkbox was set")
	}
}

func TestAdminCannotOrphanLastAdmin(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	admin := mustCreateUser(t, srv.store, "admin", "adminpass", true)
	c := login(t, h, "admin", "adminpass")
	idPath := "/admin/users/" + strconv.FormatInt(admin.ID, 10)

	// Demoting the only admin is refused.
	if body := postAs(t, h, c, idPath+"/admin", "").Body.String(); !strings.Contains(body, "last remaining administrator") {
		t.Errorf("demoting the last admin should be refused:\n%s", body)
	}
	if u, _ := srv.store.GetUser(admin.ID); !u.IsAdmin {
		t.Error("admin flag should be unchanged")
	}

	// Deleting the only admin is refused.
	if body := postAs(t, h, c, idPath+"/delete", "").Body.String(); !strings.Contains(body, "last remaining administrator") {
		t.Errorf("deleting the last admin should be refused:\n%s", body)
	}
	if u, _ := srv.store.GetUser(admin.ID); u == nil {
		t.Error("admin account should still exist")
	}

	// With a second admin, demoting/deleting the first one is fine.
	mustCreateUser(t, srv.store, "admin2", "x", true)
	postAs(t, h, c, idPath+"/admin", "")
	if u, _ := srv.store.GetUser(admin.ID); u.IsAdmin {
		t.Error("demotion should succeed once another admin exists")
	}
}

func TestAdminSetPasswordAndDelete(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	mustCreateUser(t, srv.store, "admin", "adminpass", true)
	reader := mustCreateUser(t, srv.store, "reader", "old-pass", false)
	c := login(t, h, "admin", "adminpass")
	idPath := "/admin/users/" + strconv.FormatInt(reader.ID, 10)

	postAs(t, h, c, idPath+"/password", "password=new-pass&password_confirm=new-pass")
	if !srv.checkPassword("reader", "new-pass") || srv.checkPassword("reader", "old-pass") {
		t.Error("password was not updated")
	}

	postAs(t, h, c, idPath+"/delete", "")
	if u, _ := srv.store.GetUserByName("reader"); u != nil {
		t.Error("reader should be deleted")
	}
}

// TestAdminGatingBlocksNonAdmin checks that a logged-in but non-admin user
// gets 403 from scanning, metadata editing and ComicVine actions, and that
// an admin can reach them.
func TestAdminGatingBlocksNonAdmin(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	mustCreateUser(t, srv.store, "admin", "adminpass", true)
	mustCreateUser(t, srv.store, "reader", "readerpass", false)
	adminCookie := login(t, h, "admin", "adminpass")
	readerCookie := login(t, h, "reader", "readerpass")

	adminOnly := []string{"/series/1/edit", "/series/1/match", "/issues/1/edit", "/admin"}
	for _, path := range adminOnly {
		rec := get(t, h, path, asUser(readerCookie))
		if rec.Code != http.StatusForbidden {
			t.Errorf("reader GET %s: got %d, want 403", path, rec.Code)
		}
		rec = get(t, h, path, asUser(adminCookie))
		if rec.Code != http.StatusOK {
			t.Errorf("admin GET %s: got %d, want 200", path, rec.Code)
		}
	}

	if rec := postAs(t, h, readerCookie, "/scan", ""); rec.Code != http.StatusForbidden {
		t.Errorf("reader scan: got %d, want 403", rec.Code)
	}
	if rec := postAs(t, h, adminCookie, "/scan", ""); rec.Code != http.StatusSeeOther {
		t.Errorf("admin scan: got %d, want 303", rec.Code)
	}

	// Reading/progress stays available to non-admins.
	if rec := postAs(t, h, readerCookie, "/issues/1/read", ""); rec.Code != http.StatusSeeOther {
		t.Errorf("reader mark-read should work: got %d", rec.Code)
	}
	if rec := get(t, h, "/series/1", asUser(readerCookie)); rec.Code != http.StatusOK {
		t.Errorf("reader should still browse series: got %d", rec.Code)
	}
}
