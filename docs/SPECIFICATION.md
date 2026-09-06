# ComicNest — Specyfikacja techniczna (v1)

Osobisty katalog komiksów w formie aplikacji webowej, pisany w Go. Inspirowany Komgą,
ale własny i prostszy. Następca konceptu **ComicsNest** (kod w `Old/ComicsNest` — służy
tylko jako referencja pomysłu, nie kopiujemy z niego kodu).

## 1. Cele wersji 1

- Jedno binarium Go + plik konfiguracyjny; uruchomienie tworzy lokalny serwer WWW.
- Skanowanie folderu biblioteki (`.cbz`, `.cbr`, `.pdf`) i budowanie katalogu: **serie → zeszyty (issues)**.
- Odczyt metadanych osadzonych w plikach (**ComicInfo.xml**).
- **Ręczna edycja metadanych** serii i zeszytów w przeglądarce.
- **Aktualizacja metadanych z ComicVine API** (na żądanie użytkownika, z wyborem dopasowania).
- Wyświetlanie okładek (wyciąganych z archiwów, cache na dysku).
- Pobranie pliku komiksu po kliknięciu (bez wbudowanego czytnika w v1).

### Poza zakresem v1 (świadomie)

- Czytnik stron w przeglądarce (planowany na v2 — architektura ma tego nie blokować).
- Wielu użytkowników, logowanie, uprawnienia.
- Zapis metadanych z powrotem do ComicInfo.xml w archiwum.
- Renderowanie okładek z PDF (PDF dostaje placeholder; plik nadal jest katalogowany i pobieralny).
- Automatyczne obserwowanie zmian w systemie plików (skan uruchamiany ręcznie przyciskiem).

## 2. Stack technologiczny

| Warstwa | Wybór | Uzasadnienie |
|---|---|---|
| Język | Go 1.23+ | wymóg projektu |
| HTTP | `net/http` + router stdlib (wzorce `GET /series/{id}` z Go 1.22) | zero zależności |
| Baza | SQLite przez `modernc.org/sqlite` | czysty Go, bez cgo — bezproblemowa kompilacja na Windows |
| Dostęp do bazy | `database/sql` + ręczne zapytania | schemat jest mały; bez ORM |
| Szablony | `html/template` | **uwaga:** stary projekt używał `text/template` — to podatność XSS, w nowym zawsze `html/template` |
| Interaktywność | HTMX (plik statyczny w `web/static/`, vendorowany) | edycja, filtry i status skanu bez SPA i bez build stepu |
| CSS | własny, prosty arkusz (dark theme, grid okładek) | bez frameworków |
| Konfiguracja | YAML (`gopkg.in/yaml.v3`) | jak w starym projekcie |
| CBZ | `archive/zip` (stdlib) | |
| CBR | `github.com/nwaples/rardecode/v2` | odczyt RAR bez cgo |
| Miniatury | `image` + `golang.org/x/image/draw` | skalowanie okładek do cache |
| Embedding | `embed` — szablony i statyki wkompilowane w binarium | jedno binarium do uruchomienia |

## 3. Struktura projektu

```
ComicNextClaude/
├── cmd/comicnest/main.go        # wiring: config → db → scanner → server
├── internal/
│   ├── config/                  # wczytanie/zapis config.yaml, wartości domyślne
│   ├── store/                   # schemat SQLite, migracje, zapytania (SeriesStore, IssueStore)
│   ├── library/                 # scanner: walk, rozpoznawanie serii/numerów, rozmiary
│   │   ├── scanner.go
│   │   ├── archive.go           # otwieranie CBZ/CBR, listowanie stron, wyciąganie plików
│   │   └── comicinfo.go         # parser ComicInfo.xml
│   ├── covers/                  # ekstrakcja pierwszej strony → miniatura JPEG → cache
│   ├── comicvine/               # klient API: search volume, get volume, get issue; rate limiter
│   ├── opds/                    # typy Atom/OPDS 1.2 + serializacja feedów i OpenSearch (bez HTTP/DB)
│   └── server/                  # handlery HTTP, routing, renderowanie szablonów (+ opds.go: katalog OPDS)
├── web/
│   ├── templates/               # layout.html + widoki + partiale HTMX
│   └── static/                  # htmx.min.js, styles.css, placeholder.svg, favicon.png, favicon-32.png, favicon.ico, apple-touch-icon.png
├── docs/                        # ta dokumentacja
├── .github/workflows/go.yml     # CI: testy + binaria (Win/Linux/macOS) + obraz Docker (GHCR) po każdym pushu na main, wydania z tagów v*
├── Dockerfile                   # obraz: static binary w distroless, wolumeny /comics /config /data (§4a)
├── docker-compose.yml           # przykład uruchomienia dla użytkowników
├── .dockerignore
├── imgs/                        # logo.png (źródło ikony, 1254 px; favicony w web/static są z niego skalowane) i screeny do README
├── config_example.yaml          # wzorzec konfiguracji z opisem każdej opcji (wersjonowany, §4)
├── config.yaml                  # tworzony przy pierwszym starcie (gitignore)
└── data/                        # runtime: database.sqlite, covers/ (gitignore)
```

