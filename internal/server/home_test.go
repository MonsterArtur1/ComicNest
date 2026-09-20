package server

import (
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"comicnest/internal/store"
)

// addSeries inserts n one-issue series named "S01".."Snn" (present files).
func addSeries(t *testing.T, st *store.Store, dir string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("S%02d", i)
		id, err := st.FindOrCreateSeriesByFolder(name, name, dir)
		if err != nil {
			t.Fatal(err)
		}
		cbz := filepath.Join(dir, name+" 001.cbz")
		writeTestCBZ(t, cbz, 1)
		if err := st.InsertIssue(&store.Issue{SeriesID: id, Path: cbz, IssueNumber: "1",
			MetadataSource: store.SourceFilename, FilePages: 1}); err != nil {
			t.Fatal(err)
		}
	}
}

var cardTitle = regexp.MustCompile(`<span class="card-title">([^<]+)</span>`)

func titles(body string) []string {
	var out []string
	for _, m := range cardTitle.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestHomePagination(t *testing.T) {
	srv, cbz := newTestServer(t, false)
	srv.cfg.PageSize = 4
	addSeries(t, srv.store, filepath.Dir(cbz), 10) // + "Saga" = 11 series → 3 pages
	h := srv.Handler()

	rec := get(t, h, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/: %d\n%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if got := titles(body); !slices.Equal(got, []string{"S01", "S02", "S03", "S04"}) {
		t.Errorf("page 1 = %v", got)
	}
	for _, want := range []string{`href="/?page=2" rel="next"`, `href="/?page=3"`, "page 1 of 3", "11 series"} {
		if !strings.Contains(body, want) {
			t.Errorf("page 1 missing %q", want)
		}
	}
	if strings.Contains(body, `rel="prev"`) {
		t.Error("page 1 should have no active prev link")
	}

	body = get(t, h, "/?page=3", nil).Body.String()
	if got := titles(body); !slices.Equal(got, []string{"S09", "S10", "Saga"}) {
		t.Errorf("page 3 = %v", got)
	}
	if !strings.Contains(body, `href="/?page=2" rel="prev"`) || strings.Contains(body, `rel="next"`) {
		t.Error("last page should link back only")
	}

	// Out-of-range and garbage pages clamp instead of erroring.
	if got := titles(get(t, h, "/?page=99", nil).Body.String()); !slices.Equal(got, []string{"S09", "S10", "Saga"}) {
		t.Errorf("page 99 = %v, want last page", got)
	}
	if got := titles(get(t, h, "/?page=abc", nil).Body.String()); !slices.Equal(got, []string{"S01", "S02", "S03", "S04"}) {
		t.Errorf("page abc = %v, want first page", got)
	}

	// Sort and filter survive in pager links.
	body = get(t, h, "/?sort=recent&filter=unread&page=2", nil).Body.String()
	if !strings.Contains(body, `href="/?filter=unread&amp;page=3&amp;sort=recent"`) {
		t.Errorf("pager links should keep sort/filter:\n%s", body)
	}
}

func TestHomePaginationDisabled(t *testing.T) {
	srv, cbz := newTestServer(t, false)
	srv.cfg.PageSize = 0
	addSeries(t, srv.store, filepath.Dir(cbz), 5)
	body := get(t, srv.Handler(), "/", nil).Body.String()
	if got := len(titles(body)); got != 6 {
		t.Errorf("page_size 0 should list everything, got %d cards", got)
	}
	if strings.Contains(body, `class="pager"`) {
		t.Error("no pager expected with pagination disabled")
	}
}

func TestPageLinks(t *testing.T) {
	for _, tc := range []struct {
		page, pages int
		want        []int
	}{
		{1, 1, []int{1}},
		{1, 5, []int{1, 2, 3, 4, 5}},
		{1, 10, []int{1, 2, 3, 0, 10}},
		{5, 10, []int{1, 2, 3, 4, 5, 6, 7, 0, 10}}, // gap of one page → show it
		{6, 12, []int{1, 0, 4, 5, 6, 7, 8, 0, 12}},
		{10, 10, []int{1, 0, 8, 9, 10}},
	} {
		if got := pageLinks(tc.page, tc.pages); !slices.Equal(got, tc.want) {
			t.Errorf("pageLinks(%d, %d) = %v, want %v", tc.page, tc.pages, got, tc.want)
		}
	}
}
