package server

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"comicnest/internal/comicvine"
	"comicnest/internal/config"
	"comicnest/internal/covers"
	"comicnest/internal/library"
	"comicnest/internal/store"
	"comicnest/web"
)

// Server holds application dependencies shared by all HTTP handlers.
type Server struct {
	cfg       config.Config
	store     *store.Store
	covers    *covers.Cache
	scanner   *library.Scanner
	cv        *comicvine.Client
	scrape    *scrapeJob
	templates map[string]*template.Template
	partials  *template.Template
	mux       *http.ServeMux
}

func New(cfg config.Config, st *store.Store, cv *covers.Cache, sc *library.Scanner, cvc *comicvine.Client) (*Server, error) {
	s := &Server{
		cfg:     cfg,
		store:   st,
		covers:  cv,
		scanner: sc,
		cv:      cvc,
		scrape:  &scrapeJob{},
		mux:     http.NewServeMux(),
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	s.routes()
	return s, nil
}

// funcMap holds helpers available in every template.
var funcMap = template.FuncMap{
	"prettySize":  prettySize,
	"sourceLabel": sourceLabel,
}

// prettySize renders a byte count for humans ("24.3 MB").
func prettySize(b int64) string {
	f := float64(b)
	for _, unit := range []string{"B", "KB", "MB", "GB"} {
		if f < 1024 || unit == "GB" {
			if unit == "B" {
				return fmt.Sprintf("%.0f %s", f, unit)
			}
			return fmt.Sprintf("%.1f %s", f, unit)
		}
		f /= 1024
	}
	return ""
}

// sourceLabel maps a metadata_source value to its Polish UI label.
func sourceLabel(source string) string {
	switch source {
	case "filename":
		return "z nazwy pliku"
	case "comicinfo":
		return "ComicInfo"
	case "comicvine":
		return "ComicVine"
	case "manual":
		return "ręczne"
	}
	return source
}

// parseTemplates builds one template set per page view; each set is the
// shared layout plus that view's blocks.
func (s *Server) parseTemplates() error {
	pages := []string{"index.html", "series.html", "issue.html", "search.html",
		"series_edit.html", "issue_edit.html", "comicvine_match.html", "error.html"}

	s.templates = make(map[string]*template.Template)
	for _, page := range pages {
		t, err := template.New(page).Funcs(funcMap).
			ParseFS(web.FS, "templates/layout.html", "templates/"+page)
		if err != nil {
			return fmt.Errorf("parsing template %s: %w", page, err)
		}
		s.templates[page] = t
	}

	// Partials are rendered standalone (no layout), mostly for HTMX swaps.
	partials, err := template.ParseFS(web.FS, "templates/scan_status.html", "templates/scrape_status.html")
	if err != nil {
		return fmt.Errorf("parsing partials: %w", err)
	}
	s.partials = partials
	return nil
}

func (s *Server) routes() {
	static, err := fs.Sub(web.FS, "static")
	if err != nil {
		panic(err) // embedded FS layout is fixed at compile time
	}
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	s.mux.HandleFunc("GET /{$}", s.handleHome)
	s.mux.HandleFunc("GET /series/{id}", s.handleSeries)
	s.mux.HandleFunc("GET /series/{id}/edit", s.handleSeriesEditForm)
	s.mux.HandleFunc("POST /series/{id}", s.handleSeriesEditSave)
	s.mux.HandleFunc("POST /series/{id}/unlock", s.handleSeriesUnlock)
	s.mux.HandleFunc("GET /series/{id}/match", s.handleMatchPage)
	s.mux.HandleFunc("POST /series/{id}/match/{volumeID}", s.handleMatchSave)
	s.mux.HandleFunc("POST /series/{id}/scrape", s.handleSeriesScrape)
	s.mux.HandleFunc("GET /series/{id}/scrape/status", s.handleScrapeStatus)
	s.mux.HandleFunc("GET /issues/{id}", s.handleIssue)
	s.mux.HandleFunc("GET /issues/{id}/edit", s.handleIssueEditForm)
	s.mux.HandleFunc("POST /issues/{id}", s.handleIssueEditSave)
	s.mux.HandleFunc("POST /issues/{id}/unlock", s.handleIssueUnlock)
	s.mux.HandleFunc("POST /issues/{id}/scrape", s.handleIssueScrape)
	s.mux.HandleFunc("POST /issues/{id}/delete", s.handleIssueDelete)
	s.mux.HandleFunc("GET /issues/{id}/cover", s.handleIssueCover)
	s.mux.HandleFunc("GET /issues/{id}/download", s.handleIssueDownload)
	s.mux.HandleFunc("GET /search", s.handleSearch)
	s.mux.HandleFunc("POST /scan", s.handleScanStart)
	s.mux.HandleFunc("GET /scan/status", s.handleScanStatus)
	s.mux.HandleFunc("/", s.handleNotFound) // catch-all: styled 404
}

// statusWriter records the response code for the request log.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// withLogging logs every request except static assets and HTMX status polls,
// which would only drown the log.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/static/") || p == "/scan/status" ||
			strings.HasSuffix(p, "/scrape/status") || strings.HasSuffix(p, "/cover") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s -> %d (%s)", r.Method, p, sw.status, time.Since(start).Round(time.Millisecond))
	})
}

// ListenAndServe blocks, serving the app on localhost (personal app, no auth).
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf("localhost:%d", s.cfg.Port)
	fmt.Printf("ComicNest running at http://%s/\n", addr)
	return http.ListenAndServe(addr, s.withLogging(s.mux))
}

// render writes a full page with status 200.
func (s *Server) render(w http.ResponseWriter, page string, data any) {
	s.renderStatus(w, http.StatusOK, page, data)
}

// renderStatus executes a page into a buffer first, so a template failure
// becomes a clean 500 instead of a half-written response.
func (s *Server) renderStatus(w http.ResponseWriter, status int, page string, data any) {
	t, ok := s.templates[page]
	if !ok {
		s.serverError(w, fmt.Errorf("unknown template %q", page))
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		s.serverError(w, fmt.Errorf("render %s: %w", page, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

type errorData struct {
	Status  int
	Message string
}

// errorPage renders the styled error view (falling back to plain text if
// even that template fails).
func (s *Server) errorPage(w http.ResponseWriter, status int, message string) {
	t, ok := s.templates["error.html"]
	if !ok {
		http.Error(w, message, status)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", errorData{Status: status, Message: message}); err != nil {
		log.Printf("render error page: %v", err)
		http.Error(w, message, status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.errorPage(w, http.StatusNotFound, "Nie znaleziono takiej strony ani zasobu.")
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.notFound(w, r)
}

// renderPartial writes a standalone template (an HTMX fragment).
func (s *Server) renderPartial(w http.ResponseWriter, file, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.partials.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render partial %s (%s): %v", name, file, err)
	}
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	log.Printf("server error: %v", err)
	s.errorPage(w, http.StatusInternalServerError,
		"Wystąpił błąd serwera — szczegóły w logu aplikacji.")
}