## 4. Konfiguracja (`config.yaml`)

```yaml
port: 8080
listen: localhost              # "0.0.0.0" = dostęp z sieci lokalnej (potrzebne czytnikom OPDS)
library: "D:/Library"          # korzeń biblioteki komiksów
data_dir: "./data"             # baza sqlite + cache okładek
comicvine_api_key: ""          # puste = funkcje ComicVine wyłączone (UI to komunikuje)
opds_enabled: false            # katalog OPDS pod /opds (patrz §9a)
page_size: 60                  # kafelków serii na stronę biblioteki; 0 = bez paginacji
users:                         # konta; puste = brak logowania (jeden anonimowy czytelnik)
  - name: artur
    password: sekret           # plaintext — świadomie (osobista aplikacja w LAN)
  - name: kasia
    password: inne
```

Przy braku pliku aplikacja zapisuje domyślny config i loguje instrukcję uzupełnienia.

**Plik `config_example.yaml`** (w korzeniu repozytorium, wersjonowany) jest wzorcem dla użytkownika
i jedynym pełnym spisem opcji: każdy klucz ma tam komentarz mówiący, co robi i jakie wartości
przyjmuje. **Zasada:** każda zmiana w konfiguracji (nowy klucz, zmiana nazwy lub domyślnej wartości,
usunięcie) trafia w tym samym commicie do `config_example.yaml` razem z opisem — a także do bloku
powyżej i do README. Prawdziwy `config.yaml` (z hasłami i kluczem API) pozostaje w `.gitignore`.
Klucz API **nigdy nie trafia do kodu** (w starym projekcie był zahardkodowany — patrz §10).

**Konta użytkowników (`users`).** Jedno źródło prawdy dla logowania do WWW (formularz
`/login`, sesja w ciasteczku `comicnest_session`: HttpOnly, SameSite=Strict, 30 dni, tabela sesji
w pamięci — restart wylogowuje) i dla OPDS (HTTP Basic z tymi samymi parami nazwa/hasło).
Postęp czytania jest per użytkownik (§5). Walidacja: nazwa i hasło wymagane, nazwy unikalne, bez
`:` (Basic auth). Brak `users` = stare zachowanie: wszystko otwarte, postęp anonimowego czytelnika
(`user = ''`). Hasła do OPDS pochodzą wyłącznie z `users` — dawna sekcja `opds` (z `username`/`password`)
została usunięta, a włącznik katalogu to klucz `opds_enabled`. Przy starcie z kontami postęp anonimowy przechodzi na
pierwsze konto z listy (`Store.AdoptAnonymousProgress`).

**Paginacja biblioteki (`page_size`).** Widok główny dzieli przefiltrowaną listę serii na strony po
`page_size` kafelków (domyślnie 60; parametr `?page=N`, sortowanie i filtr zachowane w linkach pagera).
`0` wyłącza paginację, wartość ujemna to błąd konfiguracji. Nie dotyczy OPDS (stała 50 wpisów).

Domyślnie nasłuch tylko na `localhost`. `listen: 0.0.0.0` wystawia aplikację w sieci lokalnej —
wtedy warto zdefiniować `users`, bo bez kont UI (także edycja metadanych) jest otwarte.

**Ścieżka configu i zmienne środowiskowe.** Plik wskazuje flaga `-config`, w drugiej kolejności
`$COMICNEST_CONFIG`, domyślnie `./config.yaml`. Zmienne `COMICNEST_LISTEN`, `COMICNEST_PORT`,
`COMICNEST_LIBRARY`, `COMICNEST_DATA_DIR`, `COMICNEST_COMICVINE_API_KEY`, `COMICNEST_OPDS_ENABLED`
i `COMICNEST_PAGE_SIZE` nadpisują wartości z pliku (`Config.applyEnv`; pusta wartość = nieustawiona,
błędny typ = błąd startu). Gdy pliku nie ma, do tworzonego domyślnego configu trafiają już wartości
ze środowiska. `users` nie ma odpowiednika w środowisku. Mechanizm istnieje głównie dla Dockera
(§4a), ale działa wszędzie.

## 4a. Docker

