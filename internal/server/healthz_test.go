package server

import (
	"net/http"
	"testing"
)

func TestHealthzIsOpen(t *testing.T) {
	srv := newAuthTestServer(t) // accounts configured: everything else redirects to /login
	rec := get(t, srv.Handler(), "/healthz", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("/healthz: %d %q", rec.Code, rec.Body.String())
	}
}
