package comicvine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient returns an enabled client pointed at the given test server,
// with a shortened rate-limit interval so tests stay fast (allowed since
// minInterval is a private, configurable field).
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c := New("test-key")
	c.baseURL = srv.URL
	c.minInterval = 200 * time.Millisecond
	return c
}

func TestEnabledAndErrNoKey(t *testing.T) {
	c := New("")
	if c.Enabled() {
		t.Fatal("expected disabled client for empty API key")
	}

	if _, err := c.SearchVolumes("batman"); err != ErrNoKey {
		t.Errorf("SearchVolumes: got err %v, want ErrNoKey", err)
	}
	if _, _, err := c.GetVolume(1); err != ErrNoKey {
		t.Errorf("GetVolume: got err %v, want ErrNoKey", err)
	}
	if _, err := c.GetIssue(1); err != ErrNoKey {
		t.Errorf("GetIssue: got err %v, want ErrNoKey", err)
	}
	if _, err := c.DownloadImage("http://example.com/x.jpg"); err != ErrNoKey {
		t.Errorf("DownloadImage: got err %v, want ErrNoKey", err)
	}

	c2 := New("abc")
	if !c2.Enabled() {
		t.Fatal("expected enabled client for non-empty API key")
	}
}

func TestSearchVolumes(t *testing.T) {
	const body = `{
		"error": "OK",
		"status_code": 1,
		"results": [
			{
				"id": 1,
				"name": "Batman",
				"start_year": "1940",
				"publisher": {"name": "DC Comics"},
				"count_of_issues": 900,
				"description": "<p>The <b>Dark</b> Knight.</p>",
				"image": {"small_url": "http://img/small1.jpg", "medium_url": "http://img/medium1.jpg"},
				"site_detail_url": "https://comicvine.gamespot.com/batman/4050-1/"
			},
			{
				"id": 2,
				"name": "No Publisher Series",
				"start_year": "2000",
				"publisher": null,
				"count_of_issues": 5,
				"description": "",
				"image": null
			}
		]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != userAgent {
			t.Errorf("User-Agent = %q, want %q", got, userAgent)
		}
		if r.URL.Path != "/search/" {
			t.Errorf("path = %q, want /search/", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("resources") != "volume" || q.Get("query") != "batman" || q.Get("limit") != "20" {
			t.Errorf("unexpected query params: %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)

	volumes, err := c.SearchVolumes("batman")
	if err != nil {
		t.Fatalf("SearchVolumes returned error: %v", err)
	}
	if len(volumes) != 2 {
		t.Fatalf("got %d volumes, want 2", len(volumes))
	}

	v1 := volumes[0]
	if v1.ID != 1 || v1.Name != "Batman" || v1.StartYear != "1940" || v1.Publisher != "DC Comics" ||
		v1.CountOfIssues != 900 || v1.ImageURL != "http://img/small1.jpg" ||
		v1.URL != "https://comicvine.gamespot.com/batman/4050-1/" {
		t.Errorf("volume 1 mismatch: %+v", v1)
	}
	if v1.Description != "The Dark Knight." {
		t.Errorf("volume 1 description = %q, want %q", v1.Description, "The Dark Knight.")
	}

	v2 := volumes[1]
	if v2.Publisher != "" {
		t.Errorf("volume 2 publisher = %q, want empty", v2.Publisher)
	}
	if v2.ImageURL != "" {
		t.Errorf("volume 2 image = %q, want empty", v2.ImageURL)
	}
}

func TestGetVolume(t *testing.T) {
	const body = `{
		"error": "OK",
		"status_code": 1,
		"results": {
			"id": 42,
			"name": "Fables",
			"start_year": "2002",
			"publisher": {"name": "Vertigo"},
			"count_of_issues": 3,
			"description": "Plain description",
			"image": {"small_url": "http://img/s.jpg", "medium_url": "http://img/m.jpg"},
			"site_detail_url": "https://comicvine.gamespot.com/fables/4050-42/",
			"issues": [
				{"id": 100, "issue_number": "1", "name": "Legends in Exile"},
				{"id": 101, "issue_number": "2", "name": "Part 2"},
				{"id": 102, "issue_number": "3", "name": "Part 3"}
			]
		}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/volume/4050-42/" {
			t.Errorf("path = %q, want /volume/4050-42/", r.URL.Path)
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)

	vol, issues, err := c.GetVolume(42)
	if err != nil {
		t.Fatalf("GetVolume returned error: %v", err)
	}
	if vol.ID != 42 || vol.Name != "Fables" || vol.Publisher != "Vertigo" {
		t.Errorf("volume mismatch: %+v", vol)
	}
	if vol.URL != "https://comicvine.gamespot.com/fables/4050-42/" {
		t.Errorf("volume URL = %q", vol.URL)
	}
	if len(issues) != 3 {
		t.Fatalf("got %d issues, want 3", len(issues))
	}
	if issues[0].ID != 100 || issues[0].IssueNumber != "1" || issues[0].Name != "Legends in Exile" {
		t.Errorf("issue 0 mismatch: %+v", issues[0])
	}
	if issues[2].IssueNumber != "3" {
		t.Errorf("issue 2 mismatch: %+v", issues[2])
	}
}

