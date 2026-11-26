package server

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"comicnest/internal/config"
	"comicnest/internal/kosync"
	"comicnest/internal/opds"
	"comicnest/internal/store"
)

// KOReader progress sync: a server for the kosync plugin (the API of
// koreader-sync-server). The user sets "Custom sync server" to
// http://<host>:<port>/kosync in KOReader; the plugin then sends
// x-auth-user / x-auth-key (md5 hex of the password) on every call.
//
// Auth: passwords are bcrypt-hashed, which cannot verify an md5, so while
// kosync is enabled the md5 of the password is also stored (users.kosync_key)
// whenever the server sees the plaintext — web login, OPDS Basic auth, admin
// create/reset password (rememberKosyncKey). Existing accounts therefore
// enable sync by logging in once after kosync is switched on. The md5 is
// unsalted and, being the credential KOReader presents, equivalent to the
// password for /kosync; it is never accepted anywhere else.
//
// Documents: KOReader names a book by a hash (partial md5 of the content, or
// md5 of the file name). Progress is stored per user and hash whether or not
// the hash belongs to a library issue (internal/library/doc_hashes.go); for
// known issues it is mirrored into reading_progress and the page-read log,
// and the other way round on GET.

const kosyncDeviceName = "ComicNest"

// kosyncRoutes registers the KOReader sync API under /kosync.
func (s *Server) kosyncRoutes() {
	s.mux.HandleFunc("GET /kosync/healthcheck", s.handleKosyncHealth)
	s.mux.HandleFunc("POST /kosync/users/create", s.handleKosyncRegister)
	s.mux.HandleFunc("GET /kosync/users/auth", s.kosyncAuth(s.handleKosyncAuth))
	s.mux.HandleFunc("PUT /kosync/syncs/progress", s.kosyncAuth(s.handleKosyncPut))
	s.mux.HandleFunc("GET /kosync/syncs/progress/{document}", s.kosyncAuth(s.handleKosyncGet))
}

// setKosyncEnabled updates the admin-editable kosync toggle (it takes effect
// after a restart, like OPDS) and returns the resulting full config.
func (s *Server) setKosyncEnabled(enabled bool) config.Config {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.cfg.KosyncEnabled = enabled
	return s.cfg
}

// rememberKosyncKey stores the KOReader verifier for an account whose
// plaintext password was just seen. No-op unless kosync routes are mounted, so
// an md5 is never kept for a feature nobody turned on.
func (s *Server) rememberKosyncKey(name, password string) {
	if !s.kosyncRoutesRegistered || name == "" {
		return
	}
	if err := s.store.SetUserKosyncKey(name, kosync.PasswordKey(password)); err != nil {
		log.Printf("kosync: storing key for %q: %v", name, err)
	}
}

func writeKosyncJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func kosyncError(w http.ResponseWriter, status int, msg string) {
	writeKosyncJSON(w, status, map[string]string{"message": msg})
}

// kosyncAuth checks x-auth-user / x-auth-key against the stored verifier and
// binds the account to the request. Like OPDS, with no accounts yet the app
// is open and everything belongs to the anonymous reader.
func (s *Server) kosyncAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authEnabled() {
			next(w, withUser(r, ""))
			return
		}
		user := r.Header.Get("x-auth-user")
		key := strings.ToLower(r.Header.Get("x-auth-key"))
		stored, err := s.store.GetUserKosyncKey(user)
		if err != nil {
			log.Printf("kosync: auth lookup for %q: %v", user, err)
		}
		if user == "" || key == "" || stored == "" || subtle.ConstantTimeCompare([]byte(key), []byte(stored)) != 1 {
			log.Printf("kosync: auth failed for %q from %s", user, r.RemoteAddr)
			kosyncError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		next(w, withUser(r, user))
	}
}

func (s *Server) handleKosyncHealth(w http.ResponseWriter, r *http.Request) {
	writeKosyncJSON(w, http.StatusOK, map[string]string{"state": "OK"})
}

// handleKosyncRegister refuses registration: accounts are created by the
// administrator. 403 is the code the plugin shows as an error message.
func (s *Server) handleKosyncRegister(w http.ResponseWriter, r *http.Request) {
	kosyncError(w, http.StatusForbidden, "Registration is disabled; ask the ComicNest administrator for an account.")
}

func (s *Server) handleKosyncAuth(w http.ResponseWriter, r *http.Request) {
	writeKosyncJSON(w, http.StatusOK, map[string]string{"authorized": "OK"})
}

type kosyncPutBody struct {
	Document   string   `json:"document"`
	Progress   string   `json:"progress"`
	Percentage *float64 `json:"percentage"`
	Device     string   `json:"device"`
	DeviceID   string   `json:"device_id"`
}

