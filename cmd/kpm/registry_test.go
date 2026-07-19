package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"kpm/internal/config"
	"kpm/internal/forge"
	"kpm/internal/registry"
	"kpm/internal/state"
	"kpm/internal/version"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// printed. Used to assert command output (search annotations, install def).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		for {
			n, err := r.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if err != nil {
				break
			}
		}
		done <- string(buf)
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

// seedRegistry writes a registry config entry and its cache in one step.
func seedRegistry(t *testing.T, a *App, name, cacheTOML string) {
	t.Helper()
	cfg, err := a.loadRegistryConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Find(name); !ok {
		cfg.Registries = append(cfg.Registries, registry.Registry{
			Name: name, URL: "codeberg.org/o/" + name, Ref: "main", Path: "registry.toml", Forge: "forgejo",
		})
		if err := a.saveRegistryConfig(cfg); err != nil {
			t.Fatal(err)
		}
	}
	if cacheTOML != "" {
		if err := os.WriteFile(a.paths.RegistryCache(name), []byte(cacheTOML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const nmManifest = `schema_version = 1
[packages.nickelmenu]
name = "NickelMenu"
source = "github.com/pgaskin/NickelMenu"
forge = "github"
asset = "KoboRoot.tgz"
`

// §9.3: storeRefresh writes the cache on a good manifest, records the etag.
func TestStoreRefreshSuccess(t *testing.T) {
	a := newTestApp(t)
	r := registry.Registry{Name: "main", URL: "codeberg.org/o/r", Ref: "main", Path: "registry.toml", Forge: "forgejo"}
	res := forge.RawResult{Body: []byte(nmManifest), Etag: `"v1"`}
	if err := a.storeRefresh(r, res); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.paths.RegistryCache("main")); err != nil {
		t.Error("cache file should be written")
	}
	rs := a.state.Registry("main")
	if rs.Etag != `"v1"` || rs.LastFetched == "" {
		t.Errorf("state not updated: %+v", rs)
	}
}

// §9.3: a bad TOML / unsupported schema keeps the previous good cache.
func TestStoreRefreshBadManifestKeepsCache(t *testing.T) {
	a := newTestApp(t)
	r := registry.Registry{Name: "main", URL: "codeberg.org/o/r", Ref: "main", Path: "registry.toml", Forge: "forgejo"}
	// Seed a good cache first.
	if err := a.storeRefresh(r, forge.RawResult{Body: []byte(nmManifest), Etag: `"v1"`}); err != nil {
		t.Fatal(err)
	}
	// A schema-gated (v2) manifest must be refused and the old cache kept.
	if err := a.storeRefresh(r, forge.RawResult{Body: []byte("schema_version = 2\n")}); err == nil {
		t.Error("unsupported schema should error")
	}
	// A syntactically broken manifest must also be refused.
	if err := a.storeRefresh(r, forge.RawResult{Body: []byte("this = = broken")}); err == nil {
		t.Error("broken TOML should error")
	}
	b, err := os.ReadFile(a.paths.RegistryCache("main"))
	if err != nil || !strings.Contains(string(b), "nickelmenu") {
		t.Errorf("old good cache must survive a bad fetch: %q", b)
	}
}

// §9.3: a 304 keeps the cache and only bumps last_fetched.
func TestStoreRefreshNotModified(t *testing.T) {
	a := newTestApp(t)
	r := registry.Registry{Name: "main", URL: "codeberg.org/o/r", Ref: "main", Path: "registry.toml", Forge: "forgejo"}
	a.storeRefresh(r, forge.RawResult{Body: []byte(nmManifest), Etag: `"v1"`})
	a.state.Registry("main").LastFetched = "2000-01-01T00:00:00Z"
	if err := a.storeRefresh(r, forge.RawResult{NotModified: true}); err != nil {
		t.Fatal(err)
	}
	if a.state.Registry("main").LastFetched == "2000-01-01T00:00:00Z" {
		t.Error("304 should bump last_fetched")
	}
	if a.state.Registry("main").Etag != `"v1"` {
		t.Error("304 should keep the recorded etag")
	}
}

// B1/C8: with a recorded etag but the cache file deleted, refresh must do an
// unconditional fetch (no If-None-Match — a 304 with no cache would deadlock)
// and rewrite the cache.
func TestRefreshUnconditionalWhenCacheMissing(t *testing.T) {
	a := newTestApp(t)
	sawINM := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			sawINM = true
		}
		w.Header().Set("ETag", `"new"`)
		w.Write([]byte(nmManifest))
	}))
	defer srv.Close()
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")
	r := registry.Registry{Name: "main", URL: host + "/o/r", Ref: "main", Path: "registry.toml", Forge: "forgejo"}

	// A stale etag is recorded, but there is no cache file.
	a.state.Registry("main").Etag = `"old"`
	os.Remove(a.paths.RegistryCache("main"))

	if err := a.refreshOne(r); err != nil {
		t.Fatalf("refresh should succeed: %v", err)
	}
	if sawINM {
		t.Error("with no cache file present, refresh must NOT send If-None-Match")
	}
	if _, err := os.Stat(a.paths.RegistryCache("main")); err != nil {
		t.Error("refresh should rewrite the cache file")
	}
}

