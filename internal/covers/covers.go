// Package covers manages an on-disk cache of issue cover thumbnails.
//
// Thumbnails are stored as JPEG files named "{issueID}.jpg" inside a
// configured directory. Saved thumbnails are scaled down to at most 400px
// wide while preserving aspect ratio; images narrower than that are left
// untouched (never upscaled).
package covers

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/gif" // register GIF decoder
	_ "image/png" // register PNG decoder
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/image/draw"

	_ "golang.org/x/image/bmp"  // register BMP decoder
	_ "golang.org/x/image/webp" // register WebP decoder
)

const (
	// maxThumbWidth is the maximum width, in pixels, of generated thumbnails.
	maxThumbWidth = 400

	// jpegQuality is the quality used when encoding thumbnails.
	jpegQuality = 85

	// maxRawBytes is the largest raw input accepted by Save.
	maxRawBytes = 50 * 1024 * 1024

	// maxDimension is the largest source image width or height accepted.
	maxDimension = 10000
)

// Cache stores issue cover thumbnails as JPEG files in a directory.
type Cache struct {
	dir string
}

// New returns a Cache rooted at dir (must already exist).
func New(dir string) *Cache {
	return &Cache{dir: dir}
}

// Path returns the on-disk path for an issue's thumbnail.
func (c *Cache) Path(issueID int64) string {
	return filepath.Join(c.dir, strconv.FormatInt(issueID, 10)+".jpg")
}

// Cleanup deletes thumbnails whose issue id is not in valid — leftovers from
// deleted issues or an earlier, recreated database (issue ids get reused, so
// a stale file would show another comic's cover). Returns how many files
// were removed.
func (c *Cache) Cleanup(valid map[int64]bool) (int, error) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0, fmt.Errorf("covers: cleanup: %w", err)
	}

	removed := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jpg" {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimSuffix(e.Name(), ".jpg"), 10, 64)
		if err != nil || valid[id] {
			continue
		}
		if err := os.Remove(filepath.Join(c.dir, e.Name())); err != nil {
			return removed, fmt.Errorf("covers: cleanup %s: %w", e.Name(), err)
		}
		removed++
	}
	return removed, nil
}

// Save decodes raw (JPEG/PNG/GIF/WebP/BMP), scales it down to at most 400 px
// wide and writes it as {dir}/{issueID}.jpg (quality 85). Atomic: writes to a
// temp file in dir first, then renames over the target.
func (c *Cache) Save(issueID int64, raw []byte) error {
	if len(raw) > maxRawBytes {
		return fmt.Errorf("covers: raw input too large (%d bytes, max %d)", len(raw), maxRawBytes)
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("covers: decoding image config: %w", err)
	}
	if cfg.Width > maxDimension || cfg.Height > maxDimension {
		return fmt.Errorf("covers: image dimensions too large (%dx%d, max %dx%d)", cfg.Width, cfg.Height, maxDimension, maxDimension)
	}

	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("covers: decoding image: %w", err)
	}

	thumb := scale(src, maxThumbWidth)

	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return fmt.Errorf("covers: creating cache dir: %w", err)
	}

	tmp, err := os.CreateTemp(c.dir, "cover-*.tmp")
	if err != nil {
		return fmt.Errorf("covers: creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		// Best-effort cleanup; ignore errors if the file was already
		// renamed or removed.
		_ = os.Remove(tmpPath)
	}()

	if err := jpeg.Encode(tmp, thumb, &jpeg.Options{Quality: jpegQuality}); err != nil {
		tmp.Close()
		return fmt.Errorf("covers: encoding jpeg: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("covers: closing temp file: %w", err)
	}

	target := c.Path(issueID)
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("covers: removing existing thumbnail: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("covers: renaming temp file into place: %w", err)
	}

	return nil
}

// scale returns src resized so its width is at most maxWidth, preserving
// aspect ratio. Images already narrower than maxWidth are returned as an
// RGBA copy without upscaling.
func scale(src image.Image, maxWidth int) *image.RGBA {
	bounds := src.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()

	dstW, dstH := srcW, srcH
	if srcW > maxWidth && srcW > 0 {
		dstW = maxWidth
		dstH = int(float64(srcH) * float64(maxWidth) / float64(srcW))
		if dstH < 1 {
			dstH = 1
		}
	}

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)
	return dst
}

// Has reports whether a thumbnail exists for the issue.
func (c *Cache) Has(issueID int64) bool {
	_, err := os.Stat(c.Path(issueID))
	return err == nil
}

// Remove deletes the thumbnail if present; missing file is not an error.
func (c *Cache) Remove(issueID int64) error {
	if err := os.Remove(c.Path(issueID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("covers: removing thumbnail: %w", err)
	}
	return nil
}
