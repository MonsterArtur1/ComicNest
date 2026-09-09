# ComicNest — Technical specification (v1)

A personal comic catalog as a web application, written in Go. Inspired by Komga,
but its own, simpler thing. Successor to the **ComicsNest** concept (code in `Old/ComicsNest` —
kept only as a reference for the idea, no code is copied from it).

## 1. Goals of version 1

- One Go binary + a config file; running it starts a local web server.
- Scan the library folder (`.cbz`, `.cbr`, `.pdf`) and build a catalog: **series → issues**.
- Read metadata embedded in files (**ComicInfo.xml**).
- **Manual metadata editing** for series and issues in the browser.
- **Metadata updates from the ComicVine API** (on user request, with a match picker).
- Display covers (extracted from archives, cached on disk).
- Download the comic file on click (no built-in reader in v1).

### Out of scope for v1 (deliberately)

- An in-browser page reader (planned for v2 — the architecture shouldn't block it).
- Multiple users, login, permissions.
- Writing metadata back into ComicInfo.xml inside the archive.
- Rendering covers from PDF (PDF gets a placeholder; the file is still catalogued and downloadable).
- Automatic filesystem watching (scans are triggered manually by a button).

## 2. Technology stack

| Layer | Choice | Rationale |
|---|---|---|
| Language | Go 1.23+ | project requirement |
| HTTP | `net/http` + the stdlib router (`GET /series/{id}` patterns from Go 1.22) | zero dependencies |
| Database | SQLite via `modernc.org/sqlite` | pure Go, no cgo — trouble-free build on Windows |
| DB access | `database/sql` + hand-written queries | the schema is small; no ORM |
| Templates | `html/template` | **note:** the old project used `text/template` — an XSS hole; the new one always uses `html/template` |
| Interactivity | HTMX (a static file in `web/static/`, vendored) | editing, filters and scan status without an SPA or a build step |
| CSS | a small custom stylesheet (dark theme, cover grid) | no frameworks |
| Configuration | YAML (`gopkg.in/yaml.v3`) | as in the old project |
| CBZ | `archive/zip` (stdlib) | |
| CBR | `github.com/nwaples/rardecode/v2` | RAR reading without cgo |
| Thumbnails | `image` + `golang.org/x/image/draw` | scaling covers for the cache |
| Embedding | `embed` — templates and static assets compiled into the binary | a single binary to run |

## 3. Project structure

```
ComicNextClaude/
├── cmd/comicnest/main.go        # wiring: config → db → scanner → server
├── internal/
│   ├── config/                  # loading/saving config.yaml, defaults
│   ├── store/                   # SQLite schema, migrations, queries (SeriesStore, IssueStore)
│   ├── library/                 # scanner: walk, series/number recognition, sizes
│   │   ├── scanner.go
│   │   ├── archive.go           # opening CBZ/CBR, listing pages, extracting files
│   │   └── comicinfo.go         # ComicInfo.xml parser
│   ├── covers/                  # first-page extraction → JPEG thumbnail → cache
│   ├── comicvine/                # API client: search volume, get volume, get issue; rate limiter
│   ├── opds/                    # Atom/OPDS 1.2 types + feed and OpenSearch serialization (no HTTP/DB)
│   └── server/                  # HTTP handlers, routing, template rendering (+ opds.go: the OPDS catalog)
├── web/
│   ├── templates/               # layout.html + views + HTMX partials
│   └── static/                  # htmx.min.js, styles.css, placeholder.svg, favicon.png, favicon-32.png, favicon.ico, apple-touch-icon.png
├── docs/                        # this documentation
├── .github/workflows/go.yml     # CI: tests + binaries (Win/Linux/macOS) + Docker image (Docker Hub) on every push to main, releases from v* tags
├── Dockerfile                   # image: static binary in distroless, /comics /config /data volumes (§4a)
├── docker-compose.yml           # a run example for users
├── .dockerignore
├── imgs/                        # logo.png (the icon source, 1254 px; the favicons under web/static are scaled from it) and README screenshots
├── config_example.yaml          # an annotated config template (versioned, §4)
├── config.yaml                  # created on first start (gitignored)
└── data/                        # runtime: database.sqlite, covers/ (gitignored)
```

## 4. Configuration (`config.yaml`)

```yaml
port: 8080
listen: localhost              # "0.0.0.0" = reachable from the local network (needed by OPDS readers)
library: "D:/Library"          # comic library root
data_dir: "./data"             # sqlite database + cover cache
comicvine_api_key: ""          # empty = ComicVine features disabled (the UI says so)
opds_enabled: false            # OPDS catalog under /opds (see §9a)
page_size: 60                  # series tiles per library page; 0 = no pagination
```

When the file is missing, the app writes a default config and logs instructions to fill it in.
User accounts are **not** part of this file — they live in the database and are managed from the
admin panel in the browser (`/admin`, see below), not by editing YAML.

**The `config_example.yaml` file** (at the repo root, versioned) is the template shown to users
and the single full list of options: every key there has a comment saying what it does and what
values it accepts. **Rule:** every config change (a new key, a rename or a default-value change,
a removal) goes into `config_example.yaml` with a description in the same commit — and into the
block above and into the README. The real `config.yaml` (with the API key) stays in `.gitignore`.
The API key **never goes into the code** (the old project hardcoded it — see §10).

**User accounts and the admin panel (`/admin`).** Accounts live in the `users` table
(`internal/store/users.go`, migration 8 in `internal/store/migrate.go`): login, password hashed
with bcrypt (`golang.org/x/crypto/bcrypt`, never plaintext — unlike the old, config-based system),
an `is_admin` flag, `last_login_at` (updated on every successful web login and OPDS Basic auth).
One source of truth for both the web login (`/login` form, a session in the `comicnest_session`
cookie: HttpOnly, SameSite=Strict, 30 days, an in-memory session table — a restart logs everyone
out) and OPDS (HTTP Basic with the same name/password pairs). Reading progress is per user (§5).

*First run* — zero accounts in the table: the app runs completely open (no login), and the
anonymous visitor is treated as an administrator (`Server.isAdmin`), so they see the `/admin`
panel, the scan button and the edit/ComicVine buttons, and can create the first real account from
there. The first account created **always** becomes an administrator, regardless of the checkbox
in the form — otherwise, the moment it exists, login becomes required and there would be no way
back into the panel. At that point, reading progress recorded by the anonymous visitor
(`user = ''`) is transferred to that account (`Store.AdoptAnonymousProgress`, called from
`handleAdminCreateUser`).

*Admin privileges.* The `requireAdmin` middleware (`internal/server/auth.go`) protects: the panel
itself (`/admin/*`), library scanning (`POST /scan`), series/issue metadata editing (edit forms,
unlocking, merging series, deleting a vanished file's record), and ComicVine search/matching/
scraping. A non-admin (logged in, but without the flag) gets a 403 on these routes; the templates
(`isAdmin` in `funcMap`, plus the `IsAdmin` field on the `scrape_status.html` partial's data, which
is rendered outside the normal template set) hide the corresponding buttons, so they don't see
them at all. Reading, marking progress, browsing and searching stay available to any logged-in
user — those aren't admin functions.

*Account management* (`/admin`, admin only): an account table (login, ✓ for admins, last login —
"never" when empty, creation date) with grant/revoke admin, change password and delete actions,
plus an add-account form. The last remaining administrator cannot have their privileges revoked or
be deleted (`Store.CountAdmins`) — that would lock everyone out of the panel permanently.

The panel also shows a Statistics section (`Store.LibraryStats`): series/one-shot/locked-series
counts, present and missing issue counts, total library size on disk, and ComicVine coverage
(from ComicVine / no ComicVine metadata / locked issues). Below it, a Scan History section lists
the most recent completed scans (`scan_history` table, migration 9; see §6) — when they ran, how
long they took, files found/processed/missing, ComicVine updates, and any error.

**Library pagination (`page_size`).** The home view splits the filtered series list into pages of
`page_size` tiles (default 60; `?page=N` parameter, sort and filter preserved in pager links). `0`
disables pagination; a negative value is a config error. Does not apply to OPDS (a fixed 50
entries).

The server listens on `localhost` only by default. `listen: 0.0.0.0` exposes the app on the local
network — at that point it's worth creating an administrator account in `/admin` right away,
since without any accounts the UI (including metadata editing) is open to anyone on the network.

**Config path and environment variables.** The file location comes from the `-config` flag, then
`$COMICNEST_CONFIG`, defaulting to `./config.yaml`. The variables `COMICNEST_LISTEN`,
`COMICNEST_PORT`, `COMICNEST_LIBRARY`, `COMICNEST_DATA_DIR`, `COMICNEST_COMICVINE_API_KEY`,
`COMICNEST_OPDS_ENABLED` and `COMICNEST_PAGE_SIZE` override values from the file
(`Config.applyEnv`; an empty value means "not set", a wrong type is a startup error). When the file
doesn't exist, the default config that gets created already carries values from the environment.
Accounts have no equivalent in the file or the environment — they live only in the database. The
environment-variable mechanism exists mainly for Docker (§4a), but works everywhere.

## 4a. Docker

The image (`Dockerfile`, multi-stage): a cgo-free binary built in `golang:alpine`, copied into
`gcr.io/distroless/static` (CA certificates for ComicVine, no shell). The container starts as
root: when `PUID`/`PGID` are set (the NAS convention — Synology, Unraid, linuxserver.io) the app
takes ownership of the config directory and `data_dir` for that user (skipping entries that are
already correct, so a restart with a large cover cache is cheap) and calls
`setgroups`/`setgid`/`setuid` before opening the database (`cmd/comicnest/privs_linux.go`; a no-op
off Linux). The library is never chowned. Without `PUID`/`PGID` the process stays root — the
`--user` variant with its own directory permissions also works. Reason: volumes on NAS boxes
belong to a user account (Synology: UID 1026, GID 100), and the fixed `nonroot` user from the
image had no write access to them.
The image sets `COMICNEST_CONFIG=/config/config.yaml`, `COMICNEST_LISTEN=0.0.0.0`,
`COMICNEST_LIBRARY=/comics`, `COMICNEST_DATA_DIR=/data`, so the three volumes (`/comics`
read-only, `/config`, `/data`) are enough, and the first start creates a working `config.yaml`.
`GET /healthz` (outside login and the request log) returns `ok`; `comicnest -healthcheck` polls it
over `127.0.0.1:port` and exits 0/1 — that's the `HEALTHCHECK` in `docker-compose.yml`, since the
image has no `curl`. Publishing: the `docker` job in `.github/workflows/go.yml` builds
`linux/amd64` + `linux/arm64` (buildx + QEMU) and pushes to Docker Hub (`jaggred/comicnest`) —
`latest` and `main-<sha>` from `main`, `X.Y.Z`/`X.Y`/`X` from tags. The version reaches the image
via `--build-arg VERSION`. Usage example in `docker-compose.yml`; keep `/data` on local disk
(SQLite on SMB/NFS risks corrupting the database).

## 5. Data model (SQLite)

```sql
CREATE TABLE series (
    id                  INTEGER PRIMARY KEY,
    name                TEXT NOT NULL,            -- display name (editable)
    folder_path         TEXT UNIQUE,              -- NULL for "virtual" series from file names
    publisher           TEXT DEFAULT '',
    description         TEXT DEFAULT '',
    comicvine_volume_id INTEGER,                  -- remembered ComicVine match
    metadata_locked     INTEGER NOT NULL DEFAULT 0, -- 1 = scan/scrape won't overwrite
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE issues (
    id                 INTEGER PRIMARY KEY,
    series_id          INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    path               TEXT NOT NULL UNIQUE,      -- absolute file path
    file_size          INTEGER NOT NULL,          -- bytes; formatted in the view layer
    file_missing       INTEGER NOT NULL DEFAULT 0,-- the file disappeared during the last scan
    issue_number       TEXT DEFAULT '',           -- TEXT: numbers like "12.1", "Annual 1" happen
    title              TEXT DEFAULT '',
    summary            TEXT DEFAULT '',
    release_date       TEXT DEFAULT '',           -- ISO yyyy-mm-dd (or just the year)
    writer             TEXT DEFAULT '',
    artist             TEXT DEFAULT '',
    publisher          TEXT DEFAULT '',
    page_count         INTEGER DEFAULT 0,
    comicvine_issue_id INTEGER,
    metadata_source    TEXT NOT NULL DEFAULT 'filename', -- filename | comicinfo | comicvine | manual
    metadata_locked    INTEGER NOT NULL DEFAULT 0,       -- 1 = manually edited, don't overwrite
    has_comicinfo      INTEGER NOT NULL DEFAULT 0,
    comicinfo_series   TEXT,                      -- <Series> from ComicInfo.xml (migration 5): NULL = not checked yet, '' = none
    cover_cached       INTEGER NOT NULL DEFAULT 0,
    created_at         TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at         TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_issues_series ON issues(series_id);
```

Metadata-overwrite rule (increasing priority):
`filename` → `comicinfo` → `comicvine` → `manual`.
A scan may overwrite data of equal or lower priority; `metadata_locked=1`
(set automatically after a manual edit) blocks everything except another manual edit.

Covers don't live in the database: a cache at `data/covers/{issue_id}.jpg` (a thumbnail ~400px
wide) plus the `cover_cached` flag. A series' cover is its first issue's cover (lowest number).

**One-shots** — the `series.one_shot` column (migration 2). Signals (any one is enough): (1) the
matched ComicVine volume has `count_of_issues == 1`, (2) ComicInfo.xml has a `Format` containing
"One-Shot" or `Count=1`, (3) after a scan the series has exactly one issue with no number. A scan
clears the flag once the series gets a second issue. UI: a "one-shot" label instead of a counter,
and the grid tile links straight to the issue. Scrape: a numberless issue plus a volume with a
single issue → that one issue gets matched.

**Streaming and reading progress** (migration 3): the `issues.file_pages` column — the actual
number of image entries in the archive (0 = not counted yet; `page_count` remains ComicInfo
metadata, editable and potentially wrong) — plus the table
`reading_progress(user, issue_id → issues ON DELETE CASCADE, page, updated_at; PK (user, issue_id))`
(migration 4 — the PK used to be just `issue_id`; old rows got `user = ''`), where `user` is the
account name from `config.yaml` (`''` = the anonymous reader with no accounts), and `page` is the
last read page, 1-based (the OPDS-PSE convention). Written via `UPSERT` with `MAX(page, new)`. A
scan fills in `file_pages` for new files and for old ones still at 0. Every progress-related query
(`ListSeries` aggregates, `ListIssuesInProgress`, `ReadingProgressFor`, …) takes `user`.

**Series from ComicInfo** (migration 5): the `issues.comicinfo_series` column stores the raw
`<Series>` value from the file, independent of the (editable) series row. `NULL` = a row from
before the migration, not checked yet, **or** an archive that couldn't be read (a zip/rar error) —
in both cases the next scan reads ComicInfo again (for old rows only for this reason; it doesn't
touch locked metadata) and fills the column; `''` = the archive was read fine but has no ComicInfo
or an empty `<Series>`, and every PDF. The column exists solely for the folder-splitting rule
(§6 point 2).

## 6. Library scanning

Started by a UI button (`POST /scan`), runs in a goroutine; the UI polls status (HTMX polling
`GET /scan/status` every 2s — a progress bar and file counter). Only one scan at a time (a mutex
plus an in-memory flag).

Algorithm:

1. `filepath.WalkDir` over `config.library`, filtering by extension: `.cbz`, `.cbr`, `.pdf`.
2. **Assigning a series** (hybrid mode — folder, falling back to the file name):
   - a file in a subfolder → series = **the name of the nearest parent folder** (folder_path =
     the path relative to the library root); nested folders are allowed, only the immediate
     parent matters;
   - a file directly in the root → series from the **file name** (folder_path = NULL), the
     series created/found by name.
   - **A folder with several series** (pass 3, `Scanner.splitMixedFolders`, after file
     synchronization, before `ReconcileOneShots`): when the files currently on disk in *one*
     subfolder carry at least two distinct non-empty `<Series>` values in ComicInfo.xml (compared
     `TrimSpace`d, case-insensitively), the folder is "mixed". Only **issues still sitting in the
     folder's own series** are moved (`series.folder_path` = the file's directory); an issue that
     already lives in a virtual series is never touched again — that's what lets a manual rename
     (with a lock) or a ComicVine match of a split-out series survive further scans. Move target:
     first, the series another file of the same folder with the same (normalized) `Series` value
     already lives in (files added later join the renamed/matched series); failing that, a virtual
     series named after the value (`FindOrCreateSeriesByName`, `folder_path NULL`). Issues without
     ComicInfo, and ones whose `Series` equals the folder's name (case-insensitively), stay in the
     folder's series (a second "Mad Max" would just duplicate it; the value still counts when
     deciding whether the folder is mixed). The rule looks at the files physically present in the
     folder, so it's idempotent. A folder with a consistent `Series` value (even one different
     from the folder name), or with ComicInfo in only some files, is **not** split — folder =
     series remains the rule. Neither a metadata lock nor a ComicVine match protects against the
     first split (the lock covers metadata text). An emptied folder series stays in the database
     (lists hide series with no issues). Example: `Mad Max/` with "Mad Max: Fury Road" and
     "Mad Max: Fury Road: Max" → two series.
   - **Manual series merging** (series edit → "Merge into another series",
     `POST /series/{id}/merge`, `Store.MergeSeries`): every issue of the source series moves to
     the target (the target keeps its own metadata, only filling empty fields from the source),
     and the source is deleted. However the source used to be found — its `folder_path` (a
     folder series) or, for a virtual series, its `name` — is recorded as an alias pointing at the
     target (`series_folder_aliases` / `series_name_aliases`, migration 7); `FindOrCreateSeriesByFolder`/
     `FindOrCreateSeriesByName` check these tables before creating a new series. Without this, the
     next scan would no longer find an entry for that folder/name and would recreate it — silently
     un-merging the series that was just merged, on every scan. Aliases that used to point at the
     source (from an earlier merge) are repointed at the new target on a later merge, so merge
     chains (A→B, then B→C) also survive a scan.
