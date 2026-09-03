# ComicNest

Osobisty katalog komiksów jako aplikacja webowa — jedno binarium Go, lokalny serwer,
biblioteka na Twoim dysku. Inspirowany [Komgą](https://komga.org/), ale prostszy i własny.

![Go](https://img.shields.io/badge/Go-1.24+-00ADD8) ![SQLite](https://img.shields.io/badge/SQLite-bez%20cgo-lightgrey)

## Funkcje

- **Skanowanie biblioteki** (`.cbz`, `.cbr`, `.pdf`) — serie z folderów, a dla plików
  luzem z nazwy pliku; ponowny skan wykrywa nowe i brakujące pliki bez duplikatów.
- **Metadane z ComicInfo.xml** osadzonego w archiwach (standard znany z Komgi).
- **Ręczna edycja metadanych** serii i zeszytów; edycja blokuje rekord 🔒 przed
  nadpisaniem przez automaty (blokadę można zdjąć).
- **ComicVine**: dopasowanie serii do wolumenu (z listą kandydatów do wyboru),
  pobieranie metadanych i okładek dla zeszytu lub całej serii, z rate limitem.
- **Okładki** wyciągane z pierwszej strony archiwum (cache miniatur na dysku).
- **Przeglądanie i wyszukiwanie**: grid serii, strona serii z filtrami
  (bez metadanych / z ComicVine / brakujące pliki), szczegóły zeszytu, pobieranie pliku.
- Frontend: Go templates + HTMX, dark theme, działa też bez JavaScriptu.

## Uruchomienie

Wymagany Go 1.24+ (kompilacja bez cgo — działa od ręki na Windows).

```
go build -o comicnest.exe ./cmd/comicnest
./comicnest.exe
```

Pierwsze uruchomienie tworzy `config.yaml` — uzupełnij ścieżkę biblioteki i uruchom
ponownie. Aplikacja wystartuje na `http://localhost:8080/` (nasłuchuje tylko na
localhost; brak logowania — to aplikacja osobista). Kliknij **Skanuj bibliotekę**.

## Konfiguracja (`config.yaml`)

```yaml
port: 8080
library: "D:/Komiksy"        # korzeń biblioteki komiksów
data_dir: "./data"           # baza SQLite + cache okładek
comicvine_api_key: ""        # klucz z https://comicvine.gamespot.com/api/
```

Bez klucza ComicVine aplikacja działa normalnie — funkcje dopasowania/scrape'u są
wyłączone z podpowiedzią w UI. Klucz trzymaj wyłącznie w `config.yaml` (plik jest
w `.gitignore`).

## Tłumaczenie komiksów na polski (AI)

ComicNest potrafi przetłumaczyć strony zeszytu (dymki) i zapisać wynik jako
`<nazwa> [PL].cbz` obok oryginału — nowy plik od razu trafia do katalogu jako
zablokowany wpis w tej samej serii. Ciężką robotę (detekcja dymków, OCR,
inpainting) wykonuje lokalnie działający
[manga-image-translator](https://github.com/zyddnys/manga-image-translator),
a samo tłumaczenie tekstu zleca skonfigurowanemu LLM.

Instalacja serwisu (raz, wymaga Pythona; GPU NVIDII mocno zalecane):

```
git clone https://github.com/zyddnys/manga-image-translator
cd manga-image-translator
pip install -r requirements.txt   # najlepiej w venv/conda
```

W katalogu projektu utwórz `.env` z kluczem LLM używanym do tłumaczenia:

```
OPENAI_API_KEY=sk-...
OPENAI_MODEL=gpt-4o-mini
```

Uruchom serwer (modele pobiorą się przy pierwszym użyciu):

```
cd server
python main.py --use-gpu
```

Na koniec wskaż serwis w `config.yaml` ComicNest:

```yaml
translator_url: "http://127.0.0.1:8001"
translator_engine: "chatgpt"   # też: gemini, deepseek, deepl, groq…
translator_lang: "POL"
```

Po restarcie na stronie zeszytu (CBZ/CBR) pojawi się przycisk
**„Przetłumacz na polski (AI)"** z postępem strona po stronie. Jedno
tłumaczenie naraz; z GPU strona zajmuje sekundy, na CPU — minuty.

## Organizacja biblioteki

Preferowana struktura — **folder = seria**:

```
D:/Komiksy/
├── Batman/
│   ├── Batman #001.cbz
│   └── Batman #002.cbz
└── Saga/
    └── Saga 055 (2020) (Digital).cbz
```

Pliki leżące luzem w korzeniu też są obsługiwane — seria powstaje z nazwy pliku.
Rozpoznawane wzorce nazw: `Tytuł #012`, `Tytuł 012 (2020)`, `Tytuł v2 015`;
grupy w nawiasach po numerze (np. `(Digital)`) są ignorowane, rok `(RRRR)` trafia
do daty wydania. Jeśli archiwum zawiera `ComicInfo.xml`, jego dane mają
pierwszeństwo nad nazwą pliku.

### Priorytety metadanych

`nazwa pliku → ComicInfo.xml → ComicVine → edycja ręczna` — skan i scrape nigdy nie
nadpisują danych z wyższego źródła; ręczna edycja dodatkowo blokuje rekord 🔒.

## Rozwój

- Dokumentacja projektowa: [docs/SPECIFICATION.md](docs/SPECIFICATION.md),
  plan wersji: [docs/ROADMAP.md](docs/ROADMAP.md).
- Testy i lint: `go test ./...`, `go vet ./...`.
- Struktura: `cmd/comicnest` (main), `internal/` (config, store, library, covers,
  comicvine, server), `web/` (szablony + statyki, wkompilowane przez `embed`).

Plany na v2: czytnik stron w przeglądarce, zapis do ComicInfo.xml, okładki z PDF,
automatyczne wykrywanie zmian w bibliotece.
