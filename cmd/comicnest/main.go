package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"comicnest/internal/comicvine"
	"comicnest/internal/config"
	"comicnest/internal/covers"
	"comicnest/internal/library"
	"comicnest/internal/server"
	"comicnest/internal/store"
)

// version is stamped by the build (-ldflags "-X main.version=…"); "dev" for
// plain `go build` / `go run`.
var version = "dev"

func main() {
	// Config path: -config flag, else $COMICNEST_CONFIG, else ./config.yaml.
	defaultPath := os.Getenv(config.EnvPrefix + "CONFIG")
	if defaultPath == "" {
		defaultPath = "config.yaml"
	}
	configPath := flag.String("config", defaultPath, "path to config.yaml (also $COMICNEST_CONFIG)")
	healthcheck := flag.Bool("healthcheck", false, "probe the running server's /healthz and exit (Docker HEALTHCHECK)")
	flag.Parse()

	if *healthcheck {
		os.Exit(probeHealth(*configPath))
	}

	log.Printf("ComicNest %s", version)
	server.Version = version
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("config: %s", *configPath)

	// Containers: PUID/PGID → own the writable dirs and switch user before
	// opening anything (see privs_linux.go).
	if err := dropPrivileges(*configPath, cfg.DataDir); err != nil {
		log.Fatalf("privileges: %v", err)
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
	userCount, err := st.CountUsers()
	if err != nil {
		log.Fatalf("store: counting users: %v", err)
	}
	if userCount > 0 {
		log.Printf("users: %d account(s) (web login + OPDS basic auth) — manage at /admin", userCount)
	} else {
		log.Printf("users: none yet — app runs open; the first visitor is an anonymous admin who can create one at /admin")
	}
	switch {
	case !cfg.OPDSEnabled:
		log.Printf("opds: disabled (set opds_enabled: true in config.yaml)")
	case userCount > 0:
		log.Printf("opds: enabled (basic auth with user accounts)")
	default:
		log.Printf("opds: enabled (no auth yet)")
	}
	if cfg.OPDSEnabled && cfg.Listen == "localhost" {
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

// probeHealth GETs /healthz on the configured port and returns a process exit
// code (0 = healthy). Used as the container HEALTHCHECK, since the distroless
// image has no curl or shell.
func probeHealth(configPath string) int {
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck: config:", err)
		return 1
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", cfg.Port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.Status)
		return 1
	}
	return 0
}