3. **File name parsing** (extension stripped) — patterns tried in order:
   - `Title #012` → series "Title", number "012" (the old project's convention),
   - `Title 012 (2020)` → series "Title", number "012", year "2020",
   - `Title v2 015` → series "Title v2", number "015",
   - `Title 052 - Subtitle 1` → series "Title", number "052", issue title "Subtitle 1" — the
     number is the **first** number encountered (not the last), so a digit ending a subtitle
     (e.g. "Barbary Coast 1") isn't taken as the issue number instead of the real one right after
     the series name. A number that looks like a year (1900–2099) is skipped in favor of the next
     number in the name, if one exists (e.g. `2000 AD 1957` → series "2000 AD", number "1957", not
     "2000"),
   - no match → the whole name becomes the issue title, the number stays empty.
4. For a **new file**: insert a record; if it's CBZ/CBR, try reading `ComicInfo.xml` (overrides
   data from the file name, `metadata_source='comicinfo'`); extract the cover (the first image
   file in natural name-sort order) and save the thumbnail.
5. For an **existing file** (matched by `path`): refresh the size; re-read ComicInfo only when the
   record isn't `manual`/`locked`.
6. After walking the tree: records whose files weren't found get `file_missing=1` (never deleted —
   the user sees them and decides). Series with no issues left are hidden.
