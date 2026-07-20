package tgz

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestZip builds a zip at path whose entries map names to raw content.
func writeTestZip(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// readFile returns the file's bytes, failing the test on error.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// noLeftovers asserts neither dst nor its .part temp exists.
func noLeftovers(t *testing.T, dst string) {
	t.Helper()
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Errorf("%s should not exist after a failed unwrap", dst)
	}
	if _, err := os.Lstat(dst + ".part"); !os.IsNotExist(err) {
		t.Errorf("%s.part should not be left behind", dst)
	}
}

// ZIP-ASSETS §4.8: sniffer table — zip magic, gzip magic, empty, short.
func TestIsZipSniffer(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"zip", []byte{'P', 'K', 0x03, 0x04, 0x14, 0x00}, true},
		{"gzip", []byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00}, false},
		{"empty", nil, false},
		{"short", []byte{'P', 'K', 0x03}, false},
	}
	for _, c := range cases {
		p := filepath.Join(dir, c.name)
		if err := os.WriteFile(p, c.data, 0o644); err != nil {
			t.Fatal(err)
		}
		if got := IsZip(p); got != c.want {
			t.Errorf("IsZip(%s) = %v, want %v", c.name, got, c.want)
		}
	}
	if IsZip(filepath.Join(dir, "does-not-exist")) {
		t.Error("IsZip on a missing file should be false")
	}
}

// ZIP-ASSETS §4.1 (core): the single inner tgz is extracted byte-identically,
// non-tgz siblings are ignored, no temp files remain, and Verify accepts it.
func TestUnwrapSingleTgz(t *testing.T) {
	dir := t.TempDir()
	inner := filepath.Join(dir, "inner.tgz")
	writeTestTgz(t, inner, []entry{
		{"./usr/local/Kobo/imageformats/libnickelclock.so", "so"},
		{"./mnt/onboard/.adds/nickelclock/uninstall", "trigger"},
	})
	innerBytes := readFile(t, inner)
	src := filepath.Join(dir, "asset.zip")
	writeTestZip(t, src, map[string][]byte{
		"KoboRoot.tgz": innerBytes,
		"README.md":    []byte("ignored sibling"),
		"LICENSE":      []byte("ignored sibling"),
	})

	dst := filepath.Join(dir, "out.tgz")
	if err := Unwrap(src, dst); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dst); string(got) != string(innerBytes) {
		t.Error("extracted tgz differs from the wrapped one")
	}
	if _, err := os.Lstat(dst + ".part"); !os.IsNotExist(err) {
		t.Error(".part temp should be renamed away")
	}
	res, err := Verify(dst)
	if err != nil {
		t.Fatalf("extracted tgz must pass Verify: %v", err)
	}
	if len(res.Manifest) != 2 {
		t.Errorf("manifest = %v", res.Manifest)
	}
}

// ZIP-ASSETS §4.2: the tgz may sit inside a directory entry; matching is on the
// base name, case-insensitively, and .tar.gz also matches.
func TestUnwrapNestedAndCaseInsensitive(t *testing.T) {
	for _, name := range []string{"NickelClock/KoboRoot.tgz", "Nested/Deeper/KOBOROOT.TGZ", "pkg/rootfs.tar.gz"} {
		dir := t.TempDir()
		inner := filepath.Join(dir, "inner.tgz")
		writeTestTgz(t, inner, []entry{{"./usr/local/App/f", "x"}})
		src := filepath.Join(dir, "asset.zip")
		writeTestZip(t, src, map[string][]byte{
			"NickelClock/":       nil, // directory entry: never a match
			name:                 readFile(t, inner),
			"NickelClock/README": []byte("sibling"),
		})
		dst := filepath.Join(dir, "out.tgz")
		if err := Unwrap(src, dst); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if _, err := Verify(dst); err != nil {
			t.Errorf("%s: extracted tgz must verify: %v", name, err)
		}
	}
}

// ZIP-ASSETS §4.3: zero tgz entries is an error and leaves nothing behind.
func TestUnwrapNoTgz(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "asset.zip")
	writeTestZip(t, src, map[string][]byte{"README.md": []byte("only docs")})
	dst := filepath.Join(dir, "out.tgz")
	err := Unwrap(src, dst)
	if err == nil || !strings.Contains(err.Error(), "contains no .tgz") {
		t.Errorf("want 'contains no .tgz' error, got %v", err)
	}
	noLeftovers(t, dst)
}

// ZIP-ASSETS §4.4: two tgz entries is an error naming the count.
func TestUnwrapMultipleTgz(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "asset.zip")
	writeTestZip(t, src, map[string][]byte{
		"KoboRoot.tgz":  []byte("a"),
		"other/two.tgz": []byte("b"),
	})
	dst := filepath.Join(dir, "out.tgz")
	err := Unwrap(src, dst)
	if err == nil || !strings.Contains(err.Error(), "2 .tgz entries, want exactly 1") {
		t.Errorf("want count-naming error, got %v", err)
	}
	noLeftovers(t, dst)
}

// ZIP-ASSETS §4.6: a truncated/corrupt zip errors cleanly with no leftovers.
func TestUnwrapCorruptZip(t *testing.T) {
	dir := t.TempDir()

	// Garbage that only carries the magic.
	garbage := filepath.Join(dir, "garbage.zip")
	if err := os.WriteFile(garbage, []byte("PK\x03\x04 not actually a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.tgz")
	if err := Unwrap(garbage, dst); err == nil {
		t.Error("garbage zip must error")
	}
	noLeftovers(t, dst)

	// A real zip truncated mid-archive (central directory lost).
	whole := filepath.Join(dir, "whole.zip")
	inner := filepath.Join(dir, "inner.tgz")
	writeTestTgz(t, inner, []entry{{"./usr/local/App/f", "x"}})
	writeTestZip(t, whole, map[string][]byte{"KoboRoot.tgz": readFile(t, inner)})
	b := readFile(t, whole)
	trunc := filepath.Join(dir, "trunc.zip")
	if err := os.WriteFile(trunc, b[:len(b)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Unwrap(trunc, dst); err == nil {
		t.Error("truncated zip must error")
	}
	noLeftovers(t, dst)
}

// ZIP-ASSETS §4.7: an inner entry streaming past the size cap is a hard error
// (cap lowered via the package-level var; no giant fixture).
func TestUnwrapOversizeCapped(t *testing.T) {
	old := maxUnwrappedBytes
	maxUnwrappedBytes = 64
	defer func() { maxUnwrappedBytes = old }()

	dir := t.TempDir()
	src := filepath.Join(dir, "asset.zip")
	writeTestZip(t, src, map[string][]byte{"KoboRoot.tgz": make([]byte, 4096)})
	dst := filepath.Join(dir, "out.tgz")
	err := Unwrap(src, dst)
	if err == nil || !strings.Contains(err.Error(), "size cap") {
		t.Errorf("want size-cap error, got %v", err)
	}
	noLeftovers(t, dst)
}

// ZIP-ASSETS §2: traversal in the matched entry's name is rejected.
func TestUnwrapRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "asset.zip")
	writeTestZip(t, src, map[string][]byte{"../evil.tgz": []byte("x")})
	dst := filepath.Join(dir, "out.tgz")
	if err := Unwrap(src, dst); err == nil {
		t.Error("traversal entry name must be rejected")
	}
	noLeftovers(t, dst)
}
