package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kpm/internal/config"
)

// doctor_test.go covers `kpm doctor` (DOCTOR.md): the signal-precedence chain,
// the /proc/maps seam, and the --json golden. Fixtures live in a KPM_SYSROOT
// sandbox (newUninstallApp) so plugin .so / .failsafe siblings and dump-logs can
// be laid down on disk and mapped through HostPath exactly as on-device.

const notePlugin = "usr/local/Kobo/imageformats/libnickelnote.so"

// installNotePkg registers nickelnote with the imageformats plugin in its
// manifest and an installed_version, so doctor treats it as a diagnosable plugin.
// installedAt controls the dump-log staleness comparison.
func installNotePkg(t *testing.T, a *App, sysroot, installedAt string) {
	t.Helper()
	if err := config.Save(a.paths.PackageFile("nickelnote"), &config.Package{
		Name: "NickelNote", Source: "github.com/onatbas/nickelnote", Forge: "github", Asset: "NickelNote-*.zip",
	}); err != nil {
		t.Fatal(err)
	}
	ps := a.state.Get("nickelnote")
	ps.InstalledVersion = "v0.0.4"
	ps.InstalledAt = installedAt
	ps.Manifest = []string{notePlugin}
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	mkfile(t, sysroot, notePlugin) // the plugin .so on disk
}

// withMaps injects a fake /proc/maps reader for the seam.
func withMaps(a *App, content string, available bool) {
	a.nickelMaps = func() (string, bool) { return content, available }
}

