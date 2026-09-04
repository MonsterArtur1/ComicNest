# ComicNest — Plan implementacji v1

Kolejność etapów dobrana tak, żeby po każdym etapie aplikacja była uruchamialna
i coś pokazywała. Szczegóły projektowe: [SPECIFICATION.md](SPECIFICATION.md).

## Etap 1 — Szkielet aplikacji ✅
- [x] `go mod init`, struktura katalogów z §3 specyfikacji
- [x] `internal/config`: wczytanie/utworzenie `config.yaml`
- [x] `internal/store`: otwarcie SQLite (modernc), migracja schematu z §5 (tabela `schema_version`)
- [x] `internal/server`: serwer HTTP, layout.html, statyki przez `embed`, strona główna (pusty grid)
- [x] Uruchomienie: `go run ./cmd/comicnest` → działa strona na porcie z configu

## Etap 2 — Skaner biblioteki ✅
- [x] `internal/library/scanner.go`: walk, filtr rozszerzeń, hybrydowe przypisanie do serii (folder / fallback nazwa pliku), wzorce parsowania nazw z §6
- [x] `internal/library/archive.go`: otwieranie CBZ (zip) i CBR (rardecode), listowanie obrazków w porządku naturalnym (+ fallback formatu dla źle nazwanych .cbz/.cbr)
- [x] `internal/library/comicinfo.go`: parser ComicInfo.xml + mapowanie pól z §7
- [x] `internal/covers`: ekstrakcja pierwszej strony → miniatura JPEG 400px → `data/covers/` (+ `GET /issues/{id}/cover` z fallbackiem do placeholdera)
- [x] Reguły nadpisywania (`metadata_source`, `metadata_locked`), oznaczanie `file_missing`
- [x] `POST /scan` w goroutine + `GET /scan/status` (postęp w pamięci, polling HTMX co 2 s)
- [x] Testy jednostkowe: parsowanie nazw plików, mapowanie ComicInfo, archiwa, miniatury

## Etap 3 — Przeglądanie katalogu ✅
- [x] Grid serii na `/` (okładki, licznik zeszytów, sortowanie; filtr tekstowy przez wyszukiwarkę)
- [x] Strona serii `/series/{id}` z listą zeszytów i filtrami metadanych (wszystkie / bez metadanych / ComicVine / brakujące)
- [x] Szczegóły zeszytu `/issues/{id}` (okładka, pełne metadane, badge źródła, breadcrumbs)
- [x] `GET /issues/{id}/cover` (cache + placeholder) i `GET /issues/{id}/download` (Content-Disposition z oryginalną nazwą)
- [x] Wyszukiwarka `/search` (serie po nazwie, zeszyty po tytule/numerze)
- [x] CSS: dark theme, responsywny grid okładek, lista zeszytów, widok szczegółów

## Etap 4 — Edycja metadanych ✅
- [x] Formularze edycji serii i zeszytu (pełne strony `/…/{id}/edit`, zwykłe formularze POST — działają bez JS)
- [x] Zapis → `metadata_source='manual'`, `metadata_locked=1`; przycisk odblokowania (dane zostają, scrape może nadpisać)
- [x] Badge źródła metadanych i kłódki na listach (z Etapu 3) + kłódka przy nazwie serii
- [x] Walidacja: nazwa serii wymagana (błąd renderowany w formularzu)

## Etap 5 — ComicVine ✅
- [x] `internal/comicvine`: klient (search/volume/issue, `field_list`, rate limiter 1 req/s, timeout 20 s, User-Agent, StripHTML dla opisów) + testy na mocku httptest
- [x] Strona dopasowania serii `/series/{id}/match` z listą kandydatów (okładka, rok, wydawca, liczba zeszytów) i wyborem użytkownika; zapis wzbogaca pusty opis/wydawcę serii
- [x] Scrape pojedynczego zeszytu (synchronicznie + komunikaty flash) i „wszystkich brakujących" w serii (w tle, jeden job naraz, postęp HTMX; z poszanowaniem blokad); normalizacja numerów ("055" ↔ "55")
- [x] Pobieranie okładek ComicVine do cache (zastępują miniatury z archiwum)
- [x] UI przy braku klucza API: funkcje wyłączone z podpowiedzią konfiguracji

## Etap 6 — Wykończenie ✅
- [x] Obsługa `file_missing` w UI: oznaczenie (badge + wyszarzenie) + „Usuń rekord" na liście serii i stronie zeszytu (z potwierdzeniem; rekordy istniejących plików chronione — 409)
- [x] Stylowana strona błędu (404/500, catch-all dla nieznanych tras), renderowanie stron przez bufor (czyste 500 zamiast urwanej strony), logowanie żądań (bez statyk i polling​u) i konfiguracji przy starcie
- [x] README (uruchomienie, konfiguracja, konwencje nazewnictwa plików, priorytety metadanych)
- [x] `go vet`, testy, build binarium na Windows (`comicnest.exe`)

**v1 ukończona.** 🎉

