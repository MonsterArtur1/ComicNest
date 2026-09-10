package server

import "net/http"

// handleIssueFavorite toggles whether the requesting user has favorited the
// issue, then returns to the page the form was submitted from. There is no
// series-level favorite — a series surfaces in the "favorite" filter/star
// once any of its issues is favorited (see Store.ListSeries).
func (s *Server) handleIssueFavorite(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}
	user := userFrom(r)
	fav, err := s.store.IsIssueFavorite(user, issue.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.store.SetIssueFavorite(user, issue.ID, !fav); err != nil {
		s.serverError(w, err)
		return
	}
	msg := "fav_ok"
	if fav {
		msg = "unfav_ok"
	}
	s.redirectBack(w, r, issue.ID, msg)
}
