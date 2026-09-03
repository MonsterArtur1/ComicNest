package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"comicnest/internal/comicvine"
	"comicnest/internal/config"
	"comicnest/internal/covers"
	"comicnest/internal/library"
	"comicnest/internal/server"
	"comicnest/internal/store"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	coversDir := filepath.Join(cfg.DataDir, "covers")
	if err := os.MkdirAll(coversDir, 0o755); err != nil {
		log.Fatalf("creating data dir: %v", err)
	}

	dbPath := filepath.Join(cfg.DataDir, "database.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	log.Printf("library: %s", cfg.Library)
	log.Printf("database: %s", dbPath)
	if cfg.ComicVineAPIKey == "" {
		log.Printf("comicvine: disabled (no API key in config.yaml)")
	} else {
		log.Printf("comicvine: enabled")
	}
	if cfg.AuthEnabled() {
		names := make([]string, len(cfg.Users))
		for i, u := range cfg.Users {
			names[i] = u.Name
		}
		log.Printf("users: %s (web login + OPDS basic auth)", strings.Join(names, ", "))
		// Progress recorded before accounts existed belongs to the first user.
		if moved, err := st.AdoptAnonymousProgress(cfg.Users[0].Name); err != nil {
			log.Printf("users: adopting anonymous progress: %v", err)
		} else if moved > 0 {
			log.Printf("users: %d reading-progress record(s) assigned to %s", moved, cfg.Users[0].Name)
		}
	} else {
		log.Printf("users: none configured — no login, single anonymous reader")
	}
	switch {
	case !cfg.OPDS.Enabled:
		log.Printf("opds: disabled (set opds.enabled: true in config.yaml)")
	case cfg.AuthEnabled():
		log.Printf("opds: enabled (basic auth with user accounts)")
	default:
		log.Printf("opds: enabled (no auth)")
	}
	if cfg.OPDS.Enabled && cfg.Listen == "localhost" {
		log.Printf("opds: listening on localhost only — set listen: 0.0.0.0 to reach the catalog from other devices")
	}

	coverCache := covers.New(coversDir)
	scanner := library.NewScanner(st, coverCache, cfg.Library)

	srv, err := server.New(cfg, st, coverCache, scanner, comicvine.New(cfg.ComicVineAPIKey))
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	log.Fatal(srv.ListenAndServe())
}
