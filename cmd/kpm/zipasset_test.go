package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"kpm/internal/config"
	"kpm/internal/forge"
)

// tgzBytes builds a gzip'd tar in memory (name -> content, tar-style ./ names).
func tgzBytes(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		data := entries[n]
		hdr := &tar.Header{Name: n, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zipBytes wraps the given entries (name -> raw content) in a zip in memory.
func zipBytes(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(entries[n]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// serveAsset returns a TLS test server serving body at any path, wired into a.
func serveAsset(t *testing.T, a *App, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	a.client = forge.NewClientWithHTTP(srv.Client())
	return srv
}

// cacheNames returns the sorted entry names in the cache dir.
func cacheNames(t *testing.T, a *App) []string {
	t.Helper()
	entries, err := os.ReadDir(a.paths.CacheDir())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// ZIP-ASSETS §4.1/§5: a zip-wrapped KoboRoot.tgz (the real NickelClock asset
// shape) downloads, unwraps to the normal cache path, and installs cleanly in
// the KPM_ROOT sandbox; the cache holds only <id>-<tag>.tgz and the manifest
// matches the inner tgz.
func TestDownloadZipAssetUnwrapsAndStages(t *testing.T) {
	a := newTestApp(t)
	inner := tgzBytes(t, map[string]string{
		"./usr/local/Kobo/imageformats/libnickelclock.so": "so",
		"./mnt/onboard/.adds/nickelclock/uninstall":       "trigger",
	})
	// The tgz sits inside a directory entry, like real release zips often do
	// (§4.2), with a non-tgz sibling that must be ignored.
	body := zipBytes(t, map[string][]byte{
		"NickelClock/KoboRoot.tgz": inner,
		"NickelClock/README.md":    []byte("ignored"),
	})
	srv := serveAsset(t, a, body)

	cache, manifest, err := a.download("nickelclock", "v0.4.0", forge.Asset{
		Name: "NickelClock-v0.4.0.zip", DownloadURL: srv.URL + "/NickelClock-v0.4.0.zip",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(a.paths.CacheDir(), "nickelclock-v0.4.0.tgz")
	if filepath.Clean(cache) != want {
		t.Errorf("cache path = %q, want %q", cache, want)
	}
	if got, _ := os.ReadFile(cache); !bytes.Equal(got, inner) {
		t.Error("cached tgz must be byte-identical to the zip's inner tgz")
	}
	sort.Strings(manifest)
	wantManifest := []string{
		"mnt/onboard/.adds/nickelclock/uninstall",
		"usr/local/Kobo/imageformats/libnickelclock.so",
	}
	if len(manifest) != 2 || manifest[0] != wantManifest[0] || manifest[1] != wantManifest[1] {
		t.Errorf("manifest = %v", manifest)
	}
	// No zip, no .part — only the unwrapped tgz stays in cache (§1.3).
	if names := cacheNames(t, a); len(names) != 1 || names[0] != "nickelclock-v0.4.0.tgz" {
		t.Errorf("cache dir = %v, want only the tgz", names)
	}

	// The extracted tgz stages like any other (merge + commit in the sandbox).
	a.stageForTest(t, []resolved{{pkg: &config.Package{ID: "nickelclock"}, tag: "v0.4.0", cache: cache, manifest: manifest}})
	members := tgzMembers(t, a.paths.StagedTgz())
	if members["./usr/local/Kobo/imageformats/libnickelclock.so"] != "so" ||
		members["./mnt/onboard/.adds/nickelclock/uninstall"] != "trigger" {
		t.Errorf("staged tgz members = %v", members)
	}
}

// ZIP-ASSETS §4.5: a plain tgz asset behaves exactly as before (regression).
func TestDownloadPlainTgzUnchanged(t *testing.T) {
	a := newTestApp(t)
	body := tgzBytes(t, map[string]string{"./usr/local/App/bin/app": "bin"})
	srv := serveAsset(t, a, body)

	cache, manifest, err := a.download("app", "v1", forge.Asset{
		Name: "KoboRoot.tgz", DownloadURL: srv.URL + "/KoboRoot.tgz",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(cache); !bytes.Equal(got, body) {
		t.Error("plain tgz must be cached byte-for-byte")
	}
	if len(manifest) != 1 || manifest[0] != "usr/local/App/bin/app" {
		t.Errorf("manifest = %v", manifest)
	}
	if names := cacheNames(t, a); len(names) != 1 || names[0] != "app-v1.tgz" {
		t.Errorf("cache dir = %v", names)
	}
}

// ZIP-ASSETS §4.3 (wiring): a zip with no inner tgz fails the download and
// leaves the cache clean — no zip, no .part.
func TestDownloadZipWithoutTgzFailsClean(t *testing.T) {
	a := newTestApp(t)
	body := zipBytes(t, map[string][]byte{"README.md": []byte("no tgz here")})
	srv := serveAsset(t, a, body)

	_, _, err := a.download("bad", "v1", forge.Asset{
		Name: "bad-v1.zip", DownloadURL: srv.URL + "/bad-v1.zip",
	})
	if err == nil {
		t.Fatal("zip without a tgz must fail")
	}
	if names := cacheNames(t, a); len(names) != 0 {
		t.Errorf("cache must be clean after the failure, got %v", names)
	}
}
