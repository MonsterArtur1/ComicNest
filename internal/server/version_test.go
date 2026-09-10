package server

import (
	"strings"
	"testing"
)

func TestVersionFooterOnlyForStampedBuilds(t *testing.T) {
	defer func(v string) { Version = v }(Version)

	Version = "dev"
	srv, _ := newTestServer(t, false)
	if body := get(t, srv.Handler(), "/", nil).Body.String(); strings.Contains(body, "site-footer") {
		t.Error("local build should not render the version footer")
	}

	Version = "v1.2.3-4-gabcdef0"
	srv, _ = newTestServer(t, false)
	body := get(t, srv.Handler(), "/", nil).Body.String()
	if !strings.Contains(body, `<footer class="site-footer"`) ||
		!strings.Contains(body, `href="https://github.com/MonsterArtur1/ComicNest"`) ||
		!strings.Contains(body, "ComicNest v1.2.3-4-gabcdef0") {
		t.Errorf("stamped build should render the footer with a GitHub link:\n%s", body)
	}
	// The login page carries the footer too, since it's the only page an
	// unauthenticated visitor ever sees.
	mustCreateUser(t, srv.store, "ania", "x", false)
	if body := get(t, srv.Handler(), "/login", nil).Body.String(); !strings.Contains(body, "site-footer") {
		t.Error("login page should render the version footer")
	}
}
