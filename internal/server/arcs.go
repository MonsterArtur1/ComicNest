package server

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"comicnest/internal/comicvine"
	"comicnest/internal/store"
)

// Story arcs span every library (like the stats page): an event rarely lives
// in one folder, and the library switcher only scopes the library grid and
// search. Read state is the requesting user's.

// arcMaxAgeDays is how long a fetched ComicVine arc issue list is trusted
// before a scan's ComicVine phase fetches it again (an admin can refresh one
// by hand any time).
const arcMaxAgeDays = 30

// arcRetryAfter keeps a failing arc fetch from being repeated for every issue
// of the same scrape or scan.
const arcRetryAfter = 10 * time.Minute

// arcFailures remembers recent failed arc fetches. The zero value is ready.
type arcFailures struct {
	mu sync.Mutex
	at map[int64]time.Time
}

func (f *arcFailures) recently(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.at[id]
	return ok && time.Since(t) < arcRetryAfter
}

func (f *arcFailures) mark(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.at == nil {
		f.at = make(map[int64]time.Time)
	}
	f.at[id] = time.Now()
}

// fetchArc downloads a ComicVine arc (details plus its full issue list, owned
// or not) and stores it.
func (s *Server) fetchArc(arc *store.Arc) error {
	cva, err := s.cv.GetStoryArc(int(arc.ComicVineID.Int64))
	if err != nil {
		return err
	}
	if cva.EnrichErr != nil {
		log.Printf("comicvine: arc %d: issue details incomplete: %v", arc.ComicVineID.Int64, cva.EnrichErr)
	}
	issues := make([]store.ArcCVIssue, 0, len(cva.Issues))
	for _, is := range cva.Issues {
		issues = append(issues, store.ArcCVIssue{
			ComicVineIssueID: int64(is.ID),
			VolumeID:         int64(is.VolumeID),
			VolumeName:       is.VolumeName,
			IssueNumber:      is.IssueNumber,
			Name:             is.Name,
			CoverDate:        is.CoverDate,
		})
	}
	return s.store.SaveArcCV(arc.ID, store.ArcDetails{
		Name:        cva.Name,
		Description: cva.Description,
		Publisher:   cva.Publisher,
		ImageURL:    cva.ImageURL,
		URL:         cva.URL,
	}, store.OrderArcCVIssues(issues))
}

// fetchArcs fetches the given arcs when they are ComicVine arcs not yet
// fetched (force: regardless), skipping ones that failed a moment ago.
func (s *Server) fetchArcs(ids []int64, force bool) {
	for _, id := range ids {
		arc, err := s.store.GetArc(id)
		if err != nil || arc == nil || !arc.ComicVineID.Valid || (arc.FetchedAt.Valid && !force) {
			continue
		}
		if s.arcFails.recently(id) {
			continue
		}
		if err := s.fetchArc(arc); err != nil {
			log.Printf("comicvine: arc %q: %v", arc.Name, err)
			s.arcFails.mark(id)
		}
	}
}

// saveComicVineExtras stores an issue's ComicVine character and story arc
// credits and fetches any arc not seen before. Credits are relational extras,
// not the metadata fields metadata_locked protects, so callers need not check
// the lock (the scrape paths do anyway; the scan's backfill does not).
func (s *Server) saveComicVineExtras(issueID int64, cv *comicvine.Issue) {
	characters := make([]store.CharacterInput, len(cv.Characters))
	for i, c := range cv.Characters {
		characters[i] = store.CharacterInput{Name: c.Name, ComicVineID: c.ID}
	}
	arcs := make([]store.ArcInput, len(cv.StoryArcs))
	for i, a := range cv.StoryArcs {
		arcs[i] = store.ArcInput{Name: a.Name, ComicVineID: a.ID}
	}
	ids, err := s.store.ReplaceComicVineExtras(issueID, characters, arcs)
	if err != nil {
		log.Printf("comicvine: saving characters/arcs of issue %d: %v", issueID, err)
		return
	}
	s.fetchArcs(ids, false)
}

