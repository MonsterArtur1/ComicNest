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
	Series  *store.Series
	Options []store.Series // other series, for the merge-target picker
	Error   string
}

// mergeOptions lists every series but the one being edited, for the merge
// form's target picker.
func (s *Server) mergeOptions(user string, excludeID int64) ([]store.Series, error) {
	all, err := s.store.ListSeries(user, "", store.SeriesSortName, store.SeriesFilterAll)
	if err != nil {
		return nil, err
	}
	out := make([]store.Series, 0, len(all))
	for _, sr := range all {
		if sr.ID != excludeID {
			out = append(out, sr)
		}
	}
	return out, nil
}

func (s *Server) handleSeriesEditForm(w http.ResponseWriter, r *http.Request) {
	series := s.getSeriesFromPath(w, r)
	if series == nil {
		return
	}
	options, err := s.mergeOptions(userFrom(r), series.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "series_edit.html", seriesEditData{Series: series, Options: options})
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
		options, err := s.mergeOptions(userFrom(r), series.ID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		s.render(w, r, "series_edit.html", seriesEditData{Series: series, Options: options, Error: "Series name cannot be empty."})
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

// handleSeriesMerge folds the edited series into another one picked from the
// merge form: every issue moves to the target series and this one is deleted.
// Fixes the common case where the same series was catalogued twice (e.g. from
// two folders) and only noticed after the fact.
func (s *Server) handleSeriesMerge(w http.ResponseWriter, r *http.Request) {
	series := s.getSeriesFromPath(w, r)
	if series == nil {
		return
	}

	renderError := func(msg string) {
		options, err := s.mergeOptions(userFrom(r), series.ID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		s.render(w, r, "series_edit.html", seriesEditData{Series: series, Options: options, Error: msg})
	}

	targetID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("into_id")), 10, 64)
	if err != nil || targetID == series.ID {
		renderError("Choose a different series to merge this one into.")
		return
	}
	target, err := s.store.GetSeries(targetID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if target == nil {
		renderError("The selected target series was not found.")
		return
	}

	if err := s.store.MergeSeries(series.ID, targetID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/series/"+strconv.FormatInt(targetID, 10), http.StatusSeeOther)
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
	Series     []store.Series // every series, for the "move to series" picker
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
	all, err := s.store.ListSeries(userFrom(r), "", store.SeriesSortName, store.SeriesFilterAll)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "issue_edit.html", issueEditData{Issue: issue, SeriesName: series.Name, Series: all})
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

	// Recataloguing to another series (e.g. the file was matched to the wrong
	// one) is a separate, simpler write — it doesn't touch issue metadata.
	if v := strings.TrimSpace(r.FormValue("series_id")); v != "" {
		if seriesID, err := strconv.ParseInt(v, 10, 64); err == nil && seriesID != issue.SeriesID {
			target, err := s.store.GetSeries(seriesID)
			if err != nil {
				s.serverError(w, err)
				return
			}
			if target != nil {
				if err := s.store.SetIssueSeries(issue.ID, seriesID); err != nil {
					s.serverError(w, err)
					return
				}
			}
		}
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
