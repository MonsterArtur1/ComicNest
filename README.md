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
- **Czytnik w przeglądarce** dla CBZ/CBR: strona po stronie, zoom (dopasowanie do
  wysokości lub szerokości, skala), klawiatura, kliknięcia, gesty, pełny ekran; wznawia od
  ostatniej strony i dzieli postęp z czytnikami OPDS.
- **Serwer OPDS** (opcjonalny) — biblioteka dostępna w czytnikach komiksów na telefonie
  i tablecie (Panels, Chunky, Moon+ Reader, Librera, KOReader…), z okładkami,
  wyszukiwaniem i opcjonalnym hasłem.
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
listen: localhost            # "0.0.0.0", żeby inne urządzenia w sieci widziały serwer
library: "D:/Komiksy"        # korzeń biblioteki komiksów
data_dir: "./data"           # baza SQLite + cache okładek
comicvine_api_key: ""        # klucz z https://comicvine.gamespot.com/api/
opds:
  enabled: false             # katalog OPDS pod http://…/opds
users:                       # konta (opcjonalne); puste = brak logowania
  - name: artur
    password: sekret         # hasło zapisane jawnie
```

## Konta użytkowników

Wpisz konta w sekcji `users`. Od tej chwili interfejs WWW wymaga zalogowania (formularz na
`/login`, „Wyloguj" w nagłówku), a czytniki OPDS pytają o tę samą nazwę i hasło. Każdy
użytkownik ma własny postęp czytania: pasek postępu, znaczki „przeczytane", filtry i sekcja
„Aktualnie czytane" pokazują tylko jego dane. Postęp sprzed wprowadzenia kont trafia na
pierwsze konto z listy. Bez sekcji `users` wszystko działa jak dotąd, bez logowania.
Hasła są w pliku jawnym tekstem, więc trzymaj `config.yaml` poza repozytorium.

Bez klucza ComicVine aplikacja działa normalnie — funkcje dopasowania/scrape'u są
wyłączone z podpowiedzią w UI. Klucz trzymaj wyłącznie w `config.yaml` (plik jest
w `.gitignore`).

## Czytniki komiksów (OPDS)

Ustaw `opds.enabled: true` i `listen: 0.0.0.0`, uruchom ponownie, a w czytniku dodaj
katalog OPDS o adresie `http://<adres-komputera>:8080/opds` (adres IP komputera
z biblioteką w sieci domowej). Katalog oferuje:

- **Wszystkie serie** (alfabetycznie, z okładkami) → zeszyty serii do pobrania,
- **Aktualnie czytane** — zeszyty zaczęte w czytniku, ale nieprzeczytane do końca,
- **Ostatnio dodane** — zeszyty w kolejności trafienia do biblioteki,
- **wyszukiwanie** po nazwie serii, tytule i numerze zeszytu (OpenSearch),
- **czytanie bez pobierania** (OPDS-PSE): czytniki takie jak Panels, Chunky, Librera czy
  Moon+ Reader strumieniują strony CBZ/CBR bezpośrednio z serwera i wznawiają od ostatniej
  strony. Postęp zapisuje się na serwerze podczas czytania; PDF-y są tylko do pobrania.

Postęp czytania widać też w interfejsie WWW: pasek pod okładką na liście zeszytów serii,
liczba przeczytanych stron i data ostatniego czytania na stronie zeszytu, a na gridzie
biblioteki zielony znaczek ✓ przy seriach przeczytanych do końca. Grid ma filtry:
nieczytane, w trakcie czytania, przeczytane, bez metadanych z ComicVine, brakujące pliki.
Zeszyt można też ręcznie oznaczyć jako przeczytany lub nieprzeczytany (przyciski na
stronie zeszytu i na liście zeszytów serii).

Na tym samym komputerze (np. Thorium Reader na PC) użyj `http://127.0.0.1:8080/opds` —
Thorium odrzuca adresy bez domeny, więc `http://localhost:8080/opds` kończy się błędem
„Błąd dostępu do kanału". Serwer wypisuje przy starcie wszystkie działające adresy.

Jeśli zdefiniujesz konta w `users`, czytnik zapyta o login (HTTP Basic) i będzie widział
postęp tego użytkownika. Przy `listen: 0.0.0.0` bez kont interfejs WWW jest otwarty
w sieci lokalnej, dlatego w takim układzie warto konta dodać. Zeszyty oznaczone jako
brakujące na dysku nie pojawiają się w katalogu.

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
