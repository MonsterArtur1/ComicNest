// Package library implements comic archive scanning: opening CBZ/CBR files,
// listing and extracting pages, parsing ComicInfo.xml, and recovering
// series/issue metadata from file names.
package library

import (
	"archive/zip"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nwaples/rardecode/v2"
)

// maxCoverSize is the maximum size (in bytes) accepted for a single extracted
// page. Anything larger is refused rather than loaded fully into memory.
const maxCoverSize = 50 * 1024 * 1024 // 50 MB

// imageExts lists the file extensions treated as comic page images.
var imageExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".webp": true,
	".bmp":  true,
}

// isCBR reports whether archivePath's extension is .cbr (case-insensitive).
func isCBR(archivePath string) bool {
	return strings.EqualFold(filepath.Ext(archivePath), ".cbr")
}

// isImageExt reports whether name has a recognized image extension.
func isImageExt(name string) bool {
	return imageExts[strings.ToLower(path.Ext(name))]
}

// isPageEntry reports whether an archive entry should be treated as a comic
// page: not a directory, not hidden (basename starting with "." or "__"),
// and having an image extension.
func isPageEntry(name string, isDir bool) bool {
	if isDir {
		return false
	}
	base := path.Base(name)
	if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "__") {
		return false
	}
	return isImageExt(base)
}

// archiveEntry describes a single entry found while walking an archive.
type archiveEntry struct {
	Name  string
	IsDir bool
	Size  int64 // -1 when unknown
	open  func() (io.ReadCloser, error)
}

// archiveEntries provides sequential access to the entries of an opened
// archive, regardless of its underlying format.
type archiveEntries interface {
	// Next returns the next entry, or io.EOF when there are no more.
	Next() (archiveEntry, error)
	Close() error
}

// zipEntries adapts a *zip.ReadCloser to archiveEntries.
type zipEntries struct {
	zr  *zip.ReadCloser
	idx int
}

func (z *zipEntries) Next() (archiveEntry, error) {
	if z.idx >= len(z.zr.File) {
		return archiveEntry{}, io.EOF
	}
	f := z.zr.File[z.idx]
	z.idx++
	return archiveEntry{
		Name:  f.Name,
		IsDir: f.FileInfo().IsDir(),
		Size:  int64(f.UncompressedSize64),
		open:  f.Open,
	}, nil
}

func (z *zipEntries) Close() error { return z.zr.Close() }

// rarEntries adapts a *rardecode.ReadCloser to archiveEntries. RAR archives
// only support sequential access, so open() simply returns the shared reader
// positioned at the current entry; it must be read before calling Next again.
type rarEntries struct {
	rc *rardecode.ReadCloser
}

func (r *rarEntries) Next() (archiveEntry, error) {
	h, err := r.rc.Next()
	if err != nil {
		return archiveEntry{}, err
	}
	size := h.UnPackedSize
	if h.UnKnownSize {
		size = -1
	}
	rc := r.rc
	return archiveEntry{
		Name:  h.Name,
		IsDir: h.IsDir,
		Size:  size,
		open:  func() (io.ReadCloser, error) { return io.NopCloser(rc), nil },
	}, nil
}

func (r *rarEntries) Close() error { return r.rc.Close() }

func openZipEntries(archivePath string) (archiveEntries, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open zip %s: %w", archivePath, err)
	}
	return &zipEntries{zr: zr}, nil
}

func openRarEntries(archivePath string) (archiveEntries, error) {
	rc, err := rardecode.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open rar %s: %w", archivePath, err)
	}
	return &rarEntries{rc: rc}, nil
}

// openArchive opens archivePath, trying the format implied by its extension
// first. Some files in the wild have the "wrong" extension (a .cbz that is
// actually a RAR file or vice versa), so on failure the other format is
// attempted before giving up.
func openArchive(archivePath string) (archiveEntries, error) {
	first, second := openZipEntries, openRarEntries
	if isCBR(archivePath) {
		first, second = openRarEntries, openZipEntries
	}
	entries, err := first(archivePath)
	if err == nil {
		return entries, nil
	}
	if entries2, err2 := second(archivePath); err2 == nil {
		return entries2, nil
	}
	return nil, err
}

// ListPages returns the archive's image entries (.jpg .jpeg .png .gif .webp
// .bmp), sorted in natural order (case-insensitive, digit runs compared
// numerically: "page2.jpg" < "page10.jpg"). Skips directories, hidden entries
// (basename starting with "." or "__"), and non-image files. Paths are as
// stored in the archive.
func ListPages(archivePath string) ([]string, error) {
	entries, err := openArchive(archivePath)
	if err != nil {
		return nil, fmt.Errorf("list pages in %s: %w", archivePath, err)
	}
	defer entries.Close()

	var pages []string
	for {
		e, err := entries.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list pages in %s: %w", archivePath, err)
		}
		if isPageEntry(e.Name, e.IsDir) {
			pages = append(pages, e.Name)
		}
	}
	sort.Slice(pages, func(i, j int) bool { return NaturalLess(pages[i], pages[j]) })
	return pages, nil
}

// ExtractCover returns the raw bytes of the first page from ListPages order.
// Error if the archive has no image entries.
func ExtractCover(archivePath string) ([]byte, error) {
	pages, err := ListPages(archivePath)
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("archive %s has no image pages", archivePath)
	}
	return ExtractPage(archivePath, pages[0])
}

// ExtractPage returns the raw bytes of the named page entry (a name from
// ListPages). Each call reopens the archive, which for RAR means a sequential
// walk to the entry — fine for occasional reads, and page extraction time is
// dwarfed by whatever the caller does with the page anyway.
func ExtractPage(archivePath, pageName string) ([]byte, error) {
	entries, err := openArchive(archivePath)
	if err != nil {
		return nil, fmt.Errorf("extract page from %s: %w", archivePath, err)
	}
	defer entries.Close()

	for {
		e, err := entries.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("extract page from %s: %w", archivePath, err)
		}
		if e.Name != pageName {
			continue
		}
		if e.Size >= 0 && e.Size > maxCoverSize {
			return nil, fmt.Errorf("page %s in %s exceeds %d byte limit", pageName, archivePath, maxCoverSize)
		}
		rc, err := e.open()
		if err != nil {
			return nil, fmt.Errorf("open page %s in %s: %w", pageName, archivePath, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxCoverSize+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read page %s in %s: %w", pageName, archivePath, err)
		}
		if int64(len(data)) > maxCoverSize {
			return nil, fmt.Errorf("page %s in %s exceeds %d byte limit", pageName, archivePath, maxCoverSize)
		}
		return data, nil
	}
	return nil, fmt.Errorf("page %s not found in %s", pageName, archivePath)
}

// isDigit reports whether r is an ASCII digit.
func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// NaturalLess reports whether a sorts before b in natural order: comparison
// is case-insensitive, and runs of digits are compared by numeric value
// rather than lexicographically, so "page2.jpg" sorts before "page10.jpg".
func NaturalLess(a, b string) bool {
	ar := []rune(strings.ToLower(a))
	br := []rune(strings.ToLower(b))
	i, j := 0, 0
	for i < len(ar) && j < len(br) {
		ca, cb := ar[i], br[j]
		if isDigit(ca) && isDigit(cb) {
			si := i
			for i < len(ar) && isDigit(ar[i]) {
				i++
			}
			sj := j
			for j < len(br) && isDigit(br[j]) {
				j++
			}
			na := strings.TrimLeft(string(ar[si:i]), "0")
			nb := strings.TrimLeft(string(br[sj:j]), "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}
	return (len(ar) - i) < (len(br) - j)
}
