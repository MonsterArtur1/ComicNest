package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"comicnest/internal/comicvine"
	"comicnest/internal/config"
	"comicnest/internal/covers"
	"comicnest/internal/library"
	"comicnest/internal/store"
)

// newMultiLibraryTestServer builds a Server configured with two libraries
// ("LibA" holding series "Alpha", "LibB" holding series "Beta"), for testing
// the admin panel's per-library layout and the main menu's library switcher.
func newMultiLibraryTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	rootA := filepath.Join(dir, "LibA")
	rootB := filepath.Join(dir, "LibB")

	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	seed := func(root, seriesName, fileName string) {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		id, err := st.FindOrCreateSeriesByFolder(seriesName, seriesName, root)
		if err != nil {
			t.Fatal(err)
		}
		cbz := filepath.Join(root, fileName)
		writeTestCBZ(t, cbz, 1)
		if err := st.InsertIssue(&store.Issue{SeriesID: id, Path: cbz, IssueNumber: "1",
			MetadataSource: store.SourceFilename, FilePages: 1}); err != nil {
			t.Fatal(err)
		}
	}
	seed(rootA, "Alpha", "Alpha 001.cbz")
	seed(rootB, "Beta", "Beta 001.cbz")

	coversDir := filepath.Join(dir, "covers")
	if err := os.MkdirAll(coversDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := covers.New(coversDir)
	cfg := config.Config{Port: 8080, Listen: "localhost", Libraries: []string{rootA, rootB}, DataDir: dir}
	scanners := map[string]*library.Scanner{
		rootA: library.NewScanner(st, cache, rootA),
		rootB: library.NewScanner(st, cache, rootB),
	}
	srv, err := New(cfg, filepath.Join(dir, "config.yaml"), st, cache, scanners, comicvine.New(""))
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// TestAdminMultiLibraryLayout checks that the admin panel switches to a
// per-library layout (one panel per configured library, plus a "Scan All"
// area) when more than one library is configured, and keeps the original
// single combined layout otherwise.
func TestAdminMultiLibraryLayout(t *testing.T) {
	srv := newMultiLibraryTestServer(t)
	h := srv.Handler()
	mustCreateUser(t, srv.store, "admin", "adminpass", true)
	c := login(t, h, "admin", "adminpass")

	body := get(t, h, "/admin", asUser(c)).Body.String()
	for _, want := range []string{"LibA", "LibB", "scan-area-all", "scan-area-0", "scan-area-1", "All libraries"} {
		if !strings.Contains(body, want) {
			t.Errorf("multi-library admin panel missing %q:\n%s", want, body)
		}
	}

	// Single/no-library mode keeps the original layout: no per-library
	// panels, no "Scan All" button.
	single, _ := newTestServer(t, false)
	hs := single.Handler()
	mustCreateUser(t, single.store, "admin", "adminpass", true)
	cs := login(t, hs, "admin", "adminpass")
	singleBody := get(t, hs, "/admin", asUser(cs)).Body.String()
	if strings.Contains(singleBody, "scan-area-all") || strings.Contains(singleBody, "All libraries") {
		t.Errorf("single-library admin panel should not show the multi-library layout:\n%s", singleBody)
	}
	if !strings.Contains(singleBody, `id="scan-area"`) {
		t.Errorf("single-library admin panel should keep the original scan-area:\n%s", singleBody)
	}
}

// TestLibrarySwitcher checks that POST /library remembers a valid selection
// via cookie, scoping the home grid to it, and that clearing/invalid values
// fall back to "all libraries".
func TestLibrarySwitcher(t *testing.T) {
	srv := newMultiLibraryTestServer(t)
	h := srv.Handler()

	rootA := srv.libraries[0].Path
	rec := postForm(t, h, "/library", "lib=0")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /library: got %d\n%s", rec.Code, rec.Body)
	}
	var libCookie *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == libraryCookie {
			libCookie = ck
		}
	}
	if libCookie == nil || libCookie.Value != "0" {
		t.Fatalf("expected %s cookie set to index 0, got %+v", libraryCookie, libCookie)
	}
	if got := srv.libraries[0].Path; got != rootA {
		t.Fatalf("sanity: libraries[0].Path = %q, want %q", got, rootA)
	}

	body := get(t, h, "/", asUser(libCookie)).Body.String()
	if !strings.Contains(body, "Alpha") || strings.Contains(body, "Beta") {
		t.Errorf("home should show only library A's series once selected:\n%s", body)
	}

	// Clearing the selection (empty value) resets to "all libraries".
	rec = postForm(t, h, "/library", "lib=")
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == libraryCookie && ck.Value != "" {
			t.Errorf("empty lib should clear the cookie, got %q", ck.Value)
		}
	}

	// An out-of-range index is rejected the same way.
	rec = postForm(t, h, "/library", "lib=99")
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == libraryCookie && ck.Value != "" {
			t.Errorf("unknown lib should clear the cookie, got %q", ck.Value)
		}
	}
}

// TestAdminRenameLibrary checks that renaming a library persists an override
// to config.yaml and applies immediately (admin heading, main-menu switcher)
// without a restart, and that resubmitting the folder's own default name
// clears the override again instead of storing a redundant one.
func TestAdminRenameLibrary(t *testing.T) {
	srv := newMultiLibraryTestServer(t)
	h := srv.Handler()
	mustCreateUser(t, srv.store, "admin", "adminpass", true)
	c := login(t, h, "admin", "adminpass")
	rootA := srv.libraries[0].Path

	rec := postAs(t, h, c, "/admin/libraries/0/name", "name=Marvel Comics")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rename: got %d\n%s", rec.Code, rec.Body)
	}
	if got := srv.libraryName(rootA); got != "Marvel Comics" {
		t.Errorf("libraryName(rootA) after rename = %q, want %q", got, "Marvel Comics")
	}
	if body := get(t, h, "/admin", asUser(c)).Body.String(); !strings.Contains(body, "Marvel Comics") {
		t.Errorf("admin panel should show the renamed library:\n%s", body)
	}
	if body := get(t, h, "/", asUser(c)).Body.String(); !strings.Contains(body, "Marvel Comics") {
		t.Errorf("main-menu switcher should show the renamed library:\n%s", body)
	}

	// Submitting the folder's own default name clears the override again.
	def := libraryFolderName(rootA)
	if rec := postAs(t, h, c, "/admin/libraries/0/name", "name="+def); rec.Code != http.StatusSeeOther {
		t.Fatalf("clear rename: got %d\n%s", rec.Code, rec.Body)
	}
	if got := srv.libraryName(rootA); got != def {
		t.Errorf("libraryName(rootA) after clearing = %q, want %q", got, def)
	}

	// An out-of-range index is a 404, not a panic.
	if rec := postAs(t, h, c, "/admin/libraries/99/name", "name=x"); rec.Code != http.StatusNotFound {
		t.Errorf("rename with bad index: got %d, want 404", rec.Code)
	}
}
