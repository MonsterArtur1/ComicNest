package library

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"comicnest/internal/covers"
	"comicnest/internal/store"
)

// ErrScanRunning is returned by Start while a scan is already in progress.
var ErrScanRunning = errors.New("scan already running")

// Status is a snapshot of scanner progress, safe to render in the UI.
type Status struct {
	Running    bool
	Found      int // comic files discovered by the walk
	Processed  int // files handled so far
	Missing    int // records whose file disappeared (set when the scan ends)
	Finished   bool // at least one scan completed since the app started
	Err        string
	StartedAt  time.Time
	FinishedAt time.Time

	// ComicVine follow-up phase (issues of matched series that still lack
	// ComicVine metadata are scraped right after the file scan).
	CVPhase   bool // currently in the ComicVine phase
	CVDone    int
	CVTotal   int
	CVUpdated int
	CVFailed  int
}

// CVUpdater runs after a successful scan and reports its progress through
// the callback (done, total, updated, failed).
type CVUpdater func(progress func(done, total, updated, failed int))

// Scanner walks the library folder and syncs its contents with the store.
// Only one scan runs at a time; progress is queryable via Status.
type Scanner struct {
	store    *store.Store
	covers   *covers.Cache
	root     string
	cvUpdate CVUpdater // optional, wired by the server

	mu     sync.Mutex
	status Status
}

// SetCVUpdater installs the post-scan ComicVine step (nil disables it).
func (sc *Scanner) SetCVUpdater(fn CVUpdater) {
	sc.cvUpdate = fn
}

func NewScanner(st *store.Store, cv *covers.Cache, libraryRoot string) *Scanner {
	return &Scanner{store: st, covers: cv, root: libraryRoot}
}

// Status returns a copy of the current scan state.
func (sc *Scanner) Status() Status {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.status
}

// Start launches a scan in the background. The returned error only covers
// preconditions; scan failures are reported through Status().Err.
func (sc *Scanner) Start() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.status.Running {
		return ErrScanRunning
	}
	if strings.TrimSpace(sc.root) == "" {
		sc.status.Err = "ścieżka biblioteki nie jest ustawiona w config.yaml"
		return errors.New(sc.status.Err)
	}
	if info, err := os.Stat(sc.root); err != nil || !info.IsDir() {
		sc.status.Err = "folder biblioteki nie istnieje: " + sc.root
		return errors.New(sc.status.Err)
	}

	sc.status = Status{Running: true, StartedAt: time.Now()}
	go sc.run()
	return nil
}

func (sc *Scanner) setStatus(update func(*Status)) {
	sc.mu.Lock()
	update(&sc.status)
	sc.mu.Unlock()
}

type foundFile struct {
	path string
	size int64
}

func (sc *Scanner) run() {
	err := sc.scan()

	if err == nil && sc.cvUpdate != nil {
		sc.setStatus(func(st *Status) { st.CVPhase = true })
		sc.cvUpdate(func(done, total, updated, failed int) {
			sc.setStatus(func(st *Status) {
				st.CVDone, st.CVTotal, st.CVUpdated, st.CVFailed = done, total, updated, failed
			})
		})
		sc.setStatus(func(st *Status) { st.CVPhase = false })
	}

	sc.setStatus(func(st *Status) {
		st.Running = false
		st.Finished = true
		st.FinishedAt = time.Now()
		if err != nil {
			st.Err = err.Error()
		}
	})
}

func (sc *Scanner) scan() error {
	// Drop thumbnails that don't belong to any known issue. Issue ids restart
	// from 1 when the database is recreated, so a stale file under a reused
	// id would show a different comic's cover.
	if ids, err := sc.store.AllIssueIDs(); err != nil {
		log.Printf("scan: cover cleanup: %v", err)
	} else if removed, err := sc.covers.Cleanup(ids); err != nil {
		log.Printf("scan: cover cleanup: %v", err)
	} else if removed > 0 {
		log.Printf("scan: removed %d orphaned cover thumbnails", removed)
	}

	// Pass 1: collect comic files so the UI can show real progress.
	var files []foundFile
	err := filepath.WalkDir(sc.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			log.Printf("scan: skipping %s: %v", path, err)
			return nil
		}
		if d.IsDir() || !isComicFile(path) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			log.Printf("scan: stat %s: %v", path, err)
			return nil
		}
		files = append(files, foundFile{path: path, size: info.Size()})
		return nil
	})
	if err != nil {
		return err
	}
	sc.setStatus(func(st *Status) { st.Found = len(files) })

	existing, err := sc.store.AllIssuePaths()
	if err != nil {
		return err
	}

	// Pass 2: sync each file with the store.
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		seen[f.path] = true
		if id, ok := existing[f.path]; ok {
			if err := sc.refreshIssue(id, f); err != nil {
				log.Printf("scan: refresh %s: %v", f.path, err)
			}
		} else {
			if err := sc.addIssue(f); err != nil {
				log.Printf("scan: add %s: %v", f.path, err)
			}
		}
		sc.setStatus(func(st *Status) { st.Processed++ })
	}

	// Records whose file disappeared get flagged, never deleted (§6.6).
	var missing []int64
	for path, id := range existing {
		if !seen[path] {
			missing = append(missing, id)
		}
	}
	if err := sc.store.MarkIssuesMissing(missing); err != nil {
		return err
	}
	sc.setStatus(func(st *Status) { st.Missing = len(missing) })

	return sc.store.ReconcileOneShots()
}

func isComicFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cbz", ".cbr", ".pdf":
		return true
	}
	return false
}

func isArchive(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cbz", ".cbr":
		return true
	}
	return false
}

// addIssue catalogues a file seen for the first time.
func (sc *Scanner) addIssue(f foundFile) error {
	parsed := ParseFilename(filepath.Base(f.path))

	issue := store.Issue{
		Path:           f.path,
		FileSize:       f.size,
		IssueNumber:    parsed.Number,
		Title:          parsed.Title,
		ReleaseDate:    parsed.Year,
		MetadataSource: store.SourceFilename,
	}

	var ci *ComicInfo
	if isArchive(f.path) {
		var found bool
		var err error
		ci, found, err = ReadComicInfo(f.path)
		if err != nil {
			log.Printf("scan: comicinfo %s: %v", f.path, err)
		} else if found {
			applyComicInfo(&issue, ci)
		}
		if issue.PageCount == 0 {
			if pages, err := ListPages(f.path); err == nil {
				issue.PageCount = len(pages)
			}
		}
	}

	seriesID, err := sc.resolveSeries(f.path, parsed, ci)
	if err != nil {
		return err
	}
	issue.SeriesID = seriesID

	if err := sc.store.InsertIssue(&issue); err != nil {
		return err
	}
	// Embedded metadata is the strongest scan-time one-shot signal; the
	// count-based reconciliation runs once at the end of the scan.
	if ci != nil && ci.IsOneShot() {
		if err := sc.store.SetSeriesOneShot(seriesID, true); err != nil {
			log.Printf("scan: one-shot flag %s: %v", f.path, err)
		}
	}
	sc.cacheCover(&issue)
	return nil
}

// refreshIssue updates an already catalogued file: file facts always, embedded
// metadata only when it doesn't outrank what we have (§5).
func (sc *Scanner) refreshIssue(id int64, f foundFile) error {
	if err := sc.store.TouchIssueFile(id, f.size); err != nil {
		return err
	}
	issue, err := sc.store.GetIssue(id)
	if err != nil || issue == nil {
		return err
	}

	canApply := !issue.MetadataLocked &&
		store.SourceRank(issue.MetadataSource) <= store.SourceRank(store.SourceComicInfo)
	if canApply && isArchive(f.path) {
		ci, found, err := ReadComicInfo(f.path)
		if err != nil {
			log.Printf("scan: comicinfo %s: %v", f.path, err)
		} else if found {
			applyComicInfo(issue, ci)
			if err := sc.store.UpdateIssueMetadata(issue); err != nil {
				return err
			}
		}
	}

	if !issue.CoverCached || !sc.covers.Has(id) {
		sc.cacheCover(issue)
	}
	return nil
}

// resolveSeries picks the series for a file: its parent folder when it lives
// in a subfolder of the library, otherwise a "virtual" series named after
// ComicInfo's Series field or the parsed file name.
func (sc *Scanner) resolveSeries(path string, parsed Parsed, ci *ComicInfo) (int64, error) {
	rel, err := filepath.Rel(sc.root, path)
	if err != nil {
		return 0, err
	}
	relDir := filepath.Dir(rel)
	if relDir != "." {
		return sc.store.FindOrCreateSeriesByFolder(filepath.ToSlash(relDir), filepath.Base(relDir))
	}

	name := ""
	if ci != nil && ci.Series != "" {
		name = ci.Series
	} else if parsed.Series != "" {
		name = parsed.Series
	} else {
		name = parsed.Title
	}
	return sc.store.FindOrCreateSeriesByName(name)
}

// applyComicInfo overrides issue fields with the non-empty ComicInfo values.
func applyComicInfo(issue *store.Issue, ci *ComicInfo) {
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&issue.IssueNumber, ci.Number)
	set(&issue.Title, ci.Title)
	set(&issue.Summary, ci.Summary)
	set(&issue.ReleaseDate, ci.ReleaseDate())
	set(&issue.Writer, ci.Writer)
	set(&issue.Artist, ci.Artist())
	set(&issue.Publisher, ci.Publisher)
	if ci.PageCount > 0 {
		issue.PageCount = ci.PageCount
	}
	issue.MetadataSource = store.SourceComicInfo
	issue.HasComicInfo = true
}

// cacheCover extracts the first page and stores its thumbnail; failures are
// logged, not fatal — the UI falls back to a placeholder.
func (sc *Scanner) cacheCover(issue *store.Issue) {
	if !isArchive(issue.Path) {
		return // PDFs keep the placeholder in v1
	}
	raw, err := ExtractCover(issue.Path)
	if err != nil {
		log.Printf("scan: cover %s: %v", issue.Path, err)
		return
	}
	if err := sc.covers.Save(issue.ID, raw); err != nil {
		log.Printf("scan: thumbnail %s: %v", issue.Path, err)
		return
	}
	if err := sc.store.SetCoverCached(issue.ID, true); err != nil {
		log.Printf("scan: cover flag %s: %v", issue.Path, err)
	}
}
