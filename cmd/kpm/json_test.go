package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/forge"
)

// json_test.go covers the machine-readable --json payloads (JSON-OUTPUT.md §4):
// each golden asserts the exact single-line BEGIN_JSON payload for a fixed
// fixture state (registry cache + state.json in a temp KPM_ROOT).

// lastJSON returns the JSON text after the BEGIN_JSON marker in out, asserting
// the full §1 protocol: the marker appears exactly once, it is the FINAL stdout
// line (nothing may trail the payload — the hook parses everything after the
// marker as JSON), and the payload is valid, compact, single-line JSON.
func lastJSON(t *testing.T, out string) string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	found := ""
	count := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "BEGIN_JSON") {
			found = strings.TrimPrefix(line, "BEGIN_JSON")
			count++
		}
	}
	if count == 0 {
		t.Fatalf("no BEGIN_JSON line in output: %q", out)
	}
	if count > 1 {
		t.Fatalf("BEGIN_JSON emitted %d times, want exactly once: %q", count, out)
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "BEGIN_JSON") {
		t.Fatalf("BEGIN_JSON is not the final stdout line (§1); trailing output %q in: %q", last, out)
	}
	if strings.Contains(found, "\n") {
		t.Fatalf("JSON payload is not single-line: %q", found)
	}
	if !json.Valid([]byte(found)) {
		t.Fatalf("JSON payload is not valid: %q", found)
	}
	return found
}

const jsonClockManifest = `schema_version = 1
[packages.nickelclock]
name = "NickelClock"
source = "github.com/shermp/NickelClock"
forge = "github"
asset = "NickelClock-*.zip"
min_kpm = "0.4.0"
description = "Show the time in the reading header"
homepage = "https://github.com/shermp/NickelClock"

  [packages.nickelclock.uninstall]
  method = "marker-remove"
  marker_file = "/mnt/onboard/.adds/nickelclock/uninstall"
`

// §2.1: the browse call — registry entries merged with installed/staged state,
// registry freshness, and the global staged summary.
func TestSearchJSONGolden(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.6.0")
	seedRegistry(t, a, "main", jsonClockManifest)
	// Installed at v0.4.0 (from state), and record when the registry was refreshed.
	a.state.Get("nickelclock").InstalledVersion = "v0.4.0"
	a.state.Registry("main").LastFetched = "2026-07-20T09:00:00Z"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() { a.cmdSearch([]string{"--json"}) })
	got := lastJSON(t, out)
	// nickelclock has a registry def and is installed (in state) but has NO local
	// packages.d def, so kpm cannot uninstall it (cmdUninstall needs a local def):
	// uninstallable is false, not driven by the registry's advertised recipe (M2).
	want := `{"packages":[{"id":"nickelclock","name":"NickelClock","description":"Show the time in the reading header","homepage":"https://github.com/shermp/NickelClock","source":"github.com/shermp/NickelClock","registry":"main","installed":"v0.4.0","pinned":null,"staged":false,"uninstallable":false,"min_kpm":"0.4.0","min_kpm_ok":true,"has_config":false}],"staged":{"count":0,"ids":[]},"registries":[{"name":"main","refreshed":"2026-07-20T09:00:00Z"}]}`
	if got != want {
		t.Errorf("search --json\n got: %s\nwant: %s", got, want)
	}
}

// §2.1: an installed-but-unregistered package (packages.d def, no registry def)
// is included with registry/description null so the UI shows everything kpm
// manages; the global staged summary reflects a pending change.
func TestSearchJSONUnregisteredAndStaged(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.6.0")
	// A hand-added package: a packages.d def with no registry provenance.
	if err := config.Save(a.paths.PackageFile("handmade"), &config.Package{
		Name: "Handmade", Source: "github.com/me/handmade", Forge: "github", Asset: "KoboRoot.tgz",
	}); err != nil {
		t.Fatal(err)
	}
	a.state.Get("handmade").InstalledVersion = "v1.0.0"
	a.state.Get("handmade").StagedVersion = "v1.1.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() { a.cmdSearch([]string{"--json"}) })
	got := lastJSON(t, out)
	want := `{"packages":[{"id":"handmade","name":"Handmade","description":null,"homepage":null,"source":"github.com/me/handmade","registry":null,"installed":"v1.0.0","pinned":null,"staged":true,"uninstallable":false,"min_kpm":null,"min_kpm_ok":true,"has_config":false}],"staged":{"count":1,"ids":["handmade"]},"registries":[]}`
	if got != want {
		t.Errorf("search --json\n got: %s\nwant: %s", got, want)
	}
}

