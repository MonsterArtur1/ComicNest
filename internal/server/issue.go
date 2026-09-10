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
	Reading   *readingView // nil when never read
	Favorite  bool
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
	"cv_ok":        {"Metadata updated from ComicVine.", false},
	"cv_nomatch":   {"No issue with this number was found in the matched ComicVine volume.", true},
	"cv_locked":    {"Metadata is locked — unlock it first.", true},
	"cv_novolume":  {"The series is not matched to a ComicVine volume.", true},
	"cv_nokey":     {"No ComicVine API key in config.yaml.", true},
	"cv_error":     {"The ComicVine update failed — see the server log for details.", true},
	"cv_unlinked":  {"The issue's ComicVine match was removed.", false},
	"read_ok":      {"Issue marked as read.", false},
	"unread_ok":    {"Issue marked as unread.", false},
	"read_nopages": {"Cannot mark as read — unknown page count (missing file, or a format with no pages).", true},
	"fav_ok":       {"Added to favorites.", false},
	"unfav_ok":     {"Removed from favorites.", false},
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

	user := userFrom(r)
	progress, err := s.store.GetReadingProgress(user, issue.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	favorite, err := s.store.IsIssueFavorite(user, issue.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}

	data := issueData{
		Issue:     issue,
		Series:    series,
		Reading:   newReadingView(issue, progress),
		Favorite:  favorite,
		Filename:  filepath.Base(issue.Path),
		CVEnabled: s.cv.Enabled(),
		CVMatched: series.ComicVineVolumeID.Valid,
	}
	if flash, ok := flashMessages[r.FormValue("msg")]; ok {
		data.Msg = flash.text
		data.MsgError = flash.isErr
	}
	s.render(w, r, "issue.html", data)
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
		s.errorPage(w, r, http.StatusConflict,
			"This issue has a file on disk — records for existing files cannot be deleted.")
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
		http.Error(w, "The file does not exist on disk (marked as missing?).", http.StatusNotFound)
		return
	}

	disposition := mime.FormatMediaType("attachment", map[string]string{
		"filename": filepath.Base(issue.Path),
	})
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Type", comicMediaType(issue.Path))
	http.ServeFile(w, r, issue.Path)
}
