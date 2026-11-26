package server

import (
	"archive/zip"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"os"
	"testing"
	"time"

	"comicnest/internal/panels"
	"comicnest/internal/store"
)

type panelsJSON struct {
	Page   int           `json:"page"`
	Panels []panels.Rect `json:"panels"`
}

// countDetections swaps in a counting wrapper around the real detector.
func countDetections(t *testing.T) *int {
	t.Helper()
	n := 0
	orig := detectPanels
	detectPanels = func(raw []byte, rtl bool, opts panels.Options) ([]panels.Rect, error) {
		n++
		return orig(raw, rtl, opts)
	}
	t.Cleanup(func() { detectPanels = orig })
	return &n
}

func decodePanels(t *testing.T, body []byte) panelsJSON {
	t.Helper()
	var p panelsJSON
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, body)
	}
	return p
}

func TestIssuePanelsCachedAndInvalidated(t *testing.T) {
	srv, cbz := newTestServer(t, false)
	h := srv.Handler()
	calls := countDetections(t)

	rec := get(t, h, "/issues/1/pages/1/panels", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("panels: %d\n%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	p := decodePanels(t, rec.Body.Bytes())
	// The test archive's pages are tiny plain squares: one full-page panel.
	if p.Page != 1 || len(p.Panels) != 1 || p.Panels[0] != panels.FullPage {
		t.Errorf("got %+v, want page 1 with the full-page panel", p)
	}
	if *calls != 1 {
		t.Fatalf("detections after first request = %d, want 1", *calls)
	}

	// Second request: served from the cache.
	if rec := get(t, h, "/issues/1/pages/1/panels", nil); rec.Code != http.StatusOK {
		t.Fatalf("second request: %d", rec.Code)
	}
	if *calls != 1 {
		t.Errorf("detections after second request = %d, want 1 (cached)", *calls)
	}
	// The RTL order is cached separately.
	get(t, h, "/issues/1/pages/1/panels?rtl=1", nil)
	if *calls != 2 {
		t.Errorf("detections after rtl request = %d, want 2", *calls)
	}

	// The file changes on disk: the cached row no longer applies.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(cbz, later, later); err != nil {
		t.Fatal(err)
	}
	get(t, h, "/issues/1/pages/1/panels", nil)
	if *calls != 3 {
		t.Errorf("detections after file change = %d, want 3", *calls)
	}
	get(t, h, "/issues/1/pages/1/panels", nil)
	if *calls != 3 {
		t.Errorf("detections after re-caching = %d, want 3", *calls)
	}

	// New panel_detection settings: recomputed once, then cached again.
	srv.cfg.PanelDetection.Sensitivity = "high"
	get(t, h, "/issues/1/pages/1/panels", nil)
	if *calls != 4 {
		t.Errorf("detections after a sensitivity change = %d, want 4", *calls)
	}
	get(t, h, "/issues/1/pages/1/panels", nil)
	if *calls != 4 {
		t.Errorf("detections after re-caching = %d, want 4", *calls)
	}
	srv.cfg.PanelDetection.Sensitivity = ""

	// Deleting the issue cascades to its cached panels.
	if err := srv.store.DeleteIssue(1); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := srv.store.GetPagePanels(1, 1, false, pageStateOf(t, cbz)); err != nil || ok {
		t.Errorf("after deleting the issue: ok=%v err=%v, want no cached row", ok, err)
	}
}

func pageStateOf(t *testing.T, path string) store.PageFileState {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return store.PageFileState{Size: fi.Size(), MTime: fi.ModTime().Unix(), Version: panels.Options{}.CacheVersion()}
}

func TestIssuePanelsNotFound(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	calls := countDetections(t)
	for _, path := range []string{
		"/issues/1/pages/3/panels",   // past the last page
		"/issues/1/pages/-1/panels",  // negative
		"/issues/1/pages/x/panels",   // not a number
		"/issues/999/pages/0/panels", // no such issue
		"/issues/2/pages/0/panels",   // file missing on disk
	} {
		if rec := get(t, h, path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, rec.Code)
		}
	}
	if *calls != 0 {
		t.Errorf("detections = %d, want 0", *calls)
	}
}

func TestIssuePanelsRequiresLogin(t *testing.T) {
	srv, _ := newTestServer(t, false)
	mustCreateUser(t, srv.store, "alice", "secret-pass", true)
	if rec := get(t, srv.Handler(), "/issues/1/pages/0/panels", nil); rec.Code == http.StatusOK {
		t.Errorf("anonymous request with accounts present: got 200, want the login gate")
	}
}

func TestIssuePanelsDetectsGrid(t *testing.T) {
	srv, cbz := newTestServer(t, false)

	// Replace the archive with one page holding two stacked bordered panels.
	img := image.NewRGBA(image.Rect(0, 0, 600, 900))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	for _, r := range []image.Rectangle{image.Rect(30, 30, 570, 440), image.Rect(30, 470, 570, 870)} {
		draw.Draw(img, r, &image.Uniform{color.Black}, image.Point{}, draw.Src)
		draw.Draw(img, r.Inset(4), &image.Uniform{color.RGBA{200, 120, 60, 255}}, image.Point{}, draw.Src)
	}
	f, err := os.Create(cbz)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("page001.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(w, img); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	rec := get(t, srv.Handler(), "/issues/1/pages/0/panels", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("panels: %d\n%s", rec.Code, rec.Body)
	}
	p := decodePanels(t, rec.Body.Bytes())
	if len(p.Panels) != 2 {
		t.Fatalf("got %+v, want 2 panels", p)
	}
	if top, bottom := p.Panels[0], p.Panels[1]; !(top.Y < bottom.Y && top.Y+top.H <= bottom.Y+0.01) {
		t.Errorf("panels not in top-to-bottom order: %+v", p.Panels)
	}
}