// CONFIG.md §3.3: config list/show/set goldens — the exact BEGIN_JSON lines the
// Phase 2 hook will parse. Sandbox via newUninstallApp (KPM_SYSROOT); files and
// the packages.d snapshot are seeded by config_test.go's helpers.

func TestConfigListJSONGolden(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelclock", []config.ModConfig{clockConfig()})
	writeDeviceFile(t, sysroot, clockPath, clockBody)
	out := captureStdout(t, func() { a.cmdConfig([]string{"list", "nickelclock", "--json"}) })
	got := lastJSON(t, out)
	want := `{"id":"nickelclock","configs":[{"name":"Settings","path":"/mnt/onboard/.adds/nickelclock/settings.ini","format":"ini","reload":"reboot","exists":true,"can_create":false,"editable":true,"description":"Clock and battery display options."}]}`
	if got != want {
		t.Errorf("config list --json\n got: %s\nwant: %s", got, want)
	}
}

func TestConfigShowJSONGolden(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelclock", []config.ModConfig{clockConfig()})
	writeDeviceFile(t, sysroot, clockPath, clockBody)
	out := captureStdout(t, func() { a.cmdConfig([]string{"show", "nickelclock", "Settings", "--json"}) })
	got := lastJSON(t, out)
	want := `{"id":"nickelclock","file":{"name":"Settings","format":"ini","reload":"reboot","exists":true},"entries":[{"section":"General","key":"Margin","line":2,"value":"10","sensitive":false},{"section":"Clock","key":"Enabled","line":5,"value":"true","sensitive":false},{"section":"Clock","key":"Placement","line":6,"value":"Footer","sensitive":false}],"truncated":false}`
	if got != want {
		t.Errorf("config show --json\n got: %s\nwant: %s", got, want)
	}
}

func TestConfigSetJSONGolden(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelclock", []config.ModConfig{clockConfig()})
	writeDeviceFile(t, sysroot, clockPath, clockBody)
	out := captureStdout(t, func() {
		a.cmdConfig([]string{"set", "nickelclock", "Settings", "--section", "Clock", "--key", "Enabled", "--value", "false", "--json"})
	})
	got := lastJSON(t, out)
	// Reuses the mutation shape; reboot_required tracks reload == "reboot".
	if want := `{"changed":["nickelclock"],"failed":[],"staged":false,"reboot_required":true}`; got != want {
		t.Errorf("config set --json\n got: %s\nwant: %s", got, want)
	}
}

// §2.3: sync --json reuses the mutation shape. `changed` = defs re-copied,
// `failed` = per-package errors; staged/reboot_required are always false (a def
// re-copy stages nothing). Two cases: a registry-drifted def is re-copied, and a
// sandbox with nothing to sync emits the empty mutation.

// syncDriftManifest advertises samplemod with a config declaration the local def
// predates — so sync re-copies it (drift → applied).
const syncDriftManifest = `schema_version = 1
[packages.samplemod]
name = "Sample Mod"
source = "codeberg.org/o/samplemod"
forge = "forgejo"
asset = "KoboRoot.tgz"

  [[packages.samplemod.configs]]
  name = "Settings"
  path = "/mnt/onboard/.adds/samplemod/settings.ini"
  format = "ini"
  reload = "reboot"
`

func TestSyncJSONApplied(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.8.1")
	seedRegistry(t, a, "main", syncDriftManifest)
	// A registry-managed local def with no config yet and no recorded baseline
	// hash → a clean apply (decision-tree case 2).
	if err := config.Save(a.paths.PackageFile("samplemod"), &config.Package{
		Name: "Sample Mod", Source: "codeberg.org/o/samplemod", Forge: "forgejo",
		Asset: "KoboRoot.tgz", Registry: "main",
	}); err != nil {
		t.Fatal(err)
	}
	a.state.Get("samplemod").InstalledVersion = "v1.0.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { a.cmdSync([]string{"--json"}) })
	got := lastJSON(t, out)
	if want := `{"changed":["samplemod"],"failed":[],"staged":false,"reboot_required":false}`; got != want {
		t.Errorf("sync --json\n got: %s\nwant: %s", got, want)
	}
}

// syncCleanManifest advertises samplemod identically to its local def, so sync
// finds nothing to do (up to date → not reported in `changed`).
const syncCleanManifest = `schema_version = 1
[packages.samplemod]
name = "Sample Mod"
source = "codeberg.org/o/samplemod"
forge = "forgejo"
asset = "KoboRoot.tgz"
`

