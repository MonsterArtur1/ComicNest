package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"comicnest/internal/library"
)

// legacyScanner returns the single scanner addressed by a request that gives
// no ?lib= value — the only configured library, or (nothing configured yet)
// the placeholder scanner main.go creates so the "set the library path"
// error still surfaces in the UI, exactly as before multi-library support.
func (s *Server) legacyScanner() (*library.Scanner, bool) {
	switch len(s.libraries) {
	case 0:
		sc, ok := s.scanners[""]
		return sc, ok
	case 1:
		sc, ok := s.scanners[s.libraries[0].Path]
		return sc, ok
	default:
		return nil, false
	}
}

// resolveScanTargets maps a request's ?lib= value to the scanner(s) it
// addresses: "" (absent) means legacyScanner, "all" means every configured
// library, and anything else is a config-order index (see libraryByIndex).
func (s *Server) resolveScanTargets(lib string) (targets []*library.Scanner, ok bool) {
	if lib == "" {
		sc, ok := s.legacyScanner()
		if !ok {
			return nil, false
		}
		return []*library.Scanner{sc}, true
	}
	if lib == "all" {
		out := make([]*library.Scanner, 0, len(s.libraries))
		for _, l := range s.libraries {
			if sc, exists := s.scanners[l.Path]; exists {
				out = append(out, sc)
			}
		}
		return out, len(out) > 0
	}
	info, _, idxOK := s.libraryByIndex(lib)
	if !idxOK {
		return nil, false
	}
	sc, exists := s.scanners[info.Path]
	return []*library.Scanner{sc}, exists
}

// handleScanStart kicks off a scan of the library (or libraries) named by
// ?lib=. For HTMX requests it responds with the status partial; plain form
// posts get redirected back to the admin panel. Precondition failures
// (library not configured, folder missing) are recorded in the scanner
// status, which the partial renders — never a 5xx.
func (s *Server) handleScanStart(w http.ResponseWriter, r *http.Request) {
	lib := r.FormValue("lib")
	targets, ok := s.resolveScanTargets(lib)
	if !ok {
		s.serverError(w, fmt.Errorf("scan: unknown library %q", lib))
		return
	}
	// A library removed from config.yaml has no Scanner anymore and so is
	// never in targets — catch it here too, not just at startup, since a
	// scan is the natural moment a user expects stale libraries to be swept.
	if paths := s.libraryPaths(); len(paths) > 0 {
		if _, err := s.store.MarkOrphanedLibrariesMissing(paths); err != nil {
			log.Printf("scan: marking orphaned library issues missing: %v", err)
		}
	}
	for _, sc := range targets {
		if err := sc.Start(); err != nil && !errors.Is(err, library.ErrScanRunning) {
			log.Printf("scan start: %v", err)
		}
	}
	if r.Header.Get("HX-Request") == "true" {
		s.renderScanStatus(w, lib)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) handleScanStatus(w http.ResponseWriter, r *http.Request) {
	s.renderScanStatus(w, r.FormValue("lib"))
}

// scanStatusView is what the scan_status.html partial renders: the scan
// progress plus the ?lib= value to keep posting/polling against, so the
// partial's own embedded form/hx-get stay addressed at the same target after
// each swap.
type scanStatusView struct {
	Lib string
	library.Status
}

// AreaID is the DOM id of this status view's own hx-swap target, unique per
// library so several can poll independently on the same admin page.
func (v scanStatusView) AreaID() string {
	if v.Lib == "" {
		return "scan-area"
	}
	return "scan-area-" + v.Lib
}

func (s *Server) renderScanStatus(w http.ResponseWriter, lib string) {
	targets, ok := s.resolveScanTargets(lib)
	if !ok {
		s.renderPartial(w, "scan_status.html", "scan-status", scanStatusView{Lib: lib})
		return
	}
	var st library.Status
	if lib == "all" {
		st = combineScanStatus(targets)
	} else {
		st = targets[0].Status()
	}
	s.renderPartial(w, "scan_status.html", "scan-status", scanStatusView{Lib: lib, Status: st})
}

// combineScanStatus merges several libraries' scan status into one summary
// for the admin panel's "Scan All" indicator.
func combineScanStatus(scanners []*library.Scanner) library.Status {
	var out library.Status
	for _, sc := range scanners {
		st := sc.Status()
		out.Found += st.Found
		out.Processed += st.Processed
		out.Missing += st.Missing
		out.CVDone += st.CVDone
		out.CVTotal += st.CVTotal
		out.CVUpdated += st.CVUpdated
		out.CVFailed += st.CVFailed
		out.Running = out.Running || st.Running
		out.CVPhase = out.CVPhase || st.CVPhase
		out.Finished = out.Finished || st.Finished
		if st.Err != "" {
			if out.Err != "" {
				out.Err += "; "
			}
			out.Err += st.Err
		}
		if st.StartedAt.After(out.StartedAt) {
			out.StartedAt = st.StartedAt
		}
		if st.FinishedAt.After(out.FinishedAt) {
			out.FinishedAt = st.FinishedAt
		}
	}
	return out
}
