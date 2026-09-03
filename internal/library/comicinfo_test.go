package library

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

const testComicInfoXML = `<?xml version="1.0" encoding="utf-8"?>
<ComicInfo>
  <Series>Amazing Test</Series>
  <Number>5</Number>
  <Title>The Beginning</Title>
  <Summary>A test summary.</Summary>
  <Year>2020</Year>
  <Month>7</Month>
  <Day>15</Day>
  <Writer>Jane Writer</Writer>
  <Penciller>Pat Penciller</Penciller>
  <Inker>Ivan Inker</Inker>
  <Publisher>Test Comics</Publisher>
  <PageCount>2</PageCount>
</ComicInfo>`

// buildCBZ writes a zip archive at dir/name containing the given entries and
// returns its path.
func buildCBZ(t *testing.T, dir, name string, files map[string][]byte) string {
	t.Helper()
	archivePath := filepath.Join(dir, name)
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create %s: %v", archivePath, err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for entryName, content := range files {
		w, err := zw.Create(entryName)
		if err != nil {
			t.Fatalf("create entry %s: %v", entryName, err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatalf("write entry %s: %v", entryName, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return archivePath
}

func TestReadComicInfo(t *testing.T) {
	dir := t.TempDir()
	archivePath := buildCBZ(t, dir, "test.cbz", map[string][]byte{
		"ComicInfo.xml": []byte(testComicInfoXML),
		"page1.jpg":     []byte("fake-jpg-1"),
		"page2.jpg":     []byte("fake-jpg-2"),
	})

	info, found, err := ReadComicInfo(archivePath)
	if err != nil {
		t.Fatalf("ReadComicInfo: %v", err)
	}
	if !found {
		t.Fatal("ReadComicInfo: expected found=true")
	}
	if info.Series != "Amazing Test" {
		t.Errorf("Series = %q, want %q", info.Series, "Amazing Test")
	}
	if info.Number != "5" {
		t.Errorf("Number = %q, want %q", info.Number, "5")
	}
	if info.Title != "The Beginning" {
		t.Errorf("Title = %q, want %q", info.Title, "The Beginning")
	}
	if info.Publisher != "Test Comics" {
		t.Errorf("Publisher = %q, want %q", info.Publisher, "Test Comics")
	}
	if info.PageCount != 2 {
		t.Errorf("PageCount = %d, want 2", info.PageCount)
	}
	if got, want := info.ReleaseDate(), "2020-07-15"; got != want {
		t.Errorf("ReleaseDate() = %q, want %q", got, want)
	}
	if got, want := info.Artist(), "Pat Penciller"; got != want {
		t.Errorf("Artist() = %q, want %q", got, want)
	}
}

func TestReadComicInfoMissing(t *testing.T) {
	dir := t.TempDir()
	archivePath := buildCBZ(t, dir, "nocomicinfo.cbz", map[string][]byte{
		"page1.jpg": []byte("fake-jpg-1"),
	})

	info, found, err := ReadComicInfo(archivePath)
	if err != nil {
		t.Fatalf("ReadComicInfo: %v", err)
	}
	if found {
		t.Fatal("ReadComicInfo: expected found=false")
	}
	if info != nil {
		t.Fatalf("ReadComicInfo: expected nil info, got %+v", info)
	}
}

func TestReadComicInfoNestedPath(t *testing.T) {
	dir := t.TempDir()
	archivePath := buildCBZ(t, dir, "nested.cbz", map[string][]byte{
		"folder/ComicInfo.xml": []byte(testComicInfoXML),
		"folder/page1.jpg":     []byte("fake-jpg-1"),
	})

	_, found, err := ReadComicInfo(archivePath)
	if err != nil {
		t.Fatalf("ReadComicInfo: %v", err)
	}
	if !found {
		t.Fatal("ReadComicInfo: expected found=true for nested ComicInfo.xml")
	}
}

func TestComicInfoReleaseDate(t *testing.T) {
	cases := []struct {
		name string
		info ComicInfo
		want string
	}{
		{"full date", ComicInfo{Year: 2020, Month: 7, Day: 15}, "2020-07-15"},
		{"year and month", ComicInfo{Year: 2020, Month: 7}, "2020-07"},
		{"year only", ComicInfo{Year: 2020}, "2020"},
		{"nothing set", ComicInfo{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.ReleaseDate(); got != tc.want {
				t.Errorf("ReleaseDate() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestComicInfoArtist(t *testing.T) {
	withPenciller := ComicInfo{Penciller: "Pat Penciller", Inker: "Ivan Inker"}
	if got, want := withPenciller.Artist(), "Pat Penciller"; got != want {
		t.Errorf("Artist() = %q, want %q", got, want)
	}

	inkerOnly := ComicInfo{Inker: "Ivan Inker"}
	if got, want := inkerOnly.Artist(), "Ivan Inker"; got != want {
		t.Errorf("Artist() = %q, want %q", got, want)
	}

	neither := ComicInfo{}
	if got, want := neither.Artist(), ""; got != want {
		t.Errorf("Artist() = %q, want %q", got, want)
	}
}