func TestSyncJSONNothingToDo(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.8.1")
	seedRegistry(t, a, "main", syncCleanManifest)
	// Local def already equals the registry def → up to date, no re-copy.
	if err := config.Save(a.paths.PackageFile("samplemod"), &config.Package{
		Name: "Sample Mod", Source: "codeberg.org/o/samplemod", Forge: "forgejo",
		Asset: "KoboRoot.tgz", Registry: "main",
	}); err != nil {
		t.Fatal(err)
	}
	a.state.Get("samplemod").InstalledVersion = "v1.0.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { a.cmdSync([]string{"--json"}) })
	got := lastJSON(t, out)
	if want := `{"changed":[],"failed":[],"staged":false,"reboot_required":false}`; got != want {
		t.Errorf("sync --json\n got: %s\nwant: %s", got, want)
	}
}

// §2.4: version --json (commit null — no VCS revision compiled in).
func TestVersionJSONGolden(t *testing.T) {
	newTestApp(t) // sets KPM_ROOT
	setVersion(t, "0.6.0")
	out := captureStdout(t, func() { run([]string{"version", "--json"}) })
	got := lastJSON(t, out)
	if want := `{"version":"0.6.0","commit":null}`; got != want {
		t.Errorf("version --json\n got: %s\nwant: %s", got, want)
	}
}

// §2.4: list --json — installed packages from state.
func TestListJSONGolden(t *testing.T) {
	a := newTestApp(t)
	if err := config.Save(a.paths.PackageFile("nickelclock"), &config.Package{
		Name: "NickelClock", Source: "github.com/shermp/NickelClock", Forge: "github",
		Asset: "KoboRoot.tgz", Registry: "main",
	}); err != nil {
		t.Fatal(err)
	}
	a.state.Get("nickelclock").InstalledVersion = "v0.4.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { a.cmdList([]string{"--json"}) })
	got := lastJSON(t, out)
	want := `{"packages":[{"id":"nickelclock","name":"NickelClock","installed":"v0.4.0","pinned":null,"source":"github.com/shermp/NickelClock","registry":"main"}]}`
	if got != want {
		t.Errorf("list --json\n got: %s\nwant: %s", got, want)
	}
}

// M2 regression: search's `uninstallable` must track whether kpm actually CAN
// uninstall — it requires a LOCAL packages.d def (cmdUninstall's precondition),
// not merely a registry-advertised recipe. Same registry def, two states.
func TestSearchUninstallableRequiresLocalDef(t *testing.T) {
	uninstallable := func(t *testing.T, payload string) any {
		t.Helper()
		var m struct {
			Packages []map[string]any `json:"packages"`
		}
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatal(err)
		}
		if len(m.Packages) != 1 {
			t.Fatalf("want 1 package, got %d", len(m.Packages))
		}
		return m.Packages[0]["uninstallable"]
	}

	// (a) local def present + installed → uninstallable.
	a := newTestApp(t)
	setVersion(t, "0.6.0")
	seedRegistry(t, a, "main", jsonClockManifest)
	if err := config.Save(a.paths.PackageFile("nickelclock"), &config.Package{
		Name: "NickelClock", Source: "github.com/shermp/NickelClock", Forge: "github",
		Asset: "NickelClock-*.zip", MinKpm: "0.4.0", Registry: "main",
		Uninstall: config.Uninstall{Method: "marker-remove", MarkerFile: "/mnt/onboard/.adds/nickelclock/uninstall"},
	}); err != nil {
		t.Fatal(err)
	}
	a.state.Get("nickelclock").InstalledVersion = "v0.4.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { a.cmdSearch([]string{"--json"}) })
	if u := uninstallable(t, lastJSON(t, out)); u != true {
		t.Errorf("with a local def, uninstallable = %v, want true", u)
	}

	// (b) registry def only (no local def) but installed → NOT uninstallable,
	// matching that cmdUninstall would refuse it (loadPackage fails).
	b := newTestApp(t)
	setVersion(t, "0.6.0")
	seedRegistry(t, b, "main", jsonClockManifest)
	b.state.Get("nickelclock").InstalledVersion = "v0.4.0"
	if err := b.state.Save(); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { b.cmdSearch([]string{"--json"}) })
	if u := uninstallable(t, lastJSON(t, out)); u != false {
		t.Errorf("with only a registry def, uninstallable = %v, want false", u)
	}
}