7. PDF: catalogued with metadata from the file name, a placeholder cover, `page_count=0`.

Once the goroutine finishes (`Scanner.run`, after the optional ComicVine follow-up phase — see §8),
it records the outcome as one row in `scan_history` (migration 9: started/finished time, found/
processed/missing counts, ComicVine updated/failed counts, the error if any) — best-effort, a
write failure is logged but never fails the scan. This is what backs the admin panel's Scan History
section (§4); the in-memory `Status` struct alone would lose everything on restart.

## 7. ComicInfo.xml

Read from the archive (any location inside the CBZ/CBR, typically at the root). Field mapping (the
parser tolerates missing fields):

| ComicInfo.xml | issues / series |
|---|---|
| `Series` | series.name (only when creating a "virtual" series) |
| `Number` | issue_number |
| `Title` | title |
| `Summary` | summary |
| `Year`/`Month`/`Day` | release_date |
| `Writer` | writer |
| `Penciller` (fallback `Inker`) | artist |
| `Publisher` | publisher |
| `PageCount` | page_count (fallback: number of images in the archive) |

## 8. ComicVine integration

The client, in `internal/comicvine/`:

- The key comes from the config; no key → scrape buttons hidden/disabled with a tooltip.
- **Rate limiter**: at least 1s between requests (ComicVine's limit is 200/h — v1 doesn't do bulk
  scraping, only per-series/per-issue actions).
