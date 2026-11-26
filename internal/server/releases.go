package server

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"comicnest/internal/comicvine"
	"comicnest/internal/store"
)

// Window of store dates the release check fetches and the pages show.
const (
	releasesPast   = 21 // days back fetched (covers "last week" in full)
	releasesFuture = 60 // days ahead fetched ("upcoming")
	homePast       = 14 // home page section: days back …
	homeFuture     = 14 // … and ahead
	homeMaxRows    = 12
	releaseEvery   = 24 * time.Hour // the scan phase checks at most this often
	syncPerScan    = 40             // volumes whose issue list one scan phase may refresh
)

const dateLayout = "2006-01-02"

// releaseStatus is a snapshot of the single, global release-check job.
type releaseStatus struct {
	Running   bool
	Finished  bool
	Err       string
	Volumes   int // followed volumes asked about
	Releases  int // issues in the answer
	CheckedAt string
}

type releaseJob struct {
	mu sync.Mutex
	st releaseStatus
}

func (j *releaseJob) status() releaseStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st
}

// tryStart atomically claims the job; false when a check is already running.
func (j *releaseJob) tryStart() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.st.Running {
		return false
	}
	j.st = releaseStatus{Running: true, CheckedAt: j.st.CheckedAt}
	return true
}

func (j *releaseJob) finish(volumes, releases int, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.st.Running = false
	j.st.Finished = true
	j.st.Volumes, j.st.Releases = volumes, releases
	if err != nil {
		j.st.Err = err.Error()
	} else {
		j.st.CheckedAt = time.Now().Format("2006-01-02 15:04")
	}
}

// today is the current UTC date as "YYYY-MM-DD" (ComicVine store dates carry
// no zone).
func today() string { return time.Now().UTC().Format(dateLayout) }

func daysFrom(base time.Time, n int) string { return base.AddDate(0, 0, n).Format(dateLayout) }

// --- ComicVine data sync ---

// syncVolume fetches a volume's complete issue list (with dates) into the cache.
func (s *Server) syncVolume(volumeID int64) error {
	listed, err := s.cv.GetVolumeIssues(int(volumeID))
	if err != nil {
		return err
	}
	return s.store.UpsertVolumeIssues(toCVIssues(listed, volumeID), volumeID, true)
}

// syncVolumeIfStale is syncVolume unless the list was fetched less than
// maxAge ago.
func (s *Server) syncVolumeIfStale(volumeID int64, maxAge time.Duration) error {
	if fresh, err := s.store.VolumeSyncedWithin(volumeID, maxAge); err == nil && fresh {
		return nil
	}
	return s.syncVolume(volumeID)
}

// refreshVolumeCache is the best-effort refresh after a match or a scrape: an
// hour-old list is fresh enough, failures are only logged.
func (s *Server) refreshVolumeCache(volumeID int64) {
	if err := s.syncVolumeIfStale(volumeID, time.Hour); err != nil {
		log.Printf("comicvine: cache volume %d issue list: %v", volumeID, err)
	}
}

func toCVIssues(listed []comicvine.ListedIssue, defaultVolume int64) []store.CVIssue {
	out := make([]store.CVIssue, 0, len(listed))
	for _, l := range listed {
		vol := int64(l.VolumeID)
		if vol == 0 {
			vol = defaultVolume
		}
		out = append(out, store.CVIssue{
			VolumeID: vol, CVIssueID: int64(l.ID), IssueNumber: l.IssueNumber, Name: l.Name,
			CoverDate: l.CoverDate, StoreDate: l.StoreDate, URL: l.URL,
		})
	}
	return out
}

// syncCVData is the scan's ComicVine bookkeeping phase: refresh the issue
// lists of matched series whose cache is missing or older than a week (at
// most syncPerScan volumes per scan — the rest follow on later scans, since
// every request waits out the client's rate limit), then the daily release
// check.
func (s *Server) syncCVData() {
	if !s.cv.Enabled() {
		return
	}
	ids, err := s.store.StaleVolumeIDs(0, syncPerScan)
	if err != nil {
		log.Printf("comicvine: stale volumes: %v", err)
	}
	for _, id := range ids {
		if err := s.syncVolume(id); err != nil {
			log.Printf("comicvine: sync volume %d: %v", id, err)
		}
	}

	last, ok, err := s.store.MetaTime(store.MetaReleasesChecked)
	if err != nil {
		log.Printf("comicvine: release stamp: %v", err)
	}
	if ok && time.Since(last) < releaseEvery {
		return
	}
	if s.releases.tryStart() {
		s.runReleaseCheck()
	}
}

// runReleaseCheck asks ComicVine for recent/upcoming issues of every followed
// volume and caches them. The caller must hold the job (tryStart).
func (s *Server) runReleaseCheck() {
	volumes, err := s.store.FollowedVolumeIDs()
	if err != nil {
		s.releases.finish(0, 0, err)
		return
	}
	if len(volumes) == 0 {
		s.stampReleaseCheck()
		s.releases.finish(0, 0, nil)
		return
	}
	ids := make([]int, len(volumes))
	for i, v := range volumes {
		ids[i] = int(v)
	}
	now := time.Now().UTC()
	listed, err := s.cv.RecentIssues(ids, daysFrom(now, -releasesPast), daysFrom(now, releasesFuture))
	// Keep whatever arrived before an error; only a clean run stamps the check.
	if len(listed) > 0 {
		if uerr := s.store.UpsertVolumeIssues(toCVIssues(listed, 0), 0, false); uerr != nil && err == nil {
			err = uerr
		}
	}
	if err != nil {
		log.Printf("comicvine: release check: %v", err)
		s.releases.finish(len(volumes), len(listed), err)
		return
	}
	s.stampReleaseCheck()
	s.releases.finish(len(volumes), len(listed), nil)
}

