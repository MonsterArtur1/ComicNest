package server

import (
	"net/http"
	"strings"

	"comicnest/internal/store"
)

type searchData struct {
	Query  string
	Series []store.Series
	Issues []store.IssueWithSeries
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.FormValue("q"))
	data := searchData{Query: q}

	if q != "" {
		var err error
		data.Series, err = s.store.ListSeries(userFrom(r), q, store.SeriesSortName, store.SeriesFilterAll, s.selectedLibrary(r))
		if err != nil {
			s.serverError(w, err)
			return
		}
		data.Issues, err = s.store.SearchIssues(q)
		if err != nil {
			s.serverError(w, err)
			return
		}
	}

	s.render(w, r, "search.html", data)
}
