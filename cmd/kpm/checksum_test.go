package main

// checksum_test.go pins feature C (INTEGRITY.md §5): registry-carried per-release
// sha256 verification at download time. Before it, TLS was kpm's ENTIRE integrity
// boundary (internal/forge/http.go:59) — a forge or CDN serving different bytes
// under the same URL was undetectable.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/forge"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// assetServer serves one release (tag) whose single asset is body, and answers
// the network probe on every other path.
func assetServer(t *testing.T, a *App, tag string, body []byte) string {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/repos/"):
			fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":"KoboRoot.tgz","size":%d,"browser_download_url":%q}]}`,
				tag, len(body), srv.URL+"/download/KoboRoot.tgz")
		case strings.HasPrefix(r.URL.Path, "/download/"):
			w.Write(body)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	a.client = forge.NewClientWithHTTP(srv.Client())
	return strings.TrimPrefix(srv.URL, "https://")
}

// registerChecksumPkg writes a packages.d def carrying a checksums table.
func registerChecksumPkg(t *testing.T, a *App, id, host string, sums map[string]string) {
	t.Helper()
	if err := config.Save(a.paths.PackageFile(id), &config.Package{
		Name: id, Source: host + "/o/" + id, Forge: config.ForgeForgejo,
		Asset: "KoboRoot.tgz", Checksums: sums,
	}); err != nil {
		t.Fatal(err)
	}
}

// §5.4: a mismatch is a hard failure that leaves NO file at the final cache name
// and no .part behind, so a later run can never pick up bad bytes.
func TestChecksumMismatchRefusesAndLeavesNoFile(t *testing.T) {
	a := newTestApp(t)
	body := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/pkg/x": "real"})
	srv := serveAsset(t, a, body)
	wrong := strings.Repeat("ab", 32)

	_, _, err := a.download("pkg", "v1.0.0", wrong, true, true, forge.Asset{
		Name: "KoboRoot.tgz", DownloadURL: srv.URL + "/KoboRoot.tgz",
	})
	if err == nil {
		t.Fatal("a checksum mismatch must fail the download")
	}
	want := fmt.Sprintf("checksum mismatch for asset %q of pkg v1.0.0: registry declares sha256 %s, downloaded %s — refusing to install",
		"KoboRoot.tgz", wrong, sha256Hex(body))
	if err.Error() != want {
		t.Errorf("error = %q\nwant  %q", err, want)
	}
	if names := cacheNames(t, a); len(names) != 0 {
		t.Errorf("neither the tgz nor the .part may survive a mismatch, cache = %v", names)
	}
	if !logContains(t, a, "MISMATCH want="+wrong) {
		t.Error("a mismatch must leave a CHECKSUM log line")
	}
}

