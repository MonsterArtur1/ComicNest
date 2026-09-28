package server

import (
	"log"
	"math"
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
	ID        int64
	Started   string // "2006-01-02 15:04" local time
	Ago       string // "3h ago", "" when the start time doesn't parse
	Duration  string // e.g. "12s", or "-" when the timestamps don't parse
	Found     int
	Processed int
	Missing   int
	CVUpdated int
	CVFailed  int
	Err       string

	// Detailed is false for scans recorded before the change counters
	// existed; the template then shows "—" instead of misleading zeros.
	Detailed     bool
	Added        int
	AddedSize    string // prettySize of the added files, "" when none
	Restored     int
	NewlyMissing int
	SeriesAdded  int
	ProblemCount int
	Problems     []store.ScanProblem
	// MoreProblems is how many problems were counted but not kept (the
	// stored list is capped at store.MaxScanProblems).
	MoreProblems int
}

// Outcome classifies the scan for the Result badge and the activity chart:
// "error" (the scan itself failed), "warn" (it finished but some files had
// problems) or "ok".
func (r scanHistoryRow) Outcome() string {
	switch {
	case r.Err != "":
		return "error"
	case r.ProblemCount > 0:
		return "warn"
	}
	return "ok"
}

// HasChanges reports whether the scan changed anything worth a chip.
func (r scanHistoryRow) HasChanges() bool {
	return r.Added > 0 || r.Restored > 0 || r.NewlyMissing > 0 || r.SeriesAdded > 0
}

// scanActivityBar is one scan in the activity chart above the history table:
// a bar up for files that arrived (new + restored), one down for files that
// went missing. Heights are log-scaled percentages so a big first import
// doesn't flatten every later scan to nothing; the tooltip has exact numbers.
type scanActivityBar struct {
	Row     scanHistoryRow
	UpPct   int
	DownPct int
	// AddedShare is the new files' share of the up bar in percent (the rest
	// is restored files, drawn in a different color: RestoredShare).
	AddedShare    int
	RestoredShare int
}

// scanHistoryView is everything the scan-history template renders for one
// library (or the single combined one): the chart, its totals and the table.
type scanHistoryView struct {
	Rows []scanHistoryRow
	// Bars run oldest → newest (left → right), unlike Rows.
	Bars []scanActivityBar
	// Totals over the listed scans, for the summary line.
	TotalAdded     int
	TotalRestored  int
	TotalMissing   int // newly missing
	TotalAddedSize string
	ProblemScans   int // scans with problems or an error
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
	// ComicVineRestartPending and OPDSRestartPending are true when the saved
	// value differs from what's actually running (the ComicVine client and
	// OPDS routes are built once at startup) — the admin panel banners this
	// until the app is restarted and the two converge again.
	ComicVineRestartPending bool
	OPDSRestartPending      bool
}

// cvTestResult is the outcome of a "Test Connection" check, shown inline
// next to the ComicVine API key field.
type cvTestResult struct {
	OK      bool
	Message string
}

// libraryPanel is one configured library's own Statistics/Scan/History block,
// shown only in multi-library mode (see adminData.Libraries).
type libraryPanel struct {
	Info        libraryInfo
	Stats       store.LibraryStats
	ScanHistory scanHistoryView
}

// The admin panel's tabs, picked with ?tab= (see adminTabFor).
const (
	tabLibrary = "library"
	tabUsers   = "users"
	tabConfig  = "config"
)

// adminTabFor picks the tab a request renders: GET /admin's ?tab= value, else
// the tab owning the POST endpoint, so a validation error re-renders the
// panel on the tab whose form was submitted.
func adminTabFor(r *http.Request) string {
	switch t := r.URL.Query().Get("tab"); t {
	case tabLibrary, tabUsers, tabConfig:
		return t
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/admin/users"):
		return tabUsers
	case strings.HasPrefix(r.URL.Path, "/admin/config"):
		return tabConfig
	}
	return tabLibrary
}