Obraz (`Dockerfile`, wieloetapowy): binarium bez cgo kompilowane w `golang:alpine`, kopiowane do
`gcr.io/distroless/static` (certyfikaty CA dla ComicVine, brak shella). Kontener startuje jako root:
przy ustawionych `PUID`/`PGID` (konwencja NAS — Synology, Unraid, linuxserver.io) aplikacja przepisuje
własność katalogu configu i `data_dir` na tego użytkownika (pomijając wpisy już poprawne, więc restart
z dużym cache okładek jest tani) i wywołuje `setgroups`/`setgid`/`setuid`, zanim otworzy bazę
(`cmd/comicnest/privs_linux.go`; poza Linuksem no-op). Biblioteka nigdy nie jest chownowana. Bez
`PUID`/`PGID` proces zostaje rootem — wariant `--user` z własnymi uprawnieniami katalogów też działa.
Powód: wolumeny na NAS-ach należą do konta użytkownika (Synology: UID 1026, GID 100), a stały
użytkownik `nonroot` z obrazu nie miał do nich zapisu.
Obraz ustawia `COMICNEST_CONFIG=/config/config.yaml`, `COMICNEST_LISTEN=0.0.0.0`,
`COMICNEST_LIBRARY=/comics`, `COMICNEST_DATA_DIR=/data`, więc trzy wolumeny (`/comics` tylko do
odczytu, `/config`, `/data`) wystarczają, a pierwszy start tworzy poprawny `config.yaml`.
`GET /healthz` (poza logowaniem i logiem żądań) zwraca `ok`; `comicnest -healthcheck` odpytuje go
po `127.0.0.1:port` i kończy się kodem 0/1 — to `HEALTHCHECK` w `docker-compose.yml`, bo obraz nie
ma `curl`. Publikacja: job `docker` w `.github/workflows/go.yml` buduje `linux/amd64` + `linux/arm64`
(buildx + QEMU) i wypycha do `ghcr.io/monsterartur1/comicnest` — `latest` i `main-<sha>` z `main`,
`X.Y.Z`/`X.Y`/`X` z tagów. Wersja trafia do obrazu przez `--build-arg VERSION`. Przykład użycia
w `docker-compose.yml`; `/data` na lokalnym dysku (SQLite na SMB/NFS grozi uszkodzeniem bazy).

## 5. Model danych (SQLite)

```sql
CREATE TABLE series (
    id                  INTEGER PRIMARY KEY,
    name                TEXT NOT NULL,            -- wyświetlana nazwa (edytowalna)
    folder_path         TEXT UNIQUE,              -- NULL dla serii "wirtualnych" z nazw plików
    publisher           TEXT DEFAULT '',
    description         TEXT DEFAULT '',
    comicvine_volume_id INTEGER,                  -- zapamiętane dopasowanie ComicVine
    metadata_locked     INTEGER NOT NULL DEFAULT 0, -- 1 = skan/scrape nie nadpisuje
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE issues (
    id                 INTEGER PRIMARY KEY,
    series_id          INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    path               TEXT NOT NULL UNIQUE,      -- absolutna ścieżka pliku
    file_size          INTEGER NOT NULL,          -- bajty; formatowanie w warstwie widoku
    file_missing       INTEGER NOT NULL DEFAULT 0,-- plik zniknął przy ostatnim skanie
    issue_number       TEXT DEFAULT '',           -- TEXT: bywają numery "12.1", "Annual 1"
    title              TEXT DEFAULT '',
    summary            TEXT DEFAULT '',
    release_date       TEXT DEFAULT '',           -- ISO yyyy-mm-dd (lub sam rok)
    writer             TEXT DEFAULT '',
    artist             TEXT DEFAULT '',
    publisher          TEXT DEFAULT '',
    page_count         INTEGER DEFAULT 0,
    comicvine_issue_id INTEGER,
    metadata_source    TEXT NOT NULL DEFAULT 'filename', -- filename | comicinfo | comicvine | manual
    metadata_locked    INTEGER NOT NULL DEFAULT 0,       -- 1 = ręcznie edytowane, nie nadpisuj
    has_comicinfo      INTEGER NOT NULL DEFAULT 0,
    comicinfo_series   TEXT,                      -- <Series> z ComicInfo.xml (migracja 5): NULL = nie sprawdzono, '' = brak
    cover_cached       INTEGER NOT NULL DEFAULT 0,
    created_at         TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at         TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_issues_series ON issues(series_id);
```

Zasada nadpisywania metadanych (priorytet rosnąco):
`filename` → `comicinfo` → `comicvine` → `manual`.
Skan może nadpisać dane o niższym lub równym priorytecie; `metadata_locked=1`
(ustawiane automatycznie po ręcznej edycji) blokuje wszystko poza kolejną ręczną edycją.

Okładki nie siedzą w bazie: cache `data/covers/{issue_id}.jpg` (miniatura ~400px szer.)
+ flaga `cover_cached`. Okładka serii = okładka pierwszego zeszytu (najniższy numer).

