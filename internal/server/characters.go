package server

import (
	"net/http"
	"strconv"
	"strings"

	"comicnest/internal/store"
)

type charactersPageData struct {
	Characters []store.CharacterCount
	Filter     string
	Total      int // matches before the cap
}

// handleCharacters serves GET /characters: characters by number of issues in
// the library; ?q= filters by name.
func (s *Server) handleCharacters(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListCharacters()
	if err != nil {
		s.serverError(w, err)
		return
	}
	filter := strings.TrimSpace(r.URL.Query().Get("q"))
	data := charactersPageData{Filter: filter}
	needle := strings.ToLower(filter)
	for _, c := range list {
		if needle == "" || strings.Contains(strings.ToLower(c.Name), needle) {
			data.Total++
			if len(data.Characters) < maxNameListRows {
				data.Characters = append(data.Characters, c)
			}
		}
	}
	s.render(w, r, "characters.html", data)
}

type characterPageData struct {
	Character *store.Character
	Groups    []seriesGroup
	Issues    int
	Read      int
}

// handleCharacter serves GET /characters/{id}: every issue featuring the
// character, grouped by series.
func (s *Server) handleCharacter(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	ch, err := s.store.GetCharacter(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if ch == nil {
		s.notFound(w, r)
		return
	}
	items, err := s.store.ListCharacterIssues(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	groups, read, err := s.groupIssues(userFrom(r), items)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "character.html", characterPageData{Character: ch, Groups: groups, Issues: len(items), Read: read})
}