## Zmiany po v1
- [x] Dopasowanie/scrape serii ustawia też jej nazwę z ComicVine (chyba że seria zablokowana)
- [x] Długi opis serii zwijany do ~300 znaków z przełącznikiem (natywny `<details>`, bez JS)
- [x] Obsługa one-shotów: flaga `one_shot` (sygnały: wolumen CV z 1 zeszytem, ComicInfo `Format`/`Count`, 1 zeszyt bez numeru), etykieta „wydanie jednorazowe", kafelek → zeszyt, scrape dopasowuje jedyny zeszyt wolumenu; fallback parsera nazw obcina grupy `(...)` i łapie rok
- [x] Checkbox „wydanie jednorazowe" w edycji serii; ręczny wybór (jak każda edycja) blokuje serię — automatyczne sygnały one-shot (skan, ComicVine) szanują blokadę
- [x] Druga faza skanu: automatyczny scrape ComicVine dla zeszytów z dopasowaną serią, ale bez danych CV (bez blokad i brakujących plików); postęp „Aktualizacja ComicVine… X/Y" i licznik w podsumowaniu skanu („ComicVine: N zaktualizowano")
- [x] Fix pomieszanych okładek po rekreacji bazy: okładki serwowane z `Cache-Control: no-cache` (rewalidacja przez Last-Modified/304 zamiast max-age 24h — ID zeszytów są reużywane), a skan na starcie usuwa osierocone miniatury z `data/covers/`
- [x] Serwer OPDS 1.2 (`opds_enabled` w configu, opcjonalne Basic auth, `listen` do wystawienia w LAN): root → serie (nawigacja, paginacja) → zeszyty serii (akwizycja), „Ostatnio dodane", wyszukiwanie + OpenSearch, pliki i okładki pod `/opds/…`; pakiet `internal/opds` + testy httptest (SPECIFICATION §9a)
- [x] OPDS pod Thorium Reader: komunikat startowy z adresami IP (Thorium odrzuca `localhost`). Widok „półki" serii (grupy `rel="collection"`, pojedyncze wydania jako zeszyt) był wdrożony i wycofany — w Thorium robił większy bałagan niż lista
- [x] Strumieniowanie stron OPDS-PSE 1.2 (`/opds/issues/{id}/pages/{n}?width=`), postęp czytania zapisywany z żądań stron (`reading_progress`, migracja 3 z `issues.file_pages`), `pse:lastRead` w feedach, sekcja „Aktualnie czytane" (`/opds/reading`); `library.ExtractPage`, `covers.Resize`
- [x] Postęp czytania w UI WWW: pasek pod okładką i „czytane: str. X z N" na liście zeszytów serii, ramka z postępem i wiersz „Przeczytano" na stronie zeszytu (`readingView`, `issueRow`)
- [x] Konta użytkowników w `config.yaml` (`users`, hasła plaintext): logowanie WWW (sesja w ciasteczku, `/login`, `/logout`), to samo hasło w OPDS (Basic), postęp per użytkownik (migracja 4: `reading_progress(user, issue_id)`), przejęcie anonimowego postępu przez pierwsze konto
- [x] Usunięcie sekcji `opds` (`username`/`password`) z configu — hasła wyłącznie z `users`; `opds.enabled` zastąpione płaskim kluczem `opds_enabled`
- [x] Czytnik stron w przeglądarce (`/issues/{id}/read`, `reader.html` + `reader.js`): zoom, przewracanie klawiaturą/klikiem/gestem/suwakiem, pełny ekran, wznawianie od ostatniej strony, postęp wspólny z OPDS (`POST /issues/{id}/progress`, strony z `?track=0`), linki do poprzedniego/następnego zeszytu na dolnym pasku (popup „Koniec zeszytu" usunięty na życzenie użytkownika)
- [x] Ręczne oznaczanie zeszytu jako przeczytany / nieprzeczytany (przyciski na stronie zeszytu i na liście serii, `POST /issues/{id}/read|unread`, powrót przez `next`)
- [x] Grid biblioteki: zielony znaczek ✓ na przeczytanych seriach i filtry (nieczytane / w trakcie / przeczytane / bez ComicVine / brakujące pliki) obok sortowania; agregaty czytania w `ListSeries` (`SeriesFilter`)
- [x] Paginacja gridu biblioteki (`?page=N`, pager z oknem stron, sortowanie/filtr zachowane) — liczba kafelków na stronę w `config.yaml` (`page_size`, domyślnie 60, 0 = bez paginacji)
- [x] GitHub Actions (`.github/workflows/build.yml`): `go vet` + `go test` na każdym pushu i PR, binaria bez cgo dla windows/amd64, linux/amd64, linux/arm64, darwin/arm64 jako artefakty; push na `main` odświeża pre-release `latest`, tag `v*` tworzy wydanie z notatkami; wersja z `git describe` wstrzykiwana do `main.version`, logowana przy starcie i pokazywana w stopce strony głównej jako link do repozytorium GitHub (`server.Version`, `appVersion` w szablonach; lokalny build `dev` nie pokazuje stopki)
- [x] Ikona aplikacji z `logo.png` (pisklę czytające komiks w gnieździe; źródło 1254 px w korzeniu repo, warianty generowane przez skalowanie po przycięciu do obrysu): `favicon.png` 192 px + `favicon-32.png` + `favicon.ico` + `apple-touch-icon.png` w `<head>` każdej strony i pod `/favicon.ico` (bez logowania), logo także obok nazwy w górnym pasku i na stronie logowania (`.brand-logo`); w OPDS jako `<icon>` każdego feedu i `<Image>` w OpenSearch (`/static/favicon.png`)

## Pomysły na v2 (nie robić teraz)
- Czytnik: tryb dwóch stron obok siebie, kierunek czytania manga (prawo→lewo)
- Zapis metadanych do ComicInfo.xml w archiwum
- Okładki z PDF
- Obserwowanie zmian w bibliotece (fsnotify)
- Kolekcje / listy czytelnicze
