package server

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"comicnest/internal/comicvine"
	"comicnest/internal/config"
	"comicnest/internal/opds"
	"comicnest/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// adminUserRow is one account shaped for the admin panel table.
type adminUserRow struct {
	ID        int64
	Name      string
	IsAdmin   bool
	LastLogin string // "never" or "2006-01-02 15:04" local time
	CreatedAt string
}

// scanHistoryRow is one past scan shaped for the admin panel's Scan History
// table.
type scanHistoryRow struct {
	Started   string // "2006-01-02 15:04" local time
	Duration  string // e.g. "12s", or "-" when the timestamps don't parse
	Found     int
	Processed int
	Missing   int
	CVUpdated int
	CVFailed  int
	Err       string
}

// adminConfig is the subset of config.yaml the admin panel can edit, plus
// whether each field is currently pinned by a COMICNEST_* environment
// variable (in which case editing it here has no effect until that variable
// is unset — see SPECIFICATION on env overrides).
type adminConfig struct {
	ComicVineAPIKey    string
	ComicVineEnvPinned bool
	OPDSEnabled        bool
	OPDSEnvPinned      bool
	PageSize           int
	PageSizeEnvPinned  bool
}

// cvTestResult is the outcome of a "Test Connection" check, shown inline
// next to the ComicVine API key field.
type cvTestResult struct {
	OK      bool
	Message string
}

type adminData struct {
	Users []adminUserRow
	// IsFirstRun is true when no account exists yet: the add-user form force
	// the new account to be an admin (there is no other way back in).
	IsFirstRun bool
	// MissingCount is the number of catalog records whose file has
	// disappeared from disk (drives the "delete all missing" button).
	MissingCount int
	// Stats is the library-wide health snapshot shown in the Statistics
	// section.
	Stats store.LibraryStats
	// ScanHistory is the most recent completed scans, newest first.
	ScanHistory []scanHistoryRow
	// Config feeds the Configuration section's form.
	Config adminConfig
	// CVTest is set after a "Test Connection" click that fell back to a
	// plain (no-JS) form post, so the result renders in the full page.
	CVTest *cvTestResult
	Error  string
}

