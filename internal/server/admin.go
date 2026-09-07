package server

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"comicnest/internal/opds"
	"comicnest/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// adminUserRow is one account shaped for the admin panel table.
type adminUserRow struct {
	ID        int64
	Name      string
	IsAdmin   bool
	LastLogin string // "nigdy" or "2006-01-02 15:04" local time
	CreatedAt string
}

type adminData struct {
	Users []adminUserRow
	// IsFirstRun is true when no account exists yet: the add-user form force
	// the new account to be an admin (there is no other way back in).
	IsFirstRun bool
	Error      string
}

// adminPageData loads the current account list for the admin panel.
func (s *Server) adminPageData() (adminData, error) {
	users, err := s.store.ListUsers()
	if err != nil {
		return adminData{}, err
	}
	rows := make([]adminUserRow, len(users))
	for i, u := range users {
		last := "nigdy"
		if t := opds.ParseDBTime(u.LastLoginAt.String, time.Time{}); !t.IsZero() {
			last = t.Local().Format("2006-01-02 15:04")
		}
		rows[i] = adminUserRow{
			ID:        u.ID,
			Name:      u.Name,
			IsAdmin:   u.IsAdmin,
			LastLogin: last,
			CreatedAt: opds.ParseDBTime(u.CreatedAt, time.Time{}).Local().Format("2006-01-02 15:04"),
		}
	}
	return adminData{Users: rows, IsFirstRun: len(rows) == 0}, nil
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	data, err := s.adminPageData()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "admin.html", data)
}

// renderAdminError redraws the panel with a flash error, keeping the
// (still valid) account list visible.
func (s *Server) renderAdminError(w http.ResponseWriter, r *http.Request, msg string) {
	data, err := s.adminPageData()
	if err != nil {
		s.serverError(w, err)
		return
	}
	data.Error = msg
	s.render(w, r, "admin.html", data)
}

// handleAdminCreateUser adds a new account. The very first account is always
// an admin, regardless of the checkbox — otherwise, the moment it exists,
// auth is enforced and there would be no way back into the admin panel.
func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	password := r.FormValue("password")
	confirm := r.FormValue("password_confirm")
	isAdmin := r.FormValue("is_admin") != ""

	count, err := s.store.CountUsers()
	if err != nil {
		s.serverError(w, err)
		return
	}
	firstAccount := count == 0
	if firstAccount {
		isAdmin = true
	}

	switch {
	case name == "":
		s.renderAdminError(w, r, "Nazwa użytkownika nie może być pusta.")
		return
	case strings.ContainsAny(name, ":\n\r"):
		s.renderAdminError(w, r, `Nazwa użytkownika nie może zawierać ":" (potrzebne do HTTP Basic w OPDS).`)
		return
	case password == "":
		s.renderAdminError(w, r, "Hasło nie może być puste.")
		return
	case password != confirm:
		s.renderAdminError(w, r, "Podane hasła nie są takie same.")
		return
	}
	existing, err := s.store.GetUserByName(name)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if existing != nil {
		s.renderAdminError(w, r, "Taki użytkownik już istnieje.")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if _, err := s.store.CreateUser(name, string(hash), isAdmin); err != nil {
		s.serverError(w, err)
		return
	}
	if firstAccount {
		// Progress recorded by the anonymous reader before any account
		// existed belongs to this first account now.
		if moved, err := s.store.AdoptAnonymousProgress(name); err != nil {
			log.Printf("admin: adopting anonymous progress for %s: %v", name, err)
		} else if moved > 0 {
			log.Printf("admin: %d reading-progress record(s) assigned to %s", moved, name)
		}
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// getUserFromPath resolves the {id} path value to an account, writing the
// error response itself when it returns nil.
func (s *Server) getUserFromPath(w http.ResponseWriter, r *http.Request) *store.User {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return nil
	}
	u, err := s.store.GetUser(id)
	if err != nil {
		s.serverError(w, err)
		return nil
	}
	if u == nil {
		s.notFound(w, r)
		return nil
	}
	return u
}

// handleAdminSetPassword resets an account's password.
func (s *Server) handleAdminSetPassword(w http.ResponseWriter, r *http.Request) {
	u := s.getUserFromPath(w, r)
	if u == nil {
		return
	}
	password := r.FormValue("password")
	confirm := r.FormValue("password_confirm")
	if password == "" || password != confirm {
		s.renderAdminError(w, r, "Nowe hasło jest puste albo powtórzone hasło się różni.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.store.SetUserPassword(u.ID, string(hash)); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// handleAdminSetAdmin toggles an account's admin flag, refusing to demote
// the last remaining admin (that would lock everyone out of the panel).
func (s *Server) handleAdminSetAdmin(w http.ResponseWriter, r *http.Request) {
	u := s.getUserFromPath(w, r)
	if u == nil {
		return
	}
	wantAdmin := r.FormValue("is_admin") != ""
	if u.IsAdmin && !wantAdmin {
		admins, err := s.store.CountAdmins()
		if err != nil {
			s.serverError(w, err)
			return
		}
		if admins <= 1 {
			s.renderAdminError(w, r, "Nie można odebrać uprawnień ostatniemu administratorowi.")
			return
		}
	}
	if err := s.store.SetUserAdmin(u.ID, wantAdmin); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// handleAdminDeleteUser removes an account, refusing to delete the last
// remaining admin.
func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	u := s.getUserFromPath(w, r)
	if u == nil {
		return
	}
	if u.IsAdmin {
		admins, err := s.store.CountAdmins()
		if err != nil {
			s.serverError(w, err)
			return
		}
		if admins <= 1 {
			s.renderAdminError(w, r, "Nie można usunąć ostatniego administratora.")
			return
		}
	}
	if err := s.store.DeleteUser(u.ID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}
