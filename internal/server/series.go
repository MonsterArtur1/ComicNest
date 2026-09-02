package server

import (
	"net/http"
	"strconv"

	"comicnest/internal/store"
)

type seriesData struct {
	Series *store.Series
	Issues []store.Issue
	Filter string
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}

	series, err := s.store.GetSeries(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if series == nil {
		s.notFound(w, r)
		return
	}

	filter := store.IssueFilter(r.FormValue("filter"))
	switch filter {
	case store.IssueFilterNoMeta, store.IssueFilterComicVine, store.IssueFilterMissing:
	default:
		filter = store.IssueFilterAll
	}

	issues, err := s.store.ListIssuesBySeries(id, filter)
	if err != nil {
		s.serverError(w, err)
		return
	}

	s.render(w, "series.html", seriesData{Series: series, Issues: issues, Filter: string(filter)})
}
