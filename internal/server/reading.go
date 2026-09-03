package server

import (
	"time"

	"comicnest/internal/opds"
	"comicnest/internal/store"
)

// readingView is reading progress shaped for templates.
type readingView struct {
	Page     int    // last page read (1-based)
	Total    int    // pages in the issue (0 = unknown)
	Percent  int    // 0–100; 0 when Total is unknown
	Finished bool   // reached the last page
	ReadAt   string // local time of the last streamed page, "2006-01-02 15:04"
}

// newReadingView combines an issue with its progress; nil when unread.
func newReadingView(i *store.Issue, p *store.ReadingProgress) *readingView {
	if p == nil || p.Page <= 0 {
		return nil
	}
	v := &readingView{Page: p.Page, Total: i.TotalPages()}
	if v.Total > 0 {
		v.Page = min(v.Page, v.Total)
		v.Percent = v.Page * 100 / v.Total
		v.Finished = v.Page >= v.Total
	}
	if t := opds.ParseDBTime(p.UpdatedAt, time.Time{}); !t.IsZero() {
		v.ReadAt = t.Local().Format("2006-01-02 15:04")
	}
	return v
}

// issueRow is an issue plus its reading progress, for list views.
type issueRow struct {
	store.Issue
	Reading *readingView
}

// issueRows attaches reading progress to issues in one query.
func (s *Server) issueRows(issues []store.Issue) ([]issueRow, error) {
	progress, err := s.progressFor(issues)
	if err != nil {
		return nil, err
	}
	rows := make([]issueRow, len(issues))
	for k := range issues {
		rows[k] = issueRow{Issue: issues[k], Reading: newReadingView(&issues[k], progressPtr(progress, issues[k].ID))}
	}
	return rows, nil
}
