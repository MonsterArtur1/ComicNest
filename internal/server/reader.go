package server

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"comicnest/internal/library"
	"comicnest/internal/store"
)

// readerData drives the in-browser page reader.
type readerData struct {
	Issue       *store.Issue
	Series      *store.Series
	Title       string
	Total       int   // pages in the archive
	StartPage   int   // 1-based page to open first
	PrevIssueID int64 // previous readable issue in the series (0 = none)
	NextIssueID int64 // next readable issue in the series (0 = none)
}

// handleReader serves the reader for an issue. The reader resumes at the
// last page recorded (by OPDS streaming or the reader itself); a finished
// issue starts over from page 1.
func (s *Server) handleReader(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}
	if issue.FileMissing || !canStreamPages(issue.Path) {
		s.errorPage(w, http.StatusNotFound,
			"Czytnik obsługuje tylko archiwa CBZ/CBR obecne na dysku — ten zeszyt można jedynie pobrać.")
		return
	}
	series, err := s.store.GetSeries(issue.SeriesID)
	if err != nil {
		s.serverError(w, err)
		return
	}

	total := issue.TotalPages()
	if issue.FilePages == 0 {
		pages, err := library.ListPages(issue.Path)
		if err != nil {
			s.serverError(w, fmt.Errorf("reader: listing pages of %s: %w", issue.Path, err))
			return
		}
		total = len(pages)
		if err := s.store.SetIssueFilePages(issue.ID, total); err != nil {
			log.Printf("reader: page count for issue %d: %v", issue.ID, err)
		}
	}
	if total == 0 {
		s.errorPage(w, http.StatusNotFound, "To archiwum nie zawiera stron z obrazami.")
		return
	}

	start := 1
	if p, err := s.store.GetReadingProgress(issue.ID); err != nil {
		s.serverError(w, err)
		return
	} else if p != nil && p.Page > 0 && p.Page < total {
		start = p.Page
	}
	if q, err := strconv.Atoi(r.FormValue("page")); err == nil && q >= 1 && q <= total {
		start = q // explicit ?page= wins (links from the issue page)
	}

	prevID, nextID, err := s.neighbourIssues(issue)
	if err != nil {
		s.serverError(w, err)
		return
	}

	title := series.Name
	if issue.IssueNumber != "" {
		title += " #" + issue.IssueNumber
	}
	if issue.Title != "" && issue.Title != series.Name {
		title += " – " + issue.Title
	}

	data := readerData{
		Issue: issue, Series: series, Title: title, Total: total, StartPage: start,
		PrevIssueID: prevID, NextIssueID: nextID,
	}
	var buf bytes.Buffer
	if err := s.reader.ExecuteTemplate(&buf, "reader", data); err != nil {
		s.serverError(w, fmt.Errorf("render reader: %w", err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

// neighbourIssues finds the readable issues right before and after the given
// one in its series' reading order.
func (s *Server) neighbourIssues(issue *store.Issue) (prev, next int64, err error) {
	all, err := s.store.ListIssuesBySeries(issue.SeriesID, store.IssueFilterAll)
	if err != nil {
		return 0, 0, err
	}
	var readable []int64
	for _, i := range all {
		if !i.FileMissing && canStreamPages(i.Path) {
			readable = append(readable, i.ID)
		}
	}
	for k, id := range readable {
		if id != issue.ID {
			continue
		}
		if k > 0 {
			prev = readable[k-1]
		}
		if k+1 < len(readable) {
			next = readable[k+1]
		}
	}
	return prev, next, nil
}

// handleIssueProgress records the page the browser reader is on (1-based).
// Like OPDS streaming it keeps the furthest page, so flipping back does not
// lose the resume point.
func (s *Server) handleIssueProgress(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}
	page, err := strconv.Atoi(r.FormValue("page"))
	if err != nil || page < 1 || (issue.TotalPages() > 0 && page > issue.TotalPages()) {
		http.Error(w, "nieprawidłowy numer strony", http.StatusBadRequest)
		return
	}
	if err := s.store.SetReadingProgress(issue.ID, page); err != nil {
		s.serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
