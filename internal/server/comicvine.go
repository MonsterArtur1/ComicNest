package server

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"comicnest/internal/comicvine"
	"comicnest/internal/store"
)

// errNoMatch means the issue's number was not found in the matched volume.
var errNoMatch = errors.New("comicvine: issue number not found in volume")

// scrapeStatus is a snapshot of the single, global series-scrape job.
type scrapeStatus struct {
	Running  bool
	SeriesID int64
	Done     int
	Total    int
	Updated  int
	Skipped  int
	Failed   int
	Finished bool
	Err      string
}

type scrapeJob struct {
	mu sync.Mutex
	st scrapeStatus
}

func (j *scrapeJob) status() scrapeStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st
}

func (j *scrapeJob) update(fn func(*scrapeStatus)) {
	j.mu.Lock()
	fn(&j.st)
	j.mu.Unlock()
}

// tryStart atomically claims the job for a series; false when one is running.
func (j *scrapeJob) tryStart(seriesID int64) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.st.Running {
		return false
	}
	j.st = scrapeStatus{Running: true, SeriesID: seriesID}
	return true
}

// scrapeStatusData feeds the scrape-status partial for one series page.
type scrapeStatusData struct {
	scrapeStatus
	CVEnabled bool
	Matched   bool
}

func (s *Server) scrapeDataFor(series *store.Series) scrapeStatusData {
	st := s.scrape.status()
	if st.SeriesID != series.ID {
		st = scrapeStatus{} // another series' job — show nothing here
	}
	st.SeriesID = series.ID
	return scrapeStatusData{
		scrapeStatus: st,
		CVEnabled:    s.cv.Enabled(),
		Matched:      series.ComicVineVolumeID.Valid,
	}
}

// handleScrapeStatus renders the ComicVine action strip on a series page.
func (s *Server) handleScrapeStatus(w http.ResponseWriter, r *http.Request) {
	series := s.getSeriesFromPath(w, r)
	if series == nil {
		return
	}
	s.renderPartial(w, "scrape_status.html", "scrape-status", s.scrapeDataFor(series))
}

// handleSeriesScrape updates all of a series' issues that still lack
// ComicVine metadata, in the background (one job at a time).
func (s *Server) handleSeriesScrape(w http.ResponseWriter, r *http.Request) {
	series := s.getSeriesFromPath(w, r)
	if series == nil {
		return
	}

	if s.cv.Enabled() && series.ComicVineVolumeID.Valid && s.scrape.tryStart(series.ID) {
		go s.runSeriesScrape(series.ID, series.ComicVineVolumeID.Int64)
	}

	if r.Header.Get("HX-Request") == "true" {
		s.renderPartial(w, "scrape_status.html", "scrape-status", s.scrapeDataFor(series))
		return
	}
	http.Redirect(w, r, "/series/"+strconv.FormatInt(series.ID, 10), http.StatusSeeOther)
}

func (s *Server) runSeriesScrape(seriesID, volumeID int64) {
	finish := func(err string) {
		s.scrape.update(func(st *scrapeStatus) {
			st.Running = false
			st.Finished = true
			st.Err = err
		})
	}

	issues, err := s.store.ListIssuesBySeries(seriesID, store.IssueFilterAll)
	if err != nil {
		finish(err.Error())
		return
	}

	var todo []store.Issue
	skipped := 0
	for _, i := range issues {
		if i.MetadataLocked || i.MetadataSource == store.SourceComicVine {
			skipped++
			continue
		}
		todo = append(todo, i)
	}
	s.scrape.update(func(st *scrapeStatus) {
		st.Total = len(todo)
		st.Skipped = skipped
	})
	if len(todo) == 0 {
		finish("")
		return
	}

	vol, volIssues, err := s.cv.GetVolume(int(volumeID))
	if err != nil {
		finish(err.Error())
		return
	}
	// A series scrape also refreshes the series' own title/metadata.
	if err := s.store.EnrichSeriesFromComicVine(seriesID, vol.Name, vol.Publisher, vol.Description); err != nil {
		log.Printf("comicvine: enrich series %d: %v", seriesID, err)
	}
	if err := s.store.SetSeriesOneShot(seriesID, vol.CountOfIssues == 1); err != nil {
		log.Printf("comicvine: one-shot flag %d: %v", seriesID, err)
	}

	for _, issue := range todo {
		if err := s.scrapeOneIssue(&issue, volIssues); err != nil {
			log.Printf("comicvine: %s: %v", issue.Path, err)
			s.scrape.update(func(st *scrapeStatus) { st.Failed++ })
		} else {
			s.scrape.update(func(st *scrapeStatus) { st.Updated++ })
		}
		s.scrape.update(func(st *scrapeStatus) { st.Done++ })
	}
	finish("")
}

