// Package kosync holds the pieces of KOReader's progress-sync protocol that
// are independent of HTTP and storage: the two document identifiers KOReader
// derives from a file.
package kosync

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// partialOffsets are the byte offsets KOReader's util.partialMD5 samples. The
// Lua loop is `for i = -1, 10 do seek(lshift(1024, 2*i)); read(1024)`; with
// LuaJIT's 32-bit bit.lshift, i = -1 shifts by -2, which wraps to 30 and
// pushes 1024 out of 32 bits — offset 0. i = 0..10 give 1 KiB, 4 KiB, 16 KiB
// ... 1 GiB (each step 4x).
var partialOffsets = func() []int64 {
	offs := []int64{0}
	for i := 0; i <= 10; i++ {
		offs = append(offs, int64(1024)<<(2*i))
	}
	return offs
}()

const partialSampleSize = 1024

// PartialMD5 computes KOReader's "Binary" document id: the md5 (lowercase
// hex) of up to 1 KiB read at each of 12 offsets, stopping at the first
// offset at or past the end of the file. A short read at the end of the file
// is included.
func PartialMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := md5.New()
	buf := make([]byte, partialSampleSize)
	for _, off := range partialOffsets {
		n, err := f.ReadAt(buf, off)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if n == 0 {
					break
				}
				continue
			}
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FilenameMD5 computes KOReader's "Filename" document id: the md5 (lowercase
// hex) of the file's base name, extension included.
func FilenameMD5(path string) string {
	sum := md5.Sum([]byte(filepath.Base(path)))
	return hex.EncodeToString(sum[:])
}

// PasswordKey is what KOReader sends as x-auth-key: the md5 hex of the
// password.
func PasswordKey(password string) string {
	sum := md5.Sum([]byte(password))
	return hex.EncodeToString(sum[:])
}
