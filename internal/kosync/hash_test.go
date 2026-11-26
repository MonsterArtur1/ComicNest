package kosync

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// naivePartialMD5 re-implements util.partialMD5 straight from the Lua,
// emulating LuaJIT's 32-bit bit.lshift (shift count masked to 5 bits), on an
// in-memory copy of the file.
func naivePartialMD5(data []byte) string {
	h := md5.New()
	for i := -1; i <= 10; i++ {
		shift := uint32(int32(2*i)) & 31
		off := int64(uint32(1024) << shift)
		if off >= int64(len(data)) {
			break
		}
		end := min(off+1024, int64(len(data)))
		h.Write(data[off:end])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func patterned(n int) []byte {
	b := make([]byte, n)
	x := uint32(12345)
	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}
	return b
}

func TestPartialMD5MatchesNaiveReads(t *testing.T) {
	dir := t.TempDir()
	// Sizes straddle every sampling boundary: empty, tiny, around 1 KiB and
	// 2 KiB (offset 1024's sample), a short tail read, and files long enough
	// to reach offsets up to 16 MiB.
	for _, size := range []int{0, 1, 1023, 1024, 1025, 2047, 2048, 4096, 4097, 5000, 70000, 300000, 1<<20 + 5, 5 << 20, 17<<20 + 100} {
		data := patterned(size)
		p := filepath.Join(dir, "f.bin")
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := PartialMD5(p)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if want := naivePartialMD5(data); got != want {
			t.Errorf("size %d: PartialMD5 = %s, want %s", size, got, want)
		}
	}
}

func TestPartialMD5IgnoresUnsampledBytes(t *testing.T) {
	dir := t.TempDir()
	data := patterned(100000)
	a := filepath.Join(dir, "a.bin")
	os.WriteFile(a, data, 0o644)
	ha, _ := PartialMD5(a)

	// Byte 3000 sits between samples [1024,2048) and [4096,5120): no effect.
	data[3000] ^= 0xff
	os.WriteFile(a, data, 0o644)
	if h, _ := PartialMD5(a); h != ha {
		t.Error("changing an unsampled byte changed the hash")
	}
	// Byte 4100 is inside the 4 KiB sample: must change it.
	data[4100] ^= 0xff
	os.WriteFile(a, data, 0o644)
	if h, _ := PartialMD5(a); h == ha {
		t.Error("changing a sampled byte did not change the hash")
	}
}

func TestPartialMD5Missing(t *testing.T) {
	if _, err := PartialMD5(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("want error for missing file")
	}
}

func TestFilenameMD5(t *testing.T) {
	sum := md5.Sum([]byte("Saga 055.cbz"))
	want := hex.EncodeToString(sum[:])
	if got := FilenameMD5(filepath.Join("some", "dir", "Saga 055.cbz")); got != want {
		t.Errorf("FilenameMD5 = %s, want %s", got, want)
	}
}

func TestPasswordKey(t *testing.T) {
	// Well-known md5("password").
	if got := PasswordKey("password"); got != "5f4dcc3b5aa765d61d8327deb882cf99" {
		t.Errorf("PasswordKey = %s", got)
	}
}
