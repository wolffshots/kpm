package main

import (
	"os"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/forge"
	"kpm/internal/state"
)

// infoSum is a well-formed sha256 for the checksum fixtures.
var infoSum = strings.Repeat("ab", 32)

// infoManifest is a registry def with every field the detail page shows.
var infoManifest = `schema_version = 1
[packages.nickelsync]
name = "NickelSync"
source = "github.com/o/nickelsync"
forge = "github"
asset = "KoboRoot.tgz"
tested_fw = "4.46"
conflicts = ["other"]

  [packages.nickelsync.uninstall]
  method = "marker-remove"
  marker_file = "/mnt/onboard/.adds/nickelsync/uninstall"

  [packages.nickelsync.fw6]
  asset = "Kobo.tgz"
  tested_fw = "6.0"

  [packages.nickelsync.checksums]
  "v0.4.0" = "` + infoSum + `"

[packages.clock]
name = "Clock"
source = "github.com/o/clock"
asset = "KoboRoot.tgz"
builtin_fw6 = "the reader clock"

  [packages.clock.uninstall]
  method = "marker-remove"
  marker_file = "/mnt/onboard/.adds/clock/uninstall"
`

// installFromRegistry writes the local def of id from the registry def, the
// way `kpm install` does, and records it as installed at version.
func installFromRegistry(t *testing.T, a *App, id, version string, manifest []string) {
	t.Helper()
	cfg, err := a.loadRegistryConfig()
	if err != nil {
		t.Fatal(err)
	}
	mans, _ := a.cachedManifests(cfg)
	for _, m := range mans {
		if d, ok := m.Manifest.Packages[id]; ok {
			if err := config.Save(a.paths.PackageFile(id), d.ToPackage(id, m.Name, "")); err != nil {
				t.Fatal(err)
			}
		}
	}
	ps := a.state.Get(id)
	ps.InstalledVersion = version
	ps.Manifest = manifest
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
}

func runInfo(t *testing.T, a *App, id string) (int, map[string]any) {
	t.Helper()
	var code int
	out := captureStdout(t, func() { code = a.cmdInfo([]string{id, "--json"}) })
	return code, decodeJSON(t, lastJSON(t, out))
}

// ---- info --json (KpmProcess::info, the detail page; read only) ---------

func TestUIContractInfo(t *testing.T) {
	a := newTestApp(t)
	writeFirmware(t, a, "4.46.23852")
	seedRegistry(t, a, "main", infoManifest)
	files := []string{"usr/local/Kobo/imageformats/libns.so", "a", "b", "c", "d"}
	installFromRegistry(t, a, "nickelsync", "v0.4.0", files)
	a.state.Get("nickelsync").LastChecked = "2026-11-25T10:00:00Z"
	a.state.Carryover = &state.Carryover{Packages: map[string]state.CarriedPackage{"nickelsync": {Version: "v0.4.0"}}}
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	code, m := runInfo(t, a, "nickelsync")
	if code != exitOK {
		t.Fatalf("info exit = %d, want %d", code, exitOK)
	}
	// The fields the detail page reads (detaildialog.cc addInfoGroups).
	wantStr(t, m, "id")
	wantStr(t, m, "name")
	wantStrOrNull(t, m, "device_fw")
	wantNumber(t, m, "line")
	wantBool(t, m, "def_differs")
	wantBool(t, m, "registered")
	for _, k := range []string{"installed", "staged", "pinned", "latest_seen", "last_checked", "checksum"} {
		wantStrOrNull(t, m, k)
	}
	gotFiles := wantArray(t, m, "files")
	wantNumber(t, m, "files_total")
	wantArray(t, m, "configs")
	def := wantObject(t, m, "def")
	for _, k := range []string{"from", "source", "forge", "asset", "update_check", "install_method", "kind"} {
		wantStr(t, def, k)
	}
	for _, k := range []string{"registry", "min_kpm", "tested_fw", "builtin_fw6"} {
		wantStrOrNull(t, def, k)
	}
	wantArray(t, def, "conflicts")
	fw6 := wantObject(t, def, "fw6")
	wantStr(t, fw6, "asset")
	co := wantObject(t, m, "carryover")
	wantStr(t, co, "state")

	if m["line"] != float64(4) || m["device_fw"] != "4.46.23852" {
		t.Errorf("line/device_fw = %v/%v", m["line"], m["device_fw"])
	}
	if def["from"] != "registry" || def["registry"] != "main" || def["asset"] != "KoboRoot.tgz" || def["tested_fw"] != "4.46" {
		t.Errorf("def = %v", def)
	}
	if fw6["asset"] != "Kobo.tgz" || fw6["tested_fw"] != "6.0" || fw6["source"] != nil {
		t.Errorf("fw6 = %v", fw6)
	}
	if len(gotFiles) != infoFiles || m["files_total"] != float64(len(files)) || gotFiles[0] != files[0] {
		t.Errorf("files = %v total %v, want the first %d of %d", gotFiles, m["files_total"], infoFiles, len(files))
	}
	if m["checksum"] != checksumDeclared || m["last_checked"] != "2026-11-25T10:00:00Z" {
		t.Errorf("checksum/last_checked = %v/%v", m["checksum"], m["last_checked"])
	}
	if co["state"] != carryInBundle || co["version"] != "v0.4.0" {
		t.Errorf("carryover = %v", co)
	}
}

