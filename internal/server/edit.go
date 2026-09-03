package server

import (
	"net/http"
	"strconv"
	"strings"

	"comicnest/internal/store"
)

// --- series ---

func (s *Server) getSeriesFromPath(w http.ResponseWriter, r *http.Request) *store.Series {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return nil
	}
	series, err := s.store.GetSeries(id)
	if err != nil {
		s.serverError(w, err)
		return nil
	}
	if series == nil {
		s.notFound(w, r)
		return nil
	}
	return series
}

type seriesEditData struct {
	Series *store.Series
	Error  string
}

func (s *Server) handleSeriesEditForm(w http.ResponseWriter, r *http.Request) {
	series := s.getSeriesFromPath(w, r)
	if series == nil {
		return
	}
	s.render(w, "series_edit.html", seriesEditData{Series: series})
}

func (s *Server) handleSeriesEditSave(w http.ResponseWriter, r *http.Request) {
	series := s.getSeriesFromPath(w, r)
	if series == nil {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	oneShot := r.FormValue("one_shot") != ""
	if name == "" {
		series.Publisher = strings.TrimSpace(r.FormValue("publisher"))
		series.Description = strings.TrimSpace(r.FormValue("description"))
		series.OneShot = oneShot
		s.render(w, "series_edit.html", seriesEditData{Series: series, Error: "Nazwa serii nie może być pusta."})
		return
	}

	err := s.store.UpdateSeriesManual(series.ID, name,
		strings.TrimSpace(r.FormValue("publisher")),
		strings.TrimSpace(r.FormValue("description")),
		oneShot)
	if err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/series/"+strconv.FormatInt(series.ID, 10), http.StatusSeeOther)
}

func (s *Server) handleSeriesUnlock(w http.ResponseWriter, r *http.Request) {
	series := s.getSeriesFromPath(w, r)
	if series == nil {
		return
	}
	if err := s.store.SetSeriesLocked(series.ID, false); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/series/"+strconv.FormatInt(series.ID, 10), http.StatusSeeOther)
}

// --- issues ---

type issueEditData struct {
	Issue      *store.Issue
	SeriesName string
	Error      string
}

func (s *Server) handleIssueEditForm(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}
	series, err := s.store.GetSeries(issue.SeriesID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "issue_edit.html", issueEditData{Issue: issue, SeriesName: series.Name})
}

func (s *Server) handleIssueEditSave(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}

	issue.IssueNumber = strings.TrimSpace(r.FormValue("issue_number"))
	issue.Title = strings.TrimSpace(r.FormValue("title"))
	issue.Summary = strings.TrimSpace(r.FormValue("summary"))
	issue.ReleaseDate = strings.TrimSpace(r.FormValue("release_date"))
	issue.Writer = strings.TrimSpace(r.FormValue("writer"))
	issue.Artist = strings.TrimSpace(r.FormValue("artist"))
	issue.Publisher = strings.TrimSpace(r.FormValue("publisher"))
	if pc, err := strconv.Atoi(strings.TrimSpace(r.FormValue("page_count"))); err == nil && pc >= 0 {
		issue.PageCount = pc
	}
	issue.MetadataSource = store.SourceManual
	issue.MetadataLocked = true

	if err := s.store.UpdateIssueMetadata(issue); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/issues/"+strconv.FormatInt(issue.ID, 10), http.StatusSeeOther)
}

func (s *Server) handleIssueUnlock(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}
	if err := s.store.SetIssueLocked(issue.ID, false); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/issues/"+strconv.FormatInt(issue.ID, 10), http.StatusSeeOther)
}