// autoScrapeCV is the scan's ComicVine phase: first the existing step (issues
// of matched series that lack ComicVine metadata get scraped), then the
// backfill of characters/arcs for issues scraped before this feature existed,
// and a refresh of arc issue lists older than arcMaxAgeDays.
func (s *Server) autoScrapeCV(progress func(done, total, updated, failed int)) {
	if !s.cv.Enabled() {
		return
	}
	var last [4]int // done, total, updated, failed of the first step
	s.scrapeMissingCV(func(done, total, updated, failed int) {
		last = [4]int{done, total, updated, failed}
		progress(done, total, updated, failed)
	})
	s.backfillComicVineExtras(last, progress)
	s.syncCVData() // issue-list cache for gap detection + the daily release check
}

// backfillComicVineExtras fetches characters and story arcs for every issue
// that has a comicvine_issue_id but no credits recorded yet (rate-limited by
// the client, one request per issue; arcs once each). Issues it handles count
// as done, never as updated — their metadata is untouched, so locked fields
// stay as they are. A failed fetch leaves the issue pending for the next scan.
func (s *Server) backfillComicVineExtras(base [4]int, progress func(done, total, updated, failed int)) {
	todo, err := s.store.IssuesNeedingCVExtras()
	if err != nil {
		log.Printf("comicvine: backfill query: %v", err)
		return
	}
	done, total, updated, failed := base[0], base[1]+len(todo), base[2], base[3]
	if len(todo) > 0 {
		log.Printf("comicvine: fetching characters and story arcs for %d issue(s)", len(todo))
		progress(done, total, updated, failed)
	}
	for _, it := range todo {
		cv, err := s.cv.GetIssue(int(it.ComicVineIssueID))
		if err != nil {
			log.Printf("comicvine: backfill %s: %v", it.Path, err)
			failed++
		} else {
			s.saveComicVineExtras(it.ID, cv)
		}
		done++
		progress(done, total, updated, failed)
	}

	stale, err := s.store.StaleArcs(arcMaxAgeDays)
	if err != nil {
		log.Printf("comicvine: stale arcs query: %v", err)
		return
	}
	ids := make([]int64, len(stale))
	for i, a := range stale {
		ids[i] = a.ID
	}
	s.fetchArcs(ids, true)
}

// --- pages ---

// arcCard is one tile of the arcs index.
type arcCard struct {
	store.ArcSummary
	ReadPct int
}

type arcsPageData struct {
	Arcs   []arcCard
	Filter string
}

// handleArcs serves GET /arcs: every arc with owned issues, with the user's
// progress; ?q= filters by name.
func (s *Server) handleArcs(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListArcs(userFrom(r))
	if err != nil {
		s.serverError(w, err)
		return
	}
	filter := strings.TrimSpace(r.URL.Query().Get("q"))
	data := arcsPageData{Filter: filter}
	for _, a := range list {
		if filter != "" && !strings.Contains(strings.ToLower(a.Name), strings.ToLower(filter)) {
			continue
		}
		card := arcCard{ArcSummary: a}
		if a.Owned > 0 {
			card.ReadPct = a.Read * 100 / a.Owned
		}
		data.Arcs = append(data.Arcs, card)
	}
	s.render(w, r, "arcs.html", data)
}

// arcEntryView is one line of an arc's reading order.
type arcEntryView struct {
	store.ArcEntry
	Reading *readingView // owned entries; nil = unread
	CanRead bool         // the in-browser reader can open it
}

// Finished reports whether the user has read the (owned) entry to the end.
func (e arcEntryView) Finished() bool { return e.Reading != nil && e.Reading.Finished }

// ReadHref is where "read" leads for an owned entry: the reader when it can
// open the file, else the issue page.
func (e arcEntryView) ReadHref() string {
	id := strconv.FormatInt(e.Issue.ID, 10)
	if e.CanRead {
		return "/issues/" + id + "/read"
	}
	return "/issues/" + id
}

type arcPageData struct {
	Arc     *store.Arc
	Entries []arcEntryView
	Owned   int
	Total   int
	Read    int
	// Next is the first owned issue the user has not finished (resuming it
	// when started) — the target of the "Continue reading" button; nil when
	// everything owned is read.
	Next      *arcEntryView
	Started   bool // the user has read or begun something in this arc
	CVEnabled bool
	Msg       string
	MsgError  bool
}