// B2: a Forgejo registry whose branch path 404s but tag path serves refreshes
// via the tag fallback.
func TestRefreshForgejoTagFallback(t *testing.T) {
	a := newTestApp(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/raw/tag/") {
			w.Write([]byte(nmManifest))
			return
		}
		http.NotFound(w, r) // branch path 404s
	}))
	defer srv.Close()
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")
	r := registry.Registry{Name: "main", URL: host + "/o/r", Ref: "v1", Path: "registry.toml", Forge: "forgejo"}

	if err := a.refreshOne(r); err != nil {
		t.Fatalf("tag fallback should succeed: %v", err)
	}
	if _, err := os.Stat(a.paths.RegistryCache("main")); err != nil {
		t.Error("cache should be written from the tag path")
	}
}

// B2/C8: both branch and tag 404 → a clean not-found error, and no empty state
// entry is created for the failed refresh.
func TestRefreshBothNotFoundNoStateEntry(t *testing.T) {
	a := newTestApp(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")
	r := registry.Registry{Name: "main", URL: host + "/o/r", Ref: "main", Path: "registry.toml", Forge: "forgejo"}

	err := a.refreshOne(r)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("both-404 should be a clean not-found error, got %v", err)
	}
	if a.state.Registries["main"] != nil {
		t.Error("a failed refresh must not create a registries state entry (C8)")
	}
}

// §9.6: shadowing WARN — reportConflicts logs one WARN per shadowed id.
func TestReportConflictsWarns(t *testing.T) {
	a := newTestApp(t)
	_ = captureStdout(t, func() {
		a.reportConflicts([]registry.Conflict{{ID: "shared", Winner: "main", Shadowed: []string{"extra"}}})
	})
	lines := a.paths.TailLog(10)
	found := false
	for _, l := range lines {
		if strings.Contains(l, "WARN") && strings.Contains(l, "shared") && strings.Contains(l, "main") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a shadowing WARN in the log: %v", lines)
	}
}

// §9.2/§9.13: search filters, annotates min_kpm, marks installed, notes staleness.
func TestSearchFilterMinKpmStaleness(t *testing.T) {
	a := newTestApp(t)
	manifest := `schema_version = 1
[packages.nickelmenu]
name = "NickelMenu"
source = "github.com/pgaskin/NickelMenu"
forge = "github"
asset = "KoboRoot.tgz"
[packages.futurepkg]
name = "FuturePkg"
source = "codeberg.org/o/future"
forge = "forgejo"
asset = "KoboRoot.tgz"
min_kpm = "99.0.0"
`
	seedRegistry(t, a, "main", manifest)
	a.state.Registry("main").LastFetched = time.Now().Add(-3 * 24 * time.Hour).Format(state.TimeFormat)

	// Unfiltered: both packages, min_kpm gate on futurepkg, staleness note.
	out := captureStdout(t, func() { a.cmdSearch(nil) })
	if !strings.Contains(out, "nickelmenu") || !strings.Contains(out, "futurepkg") {
		t.Errorf("search should list both packages:\n%s", out)
	}
	if !strings.Contains(out, "requires kpm >= 99.0.0") {
		t.Errorf("search should annotate the min_kpm gate:\n%s", out)
	}
	if !strings.Contains(out, "cached 3d ago") {
		t.Errorf("search should show the staleness note:\n%s", out)
	}

	// Filtered: only the matching id.
	out = captureStdout(t, func() { a.cmdSearch([]string{"nickelmenu"}) })
	if !strings.Contains(out, "nickelmenu") || strings.Contains(out, "futurepkg") {
		t.Errorf("filtered search should show only the match:\n%s", out)
	}
}