func TestGetIssue(t *testing.T) {
	const body = `{
		"error": "OK",
		"status_code": 1,
		"results": {
			"id": 7,
			"name": "Issue Name",
			"issue_number": "1",
			"cover_date": "2020-01-15",
			"store_date": "2020-01-08",
			"description": "<p>Something happens.<br>Then more.</p>",
			"image": {"small_url": "http://img/s.jpg", "medium_url": "http://img/m.jpg"},
			"site_detail_url": "https://comicvine.gamespot.com/fables-1/4000-7/",
			"person_credits": [
				{"name": "Alice Writer", "role": "writer"},
				{"name": "Bob Artist", "role": "penciler, inker"},
				{"name": "Carl Cover", "role": "cover"},
				{"name": "Alice Writer", "role": "writer"}
			]
		}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/issue/4000-7/" {
			t.Errorf("path = %q, want /issue/4000-7/", r.URL.Path)
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)

	issue, err := c.GetIssue(7)
	if err != nil {
		t.Fatalf("GetIssue returned error: %v", err)
	}
	if issue.ID != 7 || issue.Name != "Issue Name" || issue.IssueNumber != "1" {
		t.Errorf("issue mismatch: %+v", issue)
	}
	if issue.CoverDate != "2020-01-15" || issue.StoreDate != "2020-01-08" {
		t.Errorf("dates mismatch: cover=%q store=%q", issue.CoverDate, issue.StoreDate)
	}
	if issue.ImageURL != "http://img/m.jpg" {
		t.Errorf("ImageURL = %q, want medium url", issue.ImageURL)
	}
	if issue.URL != "https://comicvine.gamespot.com/fables-1/4000-7/" {
		t.Errorf("issue URL = %q", issue.URL)
	}
	if issue.Writers != "Alice Writer" {
		t.Errorf("Writers = %q, want %q (deduped)", issue.Writers, "Alice Writer")
	}
	if issue.Artists != "Bob Artist" {
		t.Errorf("Artists = %q, want %q", issue.Artists, "Bob Artist")
	}
	if !strings.Contains(issue.Description, "Something happens.") || !strings.Contains(issue.Description, "Then more.") {
		t.Errorf("Description = %q, missing expected text", issue.Description)
	}
	if strings.Contains(issue.Description, "<") {
		t.Errorf("Description = %q, still contains HTML", issue.Description)
	}
}

func TestAPIErrorStatusCode(t *testing.T) {
	const body = `{"error": "Object Not Found", "status_code": 101, "results": []}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)

	_, err := c.GetIssue(999)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Object Not Found") {
		t.Errorf("error = %v, want it to contain %q", err, "Object Not Found")
	}
}

func TestDownloadImage(t *testing.T) {
	imgBytes := []byte{0xFF, 0xD8, 0xFF, 0xD9} // minimal fake jpeg bytes

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != userAgent {
			t.Errorf("User-Agent = %q, want %q", got, userAgent)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(imgBytes)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)

	data, err := c.DownloadImage(srv.URL + "/cover.jpg")
	if err != nil {
		t.Fatalf("DownloadImage returned error: %v", err)
	}
	if string(data) != string(imgBytes) {
		t.Errorf("data = %v, want %v", data, imgBytes)
	}
}

func TestDownloadImageRejectsNonImageContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>not an image</html>")
	}))
	defer srv.Close()

	c := newTestClient(t, srv)

	if _, err := c.DownloadImage(srv.URL + "/notanimage"); err == nil {
		t.Fatal("expected error for non-image content type, got nil")
	}
}

func TestRateLimiter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"error":"OK","status_code":1,"results":[]}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	c.minInterval = 300 * time.Millisecond

	if _, err := c.SearchVolumes("a"); err != nil {
		t.Fatalf("first SearchVolumes returned error: %v", err)
	}

	start := time.Now()
	if _, err := c.SearchVolumes("b"); err != nil {
		t.Fatalf("second SearchVolumes returned error: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed < 280*time.Millisecond {
		t.Errorf("second request fired after only %v, want >= ~minInterval (%v)", elapsed, c.minInterval)
	}
}

func TestStripHTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain text", "hello world", "hello world"},
		{"simple tags", "<b>hello</b> <i>world</i>", "hello world"},
		{"br to newline", "line one<br>line two", "line one\nline two"},
		{"br self-closing", "line one<br/>line two", "line one\nline two"},
		{"br with space", "line one<br />line two", "line one\nline two"},
		{"closing p to newline", "<p>para one</p><p>para two</p>", "para one\npara two"},
		{"closing li to newline", "<ul><li>a</li><li>b</li></ul>", "a\nb"},
		{"entities", "Tom &amp; Jerry &lt;3&gt;", "Tom & Jerry <3>"},
		{"excess newlines collapsed", "a\n\n\n\n\nb", "a\n\nb"},
		{"trims whitespace", "  <p>padded</p>  ", "padded"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripHTML(tc.in)
			if got != tc.want {
				t.Errorf("StripHTML(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
