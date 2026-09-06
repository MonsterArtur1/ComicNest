package server

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
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

// Version is the build version shown in the page footer. main stamps it from
// the binary; "dev" (a local `go build`) shows nothing.
var Version = "dev"

// displayVersion returns the version for the UI, or "" for local builds.
func displayVersion() string {
	if Version == "" || Version == "dev" {
		return ""
	}
	return Version
}

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
	reader    *template.Template // standalone full-screen reader page
	login     *template.Template // standalone login page
	sessions  *sessions
	mux       *http.ServeMux
}

func New(cfg config.Config, st *store.Store, cv *covers.Cache, sc *library.Scanner, cvc *comicvine.Client) (*Server, error) {
	s := &Server{
		cfg:     cfg,
		store:   st,
		covers:  cv,
		scanner: sc,
		cv:      cvc,
		scrape:   &scrapeJob{},
		sessions: newSessions(),
		mux:      http.NewServeMux(),
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	s.routes()
	// After every file scan, issues of matched series missing ComicVine data
	// get scraped automatically.
	sc.SetCVUpdater(s.autoScrapeCV)
	return s, nil
}

// funcMap holds helpers available in every template.
func (s *Server) funcMap() template.FuncMap {
	return template.FuncMap{
		"prettySize":  prettySize,
		"sourceLabel": sourceLabel,
		"truncate":    truncateText,
		"inc":         func(n int) int { return n + 1 },
		"dec":         func(n int) int { return n - 1 },
		"opdsEnabled": func() bool { return s.cfg.OPDSEnabled },
		"appVersion":  displayVersion,
		"canRead":     canStreamPages, // in-browser reader works for CBZ/CBR only
		// currentUser is overridden per request in renderStatus; this default
		// only satisfies parse-time resolution.
		"currentUser": func() string { return "" },
	}
}

// truncateText cuts a string to at most n runes, preferring a word boundary,
// and appends an ellipsis.
func truncateText(s string, n int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	cut := string(runes[:n])
	if i := strings.LastIndexAny(cut, " \n\t"); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, ",.;:") + "…"
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
		t, err := template.New(page).Funcs(s.funcMap()).
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

	// The reader has its own chrome-less document instead of the layout.
	reader, err := template.New("reader.html").Funcs(s.funcMap()).ParseFS(web.FS, "templates/reader.html")
	if err != nil {
		return fmt.Errorf("parsing reader template: %w", err)
	}
	s.reader = reader

	login, err := template.New("login.html").Funcs(s.funcMap()).ParseFS(web.FS, "templates/login.html")
	if err != nil {
		return fmt.Errorf("parsing login template: %w", err)
	}
	s.login = login
	return nil
}

func (s *Server) routes() {
	static, err := fs.Sub(web.FS, "static")
	if err != nil {
		panic(err) // embedded FS layout is fixed at compile time
	}
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	// Browsers and some OPDS readers probe /favicon.ico directly.
	s.mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, static, "favicon.ico")
	})
	s.mux.HandleFunc("GET /{$}", s.handleHome)
	// Liveness probe for Docker/orchestrators: 200 once the server answers.
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("GET /login", s.handleLoginForm)
	s.mux.HandleFunc("POST /login", s.handleLogin)
	s.mux.HandleFunc("POST /logout", s.handleLogout)
	s.mux.HandleFunc("GET /series/{id}", s.handleSeries)
	s.mux.HandleFunc("GET /series/{id}/edit", s.handleSeriesEditForm)
	s.mux.HandleFunc("POST /series/{id}", s.handleSeriesEditSave)
	s.mux.HandleFunc("POST /series/{id}/unlock", s.handleSeriesUnlock)
	s.mux.HandleFunc("POST /series/{id}/merge", s.handleSeriesMerge)
	s.mux.HandleFunc("GET /series/{id}/match", s.handleMatchPage)
	s.mux.HandleFunc("POST /series/{id}/match/unlink", s.handleMatchUnlink)
	s.mux.HandleFunc("POST /series/{id}/match/{volumeID}", s.handleMatchSave)
	s.mux.HandleFunc("POST /series/{id}/scrape", s.handleSeriesScrape)
	s.mux.HandleFunc("GET /series/{id}/scrape/status", s.handleScrapeStatus)
	s.mux.HandleFunc("GET /issues/{id}", s.handleIssue)
	s.mux.HandleFunc("GET /issues/{id}/edit", s.handleIssueEditForm)
	s.mux.HandleFunc("POST /issues/{id}", s.handleIssueEditSave)
	s.mux.HandleFunc("POST /issues/{id}/unlock", s.handleIssueUnlock)
	s.mux.HandleFunc("POST /issues/{id}/scrape", s.handleIssueScrape)
	s.mux.HandleFunc("POST /issues/{id}/scrape/unlink", s.handleIssueScrapeUnlink)
	s.mux.HandleFunc("POST /issues/{id}/delete", s.handleIssueDelete)
	s.mux.HandleFunc("POST /issues/{id}/read", s.handleIssueMarkRead)
	s.mux.HandleFunc("POST /issues/{id}/unread", s.handleIssueMarkUnread)
	s.mux.HandleFunc("GET /issues/{id}/read", s.handleReader)
	s.mux.HandleFunc("GET /issues/{id}/pages/{page}", s.handleIssuePage)
	s.mux.HandleFunc("POST /issues/{id}/progress", s.handleIssueProgress)
	s.mux.HandleFunc("GET /issues/{id}/cover", s.handleIssueCover)
	s.mux.HandleFunc("GET /issues/{id}/download", s.handleIssueDownload)
	s.mux.HandleFunc("GET /search", s.handleSearch)
	s.mux.HandleFunc("POST /scan", s.handleScanStart)
	s.mux.HandleFunc("GET /scan/status", s.handleScanStatus)
	if s.cfg.OPDSEnabled {
		s.opdsRoutes()
	}
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
		if strings.HasPrefix(p, "/static/") || p == "/favicon.ico" || p == "/healthz" || p == "/scan/status" ||
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

