package tgz

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
)

// Merge streams the entries of each source tgz (in the given order) into a
// single gzip'd tar at dst. Later entries win at extraction, so callers must
// order sources so the highest-priority package comes last (kpm itself last,
// per §7.3). Returns the list of paths that appeared in more than one source
// so the caller can log a WARN.
func Merge(sources []string, dst string) ([]string, error) {
	out, err := os.Create(dst)
	if err != nil {
		return nil, err
	}
	gw := gzip.NewWriter(out)
	tw := tar.NewWriter(gw)

	seen := map[string]bool{}
	dupSet := map[string]bool{}
	var dups []string

	for _, src := range sources {
		names, err := copyEntries(src, tw)
		if err != nil {
			tw.Close()
			gw.Close()
			out.Close()
			return nil, fmt.Errorf("%s: %w", src, err)
		}
		for _, n := range names {
			if seen[n] && !dupSet[n] {
				dupSet[n] = true
				dups = append(dups, n)
			}
			seen[n] = true
		}
	}

	if err := tw.Close(); err != nil {
		gw.Close()
		out.Close()
		return nil, err
	}
	if err := gw.Close(); err != nil {
		out.Close()
		return nil, err
	}
	if err := out.Sync(); err != nil { // durable before the caller renames it (B3)
		out.Close()
		return nil, err
	}
	if err := out.Close(); err != nil {
		return nil, err
	}
	return dups, nil
}

// copyEntries copies every entry from the source tgz into tw, returning the
// normalized names it wrote (for duplicate detection).
func copyEntries(src string, tw *tar.Writer) ([]string, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gr.Close()
	// Cap total decompressed bytes to guard against gzip bombs.
	limited := &io.LimitedReader{R: gr, N: maxDecompressedBytes + 1}
	tr := tar.NewReader(limited)

	var names []string
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
			continue // pax global header: not a member, skip entirely (F4)
		}
		entries++
		if entries > maxEntries {
			return nil, fmt.Errorf("archive exceeds entry-count cap of %d", maxEntries)
		}
		// Normalize and type-check BEFORE writing so a corrupted or foreign
		// source (the cache lives on a USB-mountable FAT partition) cannot
		// smuggle an absolute/".." path or a dangerous entry type through.
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
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return nil, err
		}
		if limited.N <= 0 {
			return nil, fmt.Errorf("archive exceeds decompressed-size cap of %d bytes", maxDecompressedBytes)
		}
		// Directory entries legitimately repeat across packages sharing a
		// parent; they must not count as duplicate paths (F5).
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}
