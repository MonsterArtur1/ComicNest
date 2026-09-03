package server

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"comicnest/internal/covers"
	"comicnest/internal/library"
	"comicnest/internal/opds"
	"comicnest/internal/store"
)

// opdsPageSize is the number of entries per catalog page.
const opdsPageSize = 50

// opdsRoutes registers the OPDS catalog under /opds. Everything a reader
// touches — feeds, covers and the files themselves — lives under this prefix
// so the optional Basic auth covers all of it, while the web UI stays as is.
func (s *Server) opdsRoutes() {
	handle := func(pattern string, h http.HandlerFunc) {
		s.mux.Handle(pattern, s.opdsAuth(h))
	}
	handle("GET /opds", s.handleOPDSRoot)
	handle("GET /opds/{$}", s.handleOPDSRoot)
	handle("GET /opds/series", s.handleOPDSSeriesList)
	handle("GET /opds/series/{id}", s.handleOPDSSeries)
	handle("GET /opds/recent", s.handleOPDSRecent)
	handle("GET /opds/reading", s.handleOPDSReading)
	handle("GET /opds/search", s.handleOPDSSearch)
	handle("GET /opds/opensearch.xml", s.handleOPDSOpenSearch)
	handle("GET /opds/issues/{id}/file", s.handleIssueDownload)
	handle("GET /opds/issues/{id}/cover", s.handleIssueCover)
	handle("GET /opds/issues/{id}/pages/{page}", s.handleOPDSPage)
}