func (s *Server) stampReleaseCheck() {
	if err := s.store.SetMeta(store.MetaReleasesChecked, store.MetaTimeString(time.Now())); err != nil {
		log.Printf("comicvine: release stamp: %v", err)
	}
}

// handleReleasesCheck starts the background release check (admin only).
func (s *Server) handleReleasesCheck(w http.ResponseWriter, r *http.Request) {
	if s.cv.Enabled() && s.releases.tryStart() {
		go s.runReleaseCheck()
	}
	next := safeNext(r.FormValue("next"))
	if next == "/" {
		next = "/releases"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// --- views ---

// releaseRow is a release prepared for display.
type releaseRow struct {
	store.Release
	DateLabel string // "Wed, Nov 26"
	Upcoming  bool
}

func (s *Server) releaseRows(rs []store.Release, todayStr string) []releaseRow {
	out := make([]releaseRow, 0, len(rs))
	for _, r := range rs {
		row := releaseRow{Release: r, DateLabel: r.StoreDate, Upcoming: r.StoreDate > todayStr}
		if t, err := time.Parse(dateLayout, r.StoreDate); err == nil {
			row.DateLabel = t.Format("Mon, Jan 2")
		}
		out = append(out, row)
	}
	return out
}

// homeReleases loads the home page's "New releases" strip: followed series'
// issues from the last two weeks and the next two. nil without a ComicVine
// key or any release.
func (s *Server) homeReleases(r *http.Request) []releaseRow {
	if !s.cv.Enabled() {
		return nil
	}
	now := time.Now().UTC()
	rs, err := s.store.ListReleases(userFrom(r), daysFrom(now, -homePast), daysFrom(now, homeFuture), s.selectedLibrary(r))
	if err != nil {
		log.Printf("releases: home: %v", err)
		return nil
	}
	rows := s.releaseRows(rs, today())
	// Newest first, capped.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	if len(rows) > homeMaxRows {
		rows = rows[:homeMaxRows]
	}
	return rows
}

type releasesData struct {
	CVEnabled bool
	Following bool // the user follows at least one matched series
	ThisWeek  []releaseRow
	LastWeek  []releaseRow
	Upcoming  []releaseRow
	Status    releaseStatus
	LastCheck string // last successful check ("" = never)
}

func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	data := releasesData{CVEnabled: s.cv.Enabled(), Status: s.releases.status()}
	if data.CVEnabled {
		user, lib := userFrom(r), s.selectedLibrary(r)
		now := time.Now().UTC()
		wd := (int(now.Weekday()) + 6) % 7 // Monday-first, like /stats
		monday := now.AddDate(0, 0, -wd)
		todayStr := today()
		load := func(from, to string) []releaseRow {
			rs, err := s.store.ListReleases(user, from, to, lib)
			if err != nil {
				log.Printf("releases: %v", err)
				return nil
			}
			return s.releaseRows(rs, todayStr)
		}
		data.ThisWeek = load(daysFrom(monday, 0), daysFrom(monday, 6))
		data.LastWeek = load(daysFrom(monday, -7), daysFrom(monday, -1))
		data.Upcoming = load(daysFrom(monday, 7), daysFrom(now, releasesFuture))
		if t, ok, _ := s.store.MetaTime(store.MetaReleasesChecked); ok {
			data.LastCheck = t.Local().Format("2006-01-02 15:04")
		}
		data.Following = len(data.ThisWeek)+len(data.LastWeek)+len(data.Upcoming) > 0
		if !data.Following {
			ids, err := s.store.FollowedVolumeIDs()
			data.Following = err == nil && len(ids) > 0
		}
	}
	s.render(w, r, "releases.html", data)
}

// --- following ---

type followView struct {
	CanFollow bool // series is matched to a ComicVine volume
	Following bool
	Explicit  bool
}

func (s *Server) followViewFor(r *http.Request, series *store.Series) followView {
	if !series.ComicVineVolumeID.Valid {
		return followView{}
	}
	following, explicit, err := s.store.FollowState(userFrom(r), series.ID)
	if err != nil {
		log.Printf("follow state %d: %v", series.ID, err)
	}
	return followView{CanFollow: true, Following: following, Explicit: explicit}
}

// handleSeriesFollow toggles the user's follow state of a series (an explicit
// choice that overrides the "started reading" rule).
func (s *Server) handleSeriesFollow(w http.ResponseWriter, r *http.Request) {
	series := s.getSeriesFromPath(w, r)
	if series == nil {
		return
	}
	if series.ComicVineVolumeID.Valid {
		user := userFrom(r)
		following, _, err := s.store.FollowState(user, series.ID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if err := s.store.SetSeriesFollow(user, series.ID, !following); err != nil {
			s.serverError(w, err)
			return
		}
	}
	back := "/series/" + strconv.FormatInt(series.ID, 10)
	if n := safeNext(r.FormValue("next")); n != "/" {
		back = n
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// releaseStatusText is a one-line summary for the admin panel / releases page.
func (st releaseStatus) Summary() string {
	switch {
	case st.Running:
		return "Checking ComicVine for new releases…"
	case st.Err != "":
		return "Release check failed: " + st.Err
	case st.Finished:
		return fmt.Sprintf("Checked %d followed volume(s), %d issue(s) in the window.", st.Volumes, st.Releases)
	}
	return ""
}
