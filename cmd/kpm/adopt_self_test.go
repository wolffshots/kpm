package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/forge"
	"kpm/internal/registry"
)

// adopt_self_test.go covers `kpm adopt-self` (kpm-self-enrol-plan §2): the one
// mutating+network step the UI's "Enable self-update" button calls. It funnels
// kpm's source/forge into state.json (durable, SELF-SOURCE §5), keeps kpm.toml
// sourceless, preserves an existing pin, and is best-effort about the refresh
// (§3): a warm cache lets it enrol offline, a cold cache with no network is the
// only case that surfaces the network error.

const adoptSelfKpmManifest = `schema_version = 1
[packages.kpm]
name = "kpm"
source = "github.com/wolffshots/kpm"
forge = "github"
asset = "KoboRoot.tgz"
`

// adoptSelfOnlineRegistry configures a registry backed by a fake forgejo server
// that serves manifest for any raw path, and marks the network up — so
// adopt-self's best-effort refresh actually fetches (netUp true → refreshOne).
func adoptSelfOnlineRegistry(t *testing.T, a *App, manifest string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(manifest))
	}))
	t.Cleanup(srv.Close)
	a.client = forge.NewClientWithHTTP(srv.Client())
	a.netWait = func(string) bool { return true }
	host := strings.TrimPrefix(srv.URL, "https://")
	cfg, err := a.loadRegistryConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Registries = append(cfg.Registries, registry.Registry{
		Name: "main", URL: host + "/o/r", Ref: "main", Path: "registry.toml", Forge: config.ForgeForgejo,
	})
	if err := a.saveRegistryConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

// Primary path: an unconfigured kpm, a configured registry that offers kpm, the
// network up → adopt-self refreshes, writes the adoption identity into state,
// leaves kpm.toml sourceless, and reports changed:["kpm"].
func TestAdoptSelfUnconfiguredToAdopted(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.9.0")
	registerSelf(t, a)
	adoptSelfOnlineRegistry(t, a, adoptSelfKpmManifest)

	// Precondition: un-adopted.
	if p, _ := a.loadPackage(selfID); a.configured(p) {
		t.Fatal("kpm should start un-adopted")
	}

	var code int
	out := captureStdout(t, func() { code = a.cmdAdoptSelf([]string{"--json"}) })
	if code != exitOK {
		t.Fatalf("adopt-self exit = %d, want %d\n%s", code, exitOK, out)
	}
	if got := lastJSON(t, out); got != `{"changed":["kpm"],"failed":[],"staged":false,"reboot_required":false}` {
		t.Errorf("payload = %s", got)
	}

	// Adoption identity is durable in state.
	ps := a.state.Get(selfID)
	if ps.Source != "github.com/wolffshots/kpm" || ps.Forge != "github" {
		t.Errorf("state source/forge = %q/%q, want github.com/wolffshots/kpm/github", ps.Source, ps.Forge)
	}
	// ...and absent from the tarball-clobbered TOML.
	m := rawTOML(t, a.paths.PackageFile(selfID))
	if _, ok := m["source"]; ok {
		t.Errorf("kpm.toml must carry no source key: %v", m["source"])
	}
	if _, ok := m["forge"]; ok {
		t.Errorf("kpm.toml must carry no forge key: %v", m["forge"])
	}
	// The overlay makes it configured despite the sourceless TOML.
	if p, _ := a.loadPackage(selfID); !a.configured(p) {
		t.Error("kpm must be configured after adopt-self")
	}
}

// Re-enrolling an already-adopted kpm is idempotent: exit 0, changed:["kpm"],
// the source unchanged.
func TestAdoptSelfIdempotent(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.9.0")
	registerSelf(t, a)
	adoptSelfOnlineRegistry(t, a, adoptSelfKpmManifest)

	if code := a.cmdAdoptSelf([]string{"--json"}); code != exitOK {
		t.Fatalf("first adopt-self exit %d", code)
	}
	src := a.state.Get(selfID).Source

	var code int
	out := captureStdout(t, func() { code = a.cmdAdoptSelf([]string{"--json"}) })
	if code != exitOK {
		t.Fatalf("second adopt-self exit = %d, want %d", code, exitOK)
	}
	if got := lastJSON(t, out); got != `{"changed":["kpm"],"failed":[],"staged":false,"reboot_required":false}` {
		t.Errorf("idempotent payload = %s", got)
	}
	if a.state.Get(selfID).Source != src {
		t.Errorf("source changed on re-enrol: %q -> %q", src, a.state.Get(selfID).Source)
	}
}

// No registry configured → a clean error, exit 1.
func TestAdoptSelfNoRegistry(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.9.0")
	registerSelf(t, a)

	var code int
	out := captureStdout(t, func() { code = a.cmdAdoptSelf([]string{"--json"}) })
	if code != exitError {
		t.Fatalf("adopt-self exit = %d, want %d", code, exitError)
	}
	if got := lastJSON(t, out); got != `{"error":"no registry configured — add one first (kpm registry add <url>)"}` {
		t.Errorf("payload = %s", got)
	}
}

