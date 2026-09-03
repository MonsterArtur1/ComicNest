package server

import (
	"net/http"
	"strconv"
)

// handleIssueCover serves the cached thumbnail for an issue, or the shared
// placeholder when none has been extracted yet.
func (s *Server) handleIssueCover(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}

	issue, err := s.store.GetIssue(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if issue == nil {
		s.notFound(w, r)
		return
	}

	if !issue.CoverCached || !s.covers.Has(id) {
		http.Redirect(w, r, "/static/placeholder.svg", http.StatusFound)
		return
	}

	// Issue ids are reused when the database is recreated and covers change
	// after a ComicVine scrape, so the browser must revalidate every time.
	// ServeFile answers those revalidations with cheap 304s (Last-Modified).
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, s.covers.Path(id))
}