// adminURL is the address of an admin panel tab (the Library tab is the bare
// /admin), used to redirect back to it after a form post.
func adminURL(tab string) string {
	if tab == tabLibrary {
		return "/admin"
	}
	return "/admin?tab=" + tab
}

type adminData struct {
	// Tab is the tab being shown: tabLibrary, tabUsers or tabConfig.
	Tab   string
	Users []adminUserRow
	// IsFirstRun is true when no account exists yet: the add-user form force
	// the new account to be an admin (there is no other way back in).
	IsFirstRun bool
	// MissingCount is the number of catalog records whose file has
	// disappeared from disk, across every library (drives the "delete all
	// missing" button).
	MissingCount int
	// Libraries holds one panel per configured library — its own stats, scan
	// button and history — when more than one is configured. Templates
	// switch on `len .Libraries` to pick this per-library layout over the
	// single-library one below.
	Libraries []libraryPanel
	// Stats/ScanHistory are the single combined library's health snapshot
	// and scan history, used instead of Libraries when at most one library
	// is configured — the admin panel then looks exactly as it always has.
	Stats store.LibraryStats
	// ScanHistory is the most recent completed scans, newest first.
	ScanHistory scanHistoryView
	// Config feeds the Configuration section's form.
	Config adminConfig
	// CVTest is set after a "Test Connection" click that fell back to a
	// plain (no-JS) form post, so the result renders in the full page.
	CVTest *cvTestResult
	Error  string
}

// adminPageData loads the current account list and library status for the
// admin panel, showing the given tab.
func (s *Server) adminPageData(tab string) (adminData, error) {
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
	data := adminData{
		Tab:          tab,
		Users:        rows,
		IsFirstRun:   len(rows) == 0,
		MissingCount: missing,
		Config:       s.adminConfigView(),
	}

	if len(s.libraries) > 1 {
		for _, lib := range s.libraries {
			stats, err := s.store.LibraryStats(lib.Path)
			if err != nil {
				return adminData{}, err
			}
			history, err := s.store.ListScanHistory(15, lib.Path)
			if err != nil {
				return adminData{}, err
			}
			data.Libraries = append(data.Libraries, libraryPanel{
				Info: lib, Stats: stats, ScanHistory: newScanHistoryView(history, time.Now()),
			})
		}
	} else {
		stats, err := s.store.LibraryStats("")
		if err != nil {
			return adminData{}, err
		}
		history, err := s.store.ListScanHistory(20, "")
		if err != nil {
			return adminData{}, err
		}
		data.Stats = stats
		data.ScanHistory = newScanHistoryView(history, time.Now())
	}
	return data, nil
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
		ComicVineAPIKey:         cfg.ComicVineAPIKey,
		ComicVineEnvPinned:      envSet("COMICVINE_API_KEY"),
		OPDSEnabled:             cfg.OPDSEnabled,
		OPDSEnvPinned:           envSet("OPDS_ENABLED"),
		PageSize:                cfg.PageSize,
		PageSizeEnvPinned:       envSet("PAGE_SIZE"),
		ComicVineRestartPending: cfg.ComicVineAPIKey != s.activeComicVineAPIKey,
		OPDSRestartPending:      cfg.OPDSEnabled != s.opdsRoutesRegistered,
	}
}