// The happy path: a matching hash downloads and stages exactly as before.
func TestChecksumMatchProceeds(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	body := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/pkg/x": "real"})
	host := assetServer(t, a, "v2.0.0", body)
	registerChecksumPkg(t, a, "pkg", host, map[string]string{"v2.0.0": sha256Hex(body)})
	a.state.Get("pkg").InstalledVersion = "v1.0.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	var code int
	captureStdout(t, func() { code = a.cmdUpdate([]string{"pkg"}) })
	if code != exitOK {
		t.Fatalf("a matching checksum must install normally, exit = %d", code)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err != nil {
		t.Errorf("the package should have staged: %v", err)
	}
	if !logContains(t, a, "sha256="+sha256Hex(body)+" (verified)") {
		t.Error("a verified download must say so in the CHECKSUM log line")
	}
}

// §5.3: a checksums table that does not name THIS release is simply unverified —
// the download proceeds exactly as today, with no warning and no exit change.
// This is the normal state for 11 of 12 registry entries.
func TestChecksumAbsentTagIsUnverified(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	body := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/pkg/x": "real"})
	host := assetServer(t, a, "v2.0.0", body)
	// Attested for an older release only.
	registerChecksumPkg(t, a, "pkg", host, map[string]string{"v1.0.0": strings.Repeat("ab", 32)})
	a.state.Get("pkg").InstalledVersion = "v1.0.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"pkg"}) })
	if code != exitOK {
		t.Fatalf("an unattested release must install normally, exit = %d\n%s", code, out)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err != nil {
		t.Errorf("the package should have staged: %v", err)
	}
	if strings.Contains(out, "checksum") {
		t.Errorf("an unattested release must produce no stdout noise:\n%s", out)
	}
	// The log distinguishes "curator attests for this package but not this
	// release" from "no attestation at all" — a useful, otherwise-invisible signal.
	if !logContains(t, a, "(unverified — registry has checksums but none for this tag)") {
		t.Error("the log should say the registry has checksums but none for this tag")
	}

	// And a def with no checksums table at all reads differently.
	b := newTestApp(t)
	setVersion(t, "0.10.0")
	hostB := assetServer(t, b, "v2.0.0", body)
	registerChecksumPkg(t, b, "pkg", hostB, nil)
	b.state.Get("pkg").InstalledVersion = "v1.0.0"
	if err := b.state.Save(); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() { b.cmdUpdate([]string{"pkg"}) })
	if !logContains(t, b, "(unverified — no registry checksum)") {
		t.Error("a def with no checksums table should log the plain unverified suffix")
	}
}

// §5.4 ordering: the checksum describes the asset EXACTLY as the forge publishes
// it, so a zip-wrapped asset is hashed as the ZIP — before the unwrap rewrites
// the file. Hashing the inner tgz instead would make every curator-recorded hash
// (taken with `curl | sha256sum`) wrong.
func TestChecksumMatchedBeforeZipUnwrap(t *testing.T) {
	a := newTestApp(t)
	inner := tgzBytes(t, map[string]string{"./usr/local/Kobo/imageformats/libnickelclock.so": "so"})
	zipped := zipBytes(t, map[string][]byte{"NickelClock/KoboRoot.tgz": inner})
	srv := serveAsset(t, a, zipped)

	// The ZIP's hash verifies and unwrapping still happens.
	cache, manifest, err := a.download("nickelclock", "v0.4.0", sha256Hex(zipped), true, true, forge.Asset{
		Name: "NickelClock-v0.4.0.zip", DownloadURL: srv.URL + "/NickelClock-v0.4.0.zip",
	})
	if err != nil {
		t.Fatalf("the zip's own hash must verify: %v", err)
	}
	if filepath.Clean(cache) != filepath.Join(a.paths.CacheDir(), "nickelclock-v0.4.0.tgz") {
		t.Errorf("cache path = %q", cache)
	}
	if len(manifest) != 1 {
		t.Errorf("the zip must still be unwrapped, manifest = %v", manifest)
	}

	// The INNER tgz's hash must NOT verify — that is the ordering bug.
	b := newTestApp(t)
	srvB := serveAsset(t, b, zipped)
	if _, _, err := b.download("nickelclock", "v0.4.0", sha256Hex(inner), true, true, forge.Asset{
		Name: "NickelClock-v0.4.0.zip", DownloadURL: srvB.URL + "/NickelClock-v0.4.0.zip",
	}); err == nil {
		t.Fatal("hashing the inner tgz instead of the published zip must not verify")
	}
}

// §5.2: a curator writing "0.4.0" against an upstream tag of "v0.4.0" still gets
// verification instead of silently getting none — and vice versa.
func TestChecksumTagNormalization(t *testing.T) {
	body := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/pkg/x": "real"})
	sum := sha256Hex(body)
	cases := []struct{ upstream, declared string }{
		{"v0.4.0", "0.4.0"},
		{"0.4.0", "v0.4.0"},
	}
	for _, c := range cases {
		a := newTestApp(t)
		setVersion(t, "0.10.0")
		host := assetServer(t, a, c.upstream, body)
		registerChecksumPkg(t, a, "pkg", host, map[string]string{c.declared: sum})
		a.state.Get("pkg").InstalledVersion = "v0.1.0"
		if err := a.state.Save(); err != nil {
			t.Fatal(err)
		}
		var code int
		captureStdout(t, func() { code = a.cmdUpdate([]string{"pkg"}) })
		if code != exitOK {
			t.Fatalf("upstream %q vs declared %q: exit = %d", c.upstream, c.declared, code)
		}
		if !logContains(t, a, "(verified)") {
			t.Errorf("upstream %q vs declared %q: verification did not happen", c.upstream, c.declared)
		}
	}
}

// §5.4 blast radius: one package's download fails; the others still stage and
// the run exits 2 (partial), not 1.
func TestChecksumMismatchIsPartialNotTotal(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	body := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/pkg/x": "real"})
	host := assetServer(t, a, "v2.0.0", body)
	registerChecksumPkg(t, a, "bad", host, map[string]string{"v2.0.0": strings.Repeat("ab", 32)})
	registerChecksumPkg(t, a, "good", host, map[string]string{"v2.0.0": sha256Hex(body)})
	a.state.Get("bad").InstalledVersion = "v1.0.0"
	a.state.Get("good").InstalledVersion = "v1.0.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStdout(t, func() {
		captureStderr(t, func() { code = a.cmdUpdate([]string{"--all", "--json"}) })
	})
	if code != exitPartial {
		t.Fatalf("one mismatch + one good must be partial (exit %d), got %d\n%s", exitPartial, code, out)
	}
	var m jsonMutation
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Changed) != 1 || m.Changed[0] != "good" {
		t.Errorf("changed = %v, want [good]", m.Changed)
	}
	if len(m.Failed) != 1 || m.Failed[0].ID != "bad" ||
		!strings.Contains(m.Failed[0].Error, "checksum mismatch for asset") {
		t.Errorf("failed = %+v, want a checksum mismatch for bad", m.Failed)
	}
	// Nothing was staged for the failed package.
	if got := a.state.Get("bad").StagedVersion; got != "" {
		t.Errorf("the mismatching package must not be staged, staged_version = %q", got)
	}
	// §1.2 item 5: checksum state never reaches --json.
	if strings.Contains(lastJSON(t, out), "checksum_") {
		t.Error("no checksum-specific JSON field may be added")
	}
}
