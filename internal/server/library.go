package server

import (
	"net/http"
	"strconv"
)

// libraryCookie remembers the main menu's library selection across requests
// ("" or absent = "all libraries").
const libraryCookie = "comicnest_library"

// libraryPaths returns every configured library's root path, in config
// order (empty when nothing is configured).
func (s *Server) libraryPaths() []string {
	paths := make([]string, len(s.libraries))
	for i, l := range s.libraries {
		paths[i] = l.Path
	}
	return paths
}

// libraryByIndex resolves the ?lib= query value used by the admin panel's
// per-library scan buttons: a config-order index, or "all". ok is false for
// anything else (including on a single/no-library setup, where the concept
// doesn't apply).
func (s *Server) libraryByIndex(v string) (lib libraryInfo, all, ok bool) {
	if v == "all" {
		return libraryInfo{}, true, true
	}
	i, err := strconv.Atoi(v)
	if err != nil || i < 0 || i >= len(s.libraries) {
		return libraryInfo{}, false, false
	}
	return s.libraries[i], false, true
}

// selectedLibrary returns the library path chosen from the main menu ("" =
// "all libraries"), from the cookie set by handleSetLibrary. The cookie holds
// a config-order index rather than the path itself — an arbitrary filesystem
// path (backslashes on Windows, say) is not a valid cookie value and would
// otherwise get silently mangled by net/http's cookie sanitizer. A cookie
// naming an index that no longer exists (config changed) falls back to "all"
// rather than silently showing an empty grid.
func (s *Server) selectedLibrary(r *http.Request) string {
	if r == nil || len(s.libraries) == 0 {
		return ""
	}
	c, err := r.Cookie(libraryCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	i, err := strconv.Atoi(c.Value)
	if err != nil || i < 0 || i >= len(s.libraries) {
		return ""
	}
	return s.libraries[i].Path
}

// handleSetLibrary sets (or clears, for "all libraries") the main menu's
// library selection and returns to the library grid. lib is a config-order
// index (see selectedLibrary), matching the admin panel's own ?lib= scheme.
func (s *Server) handleSetLibrary(w http.ResponseWriter, r *http.Request) {
	val := r.FormValue("lib")
	if val != "" {
		if i, err := strconv.Atoi(val); err != nil || i < 0 || i >= len(s.libraries) {
			val = ""
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     libraryCookie,
		Value:    val,
		Path:     "/",
		MaxAge:   60 * 60 * 24 * 365,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
