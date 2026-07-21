package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// libnickelName is the on-device path leaf we hunt for inside archives.
const libnickelName = "libnickel.so.1.0.0"

// elfMagic / zipMagic / gzipMagic identify inputs by content, not extension.
var (
	elfMagic  = []byte{0x7f, 'E', 'L', 'F'}
	zipMagic  = []byte{'P', 'K', 0x03, 0x04}
	gzipMagic = []byte{0x1f, 0x8b}
)

// LoadLibnickel resolves an input (https URL, firmware .zip, KoboRoot.tgz, or a
// bare libnickel.so*) to the raw ELF bytes of libnickel.so.1.0.0. URLs are
// downloaded into cacheDir (reused on subsequent runs). The returned string is a
// short human description of where the ELF came from.
func LoadLibnickel(input, cacheDir string) ([]byte, string, error) {
	path := input
	if isURL(input) {
		p, err := download(input, cacheDir)
		if err != nil {
			return nil, "", err
		}
		path = p
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, "", err
	}

	head := make([]byte, 8)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}

	switch {
	case bytes.HasPrefix(head, elfMagic):
		data, err := io.ReadAll(f)
		return data, "ELF " + filepath.Base(path), err

	case bytes.HasPrefix(head, gzipMagic):
		data, err := libnickelFromTgz(f)
		return data, "tgz " + filepath.Base(path), err

	case bytes.HasPrefix(head, zipMagic):
		data, err := libnickelFromZip(f, fi.Size())
		return data, "zip " + filepath.Base(path), err

	default:
		return nil, "", fmt.Errorf("%s: unrecognized input (not ELF, gzip/tgz, or zip)", filepath.Base(path))
	}
}

// libnickelFromZip finds libnickel inside a firmware update zip: either directly,
// or (the usual case) inside a KoboRoot.tgz member.
func libnickelFromZip(r io.ReaderAt, size int64) ([]byte, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("reading zip: %w", err)
	}

	var koboRoot *zip.File
	for _, zf := range zr.File {
		base := path(zf.Name)
		if base == libnickelName {
			rc, err := zf.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
		if strings.EqualFold(base, "KoboRoot.tgz") {
			koboRoot = zf
		}
	}
	if koboRoot == nil {
		return nil, fmt.Errorf("zip contains neither %s nor KoboRoot.tgz", libnickelName)
	}
	rc, err := koboRoot.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return libnickelFromTgz(rc)
}

// libnickelFromTgz streams a gzipped tar and returns the first libnickel member.
func libnickelFromTgz(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if path(hdr.Name) == libnickelName {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("tgz does not contain %s", libnickelName)
}

// path returns the final path element of a slash- or backslash-separated name.
func path(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// download fetches url into cacheDir, keyed by the URL's basename, and returns
// the local path. A cached file (non-empty, matching size when known) is reused.
func download(rawurl, cacheDir string) (string, error) {
	if cacheDir == "" {
		cacheDir = "."
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	u, err := url.Parse(rawurl)
	if err != nil {
		return "", err
	}
	name := filepath.Base(u.Path)
	if name == "" || name == "." || name == "/" {
		name = "download.bin"
	}
	dest := filepath.Join(cacheDir, name)

	resp, err := http.Get(rawurl) //nolint:gosec // URL is a user-supplied firmware mirror
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", rawurl, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: HTTP %s", rawurl, resp.Status)
	}

	// Reuse cache if the sizes match (or size is unknown and a file exists).
	if fi, err := os.Stat(dest); err == nil && fi.Size() > 0 {
		if resp.ContentLength <= 0 || fi.Size() == resp.ContentLength {
			fmt.Fprintf(os.Stderr, "  cache hit: %s (%s)\n", dest, humanBytes(fi.Size()))
			return dest, nil
		}
	}

	fmt.Fprintf(os.Stderr, "  downloading %s (%s) -> %s\n", rawurl, humanBytes(resp.ContentLength), dest)
	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	pw := &progressWriter{total: resp.ContentLength, last: time.Now()}
	if _, err := io.Copy(io.MultiWriter(out, pw), resp.Body); err != nil {
		out.Close()
		return "", fmt.Errorf("downloading: %w", err)
	}
	out.Close()
	fmt.Fprintln(os.Stderr) // finish the progress line
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// progressWriter prints a periodic download progress note to stderr.
type progressWriter struct {
	n     int64
	total int64
	last  time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	if time.Since(p.last) > 500*time.Millisecond {
		p.last = time.Now()
		if p.total > 0 {
			fmt.Fprintf(os.Stderr, "\r  %s / %s (%.0f%%)   ", humanBytes(p.n), humanBytes(p.total), 100*float64(p.n)/float64(p.total))
		} else {
			fmt.Fprintf(os.Stderr, "\r  %s   ", humanBytes(p.n))
		}
	}
	return len(b), nil
}

func humanBytes(n int64) string {
	if n < 0 {
		return "unknown size"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