// §2.4: registry list --json.
func TestRegistryListJSONGolden(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", jsonClockManifest)
	a.state.Registry("main").LastFetched = "2026-07-20T09:00:00Z"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { a.cmdRegistry([]string{"list", "--json"}) })
	got := lastJSON(t, out)
	want := `{"registries":[{"name":"main","url":"codeberg.org/o/main","ref":"main","path":"registry.toml","forge":"forgejo","refreshed":"2026-07-20T09:00:00Z"}]}`
	if got != want {
		t.Errorf("registry list --json\n got: %s\nwant: %s", got, want)
	}
}

// §2.2: check --json with no packages registered → empty package list.
func TestCheckJSONNoPackages(t *testing.T) {
	a := newTestApp(t)
	out := captureStdout(t, func() { a.cmdCheck([]string{"--json"}) })
	got := lastJSON(t, out)
	// Only the "checked" timestamp varies; assert the stable shape.
	var payload jsonCheckPayload
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Packages) != 0 {
		t.Errorf("packages = %v, want empty", payload.Packages)
	}
	if payload.Checked == "" {
		t.Error("checked timestamp should be set")
	}
	if !strings.HasPrefix(got, `{"packages":[],"checked":"`) {
		t.Errorf("unexpected shape: %s", got)
	}
}

// §2.3: unstage --json with nothing staged.
func TestUnstageJSONNothingStaged(t *testing.T) {
	a := newTestApp(t)
	out := captureStdout(t, func() { a.cmdUnstage([]string{"--json"}) })
	got := lastJSON(t, out)
	if want := `{"unstaged":false,"ids":[]}`; got != want {
		t.Errorf("unstage --json\n got: %s\nwant: %s", got, want)
	}
}

// §2.4: status --json mirrors the human status as flat structured fields.
func TestStatusJSONShape(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.6.0")
	if err := config.Save(a.paths.PackageFile("nickelclock"), &config.Package{
		Name: "NickelClock", Source: "github.com/shermp/NickelClock", Forge: "github",
		Asset: "KoboRoot.tgz", Registry: "main",
	}); err != nil {
		t.Fatal(err)
	}
	a.state.Get("nickelclock").InstalledVersion = "v0.4.0"
	a.state.LastCheck = "2026-07-21T10:00:00Z"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { a.cmdStatus([]string{"--json"}) })
	got := lastJSON(t, out)
	want := `{"version":"0.6.0","checked":"2026-07-21T10:00:00Z","packages":[{"id":"nickelclock","installed":"v0.4.0","staged":null,"latest":null,"pinned":null,"state":"up-to-date"}],"staged":{"count":0,"ids":[]}}`
	if got != want {
		t.Errorf("status --json\n got: %s\nwant: %s", got, want)
	}
}

// §2.3: a mutation payload marshals with [] (never null) for empty changed/failed.
func TestMutationEmptySlicesMarshalAsArrays(t *testing.T) {
	out := captureStdout(t, func() { emitMutation(nil, nil, false, false, "") })
	got := lastJSON(t, out)
	if want := `{"changed":[],"failed":[],"staged":false,"reboot_required":false}`; got != want {
		t.Errorf("emitMutation\n got: %s\nwant: %s", got, want)
	}
}

// ---- §1 marker-is-last regressions through the REAL commands --------------
// (Deep-review finding: cmdUpdate printed human lines after BEGIN_JSON on its
// no-packages, nothing-to-stage, and staged-success paths; lastJSON now fails
// on any trailing output, so these lock the protocol on every reachable path.)

// update --all --json with zero registered packages: "no packages selected"
// must precede the empty mutation payload.
func TestUpdateJSONNoPackagesSelected(t *testing.T) {
	a := newTestApp(t)
	out := captureStdout(t, func() {
		if code := a.cmdUpdate([]string{"--all", "--json"}); code != exitOK {
			t.Errorf("exit = %d, want %d", code, exitOK)
		}
	})
	got := lastJSON(t, out)
	if want := `{"changed":[],"failed":[],"staged":false,"reboot_required":false}`; got != want {
		t.Errorf("update --json\n got: %s\nwant: %s", got, want)
	}
	if !strings.Contains(out, "no packages selected") {
		t.Error("human line should still be printed (before the marker)")
	}
}

// update --all --json where the only package is unconfigured (empty source →
// skipped silently, F7): the nothing-to-stage path must keep the marker last.
func TestUpdateJSONNothingToStage(t *testing.T) {
	a := newTestApp(t)
	if err := config.Save(a.paths.PackageFile("handmade"), &config.Package{
		Name: "Handmade", Asset: "KoboRoot.tgz",
	}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code := a.cmdUpdate([]string{"--all", "--json"}); code != exitOK {
			t.Errorf("exit = %d, want %d", code, exitOK)
		}
	})
	got := lastJSON(t, out)
	if want := `{"changed":[],"failed":[],"staged":false,"reboot_required":false}`; got != want {
		t.Errorf("update --json\n got: %s\nwant: %s", got, want)
	}
	if !strings.Contains(out, "nothing to stage") {
		t.Error("human line should still be printed (before the marker)")
	}
}

