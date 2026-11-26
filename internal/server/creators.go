package server

import (
	"net/http"
	"net/url"
	"strings"

	"comicnest/internal/store"
)

// Creator and character pages, like arcs, span every library; read state is
// the requesting user's.

// maxNameListRows caps the creator/character index; a text filter narrows it.
const maxNameListRows = 300

// creatorLink is a credited name with the key its page lives under.
type creatorLink struct {
	Name string
	Key  string
}

// URL is the creator's page.
func (c creatorLink) URL() string { return creatorURL(c.Name) }

// creatorURL is the page of the creator with that (credited) name; the URL
// carries the normalized name, path-escaped.
func creatorURL(name string) string {
	return "/creators/" + url.PathEscape(store.NormalizeName(name))
}

// creatorLinks splits a credits string into linkable names.
func creatorLinks(credits string) []creatorLink {
	var out []creatorLink
	for _, name := range store.SplitCredits(credits) {
		out = append(out, creatorLink{Name: name, Key: store.NormalizeName(name)})
	}
	return out
}

// seriesGroup is the issues of one series on a character/creator page.
type seriesGroup struct {
	SeriesID   int64
	SeriesName string
	Issues     []issueRow
}

// groupIssues attaches the user's reading state to issues (already ordered by
// series) and groups them by series. It also returns how many are finished.
func (s *Server) groupIssues(user string, items []store.IssueWithSeries) (groups []seriesGroup, read int, err error) {
	issues := make([]store.Issue, len(items))
	for i, it := range items {
		issues[i] = it.Issue
	}
	rows, err := s.issueRows(user, issues)
	if err != nil {
		return nil, 0, err
	}
	for i, row := range rows {
		if row.Reading != nil && row.Reading.Finished {
			read++
		}
		if n := len(groups); n == 0 || groups[n-1].SeriesID != items[i].SeriesID {
			groups = append(groups, seriesGroup{SeriesID: items[i].SeriesID, SeriesName: items[i].SeriesName})
		}
		g := &groups[len(groups)-1]
		g.Issues = append(g.Issues, row)
	}
	return groups, read, nil
}

type creatorsPageData struct {
	Creators []store.Creator
	Filter   string
	Total    int // matches before the cap
}

// handleCreators serves GET /creators: everyone credited in the library, most
// issues first; ?q= filters by name.
func (s *Server) handleCreators(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListCreators()
	if err != nil {
		s.serverError(w, err)
		return
	}
	filter := strings.TrimSpace(r.URL.Query().Get("q"))
	data := creatorsPageData{Filter: filter}
	needle := strings.ToLower(filter)
	for _, c := range list {
		if needle == "" || strings.Contains(strings.ToLower(c.Name), needle) {
			data.Total++
			if len(data.Creators) < maxNameListRows {
				data.Creators = append(data.Creators, c)
			}
		}
	}
	s.render(w, r, "creators.html", data)
}

type creatorPageData struct {
	Name    string
	Credits int
	Read    int
	Writer  []seriesGroup
	Artist  []seriesGroup
}

// handleCreator serves GET /creators/{name}: the creator's issues by role,
// grouped by series. {name} is the normalized name (the router has already
// unescaped it); any spelling normalizing to the same key works.
func (s *Server) handleCreator(w http.ResponseWriter, r *http.Request) {
	found, err := s.store.ListCreatorIssues(r.PathValue("name"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if found.Credits == 0 {
		s.notFound(w, r)
		return
	}
	user := userFrom(r)
	data := creatorPageData{Name: found.Name, Credits: found.Credits}
	if data.Writer, _, err = s.groupIssues(user, found.Writer); err != nil {
		s.serverError(w, err)
		return
	}
	if data.Artist, _, err = s.groupIssues(user, found.Artist); err != nil {
		s.serverError(w, err)
		return
	}
	// "Read" counts distinct issues, which a writer-artist appears in twice.
	seen := make(map[int64]bool)
	for _, groups := range [][]seriesGroup{data.Writer, data.Artist} {
		for _, g := range groups {
			for _, row := range g.Issues {
				if row.Reading != nil && row.Reading.Finished && !seen[row.ID] {
					seen[row.ID] = true
					data.Read++
				}
			}
		}
	}
	s.render(w, r, "creator.html", data)
}