// A registry is configured and reachable but offers no kpm package → a distinct
// clean error (network was fine, so it is not the "no network" message).
func TestAdoptSelfRegistryOffersNoKpm(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.9.0")
	registerSelf(t, a)
	adoptSelfOnlineRegistry(t, a, nmManifest) // nickelmenu only, no kpm

	var code int
	out := captureStdout(t, func() { code = a.cmdAdoptSelf([]string{"--json"}) })
	if code != exitError {
		t.Fatalf("adopt-self exit = %d, want %d", code, exitError)
	}
	if got := lastJSON(t, out); got != `{"error":"no configured registry offers a kpm package — refresh or add the kpm registry"}` {
		t.Errorf("payload = %s", got)
	}
}

// An existing state pin must survive an enrol (the pin lives in state, §10).
func TestAdoptSelfPreservesPin(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.9.0")
	registerSelf(t, a)
	a.state.Get(selfID).Pin = "v0.8.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	// Offline enrol from a warm cache.
	a.netWait = func(string) bool { return false }
	seedRegistry(t, a, "main", adoptSelfKpmManifest)

	if code := a.cmdAdoptSelf([]string{"--json"}); code != exitOK {
		t.Fatalf("adopt-self exit %d", code)
	}
	if got := a.state.Get(selfID).Pin; got != "v0.8.0" {
		t.Errorf("pin = %q, want v0.8.0 (preserved)", got)
	}
	// kpm.toml stays pinless (the pin belongs in state).
	m := rawTOML(t, a.paths.PackageFile(selfID))
	if v, ok := m["pin"]; ok && v != "" {
		t.Errorf("kpm.toml must not carry a pin: %v", v)
	}
}

// Best-effort refresh (§3): the network is down but the cache already offers kpm
// → adopt-self proceeds against the cache and SUCCEEDS.
func TestAdoptSelfBestEffortWarmCache(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.9.0")
	registerSelf(t, a)
	a.netWait = func(string) bool { return false } // refuse: offline
	seedRegistry(t, a, "main", adoptSelfKpmManifest)

	var code int
	out := captureStdout(t, func() { code = a.cmdAdoptSelf([]string{"--json"}) })
	if code != exitOK {
		t.Fatalf("warm-cache offline adopt-self exit = %d, want %d\n%s", code, exitOK, out)
	}
	if a.state.Get(selfID).Source != "github.com/wolffshots/kpm" {
		t.Errorf("offline enrol should have adopted from the cache: %q", a.state.Get(selfID).Source)
	}
}

// Best-effort refresh (§3): the network is down AND the cache is cold (no kpm to
// fall back on) → the only case that surfaces the network error, exit 1.
func TestAdoptSelfBestEffortColdCache(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.9.0")
	registerSelf(t, a)
	a.netWait = func(string) bool { return false } // offline
	seedRegistry(t, a, "main", "")                 // configured, but no cache written

	var code int
	out := captureStdout(t, func() { code = a.cmdAdoptSelf([]string{"--json"}) })
	if code != exitError {
		t.Fatalf("cold-cache offline adopt-self exit = %d, want %d", code, exitError)
	}
	if got := lastJSON(t, out); got != `{"error":"no network — check Wi-Fi and retry"}` {
		t.Errorf("payload = %s", got)
	}
}

// A registry whose kpm def is under-specified (e.g. a stale cache carrying a
// sourceless kpm entry) must be REFUSED by the missingDefFields guard, not
// silently adopted into a broken, still-unconfigured self-update (kpm-self-enrol
// §2, mirrors install's C1 guard). Enrol from the warm cache offline so only the
// def's shape is under test.
func TestAdoptSelfRejectsUnderspecifiedDef(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.9.0")
	registerSelf(t, a)
	a.netWait = func(string) bool { return false } // offline: read the seeded cache
	seedRegistry(t, a, "main", "schema_version = 1\n[packages.kpm]\nname = \"kpm\"\n")

	var code int
	out := captureStdout(t, func() { code = a.cmdAdoptSelf([]string{"--json"}) })
	if code != exitError {
		t.Fatalf("under-specified def adopt-self exit = %d, want %d\n%s", code, exitError, out)
	}
	if !strings.Contains(out, "missing required field") {
		t.Errorf("want a missing-field error, got: %s", out)
	}
	// The guard must fire BEFORE any write — no broken source lands in state.
	if got := a.state.Get(selfID).Source; got != "" {
		t.Errorf("must not adopt a broken def: state source = %q, want empty", got)
	}
	// ...and the kpm row stays un-configured.
	if p, _ := a.loadPackage(selfID); a.configured(p) {
		t.Error("kpm must remain un-adopted after a refused enrol")
	}
}

// adopt-self is mutating: driven through the real run() entry point against a
// held single-instance lock it must fail with the "another kpm instance" busy
// message — proving it takes the lock and never re-enters run() (kpm-self-enrol
// §4). Mirrors TestUIContractBusyLockMessage.
func TestAdoptSelfBusyLockMessage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KPM_ROOT", root)
	p := device.New()
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p.LockFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	// Hold the lock with a FRESH mtime so it is not treated as stale.
	if err := os.WriteFile(p.LockFile(), []byte("999 held\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var code int
	stderr := captureStderr(t, func() { code = run([]string{"adopt-self", "--json"}) })
	if code != exitError {
		t.Errorf("busy adopt-self exit = %d, want %d", code, exitError)
	}
	if !strings.Contains(strings.ToLower(stderr), "another kpm instance") {
		t.Errorf("stderr must contain the busy trigger \"another kpm instance\":\n%s", stderr)
	}
}