**Wydania jednorazowe (one-shoty)** — kolumna `series.one_shot` (migracja 2). Sygnały
(dowolny wystarczy): (1) dopasowany wolumen ComicVine ma `count_of_issues == 1`,
(2) ComicInfo.xml ma `Format` zawierający "One-Shot" lub `Count=1`, (3) po skanie
seria ma dokładnie 1 zeszyt bez numeru. Skan zdejmuje flagę, gdy seria zyska drugi
zeszyt. UI: etykieta „wydanie jednorazowe" zamiast licznika, kafelek na gridzie
prowadzi wprost do zeszytu. Scrape: zeszyt bez numeru + wolumen z jednym zeszytem
→ dopasowany zostaje ten jedyny zeszyt.

**Strumieniowanie i postęp czytania** (migracja 3): kolumna `issues.file_pages` — rzeczywista
liczba wpisów graficznych w archiwum (0 = jeszcze nieliczona; `page_count` pozostaje
metadaną z ComicInfo, edytowalną i potencjalnie błędną) — oraz tabela
`reading_progress(user, issue_id → issues ON DELETE CASCADE, page, updated_at; PK (user, issue_id))`
(migracja 4 — wcześniej PK po samym `issue_id`; stare wiersze dostały `user = ''`), gdzie `user` to
nazwa konta z `config.yaml` (`''` = anonimowy czytelnik bez kont), a `page` to ostatnia
przeczytana strona 1-based (konwencja OPDS-PSE). Zapis przez `UPSERT` z `MAX(page, nowa)`.
Skan uzupełnia `file_pages` dla nowych plików i dla starych z wartością 0. Wszystkie zapytania o
postęp (`ListSeries` agregaty, `ListIssuesInProgress`, `ReadingProgressFor`, …) przyjmują `user`.

**Seria z ComicInfo** (migracja 5): kolumna `issues.comicinfo_series` przechowuje surową wartość
`<Series>` z pliku, niezależnie od (edytowalnego) wiersza serii. `NULL` = wiersz z bazy sprzed
migracji, jeszcze nie sprawdzony, **albo** archiwum, którego nie dało się odczytać (błąd zip/rar) —
w obu przypadkach kolejny skan czyta ComicInfo ponownie (dla starych wierszy tylko po to; zablokowanych
metadanych nie rusza) i wypełnia kolumnę; `''` = archiwum odczytane poprawnie, ale bez ComicInfo lub z
pustym `<Series>`, oraz każdy PDF. Kolumna służy wyłącznie regule rozdzielania folderów (§6 pkt 2).

## 6. Skanowanie biblioteki

Uruchamiane przyciskiem w UI (`POST /scan`), działa w goroutine; UI odpytuje status
(HTMX polling `GET /scan/status` co 2 s — pasek postępu i licznik plików).
Tylko jeden skan naraz (mutex + flaga w pamięci).

Algorytm:

1. `filepath.WalkDir` po `config.library`, filtr rozszerzeń: `.cbz`, `.cbr`, `.pdf`.
2. **Przypisanie do serii** (tryb hybrydowy — folder z fallbackiem na nazwę pliku):
   - plik w podfolderze → seria = **nazwa najbliższego folderu-rodzica** (folder_path
     = ścieżka względna od korzenia biblioteki); zagnieżdżone foldery dozwolone,
     liczy się bezpośredni rodzic;
   - plik bezpośrednio w korzeniu → seria z **nazwy pliku** (folder_path = NULL),
     seria tworzona/odnajdowana po nazwie.
   - **Folder z kilkoma seriami** (przebieg 3, `Scanner.splitMixedFolders`, po synchronizacji
     plików, przed `ReconcileOneShots`): gdy obecne na dysku pliki *jednego* podfolderu mają
     co najmniej dwie różne niepuste wartości `<Series>` w ComicInfo.xml (porównanie po
     `TrimSpace`, bez rozróżniania wielkości liter), folder jest „mieszany". Przenoszone są
     **tylko zeszyty siedzące jeszcze w serii folderu** (`series.folder_path` = katalog pliku);
     zeszyt, który już leży w serii wirtualnej, nie jest nigdy ruszany — dzięki temu zmiana nazwy
     (ręczna, z blokadą) lub dopasowanie ComicVine rozdzielonej serii przeżywa kolejne skany.
     Cel przeniesienia: najpierw seria, w której już leży inny plik tego folderu z tą samą
     (znormalizowaną) wartością `Series` (pliki dodane później dołączają do przemianowanej
     serii), w braku takiej — seria wirtualna o tej nazwie (`FindOrCreateSeriesByName`,
     `folder_path NULL`). Zeszyty bez ComicInfo oraz te, których `Series` równa się nazwie
     folderu (bez rozróżniania wielkości liter), zostają w serii folderu (druga „Mad Max" byłaby
     duplikatem; wartość nadal liczy się przy ocenie, czy folder jest mieszany). Reguła patrzy na
     pliki fizycznie w folderze, więc jest idempotentna. Folder ze spójną wartością `Series`
     (nawet inną niż nazwa folderu) albo z ComicInfo tylko w części plików **nie** jest dzielony —
     folder = seria pozostaje regułą. Blokada metadanych ani dopasowanie ComicVine nie chronią
     przed pierwszym rozdzieleniem (blokada dotyczy tekstu metadanych). Opróżniona seria folderu
     zostaje w bazie (listy ukrywają serie bez zeszytów). Przykład: `Mad Max/` z „Mad Max: Fury
     Road" i „Mad Max: Fury Road: Max" → dwie serie.
