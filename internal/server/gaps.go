package server

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"comicnest/internal/store"
)

// seriesGapsFor computes the gap summary shown on a series page; nil when
// there is nothing worth showing (one-shot, not numbered, inferred but
// complete).
func (s *Server) seriesGapsFor(series *store.Series) *store.Gaps {
	g, err := s.store.SeriesGaps(series.ID, today())
	if err != nil {
		log.Printf("gaps: series %d: %v", series.ID, err)
		return nil
	}
	if g == nil || (!g.FromComicVine() && g.MissingCount() == 0) {
		return nil
	}
	return g
}

// gapSeriesIDs maps every series with gaps (in the library selection) to its
// number of missing issues — backs the grid's "has gaps" filter.
func (s *Server) gapSeriesIDs(r *http.Request) (map[int64]int, error) {
	list, err := s.store.ListSeriesGaps(s.selectedLibrary(r), today())
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int, len(list))
	for _, g := range list {
		out[g.SeriesID] = g.Gaps.MissingCount()
	}
	return out, nil
}

type gapsData struct {
	Rows         []store.SeriesGap
	TotalMissing int
	CVEnabled    bool
}

// handleGaps lists every series with missing issues, most missing first.
func (s *Server) handleGaps(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListSeriesGaps(s.selectedLibrary(r), today())
	if err != nil {
		s.serverError(w, err)
		return
	}
	data := gapsData{Rows: rows, CVEnabled: s.cv.Enabled()}
	for _, g := range rows {
		data.TotalMissing += g.Gaps.MissingCount()
	}
	s.render(w, r, "gaps.html", data)
}

// handleGapsText is the wishlist as plain text, one series per line, for
// taking shopping: "Series Name: #12-14, #20".
func (s *Server) handleGapsText(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListSeriesGaps(s.selectedLibrary(r), today())
	if err != nil {
		s.serverError(w, err)
		return
	}
	var b strings.Builder
	for _, g := range rows {
		fmt.Fprintf(&b, "%s: %s\n", g.Name, g.Gaps.RangeTextASCII())
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="comicnest-missing.txt"`)
	w.Write([]byte(b.String()))
}