// newScanHistoryView formats store.ScanHistoryEntry rows (newest first) for
// the Scan History section; now anchors the "3h ago" labels.
func newScanHistoryView(history []store.ScanHistoryEntry, now time.Time) scanHistoryView {
	var v scanHistoryView
	var addedBytes int64
	v.Rows = make([]scanHistoryRow, len(history))
	for i, h := range history {
		started := opds.ParseDBTime(h.StartedAt, time.Time{})
		finished := opds.ParseDBTime(h.FinishedAt, time.Time{})
		row := scanHistoryRow{
			ID:           h.ID,
			Started:      "-",
			Duration:     "-",
			Found:        h.Found,
			Processed:    h.Processed,
			Missing:      h.Missing,
			CVUpdated:    h.CVUpdated,
			CVFailed:     h.CVFailed,
			Err:          h.Err,
			Detailed:     h.Detailed,
			Added:        h.Added,
			Restored:     h.Restored,
			NewlyMissing: h.NewlyMissing,
			SeriesAdded:  h.SeriesAdded,
			ProblemCount: h.ProblemCount,
			Problems:     h.Problems,
			MoreProblems: max(h.ProblemCount-len(h.Problems), 0),
		}
		if !started.IsZero() {
			row.Started = started.Local().Format("2006-01-02 15:04")
			row.Ago = timeAgo(now.Sub(started))
		}
		if !started.IsZero() && !finished.IsZero() {
			row.Duration = finished.Sub(started).Round(time.Second).String()
		}
		if h.AddedBytes > 0 {
			row.AddedSize = prettySize(h.AddedBytes)
		}
		v.Rows[i] = row

		v.TotalAdded += h.Added
		v.TotalRestored += h.Restored
		v.TotalMissing += h.NewlyMissing
		addedBytes += h.AddedBytes
		if row.Outcome() != "ok" {
			v.ProblemScans++
		}
	}
	if addedBytes > 0 {
		v.TotalAddedSize = prettySize(addedBytes)
	}

	peak := 0
	for _, r := range v.Rows {
		peak = max(peak, r.Added+r.Restored, r.NewlyMissing)
	}
	scale := func(n int) int {
		if n <= 0 || peak == 0 {
			return 0
		}
		pct := int(math.Round(100 * math.Log1p(float64(n)) / math.Log1p(float64(peak))))
		return max(pct, 6) // keep a single file visible
	}
	v.Bars = make([]scanActivityBar, len(v.Rows))
	for i, r := range v.Rows {
		bar := scanActivityBar{
			Row:     r,
			UpPct:   scale(r.Added + r.Restored),
			DownPct: scale(r.NewlyMissing),
		}
		if up := r.Added + r.Restored; up > 0 {
			bar.AddedShare = 100 * r.Added / up
			bar.RestoredShare = 100 - bar.AddedShare
		}
		v.Bars[len(v.Rows)-1-i] = bar
	}
	return v
}

// timeAgo renders a coarse "how long ago" label for the scan history.
func timeAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m ago"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h ago"
	default:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d ago"
	}
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	data, err := s.adminPageData(adminTabFor(r))
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "admin.html", data)
}

// renderAdminError redraws the panel with a flash error, keeping the
// (still valid) account list visible.
func (s *Server) renderAdminError(w http.ResponseWriter, r *http.Request, msg string) {
	data, err := s.adminPageData(adminTabFor(r))
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
	http.Redirect(w, r, adminURL(tabUsers), http.StatusSeeOther)
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
	http.Redirect(w, r, adminURL(tabUsers), http.StatusSeeOther)
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
	http.Redirect(w, r, adminURL(tabUsers), http.StatusSeeOther)
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
	http.Redirect(w, r, adminURL(tabUsers), http.StatusSeeOther)
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
	http.Redirect(w, r, adminURL(tabConfig), http.StatusSeeOther)
}

// handleAdminRenameLibrary sets or clears a library's display-name override,
// stored in the database (not config.yaml — see Store.SetLibraryName).
// Submitting the folder's own default name (or a blank name) clears the
// override instead of storing a redundant one. Takes effect immediately, no
// restart needed (see Server.libraryName).
func (s *Server) handleAdminRenameLibrary(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || idx < 0 || idx >= len(s.libraries) {
		s.notFound(w, r)
		return
	}
	path := s.libraries[idx].Path
	name := strings.TrimSpace(r.FormValue("name"))
	if name == libraryFolderName(path) {
		name = ""
	}
	if err := s.store.SetLibraryName(path, name); err != nil {
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
	data, err := s.adminPageData(tabConfig)
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