// opdsAuth enforces HTTP Basic auth when credentials are configured.
func (s *Server) opdsAuth(next http.Handler) http.Handler {
	user, pass := s.cfg.OPDS.Username, s.cfg.OPDS.Password
	if user == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="ComicNest OPDS", charset="UTF-8"`)
			http.Error(w, "Wymagane logowanie do katalogu OPDS.", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// opdsBaseURL reconstructs the absolute origin the client used, so links in
// feeds are absolute (some readers mishandle relative hrefs).
func opdsBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host
}

// opdsPage reads the 1-based ?page= parameter (default 1).
func opdsPage(r *http.Request) int {
	p, err := strconv.Atoi(r.FormValue("page"))
	if err != nil || p < 1 {
		return 1
	}
	return p
}

// pageBounds clips a page window to [0, total) and reports whether a next
// page exists.
func pageBounds(page, total int) (from, to int, hasNext bool) {
	from = min((page-1)*opdsPageSize, total)
	to = min(from+opdsPageSize, total)
	return from, to, to < total
}

// writeFeed serializes a feed with the right media type for its kind.
func (s *Server) writeFeed(w http.ResponseWriter, f *opds.Feed, kind string) {
	w.Header().Set("Content-Type", kind+";charset=utf-8")
	if err := f.Write(w); err != nil {
		s.serverError(w, err)
	}
}

// newOPDSFeed creates a feed with the common self/start/search links.
func (s *Server) newOPDSFeed(r *http.Request, id, title string, updated time.Time) *opds.Feed {
	base := opdsBaseURL(r)
	f := opds.NewFeed("urn:comicnest:"+id, title, updated)
	f.AddLink(opds.RelSelf, base+r.URL.RequestURI(), "")
	f.AddLink(opds.RelStart, base+"/opds", opds.TypeNavigation)
	f.AddLink(opds.RelSearch, base+"/opds/opensearch.xml", opds.TypeOpenSearch)
	return f
}

// handleOPDSRoot serves the root navigation feed.
func (s *Server) handleOPDSRoot(w http.ResponseWriter, r *http.Request) {
	base := opdsBaseURL(r)
	now := time.Now()
	f := s.newOPDSFeed(r, "root", "ComicNest", now)
	f.Links[0].Type = opds.TypeNavigation

	f.Entries = []opds.Entry{
		{
			ID:      "urn:comicnest:series",
			Title:   "Wszystkie serie",
			Updated: opds.FormatTime(now),
			Content: &opds.Text{Type: "text", Value: "Serie i wydania jednorazowe alfabetycznie"},
			Links:   []opds.Link{{Rel: opds.RelSubsection, Href: base + "/opds/series", Type: opds.TypeNavigation}},
		},
		{
			ID:      "urn:comicnest:reading",
			Title:   "Aktualnie czytane",
			Updated: opds.FormatTime(now),
			Content: &opds.Text{Type: "text", Value: "Zeszyty rozpoczęte w czytniku, ale nieprzeczytane do końca"},
			Links:   []opds.Link{{Rel: opds.RelSubsection, Href: base + "/opds/reading", Type: opds.TypeAcquisition}},
		},
		{
			ID:      "urn:comicnest:recent",
			Title:   "Ostatnio dodane",
			Updated: opds.FormatTime(now),
			Content: &opds.Text{Type: "text", Value: "Zeszyty w kolejności dodania do biblioteki"},
			Links:   []opds.Link{{Rel: opds.RelNew, Href: base + "/opds/recent", Type: opds.TypeAcquisition}},
		},
	}
	s.writeFeed(w, f, opds.TypeNavigation)
}

// progressFor fetches reading progress for the given issues in one query.
func (s *Server) progressFor(issues []store.Issue) (map[int64]store.ReadingProgress, error) {
	ids := make([]int64, len(issues))
	for i, is := range issues {
		ids[i] = is.ID
	}
	return s.store.ReadingProgressFor(ids)
}

// progressPtr returns the issue's progress from a batch map, or nil.
func progressPtr(m map[int64]store.ReadingProgress, id int64) *store.ReadingProgress {
	if p, ok := m[id]; ok {
		return &p
	}
	return nil
}

// handleOPDSReading serves issues started in a reader but not finished,
// most recently read first.
func (s *Server) handleOPDSReading(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListIssuesInProgress(100)
	if err != nil {
		s.serverError(w, err)
		return
	}
	base := opdsBaseURL(r)
	f := s.newOPDSFeed(r, "reading", "Aktualnie czytane", time.Now())
	f.Links[0].Type = opds.TypeAcquisition
	f.AddLink(opds.RelUp, base+"/opds", opds.TypeNavigation)
	f.TotalResults, f.ItemsPerPage, f.StartIndex = len(items), len(items), 1
	for _, it := range items {
		f.Entries = append(f.Entries, opdsIssueEntry(base, it.Issue, it.SeriesName, &it.Progress))
	}
	s.writeFeed(w, f, opds.TypeAcquisition)
}

// maxStreamWidth caps the width a reader may request for a streamed page.
const maxStreamWidth = 4000

// handleOPDSPage streams one page of an archive (OPDS-PSE): {page} is
// 0-based; ?width=N scales the image down to N pixels wide (JPEG). Fetching a
// page records reading progress, which is how streaming readers report it.
func (s *Server) handleOPDSPage(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}
	page, err := strconv.Atoi(r.PathValue("page"))
	if err != nil || page < 0 {
		http.NotFound(w, r)
		return
	}
	if !canStreamPages(issue.Path) {
		http.Error(w, "Strumieniowanie stron działa tylko dla archiwów CBZ/CBR.", http.StatusNotFound)
		return
	}
	if _, err := os.Stat(issue.Path); err != nil {
		http.Error(w, "Plik nie istnieje na dysku (oznaczony jako brakujący?).", http.StatusNotFound)
		return
	}

	data, name, err := library.ExtractPage(issue.Path, page)
	if errors.Is(err, library.ErrPageOutOfRange) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}

	// First streamed page of a file catalogued before page counting existed:
	// store the real count so feeds can advertise it.
	if issue.FilePages == 0 {
		if pages, err := library.ListPages(issue.Path); err == nil {
			if err := s.store.SetIssueFilePages(issue.ID, len(pages)); err != nil {
				log.Printf("opds: page count for issue %d: %v", issue.ID, err)
			}
		}
	}

	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if width, _ := strconv.Atoi(r.FormValue("width")); width > 0 {
		if width > maxStreamWidth {
			width = maxStreamWidth
		}
		if resized, err := covers.Resize(data, width); err == nil {
			data, contentType = resized, "image/jpeg"
		} else {
			log.Printf("opds: resize page %d of issue %d: %v (serving original)", page, issue.ID, err)
		}
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	if err := s.store.SetReadingProgress(issue.ID, page+1); err != nil {
		log.Printf("opds: progress for issue %d: %v", issue.ID, err)
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(data)
}

// canStreamPages reports whether pages can be extracted from the file
// (image archives only — PDF pages are not rendered).
func canStreamPages(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cbz", ".cbr":
		return true
	}
	return false
}

// handleOPDSSeriesList serves the paged navigation feed of all series.
func (s *Server) handleOPDSSeriesList(w http.ResponseWriter, r *http.Request) {
	series, err := s.store.ListSeries("", store.SeriesSortName, store.SeriesFilterAll)
	if err != nil {
		s.serverError(w, err)
		return
	}
	base := opdsBaseURL(r)
	page := opdsPage(r)
	from, to, hasNext := pageBounds(page, len(series))

	f := s.newOPDSFeed(r, "series", "Wszystkie serie", time.Now())
	f.Links[0].Type = opds.TypeNavigation
	f.AddLink(opds.RelUp, base+"/opds", opds.TypeNavigation)
	if hasNext {
		f.AddLink(opds.RelNext, fmt.Sprintf("%s/opds/series?page=%d", base, page+1), opds.TypeNavigation)
	}
	if page > 1 {
		f.AddLink(opds.RelPrevious, fmt.Sprintf("%s/opds/series?page=%d", base, page-1), opds.TypeNavigation)
	}
	f.TotalResults, f.ItemsPerPage, f.StartIndex = len(series), opdsPageSize, from+1

	for _, sr := range series[from:to] {
		f.Entries = append(f.Entries, s.opdsSeriesEntry(base, sr))
	}
	s.writeFeed(w, f, opds.TypeNavigation)
}

// opdsSeriesEntry builds a navigation entry pointing at a series' feed.
func (s *Server) opdsSeriesEntry(base string, sr store.Series) opds.Entry {
	id := strconv.FormatInt(sr.ID, 10)
	desc := pluralIssues(sr.IssueCount)
	if sr.OneShot {
		desc = "wydanie jednorazowe"
	}
	if sr.Publisher != "" {
		desc += " · " + sr.Publisher
	}
	e := opds.Entry{
		ID:        "urn:comicnest:series:" + id,
		Title:     sr.Name,
		Updated:   opds.FormatTime(opds.ParseDBTime(sr.UpdatedAt, time.Now())),
		Publisher: sr.Publisher,
		Content:   &opds.Text{Type: "text", Value: desc},
		Links: []opds.Link{
			{Rel: opds.RelSubsection, Href: base + "/opds/series/" + id, Type: opds.TypeAcquisition},
		},
	}
	if sr.CoverIssueID > 0 {
		cover := base + "/opds/issues/" + strconv.FormatInt(sr.CoverIssueID, 10) + "/cover"
		e.Links = append(e.Links,
			opds.Link{Rel: opds.RelImage, Href: cover, Type: "image/jpeg"},
			opds.Link{Rel: opds.RelThumbnail, Href: cover, Type: "image/jpeg"})
	}
	return e
}

// pluralIssues renders "N zeszytów" with Polish plural forms.
func pluralIssues(n int) string {
	switch {
	case n == 1:
		return "1 zeszyt"
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 10 || n%100 >= 20):
		return fmt.Sprintf("%d zeszyty", n)
	}
	return fmt.Sprintf("%d zeszytów", n)
}

// handleOPDSSeries serves the acquisition feed of one series' issues.
func (s *Server) handleOPDSSeries(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	series, err := s.store.GetSeries(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if series == nil {
		http.NotFound(w, r)
		return
	}
	all, err := s.store.ListIssuesBySeries(id, store.IssueFilterAll)
	if err != nil {
		s.serverError(w, err)
		return
	}
	// Missing files cannot be downloaded, so readers never see them.
	var issues []store.Issue
	for _, i := range all {
		if !i.FileMissing {
			issues = append(issues, i)
		}
	}

	base := opdsBaseURL(r)
	page := opdsPage(r)
	from, to, hasNext := pageBounds(page, len(issues))
	sid := strconv.FormatInt(id, 10)

	f := s.newOPDSFeed(r, "series:"+sid, series.Name,
		opds.ParseDBTime(series.UpdatedAt, time.Now()))
	f.Links[0].Type = opds.TypeAcquisition
	f.AddLink(opds.RelUp, base+"/opds/series", opds.TypeNavigation)
	if hasNext {
		f.AddLink(opds.RelNext, fmt.Sprintf("%s/opds/series/%s?page=%d", base, sid, page+1), opds.TypeAcquisition)
	}
	if page > 1 {
		f.AddLink(opds.RelPrevious, fmt.Sprintf("%s/opds/series/%s?page=%d", base, sid, page-1), opds.TypeAcquisition)
	}
	f.TotalResults, f.ItemsPerPage, f.StartIndex = len(issues), opdsPageSize, from+1

	progress, err := s.progressFor(issues[from:to])
	if err != nil {
		s.serverError(w, err)
		return
	}
	for _, i := range issues[from:to] {
		f.Entries = append(f.Entries, opdsIssueEntry(base, i, series.Name, progressPtr(progress, i.ID)))
	}
	s.writeFeed(w, f, opds.TypeAcquisition)
}

// handleOPDSRecent serves recently added issues, newest first.
func (s *Server) handleOPDSRecent(w http.ResponseWriter, r *http.Request) {
	total, err := s.store.CountIssues()
	if err != nil {
		s.serverError(w, err)
		return
	}
	page := opdsPage(r)
	from, _, hasNext := pageBounds(page, total)
	issues, err := s.store.ListRecentIssues(opdsPageSize, from)
	if err != nil {
		s.serverError(w, err)
		return
	}

	base := opdsBaseURL(r)
	f := s.newOPDSFeed(r, "recent", "Ostatnio dodane", time.Now())
	f.Links[0].Type = opds.TypeAcquisition
	f.AddLink(opds.RelUp, base+"/opds", opds.TypeNavigation)
	if hasNext {
		f.AddLink(opds.RelNext, fmt.Sprintf("%s/opds/recent?page=%d", base, page+1), opds.TypeAcquisition)
	}
	if page > 1 {
		f.AddLink(opds.RelPrevious, fmt.Sprintf("%s/opds/recent?page=%d", base, page-1), opds.TypeAcquisition)
	}
	f.TotalResults, f.ItemsPerPage, f.StartIndex = total, opdsPageSize, from+1

	if err := s.appendIssueEntries(f, base, issues); err != nil {
		s.serverError(w, err)
		return
	}
	s.writeFeed(w, f, opds.TypeAcquisition)
}

// appendIssueEntries adds publication entries (with reading progress) for
// issues joined with their series name.
func (s *Server) appendIssueEntries(f *opds.Feed, base string, issues []store.IssueWithSeries) error {
	ids := make([]int64, len(issues))
	for i, is := range issues {
		ids[i] = is.ID
	}
	progress, err := s.store.ReadingProgressFor(ids)
	if err != nil {
		return err
	}
	for _, i := range issues {
		f.Entries = append(f.Entries, opdsIssueEntry(base, i.Issue, i.SeriesName, progressPtr(progress, i.ID)))
	}
	return nil
}

// handleOPDSSearch serves search results as one flat acquisition feed.
func (s *Server) handleOPDSSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.FormValue("q"))
	base := opdsBaseURL(r)

	f := s.newOPDSFeed(r, "search", "Wyniki wyszukiwania: "+q, time.Now())
	f.Links[0].Type = opds.TypeAcquisition
	f.AddLink(opds.RelUp, base+"/opds", opds.TypeNavigation)

	if q != "" {
		issues, err := s.store.SearchIssuesBroad(q, 200)
		if err != nil {
			s.serverError(w, err)
			return
		}
		f.TotalResults, f.ItemsPerPage, f.StartIndex = len(issues), len(issues), 1
		if err := s.appendIssueEntries(f, base, issues); err != nil {
			s.serverError(w, err)
			return
		}
	}
	s.writeFeed(w, f, opds.TypeAcquisition)
}

// handleOPDSOpenSearch serves the OpenSearch description document.
func (s *Server) handleOPDSOpenSearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", opds.TypeOpenSearch+";charset=utf-8")
	if err := opds.WriteOpenSearch(w, opdsBaseURL(r)+"/opds/search?q={searchTerms}"); err != nil {
		s.serverError(w, err)
	}
}

// opdsIssueEntry builds a publication entry with acquisition, cover and
// (for archives) page-streaming links; progress may be nil.
func opdsIssueEntry(base string, i store.Issue, seriesName string, progress *store.ReadingProgress) opds.Entry {
	id := strconv.FormatInt(i.ID, 10)

	title := seriesName
	if i.IssueNumber != "" {
		title += " #" + i.IssueNumber
	}
	if i.Title != "" && !strings.EqualFold(i.Title, seriesName) {
		title += " – " + i.Title
	}
	if title == "" {
		title = filepath.Base(i.Path)
	}

	e := opds.Entry{
		ID:        "urn:comicnest:issue:" + id,
		Title:     title,
		Updated:   opds.FormatTime(opds.ParseDBTime(i.UpdatedAt, time.Now())),
		Publisher: i.Publisher,
		Issued:    i.ReleaseDate,
		Links: []opds.Link{
			{Rel: opds.RelAcquisition, Href: base + "/opds/issues/" + id + "/file",
				Type: comicMediaType(i.Path), Title: filepath.Base(i.Path)},
		},
	}
	for _, name := range splitCreators(i.Writer) {
		e.Authors = append(e.Authors, opds.Author{Name: name})
	}
	if i.Summary != "" {
		e.Summary = &opds.Text{Type: "text", Value: i.Summary}
	} else if i.Artist != "" {
		e.Summary = &opds.Text{Type: "text", Value: "Rysunki: " + i.Artist}
	}
	if i.CoverCached {
		cover := base + "/opds/issues/" + id + "/cover"
		e.Links = append(e.Links,
			opds.Link{Rel: opds.RelImage, Href: cover, Type: "image/jpeg"},
			opds.Link{Rel: opds.RelThumbnail, Href: cover, Type: "image/jpeg"})
	}
	if pages := i.TotalPages(); pages > 0 && canStreamPages(i.Path) {
		stream := opds.Link{
			Rel:       opds.RelPageStream,
			Href:      base + "/opds/issues/" + id + "/pages/" + opds.PlaceholderPage + "?width=" + opds.PlaceholderWidth,
			Type:      "image/jpeg",
			PageCount: pages,
		}
		if progress != nil {
			stream.LastRead = progress.Page
			stream.LastReadDate = opds.FormatTime(opds.ParseDBTime(progress.UpdatedAt, time.Now()))
		}
		e.Links = append(e.Links, stream)
	}
	return e
}

// splitCreators turns a "Name, Name" credit string into individual names.
func splitCreators(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// comicMediaType maps a comic file extension to its media type. Readers use
// it to decide whether they can open the download at all.
func comicMediaType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cbz":
		return opds.TypeCBZ
	case ".cbr":
		return opds.TypeCBR
	case ".pdf":
		return opds.TypePDF
	}
	return "application/octet-stream"
}
