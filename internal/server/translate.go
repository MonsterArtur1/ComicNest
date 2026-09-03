package server

import (
	"archive/zip"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"comicnest/internal/library"
	"comicnest/internal/store"
)

// translateStatus is a snapshot of the single, global page-translation job.
type translateStatus struct {
	Running    bool
	IssueID    int64
	Done       int
	Total      int
	Finished   bool
	Err        string
	NewIssueID int64 // catalogued translated copy, once the job succeeded
}

type translateJob struct {
	mu sync.Mutex
	st translateStatus
}

func (j *translateJob) status() translateStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st
}

func (j *translateJob) update(fn func(*translateStatus)) {
	j.mu.Lock()
	fn(&j.st)
	j.mu.Unlock()
}

func (j *translateJob) tryStart(issueID int64) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.st.Running {
		return false
	}
	j.st = translateStatus{Running: true, IssueID: issueID}
	return true
}

// translateStatusData feeds the translate-status partial on one issue page.
type translateStatusData struct {
	translateStatus
	Enabled      bool
	Mine         bool // the global job belongs to this issue
	CanTranslate bool
	ThisIssueID  int64
	Lang         string
}

func isTranslatableArchive(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".cbz", ".cbr":
		return true
	}
	return false
}

// langSuffix is the marker appended to translated file names and titles.
func (s *Server) langSuffix() string {
	if s.translator.TargetLang() == "POL" {
		return " [PL]"
	}
	return " [" + s.translator.TargetLang() + "]"
}

func (s *Server) translateDataFor(issue *store.Issue) translateStatusData {
	st := s.translate.status()
	mine := st.IssueID == issue.ID
	alreadyTranslated := strings.Contains(filepath.Base(issue.Path), s.langSuffix())
	return translateStatusData{
		translateStatus: st,
		Enabled:         s.translator.Enabled(),
		Mine:            mine,
		CanTranslate: !issue.FileMissing && isTranslatableArchive(issue.Path) &&
			!alreadyTranslated,
		ThisIssueID: issue.ID,
		Lang:        s.translator.TargetLang(),
	}
}

func (s *Server) handleTranslateStatus(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}
	s.renderPartial(w, "translate_status.html", "translate-status", s.translateDataFor(issue))
}

// handleIssueTranslate starts translating an issue's pages in the background
// (one translation at a time).
func (s *Server) handleIssueTranslate(w http.ResponseWriter, r *http.Request) {
	issue := s.getIssueFromPath(w, r)
	if issue == nil {
		return
	}

	data := s.translateDataFor(issue)
	if data.Enabled && data.CanTranslate && s.translate.tryStart(issue.ID) {
		go s.runTranslate(issue)
	}

	if r.Header.Get("HX-Request") == "true" {
		s.renderPartial(w, "translate_status.html", "translate-status", s.translateDataFor(issue))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/issues/%d", issue.ID), http.StatusSeeOther)
}

func (s *Server) runTranslate(issue *store.Issue) {
	newID, err := s.translateIssue(issue)
	s.translate.update(func(st *translateStatus) {
		st.Running = false
		st.Finished = true
		st.NewIssueID = newID
		if err != nil {
			st.Err = err.Error()
			log.Printf("translate %s: %v", issue.Path, err)
		}
	})
}

// translateIssue runs the whole pipeline for one issue: extract each page,
// send it to the translator service, pack the results into a new
// "<name> [PL].cbz" next to the original, and catalogue that file as a
// locked issue in the same series.
func (s *Server) translateIssue(issue *store.Issue) (int64, error) {
	ext := filepath.Ext(issue.Path)
	outPath := strings.TrimSuffix(issue.Path, ext) + s.langSuffix() + ".cbz"
	if _, err := os.Stat(outPath); err == nil {
		return 0, fmt.Errorf("przetłumaczony plik już istnieje: %s", filepath.Base(outPath))
	}

	pages, err := library.ListPages(issue.Path)
	if err != nil {
		return 0, err
	}
	if len(pages) == 0 {
		return 0, fmt.Errorf("archiwum nie zawiera stron: %s", issue.Path)
	}
	s.translate.update(func(st *translateStatus) { st.Total = len(pages) })

	tmpPath := outPath + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return 0, err
	}
	cleanup := func() {
		out.Close()
		os.Remove(tmpPath)
	}

	zw := zip.NewWriter(out)
	var firstPage []byte
	for i, page := range pages {
		raw, err := library.ExtractPage(issue.Path, page)
		if err != nil {
			cleanup()
			return 0, err
		}
		translated, err := s.translator.TranslatePage(raw, path.Base(page))
		if err != nil {
			cleanup()
			return 0, fmt.Errorf("strona %d/%d: %w", i+1, len(pages), err)
		}
		if firstPage == nil {
			firstPage = translated
		}
		w, err := zw.Create(fmt.Sprintf("%03d.png", i+1))
		if err == nil {
			_, err = w.Write(translated)
		}
		if err != nil {
			cleanup()
			return 0, err
		}
		s.translate.update(func(st *translateStatus) { st.Done = i + 1 })
	}
	if err := zw.Close(); err != nil {
		cleanup()
		return 0, err
	}
	if err := out.Close(); err != nil {
		cleanup()
		return 0, err
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		os.Remove(tmpPath)
		return 0, err
	}

	info, err := os.Stat(outPath)
	if err != nil {
		return 0, err
	}

	// Catalogue the translated copy as a locked issue of the same series, so
	// scans refresh it but never overwrite its metadata (and the post-scan
	// ComicVine phase skips it).
	title := strings.TrimSpace(issue.Title + s.langSuffix())
	copyIssue := store.Issue{
		SeriesID:       issue.SeriesID,
		Path:           outPath,
		FileSize:       info.Size(),
		IssueNumber:    issue.IssueNumber,
		Title:          title,
		Summary:        issue.Summary,
		ReleaseDate:    issue.ReleaseDate,
		Writer:         issue.Writer,
		Artist:         issue.Artist,
		Publisher:      issue.Publisher,
		PageCount:      len(pages),
		MetadataSource: store.SourceManual,
	}
	if err := s.store.InsertIssue(&copyIssue); err != nil {
		return 0, err
	}
	copyIssue.MetadataLocked = true
	if err := s.store.UpdateIssueMetadata(&copyIssue); err != nil {
		return copyIssue.ID, err
	}

	if err := s.covers.Save(copyIssue.ID, firstPage); err != nil {
		log.Printf("translate: cover %s: %v", outPath, err)
	} else if err := s.store.SetCoverCached(copyIssue.ID, true); err != nil {
		log.Printf("translate: cover flag %s: %v", outPath, err)
	}
	return copyIssue.ID, nil
}
