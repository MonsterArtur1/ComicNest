package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Accounts live in the database (internal/store/users.go), not config.yaml.
// When none exist yet the app runs open: no login anywhere, one anonymous
// reader (""), treated as an admin so they can create the first real
// account. Once at least one account exists, the web UI needs a session
// cookie (login form) and OPDS needs HTTP Basic auth; both check the same
// name/password pairs, and every request carries the resolved user name in
// its context.

const (
	sessionCookie = "comicnest_session"
	sessionTTL    = 30 * 24 * time.Hour
)

type ctxKey int

const userKey ctxKey = iota

// userFrom returns the account name the request is authenticated as ("" for
// the anonymous reader or an unauthenticated request).
func userFrom(r *http.Request) string {
	if r == nil {
		return ""
	}
	u, _ := r.Context().Value(userKey).(string)
	return u
}

func withUser(r *http.Request, user string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userKey, user))
}

// authEnabled reports whether any account exists. A database error is
// treated as "enabled" — failing open would silently drop the login gate.
func (s *Server) authEnabled() bool {
	n, err := s.store.CountUsers()
	if err != nil {
		log.Printf("auth: counting users: %v", err)
		return true
	}
	return n > 0
}

// isAdmin reports whether the request may perform admin-only actions
// (library scan, metadata editing, ComicVine search/matching, the admin
// panel itself). Before any account exists, the anonymous visitor is
// treated as admin, so they can create the first real account.
func (s *Server) isAdmin(r *http.Request) bool {
	user := userFrom(r)
	if user == "" {
		return !s.authEnabled()
	}
	u, err := s.store.GetUserByName(user)
	if err != nil || u == nil {
		return false
	}
	return u.IsAdmin
}

// requireAdmin wraps a handler so only admins (or, before any account
// exists, the anonymous bootstrap visitor) may reach it. Library scanning,
// metadata editing and ComicVine search/matching are catalog-management
// actions, not something every reader should be able to do.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAdmin(r) {
			s.errorPage(w, r, http.StatusForbidden, "Ta czynność wymaga uprawnień administratora.")
			return
		}
		next(w, r)
	}
}

// checkPassword verifies credentials against the accounts table.
func (s *Server) checkPassword(name, password string) bool {
	u, err := s.store.GetUserByName(name)
	if err != nil || u == nil {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}

// sessions is the in-memory session table: token → user name. Sessions do
// not survive a restart (users just log in again).
type sessions struct {
	mu   sync.Mutex
	byID map[string]sessionEntry
}

type sessionEntry struct {
	user    string
	expires time.Time
}

func newSessions() *sessions { return &sessions{byID: make(map[string]sessionEntry)} }

func (ss *sessions) create(user string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	id := hex.EncodeToString(b)
	ss.mu.Lock()
	ss.byID[id] = sessionEntry{user: user, expires: time.Now().Add(sessionTTL)}
	ss.mu.Unlock()
	return id
}

func (ss *sessions) lookup(id string) (string, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	e, ok := ss.byID[id]
	if !ok {
		return "", false
	}
	if time.Now().After(e.expires) {
		delete(ss.byID, id)
		return "", false
	}
	return e.user, true
}

func (ss *sessions) drop(id string) {
	ss.mu.Lock()
	delete(ss.byID, id)
	ss.mu.Unlock()
}

// withAuth gates the web UI behind a session when accounts exist. OPDS paths
// are left to opdsAuth (HTTP Basic); the login page and static assets stay
// open. Unauthenticated page requests are redirected to the login form.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !s.authEnabled() ||
			p == "/opds" || strings.HasPrefix(p, "/opds/") ||
			p == "/login" || p == "/logout" || p == "/favicon.ico" || p == "/healthz" || strings.HasPrefix(p, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil {
			if user, ok := s.sessions.lookup(c.Value); ok {
				next.ServeHTTP(w, withUser(r, user))
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Zaloguj się, aby wykonać tę akcję.", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	})
}

type loginData struct {
	Next  string
	Error string
	Name  string
}

// handleLoginForm shows the login page (or goes home when already logged in).
func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, ok := s.sessions.lookup(c.Value); ok {
			http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
			return
		}
	}
	s.renderLogin(w, http.StatusOK, loginData{Next: r.FormValue("next")})
}

// handleLogin checks the credentials and starts a session.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if !s.checkPassword(name, r.FormValue("password")) {
		log.Printf("login failed for %q from %s", name, r.RemoteAddr)
		s.renderLogin(w, http.StatusUnauthorized, loginData{
			Next: r.FormValue("next"), Name: name, Error: "Nieprawidłowa nazwa użytkownika lub hasło.",
		})
		return
	}
	if err := s.store.TouchUserLogin(name); err != nil {
		log.Printf("login: recording last login for %q: %v", name, err)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.sessions.create(name),
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode, // also keeps cross-site form posts out
	})
	http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
}

// handleLogout ends the session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.drop(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// safeNext keeps post-login redirects on this site.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || next == "/login" {
		return "/"
	}
	return next
}

func (s *Server) renderLogin(w http.ResponseWriter, status int, data loginData) {
	var buf bytes.Buffer
	if err := s.login.ExecuteTemplate(&buf, "login", data); err != nil {
		s.serverError(w, fmt.Errorf("render login: %w", err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}
