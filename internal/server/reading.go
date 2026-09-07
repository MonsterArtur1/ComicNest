package server

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"comicnest/internal/library"
	"comicnest/internal/opds"
	"comicnest/internal/store"
)

// handleIssueMarkRead sets the issue's progress to its last page. Archives
// whose pages were never counted get counted now; an issue with an unknown
// page total cannot be marked (there is no "finished" without a last page).
func (s *Server) handleIssueMarkRead(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}
	total := issue.TotalPages()
	if total == 0 && canStreamPages(issue.Path) && !issue.FileMissing {
		if pages, err := library.ListPages(issue.Path); err == nil && len(pages) > 0 {
			total = len(pages)
			if err := s.store.SetIssueFilePages(issue.ID, total); err != nil {
				log.Printf("mark read: page count for issue %d: %v", issue.ID, err)
			}
		}
	}
	if total == 0 {
		s.redirectBack(w, r, issue.ID, "read_nopages")
		return
	}
	if err := s.store.SetReadingProgress(userFrom(r), issue.ID, total); err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectBack(w, r, issue.ID, "read_ok")
}

// handleIssueMarkUnread forgets the issue's reading progress.
func (s *Server) handleIssueMarkUnread(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}
	if err := s.store.ClearReadingProgress(userFrom(r), issue.ID); err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectBack(w, r, issue.ID, "unread_ok")
}

// redirectBack returns to the page the form was submitted from (the "next"
// field, restricted to local paths) or to the issue page with a flash code.
func (s *Server) redirectBack(w http.ResponseWriter, r *http.Request, issueID int64, msg string) {
	next := r.FormValue("next")
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/issues/" + strconv.FormatInt(issueID, 10) + "?msg=" + msg
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// readingView is reading progress shaped for templates.
type readingView struct {
	Page     int    // last page read (1-based)
	Total    int    // pages in the issue (0 = unknown)
	Percent  int    // 0–100; 0 when Total is unknown
	Finished bool   // reached the last page
	ReadAt   string // local time of the last streamed page, "2006-01-02 15:04"
}

// newReadingView combines an issue with its progress; nil when unread.
//
// A lone first page (opened and immediately closed) does not count as
// "started" — only real progress (page 2+) or an outright finish does. This
// keeps a barely-glanced-at issue out of "reading".
func newReadingView(i *store.Issue, p *store.ReadingProgress) *readingView {
	if p == nil || p.Page <= 0 {
		return nil
	}
	total := i.TotalPages()
	finished := total > 0 && p.Page >= total
	if p.Page <= 1 && !finished {
		return nil
	}
	v := &readingView{Page: p.Page, Total: total}
	if v.Total > 0 {
		v.Page = min(v.Page, v.Total)
		v.Percent = v.Page * 100 / v.Total
		v.Finished = v.Page >= v.Total
	}
	if t := opds.ParseDBTime(p.UpdatedAt, time.Time{}); !t.IsZero() {
		v.ReadAt = t.Local().Format("2006-01-02 15:04")
	}
	return v
}

// issueRow is an issue plus its reading progress, for list views.
type issueRow struct {
	store.Issue
	Reading *readingView
}

// issueRows attaches reading progress to issues in one query.
func (s *Server) issueRows(user string, issues []store.Issue) ([]issueRow, error) {
	progress, err := s.progressFor(user, issues)
	if err != nil {
		return nil, err
	}
	rows := make([]issueRow, len(issues))
	for k := range issues {
		rows[k] = issueRow{Issue: issues[k], Reading: newReadingView(&issues[k], progressPtr(progress, issues[k].ID))}
	}
	return rows, nil
}
