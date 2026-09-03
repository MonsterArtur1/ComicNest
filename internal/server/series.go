package server

import (
	"net/http"
	"strconv"

	"comicnest/internal/store"
)

type seriesData struct {
	Series *store.Series
	Issues []issueRow
	Filter string
	Next   string // this page's URL, for forms that should return here
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
	rows, err := s.issueRows(userFrom(r), issues)
	if err != nil {
		s.serverError(w, err)
		return
	}

	next := "/series/" + strconv.FormatInt(id, 10)
	if filter != store.IssueFilterAll {
		next += "?filter=" + string(filter)
	}
	s.render(w, r, "series.html", seriesData{Series: series, Issues: rows, Filter: string(filter), Next: next})
}
