// Package tgz verifies KoboRoot.tgz archives (gzip integrity + full tar walk,
// capturing the member manifest and rejecting path traversal) and merges
// several verified archives into one staged tgz for the single-slot installer.
package tgz

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// safePrefixes are the roots a well-behaved Kobo package writes into. Entries
// outside these are warned about (not blocked) per §7.2.
var safePrefixes = []string{"usr/", "etc/", "opt/", "mnt/onboard/"}

// Resource caps guard against gzip/entry-count bombs. Real Kobo packages are a
// few MB with a handful of entries, so these never trigger in practice.
const (
	maxDecompressedBytes = 512 << 20 // 512 MiB of decompressed tar data
	maxEntries           = 100000    // maximum number of tar entries
)

// VerifyResult reports the outcome of walking one archive.
type VerifyResult struct {
	Manifest []string // normalized member paths (no leading ./), files and dirs
	Warnings []string // entries outside the expected roots
}

// Verify opens the gzip'd tar at path, walks every entry to confirm integrity,
// captures the member manifest, and rejects absolute paths and ".." traversal.
// Empty archives are rejected.
func Verify(path string) (*VerifyResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return verifyReader(f)
}

func verifyReader(r io.Reader) (*VerifyResult, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	// Cap total decompressed bytes to guard against gzip bombs.
	limited := &io.LimitedReader{R: gz, N: maxDecompressedBytes + 1}
	tr := tar.NewReader(limited)
	res := &VerifyResult{}
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue // pax global header: metadata, not a member (F4)
		}
		entries++
		if entries > maxEntries {
			return nil, fmt.Errorf("archive exceeds entry-count cap of %d", maxEntries)
		}
		name, err := normalize(hdr.Name)
		if err != nil {
			return nil, err
		}
		if name == "" {
			continue // bare "./"
		}
		if err := checkEntryType(hdr, name); err != nil {
			return nil, err
		}
		// Drain file contents to validate the gzip/tar stream end to end.
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return nil, fmt.Errorf("read %q: %w", hdr.Name, err)
		}
		if limited.N <= 0 {
			return nil, fmt.Errorf("archive exceeds decompressed-size cap of %d bytes", maxDecompressedBytes)
		}
		res.Manifest = append(res.Manifest, name)
		if !hasSafePrefix(name) {
			res.Warnings = append(res.Warnings, name)
		}
	}
	if len(res.Manifest) == 0 {
		return nil, fmt.Errorf("archive is empty")
	}
	return res, nil
}

// normalize strips a leading "./", validates, and returns the clean path.
// Absolute paths and ".." traversal are rejected.
func normalize(name string) (string, error) {
	n := strings.TrimPrefix(name, "./")
	n = strings.TrimPrefix(n, "/") // handled below, but be defensive
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("absolute path in archive: %q", name)
	}
	clean := path.Clean(n)
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path traversal in archive: %q", name)
	}
	return clean, nil
}

// checkEntryType rejects entry types and permission bits that are never
// legitimate in a KoboRoot.tgz and would be dangerous when extracted as root.
// normalizedName is the already-normalized member path (from normalize).
//
//   - Character/block devices and FIFOs are rejected outright.
//   - Symlink and hardlink targets are validated: absolute targets and targets
//     that escape the archive root (via "..") are rejected. Benign same-tree
//     relative links (e.g. libfoo.so -> libfoo.so.1) pass.
//   - setuid/setgid bits are rejected (a Kobo package has no legitimate need).
func checkEntryType(hdr *tar.Header, normalizedName string) error {
	switch hdr.Typeflag {
	case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
		return fmt.Errorf("disallowed device/fifo entry in archive: %q", hdr.Name)
	case tar.TypeSymlink, tar.TypeLink:
		if err := checkLinkTarget(normalizedName, hdr.Linkname); err != nil {
			return err
		}
	}
	if hdr.FileInfo().Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return fmt.Errorf("setuid/setgid entry in archive: %q", hdr.Name)
	}
	return nil
}

// checkLinkTarget validates a symlink/hardlink target. Absolute targets are
// rejected. Relative targets are resolved against the entry's directory and
// rejected if they escape the archive root.
func checkLinkTarget(normalizedName, linkname string) error {
	if linkname == "" {
		return fmt.Errorf("empty link target for %q", normalizedName)
	}
	if strings.HasPrefix(linkname, "/") {
		return fmt.Errorf("absolute link target in archive: %q -> %q", normalizedName, linkname)
	}
	resolved := path.Clean(path.Join(path.Dir(normalizedName), linkname))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("link target escapes archive root: %q -> %q", normalizedName, linkname)
	}
	return nil
}

func hasSafePrefix(name string) bool {
	for _, p := range safePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
