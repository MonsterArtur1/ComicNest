package library

import (
	"database/sql"
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
	Found      int  // comic files discovered by the walk
	Processed  int  // files handled so far
	Missing    int  // records whose file disappeared (set when the scan ends)
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
		sc.status.Err = "library path is not set in config.yaml"
		return errors.New(sc.status.Err)
	}
	if info, err := os.Stat(sc.root); err != nil || !info.IsDir() {
		sc.status.Err = "library folder does not exist: " + sc.root
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

	final := sc.Status()
	entry := store.ScanHistoryEntry{
		StartedAt:  final.StartedAt.UTC().Format("2006-01-02 15:04:05"),
		FinishedAt: final.FinishedAt.UTC().Format("2006-01-02 15:04:05"),
		Found:      final.Found,
		Processed:  final.Processed,
		Missing:    final.Missing,
		CVUpdated:  final.CVUpdated,
		CVFailed:   final.CVFailed,
		Err:        final.Err,
		Library:    sc.root,
	}
	if err := sc.store.RecordScanHistory(entry); err != nil {
		log.Printf("scan: recording history: %v", err)
	}
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

	// AllIssuePaths spans every configured library, not just this one — scope
	// it to files under this scan's own root before treating anything absent
	// from it as missing, or a concurrent/earlier scan of a sibling library
	// would have every one of its issues wrongly flagged missing here.
	allExisting, err := sc.store.AllIssuePaths()
	if err != nil {
		return err
	}
	existing := make(map[string]int64, len(allExisting))
	for path, id := range allExisting {
		if sc.underRoot(path) {
			existing[path] = id
		}
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

	// Pass 3: a folder whose files name different series in ComicInfo.xml is
	// really several series sharing a directory.
	if err := sc.splitMixedFolders(); err != nil {
		return err
	}

	return sc.store.ReconcileOneShots()
}

// splitMixedFolders regroups issues by their ComicInfo <Series> in every
// library subfolder whose present files carry at least two distinct non-empty
// Series values (compared trimmed, case-insensitively). Only issues that still
// sit in their folder's own series are moved; a file already living in a
// virtual series is never touched again, so renames (manual or ComicVine) of a
// split-out series survive rescans. The move target is, in order: the series
// another file of the same folder with the same Series value already lives
// in (keeps late additions with the renamed/matched series), else the virtual
// series named after the value (created on demand). A value equal to the
// folder's name stays in the folder series (it would only duplicate it).
// Folders with a single consistent Series value (even one differing from the
// folder name) are left alone: folder = series remains the convention.
func (sc *Scanner) splitMixedFolders() error {
	refs, err := sc.store.ListIssueSeriesRefs()
	if err != nil {
		return err
	}

	byFolder := make(map[string][]store.IssueSeriesRef)
	for _, r := range refs {
		rel, err := filepath.Rel(sc.root, r.Path)
		if err != nil || filepath.Dir(rel) == "." || strings.HasPrefix(rel, "..") {
			continue // root-level files already take their series from ComicInfo
		}
		byFolder[filepath.Dir(r.Path)] = append(byFolder[filepath.Dir(r.Path)], r)
	}

	for folder, issues := range byFolder {
		relDir, err := filepath.Rel(sc.root, folder)
		if err != nil {
			return err
		}
		folderPath := filepath.ToSlash(relDir) // what FindOrCreateSeriesByFolder stored
		folderName := strings.ToLower(filepath.Base(folder))
		inFolderSeries := func(r store.IssueSeriesRef) bool {
			return r.SeriesFolder.Valid && r.SeriesFolder.String == folderPath
		}

		distinct := make(map[string]bool)
		existing := make(map[string]int64) // normalized value → series already split out
		for _, r := range issues {
			name := comicInfoSeriesName(r.ComicInfoSeries)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			distinct[key] = true
			if _, seen := existing[key]; !seen && !inFolderSeries(r) {
				existing[key] = r.SeriesID
			}
		}
		if len(distinct) < 2 {
			continue
		}

		moved := 0
		for _, r := range issues {
			name := comicInfoSeriesName(r.ComicInfoSeries)
			key := strings.ToLower(name)
			if name == "" || !inFolderSeries(r) || key == folderName {
				continue
			}
			target, ok := existing[key]
			if !ok {
				if target, err = sc.store.FindOrCreateSeriesByName(name, sc.root); err != nil {
					return err
				}
				existing[key] = target
			}
			if err := sc.store.SetIssueSeries(r.ID, target); err != nil {
				return err
			}
			moved++
		}
		if moved > 0 {
			log.Printf("scan: folder %s holds %d series by ComicInfo — moved %d issue(s) to their own series",
				folder, len(distinct), moved)
		}
	}
	return nil
}

// recordComicInfoSeries stores the ComicInfo Series value when it is unknown
// or has changed (the archive may have been re-tagged).
func (sc *Scanner) recordComicInfoSeries(issue *store.Issue, series string) error {
	if issue.ComicInfoSeries.Valid && issue.ComicInfoSeries.String == series {
		return nil
	}
	if err := sc.store.SetIssueComicInfoSeries(issue.ID, series); err != nil {
		return err
	}
	issue.ComicInfoSeries = sql.NullString{String: series, Valid: true}
	return nil
}

// comicInfoSeriesName normalizes a stored ComicInfo Series value ("" when
// unknown or empty).
func comicInfoSeriesName(v sql.NullString) string {
	if !v.Valid {
		return ""
	}
	return strings.TrimSpace(v.String)
}

// underRoot reports whether path lies within this scanner's own library root
// (used to scope the store-wide issue list to just this library — see scan).
func (sc *Scanner) underRoot(path string) bool {
	rel, err := filepath.Rel(sc.root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
	// comicinfo_series: "" for PDFs and archives without ComicInfo; left NULL
	// when the archive could not be read, so a later scan retries (§5).
	if !isArchive(f.path) {
		issue.ComicInfoSeries = sql.NullString{String: "", Valid: true}
	} else {
		var found bool
		var err error
		ci, found, err = ReadComicInfo(f.path)
		switch {
		case err != nil:
			log.Printf("scan: comicinfo %s: %v", f.path, err)
		case found:
			applyComicInfo(&issue, ci)
			issue.ComicInfoSeries = sql.NullString{String: strings.TrimSpace(ci.Series), Valid: true}
		default:
			issue.ComicInfoSeries = sql.NullString{String: "", Valid: true}
		}
		// The real page count drives page streaming; it also stands in for
		// missing metadata.
		if pages, err := ListPages(f.path); err == nil {
			issue.FilePages = len(pages)
			if issue.PageCount == 0 {
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
	// Rows from before the comicinfo_series column exist with NULL there; the
	// first scan after the upgrade inspects them once (backfill), regardless
	// of whether their metadata may still be overwritten.
	inspect := !issue.ComicInfoSeries.Valid
	if (canApply || inspect) && isArchive(f.path) {
		ci, found, err := ReadComicInfo(f.path)
		switch {
		case err != nil:
			log.Printf("scan: comicinfo %s: %v", f.path, err)
			inspect = false // unreadable archive: leave NULL, try again next scan
		case found:
			if err := sc.recordComicInfoSeries(issue, strings.TrimSpace(ci.Series)); err != nil {
				return err
			}
			if canApply {
				applyComicInfo(issue, ci)
				if err := sc.store.UpdateIssueMetadata(issue); err != nil {
					return err
				}
			}
			inspect = false
		}
	}
	if inspect { // no ComicInfo (or not an archive): mark as inspected, empty
		if err := sc.recordComicInfoSeries(issue, ""); err != nil {
			return err
		}
	}

	// Files catalogued before page streaming existed have no page count yet.
	if issue.FilePages == 0 && isArchive(f.path) {
		if pages, err := ListPages(f.path); err == nil && len(pages) > 0 {
			if err := sc.store.SetIssueFilePages(id, len(pages)); err != nil {
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
		return sc.store.FindOrCreateSeriesByFolder(filepath.ToSlash(relDir), filepath.Base(relDir), sc.root)
	}

	name := ""
	if ci != nil && ci.Series != "" {
		name = ci.Series
	} else if parsed.Series != "" {
		name = parsed.Series
	} else {
		name = parsed.Title
	}
	return sc.store.FindOrCreateSeriesByName(name, sc.root)
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

// ResetIssueMetadata discards whatever a ComicVine match wrote to issue and
// rebuilds it purely from the file itself — filename parsing plus
// ComicInfo.xml, if present — the same starting point a freshly scanned file
// gets. Used when a match is unlinked, so a bad match doesn't leave stale
// ComicVine text (or cover) behind. The issue is persisted, including its
// cover thumbnail, which is re-extracted from the file's own first page,
// undoing a ComicVine-downloaded one; cover failures are logged, not fatal,
// same as during a scan.
func ResetIssueMetadata(st *store.Store, cache *covers.Cache, issue *store.Issue) error {
	parsed := ParseFilename(filepath.Base(issue.Path))

	issue.IssueNumber = parsed.Number
	issue.Title = parsed.Title
	issue.Summary = ""
	issue.ReleaseDate = parsed.Year
	issue.Writer = ""
	issue.Artist = ""
	issue.Publisher = ""
	issue.PageCount = 0
	issue.MetadataSource = store.SourceFilename
	issue.HasComicInfo = false
	issue.ComicVineIssueID = sql.NullInt64{}
	issue.ComicVineURL = ""

	if isArchive(issue.Path) {
		if ci, found, err := ReadComicInfo(issue.Path); err == nil && found {
			applyComicInfo(issue, ci)
		}
	}
	if issue.PageCount == 0 && issue.FilePages > 0 {
		issue.PageCount = issue.FilePages
	}

	if err := st.UpdateIssueMetadata(issue); err != nil {
		return err
	}

	if !isArchive(issue.Path) {
		return nil
	}
	raw, err := ExtractCover(issue.Path)
	if err != nil {
		log.Printf("comicvine unlink: cover %s: %v", issue.Path, err)
		return nil
	}
	if err := cache.Save(issue.ID, raw); err != nil {
		log.Printf("comicvine unlink: thumbnail %s: %v", issue.Path, err)
		return nil
	}
	if err := st.SetCoverCached(issue.ID, true); err != nil {
		log.Printf("comicvine unlink: cover flag %s: %v", issue.Path, err)
	}
	return nil
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
