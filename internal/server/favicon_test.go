package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestFaviconServedWithoutLogin(t *testing.T) {
	srv := newAuthTestServer(t)
	h := srv.Handler()

	for _, path := range []string{"/favicon.ico", "/static/favicon-32.png", "/static/favicon.png", "/static/apple-touch-icon.png"} {
		rec := get(t, h, path, nil)
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Errorf("%s: %d (%d bytes)", path, rec.Code, rec.Body.Len())
		}
	}
	if ct := get(t, h, "/favicon.ico", nil).Header().Get("Content-Type"); !strings.Contains(ct, "icon") {
		t.Errorf("/favicon.ico content type = %q", ct)
	}

	// Every HTML document links the icon (layout, login, reader).
	c := login(t, h, "ania", "a-pass")
	for _, path := range []string{"/", "/login", "/issues/1/read"} {
		var body string
		if path == "/login" {
			body = get(t, h, path, nil).Body.String()
		} else {
			body = get(t, h, path, asUser(c)).Body.String()
		}
		if !strings.Contains(body, `<link rel="icon" href="/static/favicon-32.png"`) {
			t.Errorf("%s: no favicon link", path)
		}
	}
}

func TestOPDSFeedsCarryIcon(t *testing.T) {
	srv, _ := newTestServer(t, true)
	h := srv.Handler()
	for _, path := range []string{"/opds", "/opds/series", "/opds/series/1", "/opds/recent"} {
		body := assertXML(t, get(t, h, path, nil))
		if !strings.Contains(body, "<icon>http://nas.local:8080/static/favicon.png</icon>") {
			t.Errorf("%s: no <icon>", path)
		}
	}
	body := assertXML(t, get(t, h, "/opds/opensearch.xml", nil))
	if !strings.Contains(body, `<Image height="192" width="192" type="image/png">http://nas.local:8080/static/favicon.png</Image>`) {
		t.Errorf("opensearch: no <Image>:\n%s", body)
	}
}
