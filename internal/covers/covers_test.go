package covers

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// makePNG generates an in-memory PNG of the given dimensions filled with a
// solid color, for use as test fixture input.
func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding test png: %v", err)
	}
	return buf.Bytes()
}

func decodeJPEGDims(t *testing.T, path string) (int, int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer f.Close()
	cfg, err := jpeg.DecodeConfig(f)
	if err != nil {
		t.Fatalf("decoding jpeg config for %s: %v", path, err)
	}
	return cfg.Width, cfg.Height
}

func TestSave(t *testing.T) {
	tests := []struct {
		name       string
		w, h       int
		wantW      int
		wantH      int
	}{
		{name: "large image scaled down", w: 800, h: 1200, wantW: 400, wantH: 600},
		{name: "small image not upscaled", w: 200, h: 300, wantW: 200, wantH: 300},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			c := New(dir)

			raw := makePNG(t, tt.w, tt.h)
			if err := c.Save(1, raw); err != nil {
				t.Fatalf("Save() error = %v", err)
			}

			path := c.Path(1)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("expected thumbnail file to exist: %v", err)
			}

			gotW, gotH := decodeJPEGDims(t, path)
			if gotW != tt.wantW || gotH != tt.wantH {
				t.Errorf("thumbnail dims = %dx%d, want %dx%d", gotW, gotH, tt.wantW, tt.wantH)
			}
		})
	}
}

func TestSaveOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	c := New(dir)

	if err := c.Save(1, makePNG(t, 800, 1200)); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}
	firstW, firstH := decodeJPEGDims(t, c.Path(1))
	if firstW != 400 || firstH != 600 {
		t.Fatalf("unexpected initial dims: %dx%d", firstW, firstH)
	}

	if err := c.Save(1, makePNG(t, 200, 300)); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}
	secondW, secondH := decodeJPEGDims(t, c.Path(1))
	if secondW != 200 || secondH != 300 {
		t.Errorf("thumbnail not overwritten: got %dx%d, want 200x300", secondW, secondH)
	}
}

func TestHasAndRemove(t *testing.T) {
	dir := t.TempDir()
	c := New(dir)

	if c.Has(1) {
		t.Errorf("Has() = true before Save, want false")
	}

	if err := c.Save(1, makePNG(t, 200, 300)); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if !c.Has(1) {
		t.Errorf("Has() = false after Save, want true")
	}

	if err := c.Remove(1); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	if c.Has(1) {
		t.Errorf("Has() = true after Remove, want false")
	}

	if err := c.Remove(1); err != nil {
		t.Errorf("Remove() on nonexistent file returned error: %v", err)
	}
}

func TestSaveInvalidImage(t *testing.T) {
	dir := t.TempDir()
	c := New(dir)

	err := c.Save(1, []byte("this is definitely not an image"))
	if err == nil {
		t.Fatal("Save() with non-image bytes expected error, got nil")
	}
}

func TestPath(t *testing.T) {
	dir := t.TempDir()
	c := New(dir)

	want := filepath.Join(dir, "42.jpg")
	if got := c.Path(42); got != want {
		t.Errorf("Path(42) = %q, want %q", got, want)
	}
}
