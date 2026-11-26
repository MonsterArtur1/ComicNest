package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	"comicnest/internal/library"
	"comicnest/internal/panels"
	"comicnest/internal/store"
)

// detectPanels runs panel detection on an encoded page image; a variable so
// tests can count how often detection actually runs.
var detectPanels = panels.DetectBytes

// panelOptions maps config.yaml's panel_detection section to detector
// options.
func (s *Server) panelOptions() panels.Options {
	pd := s.config().PanelDetection
	return panels.Options{
		Sensitivity:     pd.Sensitivity,
		GutterTolerance: pd.GutterTolerance,
		MinPanelArea:    pd.MinPanelArea,
	}
}

// handleIssuePanels serves the guided view's panel rectangles of one page:
// {page} is 0-based like the page endpoint; ?rtl=1 asks for the manga
// (right-to-left) panel order. The response is
// {"page":N,"panels":[{"x":..,"y":..,"w":..,"h":..},...]} in normalized page
// coordinates (0..1); a single {0,0,1,1} panel means "no panel layout found,
// page normally". Each page is analysed once: the result is cached in
// page_panels, keyed to the archive's size/mtime and the detector version
// combined with the configured options (panels.Options.CacheVersion), so a
// changed file, an improved detector or new panel_detection settings
// recompute it.
func (s *Server) handleIssuePanels(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}
	page, err := strconv.Atoi(r.PathValue("page"))
	if err != nil || page < 0 || (issue.FilePages > 0 && page >= issue.FilePages) {
		http.NotFound(w, r)
		return
	}
	if !canStreamPages(issue.Path) {
		http.Error(w, "Page streaming only works for CBZ/CBR archives.", http.StatusNotFound)
		return
	}
	fi, err := os.Stat(issue.Path)
	if err != nil {
		http.Error(w, "The file does not exist on disk (marked as missing?).", http.StatusNotFound)
		return
	}

	rtl := r.FormValue("rtl") == "1"
	opts := s.panelOptions()
	state := store.PageFileState{Size: fi.Size(), MTime: fi.ModTime().Unix(), Version: opts.CacheVersion()}
	cached, ok, err := s.store.GetPagePanels(issue.ID, page, rtl, state)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		data, _, err := library.ExtractPage(issue.Path, page)
		if errors.Is(err, library.ErrPageOutOfRange) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.serverError(w, err)
			return
		}
		rects, err := detectPanels(data, rtl, opts)
		if err != nil {
			// An undecodable page still reads as a plain page in the reader.
			log.Printf("panels: page %d of issue %d: %v (full page)", page, issue.ID, err)
			rects = []panels.Rect{panels.FullPage}
		}
		b, err := json.Marshal(rects)
		if err != nil {
			s.serverError(w, err)
			return
		}
		cached = string(b)
		if err := s.store.SetPagePanels(issue.ID, page, rtl, state, cached); err != nil {
			log.Printf("panels: caching page %d of issue %d: %v", page, issue.ID, err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	fmt.Fprintf(w, `{"page":%d,"panels":%s}`, page, cached)
}
