package server

import (
	"net/http"

	"comicnest/internal/store"
)

type homeData struct {
	Series []store.Series
	Sort   string
	Filter string
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	sort := store.SeriesSortName
	if r.FormValue("sort") == "recent" {
		sort = store.SeriesSortRecent
	}
	filter := store.ParseSeriesFilter(r.FormValue("filter"))

	series, err := s.store.ListSeries(userFrom(r), "", sort, filter)
	if err != nil {
		s.serverError(w, err)
		return
	}

	s.render(w, r, "index.html", homeData{Series: series, Sort: string(sort), Filter: string(filter)})
}
