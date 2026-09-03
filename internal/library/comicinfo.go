package library

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"
)

// maxComicInfoSize is the maximum accepted size of a ComicInfo.xml entry.
const maxComicInfoSize = 5 * 1024 * 1024 // 5 MB

// ComicInfo mirrors the fields of the ComicInfo.xml standard that ComicNest uses.
type ComicInfo struct {
	Series    string `xml:"Series"`
	Number    string `xml:"Number"`
	Title     string `xml:"Title"`
	Summary   string `xml:"Summary"`
	Year      int    `xml:"Year"`
	Month     int    `xml:"Month"`
	Day       int    `xml:"Day"`
	Writer    string `xml:"Writer"`
	Penciller string `xml:"Penciller"`
	Inker     string `xml:"Inker"`
	Publisher string `xml:"Publisher"`
	PageCount int    `xml:"PageCount"`
	Format    string `xml:"Format"` // e.g. "One-Shot", "TPB", "Annual"
	Count     int    `xml:"Count"`  // total issues in the series, when known
}

// IsOneShot reports whether the embedded metadata marks this comic as a
// standalone publication rather than part of a series.
func (c *ComicInfo) IsOneShot() bool {
	f := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(c.Format, "-", ""), " ", ""))
	return strings.Contains(f, "oneshot") || c.Count == 1
}

// ReadComicInfo looks for a ComicInfo.xml entry (any directory level,
// case-insensitive name match on the basename) inside a CBZ/CBR archive.
// Returns (nil, false, nil) when the archive has no ComicInfo.xml; parse
// failures return an error.
func ReadComicInfo(archivePath string) (*ComicInfo, bool, error) {
	entries, err := openArchive(archivePath)
	if err != nil {
		return nil, false, fmt.Errorf("read ComicInfo.xml from %s: %w", archivePath, err)
	}
	defer entries.Close()

	for {
		e, err := entries.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, fmt.Errorf("read ComicInfo.xml from %s: %w", archivePath, err)
		}
		if e.IsDir || !strings.EqualFold(path.Base(e.Name), "ComicInfo.xml") {
			continue
		}
		if e.Size >= 0 && e.Size > maxComicInfoSize {
			return nil, false, fmt.Errorf("ComicInfo.xml in %s exceeds %d byte limit", archivePath, maxComicInfoSize)
		}
		rc, err := e.open()
		if err != nil {
			return nil, false, fmt.Errorf("open ComicInfo.xml in %s: %w", archivePath, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxComicInfoSize+1))
		rc.Close()
		if err != nil {
			return nil, false, fmt.Errorf("read ComicInfo.xml in %s: %w", archivePath, err)
		}
		if int64(len(data)) > maxComicInfoSize {
			return nil, false, fmt.Errorf("ComicInfo.xml in %s exceeds %d byte limit", archivePath, maxComicInfoSize)
		}
		info, err := parseComicInfo(data)
		if err != nil {
			return nil, false, fmt.Errorf("parse ComicInfo.xml in %s: %w", archivePath, err)
		}
		return info, true, nil
	}
	return nil, false, nil
}

// parseComicInfo decodes ComicInfo.xml contents tolerantly: unknown fields
// are ignored (default encoding/xml behavior) and strict mode is disabled so
// minor standard violations don't cause a hard failure.
func parseComicInfo(data []byte) (*ComicInfo, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var info ComicInfo
	if err := dec.Decode(&info); err != nil {
		return nil, fmt.Errorf("decode ComicInfo.xml: %w", err)
	}
	return &info, nil
}

// ReleaseDate formats Year/Month/Day as "2006-01-02", "2006-01", "2006" or ""
// depending on which parts are set (zero = unset).
func (c *ComicInfo) ReleaseDate() string {
	switch {
	case c.Year > 0 && c.Month > 0 && c.Day > 0:
		return fmt.Sprintf("%04d-%02d-%02d", c.Year, c.Month, c.Day)
	case c.Year > 0 && c.Month > 0:
		return fmt.Sprintf("%04d-%02d", c.Year, c.Month)
	case c.Year > 0:
		return fmt.Sprintf("%04d", c.Year)
	default:
		return ""
	}
}

// Artist returns Penciller, falling back to Inker.
func (c *ComicInfo) Artist() string {
	if c.Penciller != "" {
		return c.Penciller
	}
	return c.Inker
}