// TestDoctorJSONGolden pins the doctor --json payload shape (forward contract):
// nickelnote's plugin is mapped in, so the verdict is loaded, and with a device
// newer than tested_fw the fw fields are populated but no cause is appended to a
// non-bad verdict.
func TestDoctorJSONGolden(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	setVersion(t, "0.9.0")
	// Device firmware 4.45, def tested on 4.38 → fw_untested true.
	if err := os.MkdirAll(filepath.Dir(a.paths.VersionFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.paths.VersionFile(), []byte("serial,rev,4.45.23697,x,y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedRegistry(t, a, "main", `schema_version = 1
[packages.nickelnote]
name = "NickelNote"
source = "github.com/onatbas/nickelnote"
forge = "github"
asset = "NickelNote-*.zip"
tested_fw = "4.38.23429"
nh_name = "NickelNote"
`)
	installNotePkg(t, a, sysroot, "2026-07-20T00:00:00Z")
	withMaps(a, "7f00-7f10 r-xp libnickelnote.so\n", true)

	out := captureStdout(t, func() { a.cmdDoctor([]string{"--json"}) })
	got := lastJSON(t, out)
	want := `{"device_fw":"4.45.23697","packages":[{"id":"nickelnote","verdict":"loaded","detail":"plugin mapped into Nickel (proves it loaded, not that it works)","plugin":"/usr/local/Kobo/imageformats/libnickelnote.so","tested_fw":"4.38.23429","fw_untested":true}]}`
	if got != want {
		t.Errorf("doctor --json\n got: %s\nwant: %s", got, want)
	}
}

// TestDoctorNotLoaded: the plugin is absent from the map → not-loaded, and with
// fw_untested the likely-cause clause is appended.
func TestDoctorNotLoaded(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	if err := os.MkdirAll(filepath.Dir(a.paths.VersionFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(a.paths.VersionFile(), []byte("serial,rev,4.45.23697,x,y\n"), 0o644)
	seedRegistry(t, a, "main", `schema_version = 1
[packages.nickelnote]
name = "NickelNote"
source = "github.com/onatbas/nickelnote"
forge = "github"
asset = "NickelNote-*.zip"
tested_fw = "4.38.23429"
nh_name = "NickelNote"
`)
	installNotePkg(t, a, sysroot, "2026-07-20T00:00:00Z")
	withMaps(a, "7f00-7f10 r-xp libsomethingelse.so\n", true) // note NOT mapped

	out := captureStdout(t, func() { a.cmdDoctor([]string{"nickelnote", "--json"}) })
	got := lastJSON(t, out)
	for _, want := range []string{
		`"verdict":"not-loaded"`,
		`not loaded — plugin not mapped into Nickel`,
		`last confirmed on firmware 4.38.23429; your device runs 4.45.23697`,
		`"fw_untested":true`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor not-loaded missing %q\n got: %s", want, got)
		}
	}
}

// TestDoctorFailsafeQuarantine: a <so>.failsafe sibling takes precedence over
// everything, even a mapped-in plugin.
func TestDoctorFailsafeQuarantine(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	installNotePkg(t, a, sysroot, "2026-07-20T00:00:00Z")
	// Quarantine sibling on disk next to the plugin.
	mkfile(t, sysroot, notePlugin+".failsafe")
	withMaps(a, "7f00 libnickelnote.so\n", true) // even though "mapped", failsafe wins

	out := captureStdout(t, func() { a.cmdDoctor([]string{"nickelnote", "--json"}) })
	got := lastJSON(t, out)
	if !strings.Contains(got, `"verdict":"crashed"`) || !strings.Contains(got, "quarantined by failsafe") {
		t.Errorf("failsafe sibling should yield crashed verdict:\n%s", got)
	}
}

// TestDoctorLoadFailedDumpLog: a dump-log newer than the install fires load-failed;
// a dump-log OLDER than the install is ignored (stale).
func TestDoctorLoadFailedDumpLog(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	installNotePkg(t, a, sysroot, "2026-07-20T00:00:00Z")
	withMaps(a, "", true) // not in maps, but the dump-log signal precedes signal 3

	// A stale dump (before the install) must be ignored.
	stale := filepath.Join(a.paths.Root, "NickelNote_120-00-00_00-00-00.log")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldT := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	os.Chtimes(stale, oldT, oldT)

	// Only the stale dump exists → NOT load-failed (falls through to not-loaded).
	out := captureStdout(t, func() { a.cmdDoctor([]string{"nickelnote", "--json"}) })
	if got := lastJSON(t, out); !strings.Contains(got, `"verdict":"not-loaded"`) {
		t.Errorf("stale dump-log must be ignored, want not-loaded:\n%s", got)
	}

	// A fresh dump (after the install) fires load-failed and cites the filename.
	fresh := filepath.Join(a.paths.Root, "NickelNote_126-06-22_10-30-00.log")
	if err := os.WriteFile(fresh, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	newT := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	os.Chtimes(fresh, newT, newT)

	out = captureStdout(t, func() { a.cmdDoctor([]string{"nickelnote", "--json"}) })
	got := lastJSON(t, out)
	if !strings.Contains(got, `"verdict":"load-failed"`) || !strings.Contains(got, "NickelNote_126-06-22_10-30-00.log") {
		t.Errorf("fresh dump-log should fire load-failed citing the file:\n%s", got)
	}
}

// TestDoctorMapsUnavailable: with no running Nickel (the host default), signal 3
// is skipped and a clean-on-disk plugin reports loaded with the unavailable note.
func TestDoctorMapsUnavailable(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	installNotePkg(t, a, sysroot, "2026-07-20T00:00:00Z")
	withMaps(a, "", false) // maps unavailable

	out := captureStdout(t, func() { a.cmdDoctor([]string{"nickelnote", "--json"}) })
	got := lastJSON(t, out)
	if !strings.Contains(got, `"verdict":"loaded"`) || !strings.Contains(got, "mapping check unavailable") {
		t.Errorf("unavailable maps should report loaded + unavailable note:\n%s", got)
	}
}

// TestDoctorNonPlugin: a package with no imageformats .so in its manifest is not
// diagnosable.
func TestDoctorNonPlugin(t *testing.T) {
	a := newTestApp(t)
	if err := config.Save(a.paths.PackageFile("kscribbler"), &config.Package{
		Name: "Kscribbler", Source: "github.com/GianniBYoung/kscribbler", Forge: "github", Asset: "KoboRoot.tgz",
	}); err != nil {
		t.Fatal(err)
	}
	ps := a.state.Get("kscribbler")
	ps.InstalledVersion = "v1"
	ps.Manifest = []string{"opt/bin/kscribbler", "mnt/onboard/.adds/kscribbler/config.env.example"}
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { a.cmdDoctor([]string{"kscribbler", "--json"}) })
	got := lastJSON(t, out)
	if !strings.Contains(got, `"verdict":"unknown"`) || !strings.Contains(got, "not a Nickel plugin") {
		t.Errorf("non-plugin package should be unknown/non-diagnosable:\n%s", got)
	}
	if !strings.Contains(got, `"plugin":null`) {
		t.Errorf("non-plugin package should have null plugin:\n%s", got)
	}
}

// TestDoctorUnknownPackageExit1: an unregistered id is a usage error (exit 1),
// distinct from a bad-but-ran verdict (always exit 0).
func TestDoctorUnknownPackageExit1(t *testing.T) {
	a := newTestApp(t)
	if code := a.cmdDoctor([]string{"nonesuch"}); code != exitError {
		t.Errorf("unknown package should exit %d, got %d", exitError, code)
	}
	// A ran diagnosis (even a bad verdict) is exit 0.
	a2, sysroot := newUninstallApp(t)
	installNotePkg(t, a2, sysroot, "2026-07-20T00:00:00Z")
	withMaps(a2, "", true)
	if code := a2.cmdDoctor([]string{"nickelnote"}); code != exitOK {
		t.Errorf("a diagnosis that ran should exit %d, got %d", exitOK, code)
	}
}

// TestDoctorNhNameFallback: with no nh_name in the registry def, doctor derives
// the dump-log key from the display Name with spaces stripped.
func TestDoctorNhNameFallback(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	// Display name has a space; no registry def at all (entries empty).
	if err := config.Save(a.paths.PackageFile("nickelnote"), &config.Package{
		Name: "Nickel Note", Source: "github.com/onatbas/nickelnote", Forge: "github", Asset: "NickelNote-*.zip",
	}); err != nil {
		t.Fatal(err)
	}
	ps := a.state.Get("nickelnote")
	ps.InstalledVersion = "v0.0.4"
	ps.InstalledAt = "2026-07-20T00:00:00Z"
	ps.Manifest = []string{notePlugin}
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	mkfile(t, sysroot, notePlugin)
	withMaps(a, "", true)

	// A dump-log keyed on the stripped name "NickelNote".
	fresh := filepath.Join(a.paths.Root, "NickelNote_126-06-22_10-30-00.log")
	os.WriteFile(fresh, []byte("x"), 0o644)
	newT := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	os.Chtimes(fresh, newT, newT)

	out := captureStdout(t, func() { a.cmdDoctor([]string{"nickelnote", "--json"}) })
	if got := lastJSON(t, out); !strings.Contains(got, `"verdict":"load-failed"`) {
		t.Errorf("fallback nh_name (stripped display name) should match the dump-log:\n%s", got)
	}
}

// TestDoctorEmptyNoTargets: doctor with no installed packages emits the empty
// payload (marker still last) and exits 0.
func TestDoctorEmptyNoTargets(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.9.0")
	out := captureStdout(t, func() {
		if code := a.cmdDoctor([]string{"--json"}); code != exitOK {
			t.Errorf("exit = %d, want %d", code, exitOK)
		}
	})
	got := lastJSON(t, out)
	if want := `{"device_fw":null,"packages":[]}`; got != want {
		t.Errorf("empty doctor --json\n got: %s\nwant: %s", got, want)
	}
}

// TestDoctorLockClassification: doctor is read-only (no lock), so it runs while
// the single-instance lock is held.
func TestDoctorLockClassification(t *testing.T) {
	if isMutating("doctor", nil) {
		t.Error("doctor must be read-only (not mutating)")
	}
	root := t.TempDir()
	t.Setenv("KPM_ROOT", root)
	if err := os.MkdirAll(filepath.Join(root, ".adds", "kpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".adds", "kpm", "lock"), []byte("999 held"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newApp(isMutating("doctor", nil)); err != nil {
		t.Errorf("doctor should proceed without the lock: %v", err)
	}
}
