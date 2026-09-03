package main

import (
	"log"
	"os"
	"path/filepath"

	"comicnest/internal/comicvine"
	"comicnest/internal/config"
	"comicnest/internal/covers"
	"comicnest/internal/library"
	"comicnest/internal/server"
	"comicnest/internal/store"
	"comicnest/internal/translator"
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
	if cfg.TranslatorURL == "" {
		log.Printf("translator: disabled (no translator_url in config.yaml)")
	} else {
		log.Printf("translator: %s (engine %s, lang %s)", cfg.TranslatorURL, cfg.TranslatorEngine, cfg.TranslatorLang)
	}

	coverCache := covers.New(coversDir)
	scanner := library.NewScanner(st, coverCache, cfg.Library)

	srv, err := server.New(cfg, st, coverCache, scanner, comicvine.New(cfg.ComicVineAPIKey),
		translator.New(cfg.TranslatorURL, cfg.TranslatorEngine, cfg.TranslatorLang))
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	log.Fatal(srv.ListenAndServe())
}