// ListenAndServe blocks, serving the app on the configured interface
// (localhost by default — the web UI has no auth).
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Listen, s.cfg.Port)
	hosts := s.reachableHosts()
	for i, h := range hosts {
		label := "ComicNest running at"
		if i > 0 {
			label = "             also at"
		}
		fmt.Printf("%s http://%s:%d/\n", label, h, s.cfg.Port)
	}
	if s.cfg.OPDSEnabled {
		for _, h := range hosts {
			fmt.Printf("OPDS catalog: http://%s:%d/opds\n", h, s.cfg.Port)
		}
		// Thorium Reader validates catalog URLs with a "must have a TLD" rule
		// (validator.js isURL, THORIUM_ISURL_REQUIRE_TLD_FALSE unset in
		// official builds), so "localhost" is rejected while IPs pass.
		fmt.Println("OPDS note: Thorium Reader rejects \"localhost\" — use an IP address (127.0.0.1 or the LAN address above)")
	}
	return http.ListenAndServe(addr, s.withLogging(s.withAuth(s.mux)))
}

// reachableHosts lists hosts a client can use to reach the server: just the
// configured one, or — when bound to all interfaces — localhost plus every
// non-loopback IPv4 address, so the user can copy a LAN URL into a reader.
func (s *Server) reachableHosts() []string {
	if s.cfg.Listen != "0.0.0.0" && s.cfg.Listen != "" && s.cfg.Listen != "::" {
		return []string{s.cfg.Listen}
	}
	hosts := []string{"localhost"}
	ifaces, err := net.Interfaces()
	if err != nil {
		return hosts
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || ip.IsLinkLocalUnicast() {
				continue
			}
			hosts = append(hosts, ip.String())
		}
	}
	return hosts
}

// Handler exposes the routed handler (without request logging) for tests.
func (s *Server) Handler() http.Handler {
	return s.withAuth(s.mux)
}

// render writes a full page with status 200.
func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, data any) {
	s.renderStatus(w, r, http.StatusOK, page, data)
}

// renderStatus executes a page into a buffer first, so a template failure
// becomes a clean 500 instead of a half-written response. r may be nil (no
// user shown). The parsed templates are never executed directly: each render
// works on a clone with the request's user bound to currentUser, because
// html/template forbids cloning after the first execution.
func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	t, ok := s.templates[page]
	if !ok {
		s.serverError(w, fmt.Errorf("unknown template %q", page))
		return
	}
	t, err := t.Clone()
	if err != nil {
		s.serverError(w, fmt.Errorf("clone template %s: %w", page, err))
		return
	}
	user := userFrom(r)
	t.Funcs(template.FuncMap{"currentUser": func() string { return user }})

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
// even that template fails). r may be nil.
func (s *Server) errorPage(w http.ResponseWriter, r *http.Request, status int, message string) {
	t, ok := s.templates["error.html"]
	if !ok {
		http.Error(w, message, status)
		return
	}
	t, err := t.Clone()
	if err != nil {
		log.Printf("clone error page: %v", err)
		http.Error(w, message, status)
		return
	}
	user := userFrom(r)
	t.Funcs(template.FuncMap{"currentUser": func() string { return user }})

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
	s.errorPage(w, r, http.StatusNotFound, "Nie znaleziono takiej strony ani zasobu.")
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
	s.errorPage(w, nil, http.StatusInternalServerError,
		"Wystąpił błąd serwera — szczegóły w logu aplikacji.")
}
