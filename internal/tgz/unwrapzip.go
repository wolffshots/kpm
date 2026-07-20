package tgz

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// Some packages publish their release asset as a zip wrapping the real
// KoboRoot.tgz (e.g. NickelClock's NickelClock-<tag>.zip, whose sole entry is
// KoboRoot.tgz). IsZip and Unwrap let download() transparently extract the
// inner tgz so everything downstream runs unchanged (ZIP-ASSETS §1).

// maxUnwrappedBytes caps the tgz extracted from a zip asset, mirroring the
// decompressed-data cap in tgz.go (ZIP-ASSETS §2). A var, not a const, so a test
// can lower it instead of building a 512 MiB fixture.
var maxUnwrappedBytes int64 = 512 << 20 // 512 MiB

// maxZipEntries caps how many zip entries are walked while hunting for the
// inner tgz (ZIP-ASSETS §2).
const maxZipEntries = 10000

// IsZip reports whether the file at path starts with the zip local-file-header
// magic "PK\x03\x04". Detection is by content, not filename (ZIP-ASSETS §1.1);
// short or unreadable files are not zips and fall through to tgz.Verify.
func IsZip(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	return magic == [4]byte{'P', 'K', 0x03, 0x04}
}

// Unwrap extracts the single inner *.tgz/*.tar.gz entry of the zip at srcZip
// to dstTgz via the same .part → fsync → rename discipline download() uses
// (B3), so the result is indistinguishable from a directly-downloaded tgz.
// Exactly one entry may match (base name, case-insensitive — the tgz may sit
// inside a directory like NickelClock/KoboRoot.tgz); zero or more than one is
// an error, and non-tgz siblings (README, licenses) are ignored (ZIP-ASSETS
// §1.2). The caller deletes srcZip on success and on every error path.
func Unwrap(srcZip, dstTgz string) error {
	f, err := os.Open(srcZip)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	// archive/zip wants ReaderAt+size: read the archive from disk, never
	// buffered in memory (ZIP-ASSETS §2).
	zr, err := zip.NewReader(f, fi.Size())
	if err != nil {
		return fmt.Errorf("zip: %w", err)
	}
	var match *zip.File
	count := 0
	for i, zf := range zr.File {
		if i >= maxZipEntries {
			return fmt.Errorf("zip asset exceeds entry-count cap of %d", maxZipEntries)
		}
		if !isTgzName(zf.Name) {
			continue
		}
		count++
		match = zf
	}
	if count == 0 {
		return fmt.Errorf("zip asset contains no .tgz")
	}
	if count > 1 {
		return fmt.Errorf("zip asset contains %d .tgz entries, want exactly 1", count)
	}
	// Same path hygiene as the tar walk: absolute paths and ".." traversal in
	// the matched entry's name are rejected (ZIP-ASSETS §2).
	if _, err := normalize(match.Name); err != nil {
		return err
	}
	rc, err := match.Open()
	if err != nil {
		return fmt.Errorf("zip: %w", err)
	}
	defer rc.Close()

	part := dstTgz + ".part"
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	// Hard-stop the extracted size at the cap with a clear error, never a
	// silent truncation (ZIP-ASSETS §2).
	limited := &io.LimitedReader{R: rc, N: maxUnwrappedBytes + 1}
	_, werr := io.Copy(out, limited)
	if werr == nil && limited.N <= 0 {
		werr = fmt.Errorf("zip asset's inner tgz exceeds size cap of %d bytes", maxUnwrappedBytes)
	}
	if werr == nil {
		werr = out.Sync() // durable before the rename to the final name (B3)
	}
	cerr := out.Close()
	if werr != nil {
		os.Remove(part)
		return werr
	}
	if cerr != nil {
		os.Remove(part)
		return cerr
	}
	if err := os.Rename(part, dstTgz); err != nil {
		os.Remove(part)
		return err
	}
	return nil
}

// isTgzName reports whether a zip entry's base name matches *.tgz or *.tar.gz,
// case-insensitively (ZIP-ASSETS §1.2). Directory entries never match.
func isTgzName(name string) bool {
	if strings.HasSuffix(name, "/") {
		return false // directory entry
	}
	base := strings.ToLower(path.Base(name))
	return strings.HasSuffix(base, ".tgz") || strings.HasSuffix(base, ".tar.gz")
}
