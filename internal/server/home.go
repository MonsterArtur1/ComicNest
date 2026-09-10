package server

import (
	"net/http"
	"net/url"
	"strconv"

	"comicnest/internal/store"
)

type homeData struct {
	Series []store.Series
	Sort   string
	Filter string
	Stats  readingStats

	// Pagination over the filtered series list (Pages == 1 hides the pager).
	Page  int
	Pages int
	Total int
	// PageLinks are the page numbers shown in the pager; 0 marks a gap ("…").
	PageLinks []int
}

// URL builds a library link that keeps the current sort and filter and
// points at the given page.
func (d homeData) URL(page int) string {
	q := url.Values{}
	if d.Sort != string(store.SeriesSortName) {
		q.Set("sort", d.Sort)
	}
	if d.Filter != string(store.SeriesFilterAll) {
		q.Set("filter", d.Filter)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if len(q) == 0 {
		return "/"
	}
	return "/?" + q.Encode()
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	sort := store.ParseSeriesSort(r.FormValue("sort"))
	filter := store.ParseSeriesFilter(r.FormValue("filter"))

	series, err := s.store.ListSeries(userFrom(r), "", sort, filter)
	if err != nil {
		s.serverError(w, err)
		return
	}
	stats, err := s.userReadingStats(r)
	if err != nil {
		s.serverError(w, err)
		return
	}

	data := homeData{Sort: string(sort), Filter: string(filter), Stats: stats, Total: len(series), Page: 1, Pages: 1}
	if size := s.config().PageSize; size > 0 {
		data.Pages = max(1, (len(series)+size-1)/size)
		data.Page = min(pageParam(r), data.Pages)
		from, to, _ := pageWindow(data.Page, size, len(series))
		series = series[from:to]
		data.PageLinks = pageLinks(data.Page, data.Pages)
	}
	data.Series = series

	s.render(w, r, "index.html", data)
}

// pageParam reads the 1-based ?page= query parameter (default 1).
func pageParam(r *http.Request) int {
	p, err := strconv.Atoi(r.FormValue("page"))
	if err != nil || p < 1 {
		return 1
	}
	return p
}

// pageWindow clips a page of the given size to [0, total) and reports
// whether a next page exists.
func pageWindow(page, size, total int) (from, to int, hasNext bool) {
	from = min((page-1)*size, total)
	to = min(from+size, total)
	return from, to, to < total
}

// pageLinks picks the page numbers to show in the pager: the first and last
// page plus a window of two around the current one; 0 stands for a gap. A gap
// that would hide a single page is replaced by that page.
func pageLinks(page, pages int) []int {
	const around = 2
	shown := func(p int) bool {
		return p == 1 || p == pages || (p >= page-around && p <= page+around)
	}
	var out []int
	for p := 1; p <= pages; p++ {
		switch {
		case shown(p) || (shown(p-1) && shown(p+1)):
			out = append(out, p)
		case out[len(out)-1] != 0:
			out = append(out, 0)
		}
	}
	return out
}