3. **Parsowanie nazwy pliku** (bez rozszerzenia) — kolejno próbowane wzorce:
   - `Tytuł #012` → seria "Tytuł", numer "012" (konwencja starego projektu),
   - `Tytuł 012 (2020)` → seria "Tytuł", numer "012", rok "2020",
   - `Tytuł v2 015` → seria "Tytuł v2", numer "015",
   - brak dopasowania → cała nazwa jako tytuł zeszytu, numer pusty.
4. Dla **nowego pliku**: wstaw rekord; jeśli CBZ/CBR — spróbuj wczytać `ComicInfo.xml`
   (nadpisuje dane z nazwy pliku, `metadata_source='comicinfo'`); wyciągnij okładkę
   (pierwszy plik obrazkowy w porządku naturalnego sortowania nazw) i zapisz miniaturę.
5. Dla **istniejącego pliku** (po `path`): odśwież rozmiar; ComicInfo tylko gdy
   rekord nie jest `manual`/`locked`.
6. Po przejściu drzewa: rekordy, których plików nie znaleziono → `file_missing=1`
   (nie kasujemy — użytkownik widzi i decyduje). Serie bez żadnych zeszytów ukrywane.
7. PDF: katalogowany z metadanymi z nazwy pliku, okładka = placeholder, `page_count=0`.

## 7. ComicInfo.xml

Czytany z archiwum (dowolna lokalizacja w CBZ/CBR, standardowo w korzeniu).
Mapowanie pól (parser toleruje brakujące pola):

| ComicInfo.xml | issues / series |
|---|---|
| `Series` | series.name (tylko przy tworzeniu serii "wirtualnej") |
| `Number` | issue_number |
| `Title` | title |
| `Summary` | summary |
| `Year`/`Month`/`Day` | release_date |
| `Writer` | writer |
| `Penciller` (fallback `Inker`) | artist |
| `Publisher` | publisher |
| `PageCount` | page_count (fallback: liczba obrazków w archiwum) |

## 8. Integracja ComicVine

Klient w `internal/comicvine/`:

- Klucz z configu; brak klucza → przyciski scrape ukryte/wyłączone z podpowiedzią.
- **Rate limiter**: min. 1 s odstępu między żądaniami (limit ComicVine to 200/h —
  masowego scrape'u w v1 nie robimy, tylko akcje per-seria/per-zeszyt).
- Timeout 15 s, `User-Agent` własny, obsługa `status_code != 1` jako błąd z komunikatem.
- Endpointy: `search` (resources=volume), `volume/4050-{id}`, `issue/4000-{id}`.
  Pobierane pola zawężane parametrem `field_list` (mniejsze odpowiedzi).

Przepływ dopasowania — **zawsze z potwierdzeniem użytkownika** (największa słabość
starego projektu: brał ślepo pierwszy wynik wyszukiwania):

1. Na stronie serii: „Dopasuj w ComicVine" → `POST /series/{id}/match` → lista
   kandydatów (okładka, nazwa, wydawca, rok startu, liczba zeszytów) w modalu HTMX.
2. Użytkownik wybiera → zapis `comicvine_volume_id` na serii.
3. „Pobierz metadane" przy zeszycie (lub „dla wszystkich brakujących" na serii):
   po `comicvine_volume_id` pobierz listę zeszytów wolumenu, dopasuj po
   `issue_number` (porównanie znormalizowane: trim zer wiodących), pobierz szczegóły,
   zaktualizuj rekord (`metadata_source='comicvine'`) + pobierz okładkę z ComicVine
   do cache (zastępuje miniaturę z archiwum, bo zwykle lepsza).
4. Rekordy `metadata_locked=1` pomijane z informacją w UI.

## 9. Interfejs WWW — widoki i routing

