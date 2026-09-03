package server

import (
	"errors"
	"log"
	"net/http"

	"comicnest/internal/library"
)

// handleScanStart kicks off a library scan. For HTMX requests it responds with
// the status partial; plain form posts get redirected back to the home page.
// Precondition failures (library not configured, folder missing) are recorded
// in the scanner status, which the partial renders — never a 5xx.
func (s *Server) handleScanStart(w http.ResponseWriter, r *http.Request) {
	if err := s.scanner.Start(); err != nil && !errors.Is(err, library.ErrScanRunning) {
		log.Printf("scan start: %v", err)
	}
	if r.Header.Get("HX-Request") == "true" {
		s.renderScanStatus(w)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleScanStatus(w http.ResponseWriter, r *http.Request) {
	s.renderScanStatus(w)
}

func (s *Server) renderScanStatus(w http.ResponseWriter) {
	s.renderPartial(w, "scan_status.html", "scan-status", s.scanner.Status())
}
