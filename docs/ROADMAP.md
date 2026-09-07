# ComicNest — v1 implementation plan

Stages are ordered so the app stays runnable and shows something after each one.
Design details: [SPECIFICATION.md](SPECIFICATION.md).

## Stage 1 — Application skeleton ✅
- [x] `go mod init`, the directory layout from spec §3
- [x] `internal/config`: loading/creating `config.yaml`
- [x] `internal/store`: opening SQLite (modernc), schema migration from §5 (`schema_version` table)
- [x] `internal/server`: HTTP server, layout.html, static assets via `embed`, home page (empty grid)
- [x] Run it: `go run ./cmd/comicnest` → the page works on the configured port

## Stage 2 — Library scanner ✅
- [x] `internal/library/scanner.go`: walk, extension filter, hybrid series assignment (folder / file-name fallback), name-parsing patterns from §6
- [x] `internal/library/archive.go`: opening CBZ (zip) and CBR (rardecode), listing images in natural order (+ a format fallback for misnamed .cbz/.cbr)
- [x] `internal/library/comicinfo.go`: ComicInfo.xml parser + field mapping from §7
- [x] `internal/covers`: first-page extraction → 400px JPEG thumbnail → `data/covers/` (+ `GET /issues/{id}/cover` falling back to a placeholder)
- [x] Overwrite rules (`metadata_source`, `metadata_locked`), flagging `file_missing`
- [x] `POST /scan` in a goroutine + `GET /scan/status` (in-memory progress, HTMX polling every 2s)
- [x] Unit tests: file-name parsing, ComicInfo mapping, archives, thumbnails

## Stage 3 — Browsing the catalog ✅
- [x] Series grid on `/` (covers, issue count, sorting; a text filter via search)
- [x] Series page `/series/{id}` with the issue list and metadata filters (all / no metadata / ComicVine / missing)
- [x] Issue details `/issues/{id}` (cover, full metadata, source badge, breadcrumbs)
- [x] `GET /issues/{id}/cover` (cache + placeholder) and `GET /issues/{id}/download` (Content-Disposition with the original name)
- [x] Search `/search` (series by name, issues by title/number)
- [x] CSS: dark theme, a responsive cover grid, issue list, detail view

## Stage 4 — Metadata editing ✅
- [x] Series and issue edit forms (full `/…/{id}/edit` pages, plain POST forms — work without JS)
- [x] Saving → `metadata_source='manual'`, `metadata_locked=1`; an unlock button (the data stays, scraping may overwrite)
- [x] Metadata-source badges and lock icons in lists (from Stage 3) + a lock next to the series name
- [x] Validation: series name required (the error is rendered in the form)

## Stage 5 — ComicVine ✅
- [x] `internal/comicvine`: the client (search/volume/issue, `field_list`, a 1 req/s rate limiter, 20s timeout, User-Agent, StripHTML for descriptions) + tests against an httptest mock
- [x] Series match page `/series/{id}/match` with a candidate list (cover, year, publisher, issue count) and a user pick; saving enriches an empty series description/publisher
- [x] Scraping a single issue (synchronously, with flash messages) and "all missing" in a series (in the background, one job at a time, HTMX progress; respecting locks); number normalization ("055" ↔ "55")
- [x] Fetching ComicVine covers into the cache (replacing archive thumbnails)
- [x] UI when there's no API key: features disabled with a configuration hint

## Stage 6 — Polish ✅
- [x] `file_missing` handling in the UI: a marker (badge + greyed out) + "Delete record" on the series list and issue page (with confirmation; records of existing files are protected — 409)
- [x] A styled error page (404/500, a catch-all for unknown routes), buffered page rendering (a clean 500 instead of a cut-off page), request logging (excluding static assets and polling) and config logging at startup
- [x] README (running it, configuration, file-naming conventions, metadata priorities)
- [x] `go vet`, tests, a Windows binary build (`comicnest.exe`)

**v1 complete.** 🎉