- 15s timeout, a custom `User-Agent`, treats `status_code != 1` as an error with a message.
- Endpoints: `search` (resources=volume), `volume/4050-{id}`, `issue/4000-{id}`. Fields fetched are
  narrowed with `field_list` (smaller responses).

The matching flow — **always with user confirmation** (the old project's biggest weakness: it
blindly took the first search result):

1. On the series page: "Match in ComicVine" → `POST /series/{id}/match` → a list of candidates
   (cover, name, publisher, start year, issue count) in an HTMX modal, each with an "Open page ↗"
   button (a link to `site_detail_url` on comicvine.gamespot.com, to verify the candidate before
   picking it) next to "Select".
2. The user picks one → `comicvine_volume_id` + `comicvine_url` (`site_detail_url` from the API)
   are saved on the series.
3. "Fetch metadata" on an issue (or "for all missing" on the series): given `comicvine_volume_id`,
   fetch the volume's issue list, match by `issue_number` (compared normalized: leading zeros
   trimmed), fetch the details, update the record (`metadata_source='comicvine'`,
   `comicvine_url`) and fetch the cover from ComicVine into the cache (replacing the archive
   thumbnail, since it's usually better). The saved `comicvine_url` (on series and issue) shows up
   as a "View on ComicVine ↗" link on the series page (action bar) and on the issue page.
4. Records with `metadata_locked=1` are skipped, with a note in the UI.
5. Undoing a match: "Remove match" on the series page (`POST /series/{id}/match/unlink`) clears
   `series.comicvine_volume_id` + `comicvine_url` (also the series' own `publisher`/`description`
   when it's unlocked — those can only have come from `EnrichSeriesFromComicVine`, since a manual
   edit always locks) and rebuilds every unlocked issue sourced from `comicvine` straight off the
   file itself: filename parsing plus its own `ComicInfo.xml`, if present — the same starting point
   a freshly scanned file gets (`library.ResetIssueMetadata`). That also re-extracts the issue's
   cover from the archive, undoing a ComicVine-downloaded one. Locked issues (manually edited) are
   left untouched. Separately, "Remove ComicVine match" on an issue
   (`POST /issues/{id}/scrape/unlink`) does the same single-issue rebuild; a locked issue is left
   untouched instead.

## 9. Web interface — views and routing

Shared layout: a header with the name and the search box. The "Scan Library" control and scan
status live in the admin panel (`/admin`), not the header. HTMX for: edit forms (modal/inline),
filters, scan progress, the ComicVine match dialog. Every view also works without JS (plain POST
forms) — HTMX only improves the UX.

| Method and path | View / action |
|---|---|
| `GET /login` → `POST /login` (`name`, `password`, `next`) / `POST /logout` | login (only when the `users` table has at least one account; no accounts → redirect to `/`). The `withAuth` middleware: no session, GET → 303 to `/login?next=…`, other methods → 401; `/opds/*`, `/login`, `/logout`, `/static/*` are outside the gate. The username lives in the request context (`userFrom(r)`), shown in the layout as "👤 name" + "Log out" (`currentUser` bound per request on a template clone) |
| `GET /admin` (panel) → `POST /admin/users` (add) / `POST /admin/users/{id}/password` / `POST /admin/users/{id}/admin` (grant/revoke) / `POST /admin/users/{id}/delete` | account management — admin only (`requireAdmin`; before the first account: the anonymous visitor). The first account is always admin. The last admin cannot have their privileges revoked or be deleted |
| `POST /admin/missing/delete` | bulk-deletes every catalog record with `file_missing=1` (and its cached cover) — admin only; the button on `/admin` only shows up when at least one exists |
| `GET /?sort=&filter=` | the series grid (cover, name, issue count, a green ✓ badge when every available issue is read); sort: name / recently added; filters: all / unread (no issue has real progress) / reading (there's real progress, not everything finished) / read (every available issue finished, ≥1 issue) / no ComicVine metadata (some issue sourced `filename` or `comicinfo`) / missing files (some issue `file_missing`). Aggregates computed in `ListSeries` (`LEFT JOIN reading_progress`, HAVING). Just opening and closing an issue (page 1 only) doesn't count as "real progress" — the threshold is page 2+, or an outright finish (a one-page issue) |
| `GET /series/{id}` | the series page: metadata + issue list (cover, number, title, date, size, metadata-source badge, a reading-progress bar under the cover + "read: p. X of N (P%)" / "✓ read") |
| `GET /series/{id}/edit` → `POST /series/{id}` | the series edit form (name, publisher, description) — admin only |
| `POST /series/{id}/match` / `POST /series/{id}/match/{volumeID}` | search ComicVine candidates / save the choice — admin only |
| `POST /series/{id}/scrape` | fetch ComicVine metadata for the series' unmatched issues — admin only |
| `GET /issues/{id}` | issue details (full metadata, a large cover; a "Read" / "Continue (p. X)" / "Read again" button for CBZ/CBR; with reading progress, a "Reading / Read — read X of N pages (P%) · last date" box with a bar, a "Read" row in the table; "Pages" shows `file_pages` falling back to `page_count`) |
| `GET /issues/{id}/edit` → `POST /issues/{id}` | the issue edit form (number, title, description, date, credits, publisher); saving sets `manual` + `locked` — admin only |
| `POST /issues/{id}/unlock` | remove the metadata lock — admin only |
| `GET /issues/{id}/read?page=N` | the in-browser reader (a separate `reader.html` template with no layout + `static/reader.js`): one page per screen, zoom (fit height / fit width / 20–400% scale with scrolling, remembered in `localStorage`), paging (arrow keys, Space/PageUp/PageDown, Home/End, clicking the left/right 30% of the screen, swipe, wheel when the page fits entirely, a slider), fullscreen, auto-hiding bars, preloading of neighbouring pages, ◂◂/▸▸ links on the bottom bar to the series' previous/next readable issue (no end-of-issue popup — the user leaves on their own; an earlier "End of issue" overlay was removed on request). Start page: `?page=` → progress (if < page count) → 1. CBZ/CBR present on disk only (404 for PDF/missing); no `file_pages` yet → counted and saved |
| `GET /issues/{id}/pages/{n}?track=0` | the same handler as OPDS; `track=0` (used by the reader, which preloads) does not record progress |
| `POST /issues/{id}/progress` (`page=`, 1-based) | explicit progress recording from the reader (a fetch after each page change, debounced 400ms, `sendBeacon` on leaving the page); `MAX` against the existing value, as in OPDS; 400 out of range; 204 |
| `POST /issues/{id}/read` / `POST /issues/{id}/unread` | mark an issue as read (progress = page count; an archive with uncounted pages gets counted now; an unknown page count is a flash error) / unread (clears progress). The `next` field (local paths only) returns to the list page; without it, redirect to the issue page with `?msg=` |
| `POST /issues/{id}/scrape` | ComicVine for a single issue — admin only |
| `GET /issues/{id}/cover` | the cached thumbnail (Cache-Control; a placeholder when none exists) |
| `GET /issues/{id}/download` | the comic file (`Content-Disposition: attachment`, the original name, `Content-Type` by extension: `application/vnd.comicbook+zip` / `-rar` / `application/pdf`) |
| `POST /scan` — admin only / `GET /scan/status` | start a scan / an HTMX progress partial |
| `GET /search?q=` | results by series name, title and issue numbers (LIKE) |
| `GET /static/...` | static assets from `embed.FS` |
| `GET /healthz` | a liveness probe (`ok`, no login) — Docker/orchestrators |

Filters on the series page and in the grid: all / no metadata (`metadata_source='filename'`) /
from ComicVine / missing files — the equivalent of all/scraped/unscraped in the old project.

## 9a. OPDS catalog (`opds_enabled: true`)

OPDS 1.2 (Atom) — a format supported by comic readers (Panels, Chunky, Moon+ Reader, Librera,
KOReader, Mihon via an extension). The whole catalog — feeds, covers and the files themselves —
lives under the `/opds` prefix, so HTTP Basic auth (accounts from the `users` table, §4) covers
everything a reader touches; the logged-in user lands in the request context, so `pse:lastRead`,
"Currently reading" and streaming progress are theirs. With no accounts the catalog is open (an
anonymous reader). When OPDS is disabled, the routes aren't registered (404 from the catch-all).

Every feed carries an `<icon>` with the absolute address `/static/favicon.png` (192×192) — readers
show it next to the catalog name. The icon lives under `/static`, i.e. outside Basic auth, so a
reader fetches it even without credentials. The same artwork (`web/static/favicon.png`,
`favicon-32.png`, `favicon.ico`, `apple-touch-icon.png` — all scaled down from `imgs/logo.png`,
the icon's source) is also the favicon of the web pages: links in the layout's, login's and
reader's `<head>`, plus the `GET /favicon.ico` route (outside login, outside the request log).

| Path | Feed |
|---|---|
| `GET /opds` | the navigation root: "All series", "Currently reading", "Recently added", "Unread", "Read" + a `search` link |
| `GET /opds/series?page=N` | navigation: series alphabetically (50/page, `next`/`previous`, `opensearch:totalResults`); an entry is a `subsection` link to the series feed + the first issue's cover |
| `GET /opds/series/{id}?page=N` | acquisition: the series' issues in number order (excluding `file_missing`) |
| `GET /opds/recent?page=N` | acquisition: issues by `created_at DESC` (rel `sort/new`) |
| `GET /opds/reading` | acquisition, "Currently reading": issues from `reading_progress` whose last page is 2+ and below the page count (or the page count is unknown), by last read time (LIMIT 100). Page 1 alone (opened and closed) doesn't count as started reading |
| `GET /opds/read?page=N` | acquisition, "Read": issues whose last saved page reached the page count, by completion time descending (50/page) |
| `GET /opds/unread?page=N` | acquisition, "Unread": issues with no `reading_progress` entry for the given user, or with progress limited to page 1 alone (and unfinished), by date added descending (50/page) |
| `GET /opds/issues/{id}/pages/{n}?width=W` | page `n` (0-based) from a CBZ/CBR archive (OPDS-PSE); without `width`, the original file with its type by extension; with `width`, scaled down to W px (max 4000) and JPEG; fetching a page records progress `n+1` (`MAX` against the existing value); 404 out of range, for PDFs and for missing files |
| `GET /opds/search?q=` | acquisition: one flat list of issues by series name / title / number (LIMIT 200) |
| `GET /opds/opensearch.xml` | the OpenSearch description with the template `…/opds/search?q={searchTerms}` and an `<Image>` (the catalog icon) |
| `GET /opds/issues/{id}/file` | the same handler as `/issues/{id}/download` (Range/HEAD via `http.ServeFile`) |
| `GET /opds/issues/{id}/cover` | the same handler as `/issues/{id}/cover` |

**Page streaming (OPDS-PSE 1.2, `xmlns:pse="http://vaemendis.net/opds-pse/ns"`).** Every CBZ/CBR
issue with a known page count carries a link
`rel="http://vaemendis.net/opds-pse/stream" type="image/jpeg" href="…/opds/issues/{id}/pages/{pageNumber}?width={maxWidth}" pse:count="N"`,
plus — once it's been read — `pse:lastRead` (1-based) and `pse:lastReadDate` (RFC 3339). Readers
(Panels, Chunky, Librera, Moon+…) read pages without downloading the file and resume from
`lastRead`. Progress is generated server-side from page requests (readers don't report it any
other way) — prefetching a few pages ahead inflates it slightly; going back to an earlier page
never lowers it. Page count: `issues.file_pages` (the actual image entries in the archive, counted
during a scan — also for previously catalogued files — and lazily on first streaming), falling
back to metadata `page_count`. PDFs have no PSE link (no page rendering).

Issue entry: title `Series #number – title`, `author` from the `writer` field (split on commas),
`dc:publisher`, `dc:issued` (release date), `summary` (the description, or, when empty, "Art:
…"), an `http://opds-spec.org/acquisition` link with `type` by extension
(`application/vnd.comicbook+zip`, `application/vnd.comicbook-rar`, `application/pdf`), and
`image`/`image/thumbnail` links only when `cover_cached=1` (instead of a link to the placeholder
SVG). Links are **absolute** (scheme + `Host` from the request, honoring
`X-Forwarded-Proto/Host`), since some readers mishandle relative `href`s. The HTML layout adds a
`<link rel="alternate" type="…opds-catalog…">` (autodiscovery) and an "OPDS" badge in the header.

Client compatibility: Thorium Reader (3.5.x) validates the catalog address with `validator.isURL`
and `require_tld` (the `THORIUM_ISURL_REQUIRE_TLD_FALSE` build flag isn't set in official
releases), so it rejects `http://localhost:…` **before** sending the request ("Error accessing the
feed"), while IP addresses (`127.0.0.1`, a LAN IP) are accepted. Thorium handles Basic auth from
the `WWW-Authenticate: Basic` header (a login prompt, then `Authorization: Basic` on later
requests). That's why the startup message prints every reachable address (localhost + the IPv4 of
every interface when bound to `0.0.0.0`).

A Thorium limitation (noted, not worked around): it renders navigation entries as plain text with
no thumbnails, shows covers only for publications, and clicking a publication opens an info dialog
with no navigation into the catalog. An attempt at a "shelf" view (series as `rel="collection"`
groups with issues, single releases as publications) was tried and rolled back at the user's
request — it made a bigger mess than a plain list in Thorium.

## 10. Security and quality notes

- **The old project has a hardcoded ComicVine key in `Old/ComicsNest/apiProcessor.go`, and it's in
  git history — the key should be treated as leaked and revoked/replaced.**
- `html/template` instead of `text/template` (XSS escaping — ComicVine descriptions contain HTML;
  render it sanitized or as plain text).
- Files are only ever served by database `id` — never by a path from a request parameter (no
  path-traversal surface).
- One connection pool to SQLite for the whole app (not opened per request as in the old project);
  `PRAGMA journal_mode=WAL`, `busy_timeout`.
- Every handler error → log + an error page/partial; no `log.Fatal` at runtime.
- The server listens on `localhost` by default (a personal app, no auth).