Layout wspólny: nagłówek z nazwą, wyszukiwarką i przyciskiem „Skanuj bibliotekę"
(+ dyskretny status skanu). HTMX do: formularzy edycji (modal/inline), filtrów,
postępu skanu, dialogu dopasowania ComicVine. Każdy widok działa też bez JS
(zwykłe formularze POST) — HTMX tylko poprawia UX.

| Metoda i ścieżka | Widok / akcja |
|---|---|
| `GET /login` → `POST /login` (`name`, `password`, `next`) / `POST /logout` | logowanie (tylko gdy `users` zdefiniowane; bez kont → redirect na `/`). Middleware `withAuth`: bez sesji GET → 303 na `/login?next=…`, inne metody → 401; `/opds/*`, `/login`, `/logout`, `/static/*` poza bramką. Nazwa użytkownika w kontekście żądania (`userFrom(r)`), w layoucie „👤 nazwa" + „Wyloguj" (`currentUser` bindowane per żądanie na klonie szablonu) |
| `GET /?sort=&filter=` | grid serii (okładka, nazwa, liczba zeszytów, zielony znaczek ✓ gdy wszystkie dostępne zeszyty przeczytane); sort: nazwa / ostatnio dodane; filtry: wszystkie / nieczytane (żaden zeszyt nie ma postępu) / w trakcie czytania (jest postęp, nie wszystko skończone) / przeczytane (każdy dostępny zeszyt doczytany, ≥1 zeszyt) / bez metadanych z ComicVine (jakiś zeszyt ze źródłem `filename` lub `comicinfo`) / brakujące pliki (jakiś zeszyt `file_missing`). Agregaty liczone w `ListSeries` (`LEFT JOIN reading_progress`, HAVING) |
| `GET /series/{id}` | strona serii: metadane + lista zeszytów (okładka, numer, tytuł, data, rozmiar, badge źródła metadanych, pasek postępu czytania pod okładką + „czytane: str. X z N (P%)" / „✓ przeczytane") |
| `GET /series/{id}/edit` → `POST /series/{id}` | formularz edycji serii (nazwa, wydawca, opis) |
| `POST /series/{id}/match` / `POST /series/{id}/match/{volumeID}` | wyszukanie kandydatów ComicVine / zapis wyboru |
| `POST /series/{id}/scrape` | pobranie metadanych ComicVine dla zeszytów serii bez dopasowania |
| `GET /issues/{id}` | szczegóły zeszytu (pełne metadane, duża okładka; przycisk „Czytaj" / „Czytaj dalej (str. X)" / „Czytaj od nowa" dla CBZ/CBR; przy postępie czytania ramka „W trakcie czytania / Przeczytane — przeczytano X z N stron (P%) · ostatnio data" z paskiem, wiersz „Przeczytano" w tabeli; „Strony" pokazuje `file_pages` z fallbackiem na `page_count`) |
| `GET /issues/{id}/edit` → `POST /issues/{id}` | formularz edycji zeszytu (numer, tytuł, opis, data, twórcy, wydawca); zapis ustawia `manual` + `locked` |
| `POST /issues/{id}/unlock` | zdjęcie blokady metadanych |
| `GET /issues/{id}/read?page=N` | czytnik w przeglądarce (osobny szablon `reader.html` bez layoutu + `static/reader.js`): jedna strona na ekran, zoom (dopasuj wysokość / szerokość / skala 20–400% z przewijaniem, zapamiętana w `localStorage`), przewracanie (strzałki, Space/PageUp/PageDown, Home/End, klik w lewą/prawą 30% ekranu, swipe, kółko gdy strona mieści się w całości, suwak), pełny ekran, auto-ukrywane paski, preload sąsiednich stron, na dolnym pasku linki ◂◂/▸▸ do poprzedniego/następnego czytelnego zeszytu serii (bez popupu na końcu — użytkownik sam wychodzi; wcześniejsza nakładka „Koniec zeszytu" usunięta na życzenie). Start: `?page=` → postęp (gdy < liczba stron) → 1. Tylko CBZ/CBR obecne na dysku (404 dla PDF/brakujących); brak `file_pages` → liczy i zapisuje |
| `GET /issues/{id}/pages/{n}?track=0` | ten sam handler co w OPDS; `track=0` (używane przez czytnik, który preloaduje) nie zapisuje postępu |
| `POST /issues/{id}/progress` (`page=`, 1-based) | jawny zapis postępu z czytnika (fetch po zmianie strony z debounce 400 ms, `sendBeacon` przy opuszczaniu strony); `MAX` z dotychczasowym jak w OPDS; 400 poza zakresem; 204 |
| `POST /issues/{id}/read` / `POST /issues/{id}/unread` | oznaczenie zeszytu jako przeczytany (postęp = liczba stron; dla archiwum bez policzonych stron liczy je teraz; przy nieznanej liczbie stron błąd flash) / nieprzeczytany (usunięcie postępu). Pole `next` (tylko ścieżki lokalne) wraca na stronę listy; bez niego redirect na stronę zeszytu z `?msg=` |
| `POST /issues/{id}/scrape` | ComicVine dla pojedynczego zeszytu |
| `GET /issues/{id}/cover` | miniatura z cache (Cache-Control; placeholder gdy brak) |
| `GET /issues/{id}/download` | plik komiksu (`Content-Disposition: attachment`, oryginalna nazwa, `Content-Type` wg rozszerzenia: `application/vnd.comicbook+zip` / `-rar` / `application/pdf`) |
| `POST /scan` / `GET /scan/status` | start skanu / partial HTMX z postępem |
| `GET /search?q=` | wyniki po nazwach serii, tytułach i numerach zeszytów (LIKE) |
| `GET /static/...` | statyki z `embed.FS` |
| `GET /healthz` | sonda stanu (`ok`, bez logowania) — Docker/orkiestratory |

Filtry na stronie serii i w gridzie: wszystkie / bez metadanych (`metadata_source='filename'`)
/ z ComicVine / brakujące pliki — odpowiednik all/scraped/unscraped ze starego projektu.

## 9a. Katalog OPDS (`opds_enabled: true`)

OPDS 1.2 (Atom) — format obsługiwany przez czytniki komiksów (Panels, Chunky, Moon+ Reader,
Librera, KOReader, Mihon przez rozszerzenie). Cały katalog — feedy, okładki i pliki — żyje pod
prefiksem `/opds`, żeby HTTP Basic auth (konta z `users`, §4) obejmowało wszystko, czego dotyka
czytnik; zalogowany użytkownik trafia do kontekstu żądania, więc `pse:lastRead`, „Aktualnie
czytane" i postęp ze strumieniowania są jego. Bez kont katalog jest otwarty (czytelnik anonimowy).
Gdy OPDS jest wyłączony, trasy nie są rejestrowane (404 z catch-alla).

Każdy feed niesie `<icon>` z absolutnym adresem `/static/favicon.png` (192×192) — czytniki pokazują
ją obok nazwy katalogu. Ikona leży pod `/static`, czyli poza Basic auth, więc czytnik pobierze ją
także bez poświadczeń. Ta sama grafika (`web/static/favicon.png`, `favicon-32.png`, `favicon.ico`, `apple-touch-icon.png` —
wszystkie przeskalowane z `imgs/logo.png`, które jest źródłem ikony)
jest faviconem stron WWW: linki w `<head>` layoutu, loginu i czytnika oraz trasa `GET /favicon.ico`
(poza logowaniem, poza logiem żądań).

| Ścieżka | Feed |
|---|---|
| `GET /opds` | nawigacyjny root: „Wszystkie serie", „Aktualnie czytane", „Ostatnio dodane", „Nieczytane", „Przeczytane" + link `search` |
| `GET /opds/series?page=N` | nawigacyjny: serie alfabetycznie (50/stronę, `next`/`previous`, `opensearch:totalResults`); wpis = link `subsection` do feedu serii + okładka pierwszego zeszytu |
| `GET /opds/series/{id}?page=N` | akwizycyjny: zeszyty serii w kolejności numerów (bez `file_missing`) |
| `GET /opds/recent?page=N` | akwizycyjny: zeszyty wg `created_at DESC` (rel `sort/new`) |
| `GET /opds/reading` | akwizycyjny „Aktualnie czytane": zeszyty z `reading_progress`, których ostatnia strona < liczba stron (lub liczba stron nieznana), wg ostatniego czytania (LIMIT 100) |
| `GET /opds/read?page=N` | akwizycyjny „Przeczytane": zeszyty, których ostatnia zapisana strona osiągnęła liczbę stron, wg czasu ukończenia malejąco (50/stronę) |
| `GET /opds/unread?page=N` | akwizycyjny „Nieczytane": zeszyty bez żadnego wpisu w `reading_progress` dla danego użytkownika, wg daty dodania malejąco (50/stronę) |
| `GET /opds/issues/{id}/pages/{n}?width=W` | strona `n` (0-based) z archiwum CBZ/CBR (OPDS-PSE); bez `width` oryginalny plik z typem po rozszerzeniu, z `width` przeskalowanie do W px (max 4000) i JPEG; pobranie strony zapisuje postęp `n+1` (`MAX` z dotychczasowym); 404 poza zakresem, dla PDF i brakujących plików |
| `GET /opds/search?q=` | akwizycyjny: jedna płaska lista zeszytów po nazwie serii / tytule / numerze (LIMIT 200) |
| `GET /opds/opensearch.xml` | OpenSearch description z szablonem `…/opds/search?q={searchTerms}` i `<Image>` (ikona katalogu) |
| `GET /opds/issues/{id}/file` | ten sam handler co `/issues/{id}/download` (Range/HEAD przez `http.ServeFile`) |
| `GET /opds/issues/{id}/cover` | ten sam handler co `/issues/{id}/cover` |

**Strumieniowanie stron (OPDS-PSE 1.2, `xmlns:pse="http://vaemendis.net/opds-pse/ns"`).** Każdy
zeszyt CBZ/CBR ze znaną liczbą stron ma link `rel="http://vaemendis.net/opds-pse/stream"
type="image/jpeg" href="…/opds/issues/{id}/pages/{pageNumber}?width={maxWidth}" pse:count="N"`
oraz — gdy był czytany — `pse:lastRead` (1-based) i `pse:lastReadDate` (RFC 3339). Czytniki
(Panels, Chunky, Librera, Moon+…) czytają strony bez pobierania pliku i wznawiają od `lastRead`.
Postęp powstaje po stronie serwera z żądań stron (czytniki nie raportują go inaczej) — prefetch
kilku stron do przodu zawyża go nieznacznie; powrót do wcześniejszej strony postępu nie obniża.
Liczba stron: `issues.file_pages` (rzeczywiste wpisy graficzne w archiwum, liczone przy skanie —
także dla plików skatalogowanych wcześniej — oraz leniwie przy pierwszym strumieniowaniu),
z fallbackiem na `page_count` z metadanych. PDF-y nie mają linku PSE (brak renderowania stron).

Wpis zeszytu: tytuł `Seria #numer – tytuł`, `author` z pola `writer` (rozbite po przecinkach),
`dc:publisher`, `dc:issued` (data wydania), `summary` (opis, a gdy pusty — „Rysunki: …"),
link `http://opds-spec.org/acquisition` z `type` wg rozszerzenia (`application/vnd.comicbook+zip`,
`application/vnd.comicbook-rar`, `application/pdf`) i linki `image`/`image/thumbnail` tylko gdy
`cover_cached=1` (zamiast linku do placeholdera SVG). Linki są **absolutne** (schemat + `Host`
z żądania, z uwzględnieniem `X-Forwarded-Proto/Host`), bo część czytników źle rozwiązuje
względne `href`. Layout HTML dodaje `<link rel="alternate" type="…opds-catalog…">` (autodetekcja)
i plakietkę „OPDS" w nagłówku.

Zgodność klientów: Thorium Reader (3.5.x) waliduje adres katalogu przez `validator.isURL` z
`require_tld` (flaga budowania `THORIUM_ISURL_REQUIRE_TLD_FALSE` nie jest ustawiona w oficjalnych
wydaniach), więc `http://localhost:…` odrzuca **przed** wysłaniem żądania („Błąd dostępu do
kanału"), a adresy IP (`127.0.0.1`, IP w LAN) akceptuje. Basic auth Thorium obsługuje z nagłówka
`WWW-Authenticate: Basic` (okno logowania, `Authorization: Basic` w kolejnych żądaniach). Dlatego
komunikat startowy wypisuje wszystkie osiągalne adresy (localhost + IPv4 interfejsów przy `0.0.0.0`).

Ograniczenie Thorium (do wiadomości, nie obchodzone): wpisy nawigacyjne renderuje jako czysty
tekst bez miniatur, okładki pokazuje tylko dla publikacji, a kliknięcie publikacji otwiera dialog
informacji bez nawigacji do katalogu. Próba widoku „półki" (serie jako grupy `rel="collection"`
z zeszytami, pojedyncze wydania jako publikacje) została wycofana na życzenie użytkownika —
dawała większy bałagan niż zwykła lista.

## 10. Uwagi bezpieczeństwa i jakości

- **Stary projekt ma zahardkodowany klucz ComicVine w `Old/ComicsNest/apiProcessor.go`
  i jest w repo git — klucz należy uznać za ujawniony i zrewokować/wymienić.**
- `html/template` zamiast `text/template` (escapowanie XSS — opisy z ComicVine
  zawierają HTML; renderować po sanityzacji lub jako tekst).
- Pobieranie plików wyłącznie po `id` z bazy — nigdy po ścieżce z parametru
  (żadnego path traversal).
- Jedno połączenie-pula do SQLite na aplikację (nie otwieranie per-request jak
  w starym projekcie); `PRAGMA journal_mode=WAL`, `busy_timeout`.
- Wszystkie błędy handlerów → log + strona/partial błędu; bez `log.Fatal` w runtime.
- Serwer nasłuchuje na `localhost` domyślnie (aplikacja osobista, bez auth).