// A local def that predates the [fw6] table of the registry: the package is
// left out of the bundle, and info says that sync fixes it.
func TestInfoCarryoverStaleLocalDef(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", infoManifest)
	installFromRegistry(t, a, "nickelsync", "v0.5.0", nil)
	local, err := a.loadPackage("nickelsync")
	if err != nil {
		t.Fatal(err)
	}
	local.Fw6 = nil
	// config.Save keeps the tables of an existing file, so write a new one.
	if err := os.Remove(a.paths.PackageFile("nickelsync")); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(a.paths.PackageFile("nickelsync"), local); err != nil {
		t.Fatal(err)
	}

	_, m := runInfo(t, a, "nickelsync")
	co := wantObject(t, m, "carryover")
	if co["state"] != carryNotInBundle || !strings.Contains(co["reason"].(string), "sync") {
		t.Errorf("carryover = %v, want not-in-bundle with a sync reason", co)
	}
	if m["def_differs"] != true {
		t.Errorf("def_differs = %v, want true", m["def_differs"])
	}
	if m["checksum"] != checksumNoneForTag {
		t.Errorf("checksum = %v, want %s (no sum for v0.5.0)", m["checksum"], checksumNoneForTag)
	}
}

func TestInfoBuiltinFw6AndNotInstalled(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", infoManifest)
	installFromRegistry(t, a, "clock", "v1.0", nil)

	_, m := runInfo(t, a, "clock")
	def := wantObject(t, m, "def")
	if def["builtin_fw6"] != "the reader clock" || def["fw6"] != nil {
		t.Errorf("def = %v", def)
	}
	co := wantObject(t, m, "carryover")
	if co["reason"] != "built into firmware 6.x" || m["checksum"] != checksumNone {
		t.Errorf("carryover/checksum = %v/%v", co, m["checksum"])
	}

	// Registry only, not installed: no checksum, no carry-over, not registered.
	b := newTestApp(t)
	seedRegistry(t, b, "main", infoManifest)
	_, m = runInfo(t, b, "nickelsync")
	if m["registered"] != false || m["checksum"] != nil || m["carryover"] != nil || m["installed"] != nil {
		t.Errorf("not installed info = %v", m)
	}
}

func TestInfoUnknownPackage(t *testing.T) {
	a := newTestApp(t)
	code, m := runInfo(t, a, "nope")
	if code != exitError || m["error"] == nil {
		t.Errorf("info nope = %d %v, want exit 1 with error", code, m)
	}
}

// ---- check <id> --json (KpmProcess::checkOne, the detail page) ----------

func TestUIContractCheckOne(t *testing.T) {
	a := newTestApp(t)
	srv := releaseServer(t, "v0.5.0")
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")
	fakeForgePkg(t, a, host, "nickelclock", "v0.4.0")
	fakeForgePkg(t, a, host, "other", "v0.1.0")

	var code int
	out := captureStdout(t, func() { code = a.cmdCheck([]string{"nickelclock", "--json"}) })
	if code != exitOK {
		t.Fatalf("check <id> exit = %d, want %d", code, exitOK)
	}
	pkgs := wantArray(t, decodeJSON(t, lastJSON(t, out)), "packages")
	if len(pkgs) != 1 {
		t.Fatalf("check <id> packages len = %d, want 1", len(pkgs))
	}
	p0 := pkgs[0].(map[string]any)
	if p0["id"] != "nickelclock" || p0["latest"] != "v0.5.0" || p0["update"] != true {
		t.Errorf("check <id> row = %v", p0)
	}
	// A one-package check is not a check of all packages.
	if a.state.LastCheck != "" || a.state.Get("other").LastChecked != "" {
		t.Errorf("last_check = %q, other.last_checked = %q, want both empty", a.state.LastCheck, a.state.Get("other").LastChecked)
	}
	if a.state.Get("nickelclock").LastChecked == "" {
		t.Error("nickelclock.last_checked not recorded")
	}

	out = captureStdout(t, func() { code = a.cmdCheck([]string{"nope", "--json"}) })
	if code != exitError || decodeJSON(t, lastJSON(t, out))["error"] == nil {
		t.Errorf("check nope = %d, want exit 1 with error", code)
	}
}
