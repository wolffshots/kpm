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
	tr := tar.NewReader(gz)
	res := &VerifyResult{}
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
		name, err := normalize(hdr.Name)
		if err != nil {
			return nil, err
		}
		if name == "" {
			continue // bare "./"
		}
		// Drain file contents to validate the gzip/tar stream end to end.
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return nil, fmt.Errorf("read %q: %w", hdr.Name, err)
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

func hasSafePrefix(name string) bool {
	for _, p := range safePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
