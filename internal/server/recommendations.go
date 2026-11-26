package server

import (
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"comicnest/internal/store"
)

// Recommendations ("For you", store/recommend.go). Computing them takes a few
// aggregate queries over the whole library, so results are cached in memory
// per user and library for a few minutes (a dismiss/restore drops that user's
// entries at once), and the user-independent series index is shared.

const recCacheTTL = 5 * time.Minute

// homeForYouMax is the number of tiles in the home page strip.
const homeForYouMax = 6

// similarMax is the number of "More like this" tiles on the series page.
const similarMax = 6

type recEntry struct {
	at   time.Time
	recs *store.Recommendations
}

// recCache is the zero-value-ready cache; ttl 0 means recCacheTTL and a
// negative ttl disables caching (tests).
type recCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	idx   *store.SeriesIndex
	idxAt time.Time
	recs  map[string]recEntry
}

func (c *recCache) lifetime() time.Duration {
	if c.ttl == 0 {
		return recCacheTTL
	}
	return c.ttl
}

func (c *recCache) dropUser(user string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.recs {
		if len(k) > len(user) && k[:len(user)] == user && k[len(user)] == 0 {
			delete(c.recs, k)
		}
	}
}

func (s *Server) seriesIndex() (*store.SeriesIndex, error) {
	c := &s.recCache
	c.mu.Lock()
	if c.idx != nil && c.lifetime() > 0 && time.Since(c.idxAt) < c.lifetime() {
		idx := c.idx
		c.mu.Unlock()
		return idx, nil
	}
	c.mu.Unlock()
	idx, err := s.store.LoadSeriesIndex()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.idx, c.idxAt = idx, time.Now()
	c.mu.Unlock()
	return idx, nil
}

// recommendations returns the (cached) recommendations of the request's user,
// scoped to the selected library.
func (s *Server) recommendations(r *http.Request) (*store.Recommendations, error) {
	c := &s.recCache
	user, lib := userFrom(r), s.selectedLibrary(r)
	key := user + "\x00" + lib
	c.mu.Lock()
	if e, ok := c.recs[key]; ok && c.lifetime() > 0 && time.Since(e.at) < c.lifetime() {
		c.mu.Unlock()
		return e.recs, nil
	}
	c.mu.Unlock()

	idx, err := s.seriesIndex()
	if err != nil {
		return nil, err
	}
	recs, err := s.store.Recommend(user, lib, idx, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.recs == nil {
		c.recs = map[string]recEntry{}
	}
	c.recs[key] = recEntry{at: time.Now(), recs: recs}
	c.mu.Unlock()
	return recs, nil
}

// homeForYou picks the home strip: tiles taken round-robin from the buckets
// (best of each first), without dismiss buttons. nil for the empty-profile
// fallback or on error.
func (s *Server) homeForYou(r *http.Request) []store.Recommendation {
	recs, err := s.recommendations(r)
	if err != nil {
		log.Printf("recommendations: home: %v", err)
		return nil
	}
	if recs.Fallback {
		return nil
	}
	var out []store.Recommendation
	for i := 0; len(out) < homeForYouMax; i++ {
		added := false
		for _, b := range recs.Buckets {
			if i < len(b.Items) && len(out) < homeForYouMax {
				it := b.Items[i]
				it.Dismissable = false
				out = append(out, it)
				added = true
			}
		}
		if !added {
			break
		}
	}
	return out
}

// similarSeries is the series page's "More like this" list; nil on error.
func (s *Server) similarSeries(r *http.Request, seriesID int64) []store.Recommendation {
	idx, err := s.seriesIndex()
	if err != nil {
		log.Printf("recommendations: similar: %v", err)
		return nil
	}
	return store.SimilarSeries(idx, seriesID, s.selectedLibrary(r), similarMax)
}

type recommendationsData struct {
	Recs      *store.Recommendations
	Dismissed []store.DismissedSeries
}

func (s *Server) handleRecommendations(w http.ResponseWriter, r *http.Request) {
	recs, err := s.recommendations(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	dismissed, err := s.store.ListDismissedSeries(userFrom(r))
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "recommendations.html", recommendationsData{Recs: recs, Dismissed: dismissed})
}

// handleRecommendationDismiss hides a series from the user's recommendations
// ("not interested"); handleRecommendationRestore undoes it.
func (s *Server) handleRecommendationDismiss(w http.ResponseWriter, r *http.Request) {
	s.setDismissed(w, r, true)
}

func (s *Server) handleRecommendationRestore(w http.ResponseWriter, r *http.Request) {
	s.setDismissed(w, r, false)
}

func (s *Server) setDismissed(w http.ResponseWriter, r *http.Request, dismiss bool) {
	id, err := strconv.ParseInt(r.FormValue("series"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	series, err := s.store.GetSeries(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if series == nil {
		s.notFound(w, r)
		return
	}
	user := userFrom(r)
	if dismiss {
		err = s.store.DismissRecommendation(user, id)
	} else {
		err = s.store.RestoreRecommendation(user, id)
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.recCache.dropUser(user)
	back := "/recommendations"
	if n := safeNext(r.FormValue("next")); n != "/" {
		back = n
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
