package comicvine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGetIssueCredits(t *testing.T) {
	const body = `{
		"error": "OK", "status_code": 1,
		"results": {
			"id": 7, "name": "Part One", "issue_number": "1",
			"character_credits": [
				{"id": 1, "name": "Spider-Man"},
				{"id": 1, "name": "Spider-Man"},
				{"id": 2, "name": " Venom "},
				{"id": 3, "name": ""}
			],
			"story_arc_credits": [
				{"id": 4045, "name": "Secret Wars"}
			]
		}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := r.URL.Query().Get("field_list")
		if !strings.Contains(fl, "character_credits") || !strings.Contains(fl, "story_arc_credits") {
			t.Errorf("field_list = %q, want character and arc credits", fl)
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	issue, err := newTestClient(t, srv).GetIssue(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(issue.Characters) != 2 || issue.Characters[0] != (Credit{ID: 1, Name: "Spider-Man"}) || issue.Characters[1] != (Credit{ID: 2, Name: "Venom"}) {
		t.Errorf("Characters = %+v", issue.Characters)
	}
	if len(issue.StoryArcs) != 1 || issue.StoryArcs[0] != (Credit{ID: 4045, Name: "Secret Wars"}) {
		t.Errorf("StoryArcs = %+v", issue.StoryArcs)
	}
}

func TestGetStoryArc(t *testing.T) {
	var arcCalls, issueCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/story_arc/4045-9/":
			arcCalls.Add(1)
			if fl := r.URL.Query().Get("field_list"); !strings.Contains(fl, "issues") {
				t.Errorf("field_list = %q, want issues", fl)
			}
			fmt.Fprint(w, `{"error":"OK","status_code":1,"results":{
				"id": 9, "name": "Event One",
				"description": "<p>Big <b>event</b>.</p>",
				"publisher": {"name": "Marvel"},
				"image": {"small_url": "http://img/s.jpg", "medium_url": "http://img/m.jpg"},
				"site_detail_url": "https://comicvine.gamespot.com/event-one/4045-9/",
				"issues": [
					{"id": 30, "name": "Part 2", "issue_number": "2", "volume": {"id": 5, "name": "Alpha"}},
					{"id": 20, "name": "Part 1"},
					{"id": 30, "name": "dup"},
					{"id": 0, "name": "bogus"}
				]
			}}`)
		case "/issues/":
			issueCalls.Add(1)
			q := r.URL.Query()
			if got := q.Get("filter"); got != "id:30|20" {
				t.Errorf("filter = %q, want id:30|20", got)
			}
			if q.Get("limit") != "100" {
				t.Errorf("limit = %q", q.Get("limit"))
			}
			fmt.Fprint(w, `{"error":"OK","status_code":1,"results":[
				{"id": 20, "name": "Part 1", "issue_number": "1", "cover_date": "2015-05-01", "volume": {"id": 6, "name": "Beta"}},
				{"id": 30, "issue_number": "2", "cover_date": "", "store_date": "2015-06-03 00:00:00", "volume": {"id": 5, "name": "Alpha"}}
			]}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	arc, err := newTestClient(t, srv).GetStoryArc(9)
	if err != nil {
		t.Fatal(err)
	}
	if arcCalls.Load() != 1 || issueCalls.Load() != 1 {
		t.Errorf("calls: arc=%d issues=%d, want 1 each", arcCalls.Load(), issueCalls.Load())
	}
	if arc.ID != 9 || arc.Name != "Event One" || arc.Publisher != "Marvel" || arc.ImageURL != "http://img/m.jpg" ||
		arc.Description != "Big event." || !strings.HasSuffix(arc.URL, "4045-9/") {
		t.Errorf("arc = %+v", arc)
	}
	if arc.EnrichErr != nil {
		t.Errorf("EnrichErr = %v", arc.EnrichErr)
	}
	want := []ArcIssue{
		{ID: 30, Name: "Part 2", IssueNumber: "2", VolumeID: 5, VolumeName: "Alpha", CoverDate: "2015-06-03"},
		{ID: 20, Name: "Part 1", IssueNumber: "1", VolumeID: 6, VolumeName: "Beta", CoverDate: "2015-05-01"},
	}
	if len(arc.Issues) != len(want) {
		t.Fatalf("issues = %+v", arc.Issues)
	}
	for i := range want { // ComicVine's order is kept
		if arc.Issues[i] != want[i] {
			t.Errorf("issue %d = %+v, want %+v", i, arc.Issues[i], want[i])
		}
	}
}

func TestGetStoryArcEnrichFailureKeepsStubs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/issues/" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"error":"OK","status_code":1,"results":{"id":9,"name":"X","issues":[{"id":1,"issue_number":"1"}]}}`)
	}))
	defer srv.Close()

	arc, err := newTestClient(t, srv).GetStoryArc(9)
	if err != nil {
		t.Fatalf("GetStoryArc: %v", err)
	}
	if arc.EnrichErr == nil || len(arc.Issues) != 1 || arc.Issues[0].IssueNumber != "1" {
		t.Errorf("arc = %+v", arc)
	}
}

func TestGetIssuesBriefBatches(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"error":"OK","status_code":1,"results":[]}`)
	}))
	defer srv.Close()

	ids := make([]int, 250)
	for i := range ids {
		ids[i] = i + 1
	}
	if _, err := newTestClient(t, srv).GetIssuesBrief(ids); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("requests = %d, want 3 (100 ids each)", calls.Load())
	}
}

func TestStoryArcNoKey(t *testing.T) {
	c := New("")
	if _, err := c.GetStoryArc(1); err != ErrNoKey {
		t.Errorf("GetStoryArc: got %v, want ErrNoKey", err)
	}
	if _, err := c.GetIssuesBrief([]int{1}); err != ErrNoKey {
		t.Errorf("GetIssuesBrief: got %v, want ErrNoKey", err)
	}
}
