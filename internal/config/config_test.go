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
		"opds half pair":   "opds:\n  username: artur\n",
	} {
		if _, err := load(t, yaml); err == nil {
			t.Errorf("%s: expected a config error", name)
		}
	}
}

func TestLegacyOPDSCredentialsBecomeUser(t *testing.T) {
	cfg, err := load(t, "opds:\n  enabled: true\n  username: artur\n  password: sekret\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Users) != 1 || cfg.Users[0] != (User{Name: "artur", Password: "sekret"}) {
		t.Errorf("legacy opds credentials should become the single user, got %+v", cfg.Users)
	}
	// With users defined, the legacy pair is ignored.
	cfg, err = load(t, "opds:\n  username: old\n  password: old\nusers:\n  - name: ania\n    password: a\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Users) != 1 || cfg.Users[0].Name != "ania" {
		t.Errorf("users should take precedence over legacy opds credentials: %+v", cfg.Users)
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