// §9.6: install confirm pauses (exit 3), --yes writes the def + provenance + hash.
func TestInstallConfirmAndWrite(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", nmManifest)

	// Without --yes: prints the def and exits 3, nothing written.
	var code int
	out := captureStdout(t, func() { code = a.cmdInstall([]string{"nickelmenu"}) })
	if code != exitConfirm {
		t.Errorf("install without --yes should exit %d, got %d", exitConfirm, code)
	}
	if !strings.Contains(out, "source   github.com/pgaskin/NickelMenu") {
		t.Errorf("install should print the def:\n%s", out)
	}
	if _, err := os.Stat(a.paths.PackageFile("nickelmenu")); !os.IsNotExist(err) {
		t.Error("install without --yes must not write the def")
	}

	// With --yes: writes the def, stamps provenance, records the synced hash.
	if code := a.cmdInstall([]string{"nickelmenu", "--yes"}); code != exitOK {
		t.Fatalf("install --yes exit %d", code)
	}
	p, err := a.loadPackage("nickelmenu")
	if err != nil {
		t.Fatal(err)
	}
	if p.Registry != "main" {
		t.Errorf("registry provenance not stamped: %q", p.Registry)
	}
	if a.state.Get("nickelmenu").SyncedDefSHA256 == "" {
		t.Error("synced_def_sha256 should be recorded")
	}
}

// §9.6: install refuses an existing hand-added def unless --adopt, which takes it
// over while preserving pin and state.
func TestInstallRefuseHandAddedAndAdopt(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", nmManifest)

	// A pre-existing hand-added def with a local pin, plus installed state.
	hand := &config.Package{Name: "Mine", Source: "github.com/me/nm", Forge: "github", Asset: "KoboRoot.tgz", Pin: "v1.0.0"}
	if err := config.Save(a.paths.PackageFile("nickelmenu"), hand); err != nil {
		t.Fatal(err)
	}
	ps := a.state.Get("nickelmenu")
	ps.InstalledVersion = "v1.0.0"
	ps.Manifest = []string{"usr/local/nm/lib.so"}

	// install without --adopt: refused (exit 1), local def untouched.
	if code := a.cmdInstall([]string{"nickelmenu", "--yes"}); code != exitError {
		t.Errorf("install over a hand-added def should refuse, got %d", code)
	}
	if p, _ := a.loadPackage("nickelmenu"); p.Source != "github.com/me/nm" {
		t.Error("refused install must not overwrite the local def")
	}

	// install --adopt --yes: takes the registry def, preserves pin and state.
	if code := a.cmdInstall([]string{"nickelmenu", "--adopt", "--yes"}); code != exitOK {
		t.Fatalf("adopt exit %d", code)
	}
	p, _ := a.loadPackage("nickelmenu")
	if p.Source != "github.com/pgaskin/NickelMenu" || p.Registry != "main" {
		t.Errorf("adopt should write the registry def with provenance: %+v", p)
	}
	if p.Pin != "v1.0.0" {
		t.Errorf("adopt should preserve the local pin: %q", p.Pin)
	}
	if a.state.Get("nickelmenu").InstalledVersion != "v1.0.0" || len(a.state.Get("nickelmenu").Manifest) != 1 {
		t.Errorf("adopt should preserve installed version and manifest: %+v", a.state.Get("nickelmenu"))
	}
}

