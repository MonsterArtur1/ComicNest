package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds all application settings, loaded from config.yaml. User
// accounts are not part of it — they live in the database (see
// internal/store/users.go) and are managed from the admin panel (/admin) at
// runtime, not by editing this file.
type Config struct {
	Port int `yaml:"port"`
	// Listen is the interface to bind to. "localhost" keeps the app private to
	// this machine; "0.0.0.0" exposes it on the LAN (needed for OPDS readers on
	// phones/tablets — create an admin account then, so the UI is behind a login).
	Listen  string `yaml:"listen"`
	Library string `yaml:"library"`
	// Libraries, when non-empty, splits the catalog into multiple independent
	// libraries — one per path — each with its own scan button and stats in
	// the admin panel, and a switcher in the main menu ("all" aggregates
	// every library there). It takes precedence over Library; when empty,
	// Library (if set) is used as the sole library, so existing single-folder
	// configs keep working unchanged. OPDS is unaffected either way: it
	// always serves one combined catalog across every configured library.
	Libraries       []string `yaml:"libraries,omitempty"`
	DataDir         string   `yaml:"data_dir"`
	ComicVineAPIKey string   `yaml:"comicvine_api_key"`
	// OPDSEnabled turns on the OPDS catalog for external comic readers. The
	// catalog is protected with HTTP Basic auth using the accounts table.
	OPDSEnabled bool `yaml:"opds_enabled"`
	// PageSize is the number of series tiles per page of the library grid.
	// 0 disables pagination (everything on one page).
	PageSize int `yaml:"page_size"`
}

// DefaultPageSize is used when config.yaml does not set page_size.
const DefaultPageSize = 60

func defaults() Config {
	return Config{
		Port:            8080,
		Listen:          "localhost",
		Library:         "",
		DataDir:         "./data",
		ComicVineAPIKey: "",
		OPDSEnabled:     false,
		PageSize:        DefaultPageSize,
	}
}

// EnvPrefix is the prefix of environment variables that override config.yaml
// (COMICNEST_LISTEN, COMICNEST_PORT, COMICNEST_LIBRARY, COMICNEST_DATA_DIR,
// COMICNEST_COMICVINE_API_KEY, COMICNEST_OPDS_ENABLED, COMICNEST_PAGE_SIZE).
// The Docker image uses them to point at its volumes; they also let the API
// key live outside the file.
const EnvPrefix = "COMICNEST_"

// Load reads the config file at path and applies environment overrides. If
// the file does not exist, a default config (with the overrides baked in) is
// written there and returned, so the user has a file to fill in.
func Load(path string) (Config, error) {
	cfg := defaults()

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := cfg.applyEnv(); err != nil {
			return cfg, err
		}
		if werr := Save(path, cfg); werr != nil {
			return cfg, fmt.Errorf("writing default config: %w", werr)
		}
		fmt.Printf("created default config at %s — set 'library' to your comics folder\n", path)
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("reading config: %w", err)
	}

	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := cfg.applyEnv(); err != nil {
		return cfg, err
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return cfg, fmt.Errorf("invalid port %d in %s", cfg.Port, path)
	}
	if cfg.Listen == "" {
		cfg.Listen = defaults().Listen
	}
	if cfg.DataDir == "" {
		cfg.DataDir = defaults().DataDir
	}
	if cfg.PageSize < 0 {
		return cfg, fmt.Errorf("invalid page_size %d in %s (0 = no pagination)", cfg.PageSize, path)
	}
	cfg.Libraries = cleanLibraries(cfg.Libraries)
	seen := make(map[string]bool, len(cfg.Libraries))
	for _, p := range cfg.Libraries {
		key := strings.ToLower(p)
		if seen[key] {
			return cfg, fmt.Errorf("duplicate path %q in libraries (%s)", p, path)
		}
		seen[key] = true
	}
	return cfg, nil
}

// cleanLibraries trims whitespace and drops blank entries.
func cleanLibraries(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// LibraryPaths returns the configured library roots: Libraries when set,
// otherwise a single-element slice built from Library (or nil when neither
// is configured). Every caller that needs to iterate the app's libraries
// goes through this, so the two config styles are indistinguishable past
// this point.
func (c Config) LibraryPaths() []string {
	if len(c.Libraries) > 0 {
		return c.Libraries
	}
	if c.Library != "" {
		return []string{c.Library}
	}
	return nil
}

// applyEnv overrides fields from COMICNEST_* environment variables (empty
// values are ignored).
func (c *Config) applyEnv() error {
	str := func(key string, dst *string) {
		if v, ok := os.LookupEnv(EnvPrefix + key); ok && v != "" {
			*dst = v
		}
	}
	str("LISTEN", &c.Listen)
	str("LIBRARY", &c.Library)
	str("DATA_DIR", &c.DataDir)
	str("COMICVINE_API_KEY", &c.ComicVineAPIKey)

	for _, f := range []struct {
		key string
		dst *int
	}{{"PORT", &c.Port}, {"PAGE_SIZE", &c.PageSize}} {
		v, ok := os.LookupEnv(EnvPrefix + f.key)
		if !ok || v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s%s: %q is not a number", EnvPrefix, f.key, v)
		}
		*f.dst = n
	}
	if v, ok := os.LookupEnv(EnvPrefix + "OPDS_ENABLED"); ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("%sOPDS_ENABLED: %q is not a boolean", EnvPrefix, v)
		}
		c.OPDSEnabled = b
	}
	return nil
}

// Save writes cfg to path as YAML, overwriting whatever is there. Used both
// to create the initial file and by the admin panel, which edits a few
// fields (ComicVineAPIKey, OPDSEnabled, PageSize) from the browser.
func Save(path string, cfg Config) error {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
