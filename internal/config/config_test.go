package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, yaml string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestUsersParsedAndValidated(t *testing.T) {
	cfg, err := load(t, "port: 8080\nusers:\n  - name: ania\n    password: a\n  - name: bartek\n    password: b\n")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AuthEnabled() || len(cfg.Users) != 2 || cfg.FindUser("bartek") == nil || cfg.FindUser("nobody") != nil {
		t.Errorf("users not parsed: %+v", cfg.Users)
	}

	for name, yaml := range map[string]string{
		"missing password": "users:\n  - name: ania\n",
		"missing name":     "users:\n  - password: x\n",
		"duplicate":        "users:\n  - name: a\n    password: x\n  - name: a\n    password: y\n",
		"colon in name":    "users:\n  - name: \"a:b\"\n    password: x\n",
	} {
		if _, err := load(t, yaml); err == nil {
			t.Errorf("%s: expected a config error", name)
		}
	}
}

func TestOPDSEnabledFlag(t *testing.T) {
	cfg, err := load(t, "port: 8080\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OPDSEnabled {
		t.Error("opds_enabled should default to false")
	}
	cfg, err = load(t, "opds_enabled: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.OPDSEnabled {
		t.Error("opds_enabled: true not parsed")
	}
}

func TestNoUsersMeansOpen(t *testing.T) {
	cfg, err := load(t, "port: 8080\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthEnabled() {
		t.Error("no users → auth disabled")
	}
	if !strings.EqualFold(cfg.Listen, "localhost") {
		t.Errorf("default listen = %q", cfg.Listen)
	}
}

func TestPageSize(t *testing.T) {
	cfg, err := load(t, "port: 8080\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PageSize != DefaultPageSize {
		t.Errorf("default page_size = %d, want %d", cfg.PageSize, DefaultPageSize)
	}
	cfg, err = load(t, "page_size: 24\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PageSize != 24 {
		t.Errorf("page_size: 24 not parsed, got %d", cfg.PageSize)
	}
	if cfg, err := load(t, "page_size: 0\n"); err != nil || cfg.PageSize != 0 {
		t.Errorf("page_size: 0 (no pagination) should be accepted: %d, %v", cfg.PageSize, err)
	}
	if _, err := load(t, "page_size: -1\n"); err == nil {
		t.Error("negative page_size should be a config error")
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("COMICNEST_LISTEN", "0.0.0.0")
	t.Setenv("COMICNEST_LIBRARY", "/comics")
	t.Setenv("COMICNEST_DATA_DIR", "/data")
	t.Setenv("COMICNEST_PORT", "9090")
	t.Setenv("COMICNEST_PAGE_SIZE", "12")
	t.Setenv("COMICNEST_OPDS_ENABLED", "true")
	t.Setenv("COMICNEST_COMICVINE_API_KEY", "k-from-env")

	// Existing file: env wins over file values.
	cfg, err := load(t, "port: 8080\nlisten: localhost\nlibrary: D:/x\ncomicvine_api_key: k-file\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "0.0.0.0" || cfg.Library != "/comics" || cfg.DataDir != "/data" || cfg.Port != 9090 ||
		cfg.PageSize != 12 || !cfg.OPDSEnabled || cfg.ComicVineAPIKey != "k-from-env" {
		t.Errorf("env overrides not applied: %+v", cfg)
	}

	// Missing file: defaults + env are written to disk, so the created file
	// already points at the right places.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"listen: 0.0.0.0", "library: /comics", "data_dir: /data", "port: 9090"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("created config missing %q:\n%s", want, raw)
		}
	}

	// Empty value = not set; garbage is an error.
	t.Setenv("COMICNEST_PORT", "")
	if cfg, err := load(t, "port: 8081\n"); err != nil || cfg.Port != 8081 {
		t.Errorf("empty env should be ignored: %d, %v", cfg.Port, err)
	}
	t.Setenv("COMICNEST_PORT", "abc")
	if _, err := load(t, "port: 8081\n"); err == nil {
		t.Error("non-numeric COMICNEST_PORT should be an error")
	}
	t.Setenv("COMICNEST_PORT", "")
	t.Setenv("COMICNEST_OPDS_ENABLED", "maybe")
	if _, err := load(t, "port: 8081\n"); err == nil {
		t.Error("non-boolean COMICNEST_OPDS_ENABLED should be an error")
	}
}
