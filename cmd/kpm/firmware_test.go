package main

// firmware_test.go covers kpm on firmware 6.x (QT6-FIRMWARE6.md section 7):
// staging .kobo/Kobo.tgz on line 6, the [fw6] asset, the refusal of a package
// with no 6.x build, and the firmware-change check.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/forge"
	"kpm/internal/registry"
)

const (
	fw4 = "4.46.23852"
	fw6 = "6.0.279225"
)

// writeFirmware writes .kobo/version with the firmware in the third field.
func writeFirmware(t *testing.T, a *App, fw string) {
	t.Helper()
	line := "N000000000000,4.1.15," + fw + ",4.1.15,4.1.15,00000000-0000-0000-0000-000000000390\n"
	if err := os.MkdirAll(filepath.Dir(a.paths.VersionFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.paths.VersionFile(), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

// assetsServer serves one release (tag) with the given assets, by name.
func assetsServer(t *testing.T, a *App, tag string, assets map[string][]byte) string {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/repos/"):
			var list []string
			for name, body := range assets {
				list = append(list, fmt.Sprintf(`{"name":%q,"size":%d,"browser_download_url":%q}`,
					name, len(body), srv.URL+"/download/"+name))
			}
			fmt.Fprintf(w, `{"tag_name":%q,"assets":[%s]}`, tag, strings.Join(list, ","))
		case strings.HasPrefix(r.URL.Path, "/download/"):
			w.Write(assets[strings.TrimPrefix(r.URL.Path, "/download/")])
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	a.client = forge.NewClientWithHTTP(srv.Client())
	return strings.TrimPrefix(srv.URL, "https://")
}

// dualRelease serves a release with a KoboRoot.tgz for 4.x and a Kobo.tgz for
// 6.x, and registers pkg with an [fw6] table when withFw6 is true.
func dualRelease(t *testing.T, a *App, withFw6 bool, kobo []byte) {
	t.Helper()
	root := tgzBytes(t, map[string]string{"./usr/local/Kobo/imageformats/libpkg.so": "qt5"})
	host := assetsServer(t, a, "v2.0.0", map[string][]byte{"KoboRoot.tgz": root, "Kobo.tgz": kobo})
	p := &config.Package{Name: "pkg", Source: host + "/o/pkg", Forge: config.ForgeForgejo, Asset: "KoboRoot.tgz"}
	if withFw6 {
		p.Fw6 = &config.Fw6{Asset: "Kobo.tgz"}
	}
	if err := config.Save(a.paths.PackageFile("pkg"), p); err != nil {
		t.Fatal(err)
	}
}

// On line 6, update downloads the [fw6] asset and stages it as .kobo/Kobo.tgz.
// The manifest holds the full device paths (section 3b rule 6).
func TestUpdateLine6StagesKoboTgz(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.13.0")
	writeFirmware(t, a, fw6)
	kobo := tgzBytes(t, map[string]string{"imageformats/libpkg-qt6.so": "qt6"})
	dualRelease(t, a, true, kobo)

	var code int
	captureStdout(t, func() { code = a.cmdUpdate([]string{"pkg"}) })
	if code != exitOK {
		t.Fatalf("update on line 6 = %d, want %d", code, exitOK)
	}
	koboTgz := filepath.Join(a.paths.Root, ".kobo", "Kobo.tgz")
	if a.paths.StagedTgz() != koboTgz {
		t.Fatalf("StagedTgz() = %s, want %s", a.paths.StagedTgz(), koboTgz)
	}
	if _, err := os.Stat(koboTgz); err != nil {
		t.Fatalf("Kobo.tgz not staged: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.paths.Root, ".kobo", "KoboRoot.tgz")); err == nil {
		t.Error("line 6 must not stage KoboRoot.tgz")
	}
	got := a.state.Get("pkg").StagedManifest
	if len(got) != 1 || got[0] != "usr/local/Kobo/imageformats/libpkg-qt6.so" {
		t.Errorf("staged manifest = %v, want the usr/local/Kobo/imageformats path", got)
	}
}

// A KoboRoot.tgz as the fw6 asset is refused before staging.
func TestUpdateLine6RefusesKoboRootShape(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.13.0")
	writeFirmware(t, a, fw6)
	dualRelease(t, a, true, tgzBytes(t, map[string]string{"./usr/local/Kobo/imageformats/x.so": "x"}))

	var code int
	var stderr string
	captureStdout(t, func() { stderr = captureStderr(t, func() { code = a.cmdUpdate([]string{"pkg"}) }) })
	if code == exitOK {
		t.Fatal("a KoboRoot.tgz on line 6 must fail")
	}
	if !strings.Contains(stderr, "the asset is a KoboRoot.tgz for firmware 4.x") {
		t.Errorf("stderr = %q", stderr)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err == nil {
		t.Error("nothing may be staged")
	}
}

// A tgz package with no [fw6] table fails when named and is a skip under --all
// (section 3c rule 4).
func TestUpdateLine6Unavailable(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.13.0")
	writeFirmware(t, a, fw6)
	dualRelease(t, a, false, tgzBytes(t, map[string]string{"imageformats/x.so": "x"}))

	var code int
	var stderr string
	captureStdout(t, func() { stderr = captureStderr(t, func() { code = a.cmdUpdate([]string{"pkg"}) }) })
	if code == exitOK || !strings.Contains(stderr, config.Unavailable6) {
		t.Errorf("named: exit %d, stderr %q; want a failure with %q", code, stderr, config.Unavailable6)
	}
	var out string
	out = captureStdout(t, func() { code = a.cmdUpdate([]string{"--all"}) })
	if code != exitOK || !strings.Contains(out, "skipped") {
		t.Errorf("--all: exit %d, stdout %q; want a skip with a note", code, out)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err == nil {
		t.Error("nothing may be staged")
	}
}

// On line 4 the same def stages its base KoboRoot.tgz, as before.
func TestUpdateLine4IgnoresFw6(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.13.0")
	writeFirmware(t, a, fw4)
	dualRelease(t, a, true, tgzBytes(t, map[string]string{"imageformats/x.so": "x"}))

	var code int
	captureStdout(t, func() { code = a.cmdUpdate([]string{"pkg"}) })
	if code != exitOK {
		t.Fatalf("update on line 4 = %d", code)
	}
	if !strings.HasSuffix(a.paths.StagedTgz(), "KoboRoot.tgz") {
		t.Fatalf("StagedTgz() = %s", a.paths.StagedTgz())
	}
	got := a.state.Get("pkg").StagedManifest
	if len(got) == 0 || got[len(got)-1] != "usr/local/Kobo/imageformats/libpkg.so" {
		t.Errorf("staged manifest = %v", got)
	}
}

// The 4.x to 6.x upgrade (section 3f): newApp deletes the KoboRoot.tgz that kpm
// staged on 4.x and does not promote it, forgets a package whose files are all
// gone, keeps one with files left, keeps kpm, and records the new firmware.
func TestFirmwareChangeOnUpgrade(t *testing.T) {
	a := newTestApp(t)
	sys := t.TempDir()
	t.Setenv("KPM_SYSROOT", sys)

	// A commit of a KoboRoot.tgz on 4.x that the device never consumed.
	writeFirmware(t, a, fw4)
	cache := filepath.Join(a.paths.CacheDir(), "nm-v1.tgz")
	tinyTgz(t, cache, "./usr/local/Kobo/imageformats/libnm.so", "nm")
	a.stageForTest(t, []resolved{{pkg: &config.Package{ID: "nm"}, tag: "v2", cache: cache,
		manifest: []string{"usr/local/Kobo/imageformats/libnm.so"}}})
	a.state.StagedCommitted = true
	old := a.paths.StagedTgz()

	gone := a.state.Get("gone")
	gone.InstalledVersion = "v1"
	// A user-partition file survives the update. It must not keep the package
	// installed (vm finding of 2026-11-18: NickelMenu has this shape).
	gone.Manifest = []string{"usr/local/Kobo/imageformats/libgone.so", "mnt/onboard/.adds/gone/readme"}
	kept := a.state.Get("kept")
	kept.InstalledVersion = "v1"
	kept.Manifest = []string{"usr/local/Kobo/imageformats/libkept.so", "usr/bin/kept"}
	self := a.state.Get(selfID)
	self.InstalledVersion = "0.12.0"
	self.Manifest = []string{"usr/local/Kobo/imageformats/libnkpm.so"}
	a.state.Firmware = fw4
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sys, "usr", "bin", "kept"), "kept")
	mustWrite(t, filepath.Join(sys, "mnt", "onboard", ".adds", "gone", "readme"), "gone")

	writeFirmware(t, a, fw6)
	b, err := newApp(true)
	if err != nil {
		t.Fatal(err)
	}
	defer b.releaseLock()

	if _, err := os.Stat(old); err == nil {
		t.Error("the staged KoboRoot.tgz of 4.x must be deleted")
	}
	if ps := b.state.Get("nm"); ps.StagedVersion != "" || ps.InstalledVersion != "" {
		t.Errorf("nm: staged %q installed %q, want both empty (not promoted)", ps.StagedVersion, ps.InstalledVersion)
	}
	if b.state.StagedSHA256 != "" || b.state.StagedCommitted {
		t.Error("the staged identity must be cleared")
	}
	if ps := b.state.Get("gone"); ps.InstalledVersion != "" || ps.Manifest != nil {
		t.Errorf("gone: installed %q manifest %v, want cleared", ps.InstalledVersion, ps.Manifest)
	}
	if ps := b.state.Get("kept"); ps.InstalledVersion != "v1" || len(ps.MissingFiles) != 1 || ps.MissingFiles[0] != "usr/local/Kobo/imageformats/libkept.so" {
		t.Errorf("kept: installed %q missing %v, want installed with the .so missing", ps.InstalledVersion, ps.MissingFiles)
	}
	ps := b.state.Get(selfID)
	if ps.InstalledVersion != "0.12.0" || !selfRepair(selfID, ps) {
		t.Errorf("kpm: installed %q missing %v, want installed and marked for self-repair", ps.InstalledVersion, ps.MissingFiles)
	}
	ps.LatestSeen = "0.12.0"
	if _, avail := updateTarget(&config.Package{ID: selfID}, ps, ""); !avail {
		t.Error("check must offer the kpm self-repair as an update")
	}
	if b.state.Firmware != fw6 {
		t.Errorf("state firmware = %q, want %q", b.state.Firmware, fw6)
	}
	if !logContains(t, b, "gone  firmware changed") {
		t.Error("the cleared install must be logged")
	}
}

// kpm installed by hand and enrolled with adopt-self has no manifest. After the
// upgrade to 6.x its Qt 6 hook is missing, and kpm must repair itself
// (NickelSync suite finding of 2026-11-18).
func TestFirmwareChangeSelfWithoutManifest(t *testing.T) {
	a := newTestApp(t)
	t.Setenv("KPM_SYSROOT", t.TempDir())
	self := a.state.Get(selfID)
	self.InstalledVersion = "0.13.0"
	a.state.Firmware = fw4
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	writeFirmware(t, a, fw6)
	b, err := newApp(true)
	if err != nil {
		t.Fatal(err)
	}
	defer b.releaseLock()
	ps := b.state.Get(selfID)
	if !selfRepair(selfID, ps) {
		t.Fatalf("kpm: missing %v, want the Qt 6 hook missing", ps.MissingFiles)
	}
	if ps.MissingFiles[0] != "usr/local/Kobo/imageformats/libnkpm-qt6.so" {
		t.Errorf("kpm: missing %v, want the Qt 6 hook", ps.MissingFiles)
	}
	// reverifyMissing reads the manifest. The hook must stay missing until the
	// reinstall.
	if b.reverifyMissing() || !selfRepair(selfID, ps) {
		t.Errorf("kpm: missing %v after reverify, want the Qt 6 hook still missing", ps.MissingFiles)
	}
}

// A firmware that is not recorded yet is recorded on 4.x, and nothing is
// cleared.
func TestFirmwareFirstRecordIsNotAChange(t *testing.T) {
	a := newTestApp(t)
	t.Setenv("KPM_SYSROOT", t.TempDir())
	writeFirmware(t, a, fw4)
	ps := a.state.Get("gone")
	ps.InstalledVersion = "v1"
	ps.Manifest = []string{"usr/local/Kobo/imageformats/libgone.so"}
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	b, err := newApp(true)
	if err != nil {
		t.Fatal(err)
	}
	defer b.releaseLock()
	if b.state.Get("gone").InstalledVersion != "v1" {
		t.Error("a first record must not clear installs")
	}
	if b.state.Firmware != fw4 {
		t.Errorf("state firmware = %q, want %q", b.state.Firmware, fw4)
	}
}

// kpm 0.12.0 records no firmware. Its state on a 6.x device means an upgrade
// from 4.x: the check runs (emulator finding of 2026-11-18).
func TestFirmwareUnrecordedOn6IsAnUpgrade(t *testing.T) {
	a := newTestApp(t)
	t.Setenv("KPM_SYSROOT", t.TempDir())
	writeFirmware(t, a, fw6)
	ps := a.state.Get("gone")
	ps.InstalledVersion = "v1"
	ps.Manifest = []string{"usr/local/Kobo/imageformats/libgone.so"}
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	b, err := newApp(true)
	if err != nil {
		t.Fatal(err)
	}
	defer b.releaseLock()
	if b.state.Get("gone").InstalledVersion != "" {
		t.Error("an unrecorded firmware on 6.x with a 4.x state must run the upgrade check")
	}
	if b.state.Firmware != fw6 {
		t.Errorf("state firmware = %q, want %q", b.state.Firmware, fw6)
	}
}

// A staged KoboRoot.tgz that kpm did not stage stays after a line change.
func TestFirmwareChangeKeepsForeignStaged(t *testing.T) {
	a := newTestApp(t)
	t.Setenv("KPM_SYSROOT", t.TempDir())
	foreign := a.paths.StagedTgzFor(config.Line4)
	tinyTgz(t, foreign, "./usr/x", "user")
	a.state.Firmware = fw4
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	writeFirmware(t, a, fw6)
	b, err := newApp(true)
	if err != nil {
		t.Fatal(err)
	}
	defer b.releaseLock()
	if _, err := os.Stat(foreign); err != nil {
		t.Error("a foreign KoboRoot.tgz must stay")
	}
}

// Doctor reports loaded when any build of a dual package is mapped.
func TestPluginSOsDual(t *testing.T) {
	got := pluginSOs([]string{
		"usr/local/Kobo/imageformats",
		"usr/local/Kobo/imageformats/libnkpm.so",
		"usr/local/Kobo/imageformats/libnkpm-qt6.so",
		"usr/local/Kobo/kpm-seed/bin/kpm",
	})
	if len(got) != 2 || got[0] != "/usr/local/Kobo/imageformats/libnkpm.so" || got[1] != "/usr/local/Kobo/imageformats/libnkpm-qt6.so" {
		t.Errorf("pluginSOs = %v", got)
	}
}

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A def that an older kpm wrote has no [fw6] table. On the first run after the
// upgrade to 6.x, kpm syncs it from the registry cache, so the package is
// available again (vm finding of 2026-11-18).
func TestFirmwareChangeSyncsFw6Def(t *testing.T) {
	a := newTestApp(t)
	t.Setenv("KPM_SYSROOT", t.TempDir())
	seedRegistry(t, a, "main", nmManifest+"\n  [packages.nickelmenu.fw6]\n  asset = \"Kobo.tgz\"\n")
	old := &config.Package{Name: "NickelMenu", Source: "github.com/pgaskin/NickelMenu", Forge: "github",
		Asset: "KoboRoot.tgz", Registry: "main"}
	if err := config.Save(a.paths.PackageFile("nickelmenu"), old); err != nil {
		t.Fatal(err)
	}
	h, _ := registry.HashDef(registry.DefFromPackage(old))
	a.state.Get("nickelmenu").SyncedDefSHA256 = h
	a.state.Firmware = fw4
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	writeFirmware(t, a, fw6)
	var b *App
	captureStdout(t, func() {
		var err error
		if b, err = newApp(true); err != nil {
			t.Fatal(err)
		}
	})
	defer b.releaseLock()
	p, err := b.loadPackage("nickelmenu")
	if err != nil {
		t.Fatal(err)
	}
	if p.Fw6 == nil || p.Fw6.Asset != "Kobo.tgz" || !p.AvailableOn(config.Line6) {
		t.Errorf("nickelmenu fw6 = %+v, want the registry [fw6] table", p.Fw6)
	}
}

// Doctor finds the Qt 6 kpm hook although the state manifest is the 4.x one,
// as after a hand install of Kobo.tgz on 6.x (vm finding of 2026-11-18).
func TestDoctorSelfQt6Hook(t *testing.T) {
	a := newTestApp(t)
	ps := a.state.Get(selfID)
	ps.InstalledVersion = "v0.13.0"
	ps.Manifest = []string{"mnt/onboard/.adds/kpm/bin/kpm", "usr/local/Kobo/imageformats/libnkpm.so"}
	a.nickelMaps = func() (string, bool) {
		return "b6f00000-b6f40000 r-xp 00000000 b3:06 1234 /usr/local/Kobo/imageformats/libnkpm-qt6.so\n", true
	}
	res := a.diagnose(&config.Package{ID: selfID, Name: "kpm"}, nil, fw6)
	if res.verdict != "loaded" || res.plugin != "/usr/local/Kobo/imageformats/libnkpm-qt6.so" {
		t.Errorf("verdict %q plugin %q, want loaded from libnkpm-qt6.so", res.verdict, res.plugin)
	}
}