// handleKosyncPut stores the position pushed by KOReader and, when the
// document is a library issue, mirrors it into the user's reading progress.
func (s *Server) handleKosyncPut(w http.ResponseWriter, r *http.Request) {
	var body kosyncPutBody
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.Document == "" || len(body.Document) > 256 || body.Progress == "" || body.Percentage == nil {
		kosyncError(w, http.StatusForbidden, "Invalid request")
		return
	}
	user := userFrom(r)
	doc := strings.ToLower(body.Document)
	pct := math.Min(math.Max(*body.Percentage, 0), 1)

	issue, err := s.store.FindIssueByDocument(doc)
	if err != nil {
		log.Printf("kosync: finding document %s: %v", doc, err)
		kosyncError(w, http.StatusInternalServerError, "Unknown server error")
		return
	}
	if issue != nil {
		if page := kosyncPage(body.Progress, pct, issue.TotalPages()); page > 0 {
			if err := s.store.SetReadingProgress(user, issue.ID, page); err != nil {
				log.Printf("kosync: progress for issue %d: %v", issue.ID, err)
			} else if err := s.store.RecordPageRead(user, issue.ID, page); err != nil {
				log.Printf("kosync: page read for issue %d: %v", issue.ID, err)
			}
		}
	}

	// Stamped after the reading_progress write above so that write does not
	// look "newer than the last sync" to handleKosyncGet.
	ts := time.Now().Unix()
	err = s.store.SaveKosyncProgress(user, store.KosyncProgress{
		Document: doc, Progress: body.Progress, Percentage: pct,
		Device: body.Device, DeviceID: body.DeviceID, Timestamp: ts,
	})
	if err != nil {
		log.Printf("kosync: saving progress: %v", err)
		kosyncError(w, http.StatusInternalServerError, "Unknown server error")
		return
	}
	writeKosyncJSON(w, http.StatusOK, map[string]any{"document": body.Document, "timestamp": ts})
}

type kosyncProgressResp struct {
	Document   string  `json:"document"`
	Progress   string  `json:"progress"`
	Percentage float64 `json:"percentage"`
	Device     string  `json:"device"`
	DeviceID   string  `json:"device_id"`
	Timestamp  int64   `json:"timestamp"`
}

// handleKosyncGet returns the position for a document: the last one KOReader
// pushed, unless ComicNest's own progress for that issue (web reader, OPDS)
// is newer. An unknown document yields an empty object.
func (s *Server) handleKosyncGet(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r)
	doc := strings.ToLower(r.PathValue("document"))

	saved, err := s.store.GetKosyncProgress(user, doc)
	if err != nil {
		log.Printf("kosync: reading progress: %v", err)
		kosyncError(w, http.StatusInternalServerError, "Unknown server error")
		return
	}
	issue, err := s.store.FindIssueByDocument(doc)
	if err != nil {
		log.Printf("kosync: finding document %s: %v", doc, err)
		kosyncError(w, http.StatusInternalServerError, "Unknown server error")
		return
	}
	if issue != nil {
		p, err := s.store.GetReadingProgress(user, issue.ID)
		if err != nil {
			log.Printf("kosync: reading_progress for issue %d: %v", issue.ID, err)
		}
		if p != nil && p.Page > 0 {
			total := issue.TotalPages()
			if t := opds.ParseDBTime(p.UpdatedAt, time.Time{}); !t.IsZero() &&
				(saved == nil || t.Unix() > saved.Timestamp) &&
				(p.Page > 1 || (total > 0 && p.Page >= total)) { // a lone first page isn't "started"
				page := p.Page
				pct := 0.0
				if total > 0 {
					page = min(page, total)
					pct = float64(page) / float64(total)
				}
				writeKosyncJSON(w, http.StatusOK, kosyncProgressResp{
					Document: doc, Progress: strconv.Itoa(page), Percentage: pct,
					Device: kosyncDeviceName, DeviceID: "comicnest", Timestamp: t.Unix(),
				})
				return
			}
		}
	}
	if saved == nil {
		writeKosyncJSON(w, http.StatusOK, map[string]any{})
		return
	}
	writeKosyncJSON(w, http.StatusOK, kosyncProgressResp{
		Document: saved.Document, Progress: saved.Progress, Percentage: saved.Percentage,
		Device: saved.Device, DeviceID: saved.DeviceID, Timestamp: saved.Timestamp,
	})
}

// kosyncPage maps a KOReader position onto a 1-based ComicNest page. Paged
// documents (CBZ/CBR/PDF) send the page number as the progress string; when
// that is not a number (e.g. a reflowable xpointer) the page is derived from
// the percentage. The page is clamped to the issue's page total when known.
// A position on the last page marks the issue finished (page == total, same
// as the web reader). Returns 0 when no page can be determined.
func kosyncPage(progress string, pct float64, total int) int {
	page, err := strconv.Atoi(strings.TrimSpace(progress))
	derived := false
	if err != nil || page < 1 {
		derived = true
		page = 0
		if total > 0 && pct > 0 {
			page = int(math.Round(pct * float64(total)))
			page = max(page, 1)
		}
	}
	if total > 0 {
		page = min(page, total)
		if derived && pct >= 0.995 {
			page = total
		}
	}
	return page
}