// adminPageData loads the current account list and library status for the
// admin panel.
func (s *Server) adminPageData() (adminData, error) {
	users, err := s.store.ListUsers()
	if err != nil {
		return adminData{}, err
	}
	rows := make([]adminUserRow, len(users))
	for i, u := range users {
		last := "never"
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
	missing, err := s.store.CountMissingIssues()
	if err != nil {
		return adminData{}, err
	}
	stats, err := s.store.LibraryStats()
	if err != nil {
		return adminData{}, err
	}
	history, err := s.store.ListScanHistory(20)
	if err != nil {
		return adminData{}, err
	}
	return adminData{
		Users:        rows,
		IsFirstRun:   len(rows) == 0,
		MissingCount: missing,
		Stats:        stats,
		ScanHistory:  scanHistoryRows(history),
		Config:       s.adminConfigView(),
	}, nil
}

// adminConfigView reads the current admin-editable settings, noting which
// ones are pinned by a COMICNEST_* environment variable and therefore can't
// really be changed from here.
func (s *Server) adminConfigView() adminConfig {
	cfg := s.config()
	envSet := func(key string) bool {
		v, ok := os.LookupEnv(config.EnvPrefix + key)
		return ok && v != ""
	}
	return adminConfig{
		ComicVineAPIKey:    cfg.ComicVineAPIKey,
		ComicVineEnvPinned: envSet("COMICVINE_API_KEY"),
		OPDSEnabled:        cfg.OPDSEnabled,
		OPDSEnvPinned:      envSet("OPDS_ENABLED"),
		PageSize:           cfg.PageSize,
		PageSizeEnvPinned:  envSet("PAGE_SIZE"),
	}
}

// scanHistoryRows formats store.ScanHistoryEntry rows for the Scan History
// table.
func scanHistoryRows(history []store.ScanHistoryEntry) []scanHistoryRow {
	rows := make([]scanHistoryRow, len(history))
	for i, h := range history {
		started := opds.ParseDBTime(h.StartedAt, time.Time{})
		finished := opds.ParseDBTime(h.FinishedAt, time.Time{})
		startedStr, duration := "-", "-"
		if !started.IsZero() {
			startedStr = started.Local().Format("2006-01-02 15:04")
		}
		if !started.IsZero() && !finished.IsZero() {
			duration = finished.Sub(started).Round(time.Second).String()
		}
		rows[i] = scanHistoryRow{
			Started:   startedStr,
			Duration:  duration,
			Found:     h.Found,
			Processed: h.Processed,
			Missing:   h.Missing,
			CVUpdated: h.CVUpdated,
			CVFailed:  h.CVFailed,
			Err:       h.Err,
		}
	}
	return rows
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
		s.renderAdminError(w, r, "Username cannot be empty.")
		return
	case strings.ContainsAny(name, ":\n\r"):
		s.renderAdminError(w, r, `Username cannot contain ":" (needed for HTTP Basic in OPDS).`)
		return
	case password == "":
		s.renderAdminError(w, r, "Password cannot be empty.")
		return
	case password != confirm:
		s.renderAdminError(w, r, "The passwords do not match.")
		return
	}
	existing, err := s.store.GetUserByName(name)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if existing != nil {
		s.renderAdminError(w, r, "That username already exists.")
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
		s.renderAdminError(w, r, "The new password is empty or the confirmation doesn't match.")
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
			s.renderAdminError(w, r, "Cannot revoke admin rights from the last remaining administrator.")
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
			s.renderAdminError(w, r, "Cannot delete the last remaining administrator.")
			return
		}
	}
	if err := s.store.DeleteUser(u.ID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// handleAdminDeleteMissing removes every catalog record whose file has
// disappeared from disk (the bulk equivalent of handleIssueDelete).
func (s *Server) handleAdminDeleteMissing(w http.ResponseWriter, r *http.Request) {
	issues, err := s.store.ListMissingIssues()
	if err != nil {
		s.serverError(w, err)
		return
	}
	for _, issue := range issues {
		if err := s.store.DeleteIssue(issue.ID); err != nil {
			s.serverError(w, err)
			return
		}
		if err := s.covers.Remove(issue.ID); err != nil {
			log.Printf("delete missing issue %d: removing cover: %v", issue.ID, err)
		}
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// handleAdminSaveConfig persists the admin-editable subset of config.yaml
// (ComicVine key, OPDS toggle, page size). The page size takes effect on the
// next request; the ComicVine key and OPDS toggle need a restart (the
// client and routes are built once at startup).
func (s *Server) handleAdminSaveConfig(w http.ResponseWriter, r *http.Request) {
	apiKey := strings.TrimSpace(r.FormValue("comicvine_api_key"))
	opdsEnabled := r.FormValue("opds_enabled") != ""
	pageSize, err := strconv.Atoi(strings.TrimSpace(r.FormValue("page_size")))
	if err != nil || pageSize < 0 {
		s.renderAdminError(w, r, "Series per page must be a non-negative number (0 = no pagination).")
		return
	}
	updated := s.setEditableConfig(apiKey, opdsEnabled, pageSize)
	if err := config.Save(s.configPath, updated); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// handleAdminTestComicVine checks a ComicVine API key against the live API,
// without saving it — so a bad key is caught before it's written to
// config.yaml (and before it would otherwise only surface at scrape time).
func (s *Server) handleAdminTestComicVine(w http.ResponseWriter, r *http.Request) {
	apiKey := strings.TrimSpace(r.FormValue("comicvine_api_key"))
	result := testComicVineKey(apiKey)

	if r.Header.Get("HX-Request") == "true" {
		s.renderPartial(w, "cv_test_result.html", "cv-test-result", result)
		return
	}
	data, err := s.adminPageData()
	if err != nil {
		s.serverError(w, err)
		return
	}
	data.CVTest = &result
	data.Config.ComicVineAPIKey = apiKey // echo what was just tested, not the saved value
	s.render(w, r, "admin.html", data)
}

// testComicVineKey performs a minimal live request against the ComicVine
// API with a throwaway client, to check whether apiKey actually works.
func testComicVineKey(apiKey string) cvTestResult {
	if apiKey == "" {
		return cvTestResult{Message: "Enter an API key first."}
	}
	if err := comicvine.New(apiKey).TestKey(); err != nil {
		return cvTestResult{Message: err.Error()}
	}
	return cvTestResult{OK: true, Message: "Connected — the key works."}
}
