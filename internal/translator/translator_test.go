package translator

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDisabledClient(t *testing.T) {
	c := New("", "", "")
	if c.Enabled() {
		t.Fatal("client with empty URL should be disabled")
	}
	if _, err := c.TranslatePage([]byte("x"), "p.png"); err != ErrDisabled {
		t.Fatalf("want ErrDisabled, got %v", err)
	}
}

func TestTranslatePage(t *testing.T) {
	want := []byte("PNGDATA")
	var gotConfig pageConfig
	var gotFilename string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/translate/with-form/image" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		f, hdr, err := r.FormFile("image")
		if err != nil {
			t.Fatalf("image field: %v", err)
		}
		defer f.Close()
		gotFilename = hdr.Filename
		img, _ := io.ReadAll(f)
		if string(img) != "IMGDATA" {
			t.Errorf("image payload = %q", img)
		}
		if err := json.Unmarshal([]byte(r.FormValue("config")), &gotConfig); err != nil {
			t.Fatalf("config field: %v", err)
		}
		w.Write(want)
	}))
	defer srv.Close()

	c := New(srv.URL+"/", "chatgpt", "POL")
	out, err := c.TranslatePage([]byte("IMGDATA"), "page1.png")
	if err != nil {
		t.Fatalf("TranslatePage: %v", err)
	}
	if string(out) != string(want) {
		t.Errorf("result = %q, want %q", out, want)
	}
	if gotFilename != "page1.png" {
		t.Errorf("filename = %q", gotFilename)
	}
	if gotConfig.Translator.Translator != "chatgpt" || gotConfig.Translator.TargetLang != "POL" {
		t.Errorf("config = %+v", gotConfig)
	}
}

func TestServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "chatgpt", "POL")
	_, err := c.TranslatePage([]byte("x"), "p.png")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want error containing server message, got %v", err)
	}
}

func TestEmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	c := New(srv.URL, "chatgpt", "POL")
	if _, err := c.TranslatePage([]byte("x"), "p.png"); err == nil {
		t.Fatal("want error on empty body")
	}
}