// The primary UI path: update <id> --json resolves, downloads, and stages a
// real (fake-forge) release — §2.3 payload with staged/reboot_required true,
// and nothing after the marker (the old code printed "N package(s) staged —
// reboot to install" after it).
func TestUpdateJSONStagedSuccessMarkerLast(t *testing.T) {
	a := newTestApp(t)
	inner := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/pkg/file": "x"})
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/repos/") {
			fmt.Fprintf(w, `{"tag_name":"v1.1.0","assets":[{"name":"KoboRoot.tgz","browser_download_url":%q}]}`,
				srv.URL+"/KoboRoot.tgz")
			return
		}
		w.Write(inner)
	}))
	t.Cleanup(srv.Close)
	a.client = forge.NewClientWithHTTP(srv.Client())

	host := strings.TrimPrefix(srv.URL, "https://")
	if err := config.Save(a.paths.PackageFile("pkg"), &config.Package{
		Name: "Pkg", Source: host + "/me/pkg", Forge: "forgejo", Asset: "KoboRoot.tgz",
	}); err != nil {
		t.Fatal(err)
	}
	a.state.Get("pkg").InstalledVersion = "v1.0.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if code := a.cmdUpdate([]string{"pkg", "--json"}); code != exitOK {
			t.Errorf("exit = %d, want %d", code, exitOK)
		}
	})
	got := lastJSON(t, out)
	if want := `{"changed":["pkg"],"failed":[],"staged":true,"reboot_required":true}`; got != want {
		t.Errorf("update --json\n got: %s\nwant: %s", got, want)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err != nil {
		t.Errorf("staged tgz should exist: %v", err)
	}
}

// install <id> --json without --yes (exit 3): the confirm hint must precede the
// best-effort failure payload; the exit code stays authoritative (§2.3).
func TestInstallJSONConfirmMarkerLast(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", jsonClockManifest)
	out := captureStdout(t, func() {
		if code := a.cmdInstall([]string{"nickelclock", "--json"}); code != exitConfirm {
			t.Errorf("exit = %d, want %d", code, exitConfirm)
		}
	})
	got := lastJSON(t, out)
	if !strings.Contains(got, "--yes") {
		t.Errorf("payload should carry the confirm hint: %s", got)
	}
}

// uninstall <id> --json without --yes (exit 3): same marker-is-last guarantee.
func TestUninstallJSONConfirmMarkerLast(t *testing.T) {
	a := newTestApp(t)
	t.Setenv("KPM_SYSROOT", t.TempDir()) // off-device guard (G5)
	if err := config.Save(a.paths.PackageFile("clock"), &config.Package{
		Name: "Clock", Source: "github.com/x/clock", Forge: "github", Asset: "KoboRoot.tgz",
		Uninstall: config.Uninstall{Method: "marker-remove", MarkerFile: "/mnt/onboard/.adds/clock/uninstall"},
	}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code := a.cmdUninstall([]string{"clock", "--json"}); code != exitConfirm {
			t.Errorf("exit = %d, want %d", code, exitConfirm)
		}
	})
	got := lastJSON(t, out)
	if !strings.Contains(got, "--yes") {
		t.Errorf("payload should carry the confirm hint: %s", got)
	}
}

// §2.1 uninstallable now asks uninstall.Compute, so a marker path the policy
// would refuse (outside every deletable root) must report uninstallable:false
// instead of over-promising an uninstall that would fail.
func TestSearchJSONUninstallableRespectsPolicy(t *testing.T) {
	a := newTestApp(t)
	if err := config.Save(a.paths.PackageFile("badmarker"), &config.Package{
		Name: "BadMarker", Source: "github.com/x/badmarker", Forge: "github", Asset: "KoboRoot.tgz",
		Uninstall: config.Uninstall{Method: "marker-remove", MarkerFile: "/etc/passwd"},
	}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { a.cmdSearch([]string{"--json"}) })
	got := lastJSON(t, out)
	if !strings.Contains(got, `"id":"badmarker"`) {
		t.Fatalf("badmarker missing from payload: %s", got)
	}
	if !strings.Contains(got, `"uninstallable":false`) {
		t.Errorf("policy-refused marker must report uninstallable:false: %s", got)
	}
}
