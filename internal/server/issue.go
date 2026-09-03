package server

import (
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"comicnest/internal/store"
)

type issueData struct {
	Issue     *store.Issue
	Series    *store.Series
	Filename  string
	CVEnabled bool
	CVMatched bool
	Msg       string
	MsgError  bool
}

// flashMessages maps ?msg= codes (set by redirects) to user-visible text.
var flashMessages = map[string]struct {
	text  string
	isErr bool
}{
	"cv_ok":       {"Metadane zaktualizowane z ComicVine.", false},
	"cv_nomatch":  {"Nie znaleziono zeszytu o tym numerze w dopasowanym wolumenie ComicVine.", true},
	"cv_locked":   {"Metadane są zablokowane — najpierw zdejmij blokadę.", true},
	"cv_novolume": {"Seria nie jest dopasowana do wolumenu ComicVine.", true},
	"cv_nokey":    {"Brak klucza API ComicVine w config.yaml.", true},
	"cv_error":    {"Aktualizacja z ComicVine nie powiodła się — szczegóły w logu serwera.", true},
}

// getIssueFromPath resolves the {id} path value to an issue, writing the
// error response itself when it returns nil.
func (s *Server) getIssueFromPath(w http.ResponseWriter, r *http.Request) *store.Issue {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return nil
	}
	issue, err := s.store.GetIssue(id)
	if err != nil {
		s.serverError(w, err)
		return nil
	}
	if issue == nil {
		s.notFound(w, r)
		return nil
	}
	return issue
}

func (s *Server) handleIssue(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}
	series, err := s.store.GetSeries(issue.SeriesID)
	if err != nil {
		s.serverError(w, err)
		return
	}

	data := issueData{
		Issue:     issue,
		Series:    series,
		Filename:  filepath.Base(issue.Path),
		CVEnabled: s.cv.Enabled(),
		CVMatched: series.ComicVineVolumeID.Valid,
	}
	if flash, ok := flashMessages[r.FormValue("msg")]; ok {
		data.Msg = flash.text
		data.MsgError = flash.isErr
	}
	s.render(w, "issue.html", data)
}

// handleIssueDelete removes the record of an issue whose file disappeared
// from the library. Records with a live file cannot be deleted — the library
// on disk is the source of truth.
func (s *Server) handleIssueDelete(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}
	if !issue.FileMissing {
		s.errorPage(w, http.StatusConflict,
			"Ten zeszyt ma plik na dysku — rekordów istniejących plików nie można usuwać.")
		return
	}

	if err := s.store.DeleteIssue(issue.ID); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.covers.Remove(issue.ID); err != nil {
		log.Printf("delete issue %d: removing cover: %v", issue.ID, err)
	}
	http.Redirect(w, r, "/series/"+strconv.FormatInt(issue.SeriesID, 10), http.StatusSeeOther)
}

// handleIssueDownload serves the comic file itself. Files are addressed only
// by database id — the request never carries a path (no traversal surface).
func (s *Server) handleIssueDownload(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}

	if _, err := os.Stat(issue.Path); err != nil {
		http.Error(w, "Plik nie istnieje na dysku (oznaczony jako brakujący?).", http.StatusNotFound)
		return
	}

	disposition := mime.FormatMediaType("attachment", map[string]string{
		"filename": filepath.Base(issue.Path),
	})
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Type", comicMediaType(issue.Path))
	http.ServeFile(w, r, issue.Path)
}
