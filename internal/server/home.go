package server

import (
	"net/http"

	"comicnest/internal/store"
)

type homeData struct {
	Series []store.Series
	Sort   string
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	sort := store.SeriesSortName
	if r.FormValue("sort") == "recent" {
		sort = store.SeriesSortRecent
	}

	series, err := s.store.ListSeries("", sort)
	if err != nil {
		s.serverError(w, err)
		return
	}

	s.render(w, "index.html", homeData{Series: series, Sort: string(sort)})
}
