package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds all application settings, loaded from config.yaml.
type Config struct {
	Port            int    `yaml:"port"`
	Library         string `yaml:"library"`
	DataDir         string `yaml:"data_dir"`
	ComicVineAPIKey string `yaml:"comicvine_api_key"`
}

func defaults() Config {
	return Config{
		Port:            8080,
		Library:         "",
		DataDir:         "./data",
		ComicVineAPIKey: "",
	}
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
	if cfg.DataDir == "" {
		cfg.DataDir = defaults().DataDir
	}
	return cfg, nil
}

func save(path string, cfg Config) error {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