var arcFlashMessages = map[string]struct {
	text  string
	isErr bool
}{
	"refreshed": {"Reading order refreshed from ComicVine.", false},
	"nokey":     {"No ComicVine API key in config.yaml.", true},
	"nolink":    {"This arc has no ComicVine counterpart to refresh from (it comes from ComicInfo only).", true},
	"fetchfail": {"The ComicVine update failed — see the server log for details.", true},
}

func (s *Server) getArcFromPath(w http.ResponseWriter, r *http.Request) *store.Arc {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return nil
	}
	arc, err := s.store.GetArc(id)
	if err != nil {
		s.serverError(w, err)
		return nil
	}
	if arc == nil {
		s.notFound(w, r)
		return nil
	}
	return arc
}

// handleArc serves GET /arcs/{id}: the reading order, owned and missing
// entries, counts and the continue-reading button.
func (s *Server) handleArc(w http.ResponseWriter, r *http.Request) {
	arc := s.getArcFromPath(w, r)
	if arc == nil {
		return
	}
	entries, err := s.store.ArcEntries(arc.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}

	var ids []int64
	for _, e := range entries {
		if e.Owned {
			ids = append(ids, e.Issue.ID)
		}
	}
	progress, err := s.store.ReadingProgressFor(userFrom(r), ids)
	if err != nil {
		s.serverError(w, err)
		return
	}

	data := arcPageData{Arc: arc, Total: len(entries), CVEnabled: s.cv.Enabled()}
	data.Entries = make([]arcEntryView, len(entries))
	for i, e := range entries {
		v := arcEntryView{ArcEntry: e}
		if e.Owned {
			data.Owned++
			v.CanRead = canStreamPages(e.Issue.Path)
			v.Reading = newReadingView(&e.Issue.Issue, progressPtr(progress, e.Issue.ID))
			if v.Finished() {
				data.Read++
			}
			if v.Reading != nil {
				data.Started = true
			}
		}
		data.Entries[i] = v
	}
	for i := range data.Entries {
		if e := &data.Entries[i]; e.Owned && !e.Finished() {
			data.Next = e
			break
		}
	}
	if flash, ok := arcFlashMessages[r.FormValue("msg")]; ok {
		data.Msg, data.MsgError = flash.text, flash.isErr
	}
	s.render(w, r, "arc.html", data)
}

// handleArcRefresh re-fetches an arc from ComicVine (admin only): its details
// and the full issue list, replacing what was stored.
func (s *Server) handleArcRefresh(w http.ResponseWriter, r *http.Request) {
	arc := s.getArcFromPath(w, r)
	if arc == nil {
		return
	}
	back := func(msg string) {
		http.Redirect(w, r, "/arcs/"+strconv.FormatInt(arc.ID, 10)+"?msg="+msg, http.StatusSeeOther)
	}
	switch {
	case !s.cv.Enabled():
		back("nokey")
	case !arc.ComicVineID.Valid:
		back("nolink")
	default:
		if err := s.fetchArc(arc); err != nil {
			log.Printf("comicvine: refresh arc %q: %v", arc.Name, err)
			s.arcFails.mark(arc.ID)
			back("fetchfail")
			return
		}
		back("refreshed")
	}
}

// arcLink is an arc an issue belongs to, for the issue page.
type arcLink struct {
	store.ArcPosition
}

// issueExtras is what the issue page shows besides the metadata table:
// arcs (with the place in each), characters and linked credits.
type issueExtras struct {
	Arcs       []arcLink
	Characters []store.Character
	Writers    []creatorLink
	Artists    []creatorLink
}

func (s *Server) issueExtras(issue *store.Issue) (issueExtras, error) {
	var x issueExtras
	positions, err := s.store.ArcPositionsOfIssue(issue.ID)
	if err != nil {
		return x, err
	}
	for _, p := range positions {
		x.Arcs = append(x.Arcs, arcLink{p})
	}
	if x.Characters, err = s.store.IssueCharacters(issue.ID); err != nil {
		return x, err
	}
	x.Writers = creatorLinks(issue.Writer)
	x.Artists = creatorLinks(issue.Artist)
	return x, nil
}
