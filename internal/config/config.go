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

// OPDSConfig controls the OPDS catalog for external comic readers.
type OPDSConfig struct {
	Enabled bool `yaml:"enabled"`
	// Deprecated: Username/Password define a single catalog login. When
	// `users` is empty they are turned into the only user account, so old
	// configs keep working; with `users` set they are ignored.
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
}

// Config holds all application settings, loaded from config.yaml.
type Config struct {
	Port int `yaml:"port"`
	// Listen is the interface to bind to. "localhost" keeps the app private to
	// this machine; "0.0.0.0" exposes it on the LAN (needed for OPDS readers on
	// phones/tablets — define users then, so the UI is behind a login).
	Listen          string     `yaml:"listen"`
	Library         string     `yaml:"library"`
	DataDir         string     `yaml:"data_dir"`
	ComicVineAPIKey string     `yaml:"comicvine_api_key"`
	OPDS            OPDSConfig `yaml:"opds"`
	// Users enables login. Empty = no accounts: the UI and OPDS are open and
	// everything is tracked for one anonymous reader.
	Users []User `yaml:"users"`
}

func defaults() Config {
	return Config{
		Port:            8080,
		Listen:          "localhost",
		Library:         "",
		DataDir:         "./data",
		ComicVineAPIKey: "",
		OPDS:            OPDSConfig{Enabled: false},
		Users:           nil,
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
	if err := cfg.normalizeUsers(path); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// normalizeUsers validates accounts and folds the legacy opds.username /
// opds.password pair into the user list when no users are defined.
func (c *Config) normalizeUsers(path string) error {
	if (c.OPDS.Username == "") != (c.OPDS.Password == "") {
		return fmt.Errorf("opds: username and password must be set together in %s", path)
	}
	if len(c.Users) == 0 && c.OPDS.Username != "" {
		c.Users = []User{{Name: c.OPDS.Username, Password: c.OPDS.Password}}
	}
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
