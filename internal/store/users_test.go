package store

import "testing"

func TestUserCRUD(t *testing.T) {
	st := openTestStore(t)

	if n, err := st.CountUsers(); err != nil || n != 0 {
		t.Fatalf("CountUsers on empty store: %d, %v", n, err)
	}
	if u, err := st.GetUserByName("ania"); err != nil || u != nil {
		t.Fatalf("GetUserByName on empty store: %+v, %v", u, err)
	}

	id, err := st.CreateUser("ania", "hash1", true)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := st.CreateUser("bartek", "hash2", false); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if n, err := st.CountUsers(); err != nil || n != 2 {
		t.Fatalf("CountUsers: %d, %v", n, err)
	}
	if n, err := st.CountAdmins(); err != nil || n != 1 {
		t.Fatalf("CountAdmins: %d, %v", n, err)
	}

	u, err := st.GetUserByName("ania")
	if err != nil || u == nil {
		t.Fatalf("GetUserByName: %+v, %v", u, err)
	}
	if u.ID != id || u.PasswordHash != "hash1" || !u.IsAdmin || u.LastLoginAt.Valid {
		t.Errorf("unexpected user: %+v", u)
	}

	users, err := st.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[0].Name != "ania" || users[1].Name != "bartek" {
		t.Errorf("ListUsers order/content: %+v", users)
	}

	if err := st.TouchUserLogin("ania"); err != nil {
		t.Fatal(err)
	}
	u, _ = st.GetUser(id)
	if !u.LastLoginAt.Valid || u.LastLoginAt.String == "" {
		t.Errorf("last_login_at should be set after TouchUserLogin: %+v", u)
	}

	if err := st.SetUserPassword(id, "hash1-new"); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.GetUser(id); u.PasswordHash != "hash1-new" {
		t.Errorf("password not updated: %+v", u)
	}

	if err := st.SetUserAdmin(id, false); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CountAdmins(); err != nil || n != 0 {
		t.Fatalf("CountAdmins after demotion: %d, %v", n, err)
	}

	if err := st.DeleteUser(id); err != nil {
		t.Fatal(err)
	}
	if u, err := st.GetUser(id); err != nil || u != nil {
		t.Errorf("deleted user should be gone: %+v, %v", u, err)
	}
	if n, err := st.CountUsers(); err != nil || n != 1 {
		t.Fatalf("CountUsers after delete: %d, %v", n, err)
	}
}

func TestCreateUserDuplicateNameFails(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.CreateUser("ania", "hash", false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser("ania", "other-hash", true); err == nil {
		t.Error("duplicate name should fail (UNIQUE constraint)")
	}
}
