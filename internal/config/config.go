package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// User is an account defined in config.yaml. The same credentials log into
// the web UI and authenticate OPDS readers; reading progress is per user.
type User struct {
	Name     string `yaml:"name"`
	Password string `yaml:"password"` // plain text, by design (personal LAN app)
}

// Config holds all application settings, loaded from config.yaml.
type Config struct {
	Port int `yaml:"port"`
	// Listen is the interface to bind to. "localhost" keeps the app private to
	// this machine; "0.0.0.0" exposes it on the LAN (needed for OPDS readers on
	// phones/tablets — define users then, so the UI is behind a login).
	Listen          string `yaml:"listen"`
	Library         string `yaml:"library"`
	DataDir         string `yaml:"data_dir"`
	ComicVineAPIKey string `yaml:"comicvine_api_key"`
	// OPDSEnabled turns on the OPDS catalog for external comic readers. The
	// catalog is protected with HTTP Basic auth using the `users` accounts.
	OPDSEnabled bool `yaml:"opds_enabled"`
	// Users enables login. Empty = no accounts: the UI and OPDS are open and
	// everything is tracked for one anonymous reader.
	Users []User `yaml:"users"`
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
		Users:           nil,
		PageSize:        DefaultPageSize,
	}
}

// AuthEnabled reports whether any user accounts are configured.
func (c Config) AuthEnabled() bool { return len(c.Users) > 0 }

// FindUser returns the user with the given name, or nil.
func (c Config) FindUser(name string) *User {
	for i := range c.Users {
		if c.Users[i].Name == name {
			return &c.Users[i]
		}
	}
	return nil
}

// Load reads the config file at path. If the file does not exist, a default
// config is written there and returned, so the user has a file to fill in.
func Load(path string) (Config, error) {
	cfg := defaults()

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if werr := save(path, cfg); werr != nil {
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
	if err := cfg.normalizeUsers(path); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// normalizeUsers validates accounts: names required and unique, passwords
// required.
func (c *Config) normalizeUsers(path string) error {
	seen := make(map[string]bool)
	for i, u := range c.Users {
		u.Name = strings.TrimSpace(u.Name)
		c.Users[i].Name = u.Name
		switch {
		case u.Name == "":
			return fmt.Errorf("users[%d]: name is required in %s", i, path)
		case strings.ContainsAny(u.Name, ":\n\r"):
			return fmt.Errorf("users[%d]: name %q must not contain ':' (HTTP Basic auth)", i, u.Name)
		case u.Password == "":
			return fmt.Errorf("users[%d] (%s): password is required in %s", i, u.Name, path)
		case seen[u.Name]:
			return fmt.Errorf("users: duplicate name %q in %s", u.Name, path)
		}
		seen[u.Name] = true
	}
	return nil
}

func save(path string, cfg Config) error {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