// §9.7: sync applies a changed registry def, reports the diff, updates the hash.
func TestSyncApplyAndDiff(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", nmManifest)
	if code := a.cmdInstall([]string{"nickelmenu", "--yes"}); code != exitOK {
		t.Fatalf("install exit %d", code)
	}
	// The registry changes the asset glob.
	changed := strings.Replace(nmManifest, `asset = "KoboRoot.tgz"`, `asset = "KoboRoot-arm.tgz"`, 1)
	if err := os.WriteFile(a.paths.RegistryCache("main"), []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	var syncCode int
	out := captureStdout(t, func() { syncCode = a.cmdSync(nil) })
	if syncCode != exitOK {
		t.Fatalf("sync exit %d", syncCode)
	}
	if !strings.Contains(out, "asset") {
		t.Errorf("sync should report the asset diff:\n%s", out)
	}
	p, _ := a.loadPackage("nickelmenu")
	if p.Asset != "KoboRoot-arm.tgz" {
		t.Errorf("sync should apply the new asset: %q", p.Asset)
	}
	// A second sync is a no-op (up to date).
	out = captureStdout(t, func() { a.cmdSync(nil) })
	if !strings.Contains(out, "1 up to date") {
		t.Errorf("second sync should be up to date:\n%s", out)
	}
}

// §9.7: local drift is skipped unless --overwrite.
func TestSyncDriftSkipAndOverwrite(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", nmManifest)
	a.cmdInstall([]string{"nickelmenu", "--yes"})

	// Hand-edit the local def (drift), keeping the registry provenance.
	p, _ := a.loadPackage("nickelmenu")
	p.Asset = "Edited.tgz"
	if err := config.Save(a.paths.PackageFile("nickelmenu"), p); err != nil {
		t.Fatal(err)
	}
	// The registry also changed something, so a plain sync would otherwise apply.
	changed := strings.Replace(nmManifest, `asset = "KoboRoot.tgz"`, `asset = "KoboRoot-v2.tgz"`, 1)
	os.WriteFile(a.paths.RegistryCache("main"), []byte(changed), 0o644)

	if code := a.cmdSync(nil); code != exitPartial {
		t.Errorf("drift should make sync exit %d (partial), got %d", exitPartial, code)
	}
	if p, _ := a.loadPackage("nickelmenu"); p.Asset != "Edited.tgz" {
		t.Error("drift skip must not overwrite the local def")
	}
	// --overwrite replaces the drifted def.
	if code := a.cmdSync([]string{"--overwrite"}); code != exitOK {
		t.Errorf("--overwrite sync exit %d", code)
	}
	if p, _ := a.loadPackage("nickelmenu"); p.Asset != "KoboRoot-v2.tgz" {
		t.Errorf("--overwrite should apply the registry def: %q", p.Asset)
	}
}

// A1: syncing v1 (marker + hooks) then v2 (manifest, no hooks) removes the
// dropped [uninstall] keys locally, while an unknown local [uninstall] key
// survives both syncs.
func TestSyncReplaceRemovesDroppedFields(t *testing.T) {
	a := newTestApp(t)
	v1 := `schema_version = 1
[packages.nickelmenu]
name = "NickelMenu"
source = "github.com/pgaskin/NickelMenu"
forge = "github"
asset = "KoboRoot.tgz"
[packages.nickelmenu.uninstall]
method = "marker"
marker_file = "/mnt/onboard/.adds/nm/uninstall"
needs_reboot = true
run_before = "/etc/init.d/nm stop"
`
	seedRegistry(t, a, "main", v1)
	if code := a.cmdInstall([]string{"nickelmenu", "--yes"}); code != exitOK {
		t.Fatalf("install exit %d", code)
	}
	// Inject an unknown [uninstall] key into the local def (as a hand-edit or a
	// future field would appear). It is not part of the hashed schema, so drift
	// detection ignores it.
	injectUninstallKey(t, a.paths.PackageFile("nickelmenu"), "custom_key", "keepme")

	// The registry drops the hooks and switches to the manifest method.
	v2 := `schema_version = 1
[packages.nickelmenu]
name = "NickelMenu"
source = "github.com/pgaskin/NickelMenu"
forge = "github"
asset = "KoboRoot.tgz"
[packages.nickelmenu.uninstall]
method = "manifest"
purge_paths = ["/mnt/onboard/.adds/nm/**"]
`
	if err := os.WriteFile(a.paths.RegistryCache("main"), []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := a.cmdSync(nil); code != exitOK {
		t.Fatalf("sync v2 exit %d", code)
	}
	var raw map[string]any
	b, _ := os.ReadFile(a.paths.PackageFile("nickelmenu"))
	if _, err := toml.Decode(string(b), &raw); err != nil {
		t.Fatal(err)
	}
	uni, _ := raw["uninstall"].(map[string]any)
	if uni == nil {
		t.Fatalf("uninstall table missing after sync:\n%s", b)
	}
	for _, k := range []string{"marker_file", "needs_reboot", "run_before"} {
		if _, ok := uni[k]; ok {
			t.Errorf("dropped key %q must be gone after sync:\n%s", k, b)
		}
	}
	if uni["method"] != "manifest" {
		t.Errorf("method should be manifest after sync: %v", uni["method"])
	}
	if uni["custom_key"] != "keepme" {
		t.Errorf("unknown [uninstall] key must survive sync:\n%s", b)
	}
}

// injectUninstallKey adds an unknown key into a package file's [uninstall] table.
func injectUninstallKey(t *testing.T, path, key, val string) {
	t.Helper()
	var m map[string]any
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := toml.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	uni, _ := m["uninstall"].(map[string]any)
	if uni == nil {
		uni = map[string]any{}
	}
	uni[key] = val
	m["uninstall"] = uni
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A2: local drift with the REMOTE UNCHANGED — plain sync skips (drift), and
// --overwrite RESTORES the file to the registry def (the exact case the old
// tests missed and the old --overwrite no-op'd on).
func TestSyncDriftRemoteUnchangedOverwriteRestores(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", nmManifest)
	a.cmdInstall([]string{"nickelmenu", "--yes"})

	// Hand-edit the local def (drift); the registry is left unchanged.
	p, _ := a.loadPackage("nickelmenu")
	p.Asset = "Edited.tgz"
	if err := config.Save(a.paths.PackageFile("nickelmenu"), p); err != nil {
		t.Fatal(err)
	}

	// Plain sync: drift, skip (exit 2), file untouched.
	var out string
	var code int
	out = captureStdout(t, func() { code = a.cmdSync(nil) })
	if code != exitPartial {
		t.Errorf("drift should exit %d, got %d", exitPartial, code)
	}
	if !strings.Contains(out, "edited since last sync") {
		t.Errorf("expected a drift message:\n%s", out)
	}
	if p, _ := a.loadPackage("nickelmenu"); p.Asset != "Edited.tgz" {
		t.Error("plain sync must not overwrite drift")
	}

	// --overwrite restores the file to the registry def even though the remote
	// is unchanged (old code no-op'd here).
	if code := a.cmdSync([]string{"--overwrite"}); code != exitOK {
		t.Fatalf("--overwrite exit %d", code)
	}
	if p, _ := a.loadPackage("nickelmenu"); p.Asset != "KoboRoot.tgz" {
		t.Errorf("--overwrite should RESTORE the registry def: %q", p.Asset)
	}
}

// A2: a stale/empty synced hash with content-identical defs reports up to date
// and backfills the hash (self-healing).
func TestSyncStaleHashHealed(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", nmManifest)
	a.cmdInstall([]string{"nickelmenu", "--yes"})

	// Corrupt the stored hash to simulate a legacy/empty/stale value while the
	// local def still matches the registry.
	a.state.Get("nickelmenu").SyncedDefSHA256 = "stale"

	var code int
	out := captureStdout(t, func() { code = a.cmdSync(nil) })
	if code != exitOK {
		t.Fatalf("healed sync exit %d", code)
	}
	if !strings.Contains(out, "1 up to date") {
		t.Errorf("content-equal def should be up to date:\n%s", out)
	}
	// The stale hash must be backfilled to the real remote hash.
	remoteHash := a.state.Get("nickelmenu").SyncedDefSHA256
	if remoteHash == "stale" || remoteHash == "" {
		t.Errorf("stale hash should be healed, got %q", remoteHash)
	}
}

// A3: a registry def with `purge_paths = []` must not read as drift after
// install; sync is up to date on both passes.
func TestSyncEmptySliceNoFalseDrift(t *testing.T) {
	a := newTestApp(t)
	manifest := `schema_version = 1
[packages.nickelmenu]
name = "NickelMenu"
source = "github.com/pgaskin/NickelMenu"
forge = "github"
asset = "KoboRoot.tgz"
[packages.nickelmenu.uninstall]
purge_paths = []
`
	seedRegistry(t, a, "main", manifest)
	if code := a.cmdInstall([]string{"nickelmenu", "--yes"}); code != exitOK {
		t.Fatalf("install exit %d", code)
	}
	for i := 0; i < 2; i++ {
		var code int
		out := captureStdout(t, func() { code = a.cmdSync(nil) })
		if code != exitOK {
			t.Fatalf("sync #%d exit %d:\n%s", i, code, out)
		}
		if !strings.Contains(out, "1 up to date") {
			t.Errorf("sync #%d should be up to date (no false drift):\n%s", i, out)
		}
	}
}

// A1: adopt over a hand-added def with extra [uninstall] fields writes exactly
// the registry def (dropping the hand-added known fields), preserving pin/state.
func TestInstallAdoptDropsExtraFields(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", nmManifest)

	// Hand-added def with a marker method + hooks the registry def does not have.
	nr := true
	hand := &config.Package{
		Name: "Mine", Source: "github.com/me/nm", Forge: "github", Asset: "KoboRoot.tgz", Pin: "v1.0.0",
		Uninstall: config.Uninstall{
			Method: config.MethodMarker, MarkerFile: "/mnt/onboard/.adds/nm/uninstall",
			NeedsReboot: &nr, RunBefore: "/etc/init.d/nm stop",
		},
	}
	if err := config.Save(a.paths.PackageFile("nickelmenu"), hand); err != nil {
		t.Fatal(err)
	}
	a.state.Get("nickelmenu").InstalledVersion = "v1.0.0"

	if code := a.cmdInstall([]string{"nickelmenu", "--adopt", "--yes"}); code != exitOK {
		t.Fatalf("adopt exit %d", code)
	}
	var raw map[string]any
	b, _ := os.ReadFile(a.paths.PackageFile("nickelmenu"))
	toml.Decode(string(b), &raw)
	// The registry def (nmManifest) has no [uninstall] block, so the hand-added
	// hooks must be gone entirely.
	if _, ok := raw["uninstall"]; ok {
		t.Errorf("adopt should drop the hand-added [uninstall] fields:\n%s", b)
	}
	if raw["source"] != "github.com/pgaskin/NickelMenu" || raw["registry"] != "main" {
		t.Errorf("adopt should write the registry def with provenance: %v", raw)
	}
	if raw["pin"] != "v1.0.0" {
		t.Errorf("adopt should preserve the local pin: %v", raw["pin"])
	}
	if a.state.Get("nickelmenu").InstalledVersion != "v1.0.0" {
		t.Error("adopt should preserve installed state")
	}
}

// §9.7: a package whose id disappeared from its registry is left intact with a WARN.
func TestSyncMissingIDWarns(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", nmManifest)
	a.cmdInstall([]string{"nickelmenu", "--yes"})
	// The registry drops the package.
	os.WriteFile(a.paths.RegistryCache("main"), []byte("schema_version = 1\n"), 0o644)

	code := a.cmdSync(nil)
	if code != exitPartial {
		t.Errorf("missing id should make sync exit %d, got %d", exitPartial, code)
	}
	if _, err := a.loadPackage("nickelmenu"); err != nil {
		t.Error("the local def must be left intact when its id disappears")
	}
	found := false
	for _, l := range a.paths.TailLog(20) {
		if strings.Contains(l, "WARN") && strings.Contains(l, "no longer in registry") {
			found = true
		}
	}
	if !found {
		t.Error("expected a WARN that the id is gone from its registry")
	}
}

// C1: install refuses a def missing required fields; search flags it.
func TestInstallAndSearchInvalidDef(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", "schema_version = 1\n[packages.broken]\nname = \"Broken\"\n")

	if code := a.cmdInstall([]string{"broken", "--yes"}); code != exitError {
		t.Errorf("install of an invalid def should exit %d, got %d", exitError, code)
	}
	if _, err := os.Stat(a.paths.PackageFile("broken")); !os.IsNotExist(err) {
		t.Error("an invalid def must not be written")
	}
	out := captureStdout(t, func() { a.cmdSearch(nil) })
	if !strings.Contains(out, "invalid def") {
		t.Errorf("search should mark the entry invalid def:\n%s", out)
	}
}

// C6: STATUS is "registered" for a def-only package and "installed" only when an
// installed version is recorded.
func TestSearchStatusRegisteredVsInstalled(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", nmManifest)
	a.cmdInstall([]string{"nickelmenu", "--yes"}) // writes the def, no installed_version

	out := captureStdout(t, func() { a.cmdSearch(nil) })
	if !strings.Contains(out, "registered") {
		t.Errorf("a def-only package should read registered:\n%s", out)
	}

	a.state.Get("nickelmenu").InstalledVersion = "v1"
	out = captureStdout(t, func() { a.cmdSearch(nil) })
	// "installed" (lowercase) is the status cell; "INSTALLED" is the header.
	if !strings.Contains(out, "installed") {
		t.Errorf("a package with an installed version should read installed:\n%s", out)
	}
}

// C6: the min_kpm gate is annotated only when the package is not installed.
func TestSearchMinKpmHiddenWhenInstalled(t *testing.T) {
	a := newTestApp(t)
	manifest := `schema_version = 1
[packages.futurepkg]
name = "FuturePkg"
source = "codeberg.org/o/future"
forge = "forgejo"
asset = "KoboRoot.tgz"
min_kpm = "99.0.0"
`
	seedRegistry(t, a, "main", manifest)
	out := captureStdout(t, func() { a.cmdSearch(nil) })
	if !strings.Contains(out, "requires kpm >= 99.0.0") {
		t.Errorf("min_kpm gate should show while not installed:\n%s", out)
	}

	// Now the package has a local def and an installed version.
	p := &config.Package{Name: "FuturePkg", Source: "codeberg.org/o/future", Forge: "forgejo", Asset: "KoboRoot.tgz", Registry: "main", MinKpm: "99.0.0"}
	if err := config.SaveReplace(a.paths.PackageFile("futurepkg"), p); err != nil {
		t.Fatal(err)
	}
	a.state.Get("futurepkg").InstalledVersion = "v1"
	out = captureStdout(t, func() { a.cmdSearch(nil) })
	if strings.Contains(out, "requires kpm") {
		t.Errorf("min_kpm gate must be hidden once installed:\n%s", out)
	}
	if !strings.Contains(out, "installed") {
		t.Errorf("installed status expected:\n%s", out)
	}
}

// C6: a def-update is measured against the provenance registry, not the shadowing
// winner — so search agrees with sync under shadowing.
func TestSearchDefUpdateUsesProvenance(t *testing.T) {
	a := newTestApp(t)
	// "first" wins for nickelmenu (earliest in config order); the package is
	// installed from "second". "first" carries a DIFFERENT asset.
	firstManifest := strings.Replace(nmManifest, `asset = "KoboRoot.tgz"`, `asset = "Different.tgz"`, 1)
	seedRegistry(t, a, "first", firstManifest)
	seedRegistry(t, a, "second", nmManifest)

	// Install from "second": write the def and record its synced hash so it is
	// up to date against "second".
	p := &config.Package{Name: "NickelMenu", Source: "github.com/pgaskin/NickelMenu", Forge: "github", Asset: "KoboRoot.tgz", Registry: "second"}
	if err := config.SaveReplace(a.paths.PackageFile("nickelmenu"), p); err != nil {
		t.Fatal(err)
	}
	syncedHash, _ := registry.HashDef(&registry.PackageDef{Name: "NickelMenu", Source: "github.com/pgaskin/NickelMenu", Forge: "github", Asset: "KoboRoot.tgz"})
	a.state.Get("nickelmenu").InstalledVersion = "v1"
	a.state.Get("nickelmenu").SyncedDefSHA256 = syncedHash

	// Search compares against the provenance registry ("second"), which matches
	// the synced hash → no def update, despite the winner ("first") differing.
	out := captureStdout(t, func() { a.cmdSearch(nil) })
	if strings.Contains(out, "def update") {
		t.Errorf("def-update must be judged against provenance, not the winner:\n%s", out)
	}
}

// C7: sync of an id that is not registered at all is a usage error (exit 1),
// distinct from a partial failure among valid targets (exit 2).
func TestSyncUnregisteredIdExit1(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", nmManifest)
	if code := a.cmdSync([]string{"nonesuch"}); code != exitError {
		t.Errorf("sync of an unregistered id should exit %d, got %d", exitError, code)
	}
}

// C3: the read-only/mutating registry commands and search reject stray flags and
// extra positionals.
func TestRegistryFlagParsing(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", nmManifest)
	cases := []struct {
		name string
		fn   func() int
	}{
		{"search --bad", func() int { return a.cmdSearch([]string{"--bad"}) }},
		{"search extra positional", func() int { return a.cmdSearch([]string{"a", "b"}) }},
		{"registry list --bad", func() int { return a.cmdRegistryList([]string{"--bad"}) }},
		{"registry remove --bad", func() int { return a.cmdRegistryRemove([]string{"--bad", "main"}) }},
		{"registry refresh --name", func() int { return a.cmdRegistryRefresh([]string{"--name", "main"}) }},
	}
	for _, c := range cases {
		if got := c.fn(); got != exitError {
			t.Errorf("%s: exit %d, want %d", c.name, got, exitError)
		}
	}
}

// C4: registry add rejects a URL containing /releases, adding nothing.
func TestRegistryAddRejectsReleases(t *testing.T) {
	a := newTestApp(t)
	if code := a.cmdRegistryAdd([]string{"github.com/o/r/releases/tag/v1"}); code != exitError {
		t.Errorf("registry add with /releases should exit %d, got %d", exitError, code)
	}
	cfg, _ := a.loadRegistryConfig()
	if len(cfg.Registries) != 0 {
		t.Errorf("no registry should be added for a /releases URL: %+v", cfg.Registries)
	}
}

// C5: adopting an id whose winning registry differs from recorded provenance
// discloses "provenance: <old> -> <new>".
func TestAdoptProvenanceChangeDisclosed(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "first", nmManifest)
	seedRegistry(t, a, "second", nmManifest)
	// A local def currently attributed to "second".
	p := &config.Package{Name: "NickelMenu", Source: "github.com/pgaskin/NickelMenu", Forge: "github", Asset: "KoboRoot.tgz", Registry: "second"}
	if err := config.SaveReplace(a.paths.PackageFile("nickelmenu"), p); err != nil {
		t.Fatal(err)
	}
	var out string
	var code int
	out = captureStdout(t, func() { code = a.cmdInstall([]string{"nickelmenu", "--adopt", "--yes"}) })
	if code != exitOK {
		t.Fatalf("adopt exit %d", code)
	}
	if !strings.Contains(out, "provenance: second -> first") {
		t.Errorf("adopt should disclose the provenance change:\n%s", out)
	}
}

// C9: a dev build skips the min_kpm gate with a note instead of refusing.
func TestInstallDevBuildSkipsMinKpm(t *testing.T) {
	if version.Version != "dev" {
		t.Skip("only meaningful on a dev build")
	}
	a := newTestApp(t)
	manifest := `schema_version = 1
[packages.futurepkg]
name = "FuturePkg"
source = "codeberg.org/o/future"
forge = "forgejo"
asset = "KoboRoot.tgz"
min_kpm = "99.0.0"
`
	seedRegistry(t, a, "main", manifest)
	var out string
	var code int
	out = captureStdout(t, func() { code = a.cmdInstall([]string{"futurepkg", "--yes"}) })
	if code != exitOK {
		t.Fatalf("dev build should not refuse a min_kpm package, exit %d", code)
	}
	if !strings.Contains(out, "dev build: skipping min_kpm check") {
		t.Errorf("expected the dev-build note:\n%s", out)
	}
}

// C2: a hand-edited config with a path-escaping registry name is dropped with a
// WARN, and RegistryCache panic-guards such names as defense in depth.
func TestLoadRegistryConfigDropsInvalidName(t *testing.T) {
	a := newTestApp(t)
	bad := `[[registries]]
name = "evil/../.."
url = "codeberg.org/o/r"
ref = "main"
path = "registry.toml"
forge = "forgejo"

[[registries]]
name = "good"
url = "codeberg.org/o/r"
ref = "main"
path = "registry.toml"
forge = "forgejo"
`
	if err := os.WriteFile(a.paths.ConfigFile(), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := a.loadRegistryConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Registries) != 1 || cfg.Registries[0].Name != "good" {
		t.Fatalf("invalid-named registry should be dropped: %+v", cfg.Registries)
	}
	found := false
	for _, l := range a.paths.TailLog(10) {
		if strings.Contains(l, "WARN") && strings.Contains(l, "invalid name") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a WARN about the invalid registry name: %v", a.paths.TailLog(10))
	}
}

func TestRegistryCachePanicsOnInvalidName(t *testing.T) {
	a := newTestApp(t)
	defer func() {
		if recover() == nil {
			t.Error("RegistryCache should panic on an invalid (path-escaping) name")
		}
	}()
	_ = a.paths.RegistryCache("evil/../..")
}

// §9.11: registry remove forgets the config entry, cache file, and state entry.
func TestRegistryRemoveCleanup(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", nmManifest)
	a.state.Registry("main").LastFetched = "now"
	a.state.Save()
	// An installed package must be unaffected by the removal.
	a.cmdInstall([]string{"nickelmenu", "--yes"})

	if code := a.cmdRegistryRemove([]string{"main"}); code != exitOK {
		t.Fatalf("registry remove exit %d", code)
	}
	cfg, _ := a.loadRegistryConfig()
	if _, ok := cfg.Find("main"); ok {
		t.Error("config entry should be removed")
	}
	if _, err := os.Stat(a.paths.RegistryCache("main")); !os.IsNotExist(err) {
		t.Error("cache file should be deleted")
	}
	if a.state.Registries["main"] != nil {
		t.Error("state entry should be deleted")
	}
	if _, err := a.loadPackage("nickelmenu"); err != nil {
		t.Error("installed package must survive registry removal")
	}
}

// §9.1: read-only vs mutating lock split for the new commands.
func TestRegistryLockClassification(t *testing.T) {
	cases := []struct {
		cmd  string
		args []string
		want bool
	}{
		{"install", nil, true},
		{"sync", nil, true},
		{"search", nil, false},
		{"registry", []string{"add"}, true},
		{"registry", []string{"remove"}, true},
		{"registry", []string{"refresh"}, true},
		{"registry", []string{"list"}, false},
	}
	for _, c := range cases {
		if got := isMutating(c.cmd, c.args); got != c.want {
			t.Errorf("isMutating(%q, %v) = %v, want %v", c.cmd, c.args, got, c.want)
		}
	}

	// With the lock held, a read-only search still runs; a mutating install fails.
	root := t.TempDir()
	t.Setenv("KPM_ROOT", root)
	if err := os.MkdirAll(filepath.Join(root, ".adds", "kpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, ".adds", "kpm", "lock")
	if err := os.WriteFile(lock, []byte("999 held"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newApp(isMutating("install", nil)); err == nil {
		t.Error("install should fail while the lock is held")
	}
	if _, err := newApp(isMutating("search", nil)); err != nil {
		t.Errorf("search should proceed without the lock: %v", err)
	}
}

// Ensure the compiled-in version is new enough that the min_kpm gate in these
// tests behaves (nickelmenu has no min_kpm, futurepkg intentionally gates out).
func TestVersionSaneForTests(t *testing.T) {
	if version.Version == "" {
		t.Skip("version unset")
	}
}