// scrapeOneIssue matches an issue by number inside the volume's issue list,
// fetches its details and stores them (plus the ComicVine cover).
func (s *Server) scrapeOneIssue(issue *store.Issue, volIssues []comicvine.VolumeIssue) error {
	cvID := 0
	want := normalizeIssueNumber(issue.IssueNumber)
	for _, vi := range volIssues {
		if normalizeIssueNumber(vi.IssueNumber) == want && want != "" {
			cvID = vi.ID
			break
		}
	}
	// One-shots have no number in the file name, but the matched volume has
	// exactly one issue — that has to be the one.
	if cvID == 0 && want == "" && len(volIssues) == 1 {
		cvID = volIssues[0].ID
	}
	if cvID == 0 {
		return errNoMatch
	}

	cvIssue, err := s.cv.GetIssue(cvID)
	if err != nil {
		return err
	}
	applyComicVine(issue, cvIssue)
	if err := s.store.UpdateIssueMetadata(issue); err != nil {
		return err
	}

	if cvIssue.ImageURL != "" {
		if raw, err := s.cv.DownloadImage(cvIssue.ImageURL); err != nil {
			log.Printf("comicvine: cover %s: %v", issue.Path, err)
		} else if err := s.covers.Save(issue.ID, raw); err != nil {
			log.Printf("comicvine: thumbnail %s: %v", issue.Path, err)
		} else if err := s.store.SetCoverCached(issue.ID, true); err != nil {
			log.Printf("comicvine: cover flag %s: %v", issue.Path, err)
		}
	}
	return nil
}

// handleIssueScrape updates a single issue from ComicVine, synchronously.
func (s *Server) handleIssueScrape(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}
	back := func(msg string) {
		http.Redirect(w, r, "/issues/"+strconv.FormatInt(issue.ID, 10)+"?msg="+msg, http.StatusSeeOther)
	}

	if !s.cv.Enabled() {
		back("cv_nokey")
		return
	}
	if issue.MetadataLocked {
		back("cv_locked")
		return
	}
	series, err := s.store.GetSeries(issue.SeriesID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !series.ComicVineVolumeID.Valid {
		back("cv_novolume")
		return
	}

	_, volIssues, err := s.cv.GetVolume(int(series.ComicVineVolumeID.Int64))
	if err != nil {
		log.Printf("comicvine: volume for %s: %v", issue.Path, err)
		back("cv_error")
		return
	}
	switch err := s.scrapeOneIssue(issue, volIssues); {
	case err == errNoMatch:
		back("cv_nomatch")
	case err != nil:
		log.Printf("comicvine: %s: %v", issue.Path, err)
		back("cv_error")
	default:
		back("cv_ok")
	}
}

// --- volume matching ---

type matchData struct {
	Series     *store.Series
	Query      string
	Candidates []comicvine.Volume
	Error      string
}

// handleMatchPage searches ComicVine volumes; without an explicit query it
// searches for the series name right away.
func (s *Server) handleMatchPage(w http.ResponseWriter, r *http.Request) {
	series := s.getSeriesFromPath(w, r)
	if series == nil {
		return
	}

	data := matchData{Series: series, Query: strings.TrimSpace(r.FormValue("q"))}
	if data.Query == "" {
		data.Query = series.Name
	}

	if !s.cv.Enabled() {
		data.Error = "Brak klucza API — ustaw comicvine_api_key w config.yaml."
	} else {
		var err error
		data.Candidates, err = s.cv.SearchVolumes(data.Query)
		if err != nil {
			log.Printf("comicvine: search %q: %v", data.Query, err)
			data.Error = "Wyszukiwanie w ComicVine nie powiodło się: " + err.Error()
		}
	}
	s.render(w, "comicvine_match.html", data)
}

// handleMatchSave stores the chosen volume and enriches empty series fields.
func (s *Server) handleMatchSave(w http.ResponseWriter, r *http.Request) {
	series := s.getSeriesFromPath(w, r)
	if series == nil {
		return
	}
	volumeID, err := strconv.ParseInt(r.PathValue("volumeID"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}

	if err := s.store.SetSeriesComicVineVolume(series.ID, volumeID); err != nil {
		s.serverError(w, err)
		return
	}
	if vol, _, err := s.cv.GetVolume(int(volumeID)); err != nil {
		log.Printf("comicvine: enrich series %d: %v", series.ID, err)
	} else {
		if err := s.store.EnrichSeriesFromComicVine(series.ID, vol.Name, vol.Publisher, vol.Description); err != nil {
			log.Printf("comicvine: enrich series %d: %v", series.ID, err)
		}
		if err := s.store.SetSeriesOneShot(series.ID, vol.CountOfIssues == 1); err != nil {
			log.Printf("comicvine: one-shot flag %d: %v", series.ID, err)
		}
	}
	http.Redirect(w, r, "/series/"+strconv.FormatInt(series.ID, 10), http.StatusSeeOther)
}

// --- helpers ---

// applyComicVine overrides issue fields with non-empty ComicVine values.
func applyComicVine(issue *store.Issue, cv *comicvine.Issue) {
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&issue.IssueNumber, cv.IssueNumber)
	set(&issue.Title, cv.Name)
	set(&issue.Summary, cv.Description)
	if cv.StoreDate != "" {
		issue.ReleaseDate = cv.StoreDate
	} else {
		set(&issue.ReleaseDate, cv.CoverDate)
	}
	set(&issue.Writer, cv.Writers)
	set(&issue.Artist, cv.Artists)
	issue.ComicVineIssueID = sql.NullInt64{Int64: int64(cv.ID), Valid: true}
	issue.MetadataSource = store.SourceComicVine
}

// normalizeIssueNumber makes "012", "12" and "12 " compare equal.
func normalizeIssueNumber(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	trimmed := strings.TrimLeft(s, "0")
	if trimmed == "" && s != "" {
		return "0"
	}
	return trimmed
}
