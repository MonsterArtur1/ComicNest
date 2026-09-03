package library

import (
	"reflect"
	"testing"
)

func TestListPages(t *testing.T) {
	dir := t.TempDir()
	archivePath := buildCBZ(t, dir, "pages.cbz", map[string][]byte{
		"b/page10.png": []byte("page10-content"),
		"b/page2.png":  []byte("page2-content"),
		"cover.jpg":    []byte("cover-content"),
		"notes.txt":    []byte("not an image"),
		".hidden.jpg":  []byte("hidden image"),
	})

	pages, err := ListPages(archivePath)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}

	// Natural order over the full entry path: "/" (0x2F) sorts before any
	// letter, so entries under "b/" come before the root-level "cover.jpg",
	// and "page2.png" comes before "page10.png" within "b/".
	want := []string{"b/page2.png", "b/page10.png", "cover.jpg"}
	if !reflect.DeepEqual(pages, want) {
		t.Errorf("ListPages() = %v, want %v", pages, want)
	}
}

func TestExtractCover(t *testing.T) {
	dir := t.TempDir()
	archivePath := buildCBZ(t, dir, "pages.cbz", map[string][]byte{
		"b/page10.png": []byte("page10-content"),
		"b/page2.png":  []byte("page2-content"),
		"cover.jpg":    []byte("cover-content"),
		"notes.txt":    []byte("not an image"),
	})

	data, err := ExtractCover(archivePath)
	if err != nil {
		t.Fatalf("ExtractCover: %v", err)
	}
	if got, want := string(data), "page2-content"; got != want {
		t.Errorf("ExtractCover() = %q, want %q", got, want)
	}
}

func TestExtractCoverNoImages(t *testing.T) {
	dir := t.TempDir()
	archivePath := buildCBZ(t, dir, "empty.cbz", map[string][]byte{
		"notes.txt": []byte("not an image"),
	})

	if _, err := ExtractCover(archivePath); err == nil {
		t.Fatal("ExtractCover: expected error for archive with no images")
	}
}

func TestIsPageEntry(t *testing.T) {
	cases := []struct {
		name  string
		isDir bool
		want  bool
	}{
		{"cover.jpg", false, true},
		{"folder/page1.png", false, true},
		{"folder", true, false},
		{"notes.txt", false, false},
		{".hidden.jpg", false, false},
		{"__MACOSX/__cover.jpg", false, false},
		{"image.WEBP", false, true},
	}
	for _, tc := range cases {
		if got := isPageEntry(tc.name, tc.isDir); got != tc.want {
			t.Errorf("isPageEntry(%q, %v) = %v, want %v", tc.name, tc.isDir, got, tc.want)
		}
	}
}