## Changes after v1
- [x] Matching/scraping a series also sets its name from ComicVine (unless the series is locked)
- [x] A long series description collapses to ~300 characters with a toggle (native `<details>`, no JS)
- [x] One-shot handling: the `one_shot` flag (signals: a CV volume with 1 issue, ComicInfo `Format`/`Count`, 1 issue with no number), a "one-shot" label, the tile links to the issue, scraping matches the volume's single issue; the name-parser fallback strips `(...)` groups and catches the year
- [x] A "one-shot" checkbox in series editing; a manual choice (like any edit) locks the series — automatic one-shot signals (scan, ComicVine) respect the lock
- [x] A second scan phase: automatic ComicVine scraping for issues of a matched series that still lack CV data (skipping locks and missing files); progress "Updating ComicVine… X/Y" and a counter in the scan summary ("ComicVine: N updated")
- [x] Fixed mixed-up covers after recreating the database: covers are served with `Cache-Control: no-cache` (revalidation via Last-Modified/304 instead of a 24h max-age — issue IDs get reused), and a scan removes orphaned thumbnails from `data/covers/` at startup
- [x] An OPDS 1.2 server (`opds_enabled` in the config, optional Basic auth, `listen` to expose it on the LAN): root → series (navigation, pagination) → series issues (acquisition), "Recently added", search + OpenSearch, files and covers under `/opds/…`; the `internal/opds` package + httptest tests (SPECIFICATION §9a)
- [x] OPDS under Thorium Reader: a startup message with IP addresses (Thorium rejects `localhost`). A series "shelf" view (`rel="collection"` groups, single releases as an issue) was implemented and rolled back — it made a bigger mess than a plain list in Thorium
- [x] OPDS-PSE 1.2 page streaming (`/opds/issues/{id}/pages/{n}?width=`), reading progress recorded from page requests (`reading_progress`, migration 3 for `issues.file_pages`), `pse:lastRead` in feeds, a "Currently reading" section (`/opds/reading`); `library.ExtractPage`, `covers.Resize`
- [x] Reading progress in the web UI: a bar under the cover and "read: p. X of N" on the series issue list, a progress box and a "Read" row on the issue page (`readingView`, `issueRow`)
- [x] User accounts in `config.yaml` (`users`, plaintext passwords): web login (a session cookie, `/login`, `/logout`), the same password in OPDS (Basic), per-user progress (migration 4: `reading_progress(user, issue_id)`), the first account adopting anonymous progress
- [x] Removed the `opds` section (`username`/`password`) from the config — passwords come only from `users`; `opds.enabled` replaced by the flat `opds_enabled` key
- [x] An in-browser page reader (`/issues/{id}/read`, `reader.html` + `reader.js`): zoom, paging by keyboard/click/gesture/slider, fullscreen, resuming from the last page, progress shared with OPDS (`POST /issues/{id}/progress`, pages via `?track=0`), links to the previous/next issue on the bottom bar (the "End of issue" popup was removed at the user's request)
- [x] Manually marking an issue read / unread (buttons on the issue page and the series list, `POST /issues/{id}/read|unread`, returning via `next`)
- [x] Library grid: a green ✓ badge on fully-read series and filters (unread / reading / read / no ComicVine / missing files) next to sorting; reading aggregates in `ListSeries` (`SeriesFilter`)
- [x] Library grid pagination (`?page=N`, a pager with a page window, sort/filter preserved) — tiles per page set in `config.yaml` (`page_size`, default 60, 0 = no pagination)
- [x] Docker: `Dockerfile` (a static binary in `distroless/static:nonroot`, ~15 MB), `docker-compose.yml`, `.dockerignore`; `/comics` (ro) `/config` `/data` volumes; the image on Docker Hub (`jaggred/comicnest`, amd64+arm64) from a `docker` job in the workflow. In the app: the `-config` flag / `$COMICNEST_CONFIG`, `COMICNEST_*` overrides (`Config.applyEnv`), `GET /healthz` and a `-healthcheck` mode for `HEALTHCHECK`; `PUID`/`PGID` handling (start as root, chown `/config` + `/data`, `setuid` before opening the database) after distroless's `nonroot` turned out to have no permissions on Synology volumes
- [x] GitHub Actions (`.github/workflows/go.yml`): `go vet` + `go test` on every push and PR, cgo-free binaries for windows/amd64, linux/amd64, linux/arm64, darwin/arm64 as artifacts; a push to `main` refreshes the `latest` pre-release, a `v*` tag creates a release with notes; the version from `git describe` is injected into `main.version`, logged at startup and shown in the home page footer as a link to the GitHub repository (`server.Version`, `appVersion` in templates; a local `dev` build shows no footer)
- [x] An app icon from `logo.png` (a chick reading a comic in a nest; a 1254px source in `imgs/`, variants generated by scaling after cropping to the silhouette): `favicon.png` 192px + `favicon-32.png` + `favicon.ico` + `apple-touch-icon.png` in every page's `<head>` and under `/favicon.ico` (no login needed), the logo also next to the name in the top bar and on the login page (`.brand-logo`); in OPDS as every feed's `<icon>` and as the `<Image>` in OpenSearch (`/static/favicon.png`)
- [x] Fix: a folder with several series per ComicInfo (e.g. `Mad Max/` with "Mad Max: Fury Road" and "Mad Max: Fury Road: Max") used to be treated as one series. A scan now remembers ComicInfo's `<Series>` (`issues.comicinfo_series`, migration 5; backfilled on the first scan, NULL for an unreadable archive = retried later), and pass 3 (`splitMixedFolders`) splits a folder with ≥2 distinct values: it moves only issues still sitting in the folder's series, into the series that already holds other files of the folder with the same value, or, failing that, into a virtual series named after that value — so a manual rename or a ComicVine match of the split-out series survives further scans; a value equal to the folder's name stays in the folder's series; a folder with a consistent value, or with no ComicInfo, is unchanged (SPECIFICATION §5, §6 point 2; tests in `scanner_test.go`)
- [x] Admin panel: a Statistics section (`Store.LibraryStats`, two queries) showing series/one-shot/locked-series counts, present/missing issue counts, total library size (`prettySize`), and ComicVine coverage (from ComicVine / no ComicVine metadata / locked issues)
- [x] Admin panel: a Scan History section (`scan_history` table, migration 9) recording one row per completed scan — started/finished time, files found/processed/missing, ComicVine updated/failed counts, and the error if any — written from `Scanner.run` (best-effort, never fails the scan) and listed newest-first, capped at 20
- [x] Admin panel: a Configuration section editing three `config.yaml` values from the browser — ComicVine API key (with a "Test Connection" button hitting the live API with a throwaway client, before the key is saved), `opds_enabled`, and `page_size` (`POST /admin/config`, `POST /admin/config/test-comicvine`). Page size applies immediately (`Server.setEditableConfig`/`Server.config`, guarded by a mutex since it's read on every home-page request); the ComicVine key and OPDS toggle still need a restart (the client and `/opds` routes are only built at startup — `Server.opdsRoutesRegistered` tracks what's actually mounted, separate from the saved setting shown in the form). A field pinned by its `COMICNEST_*` environment variable shows a note instead of silently doing nothing. `port`, `listen`, `library`, `data_dir` stay file-only, per user request.

## Ideas for v2 (not doing now)
- Admin panel: edit the remaining `config.yaml` values (`port`, `listen`, `library`, `data_dir`) from the browser
- Reader: a two-page side-by-side mode, manga reading direction (right→left)
- Writing metadata back into ComicInfo.xml inside the archive
- Converting PDF to CBR/CBZ
- Watching the library for changes (fsnotify)
- Deadpool Polska — ComicVine has nothing but a cover for it, so it gets ignored; it shouldn't be
- Collections / reading lists
