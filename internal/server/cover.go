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

	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, s.covers.Path(id))
}
