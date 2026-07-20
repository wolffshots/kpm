package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"kpm/internal/config"
	"kpm/internal/state"
)

// tgzMembers returns the members (name -> content) in a tgz.
func tgzMembers(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, _ := gzip.NewReader(f)
	tr := tar.NewReader(gr)
	out := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		buf := make([]byte, hdr.Size)
		tr.Read(buf)
		out[hdr.Name] = string(buf)
	}
	return out
}

// A1: re-staging must keep previously staged packages in the merged tgz.
func TestStageRemergesPreviousStaging(t *testing.T) {
	a := newTestApp(t)
	cacheA := filepath.Join(a.paths.CacheDir(), "nh-v1.tgz")
	tinyTgz(t, cacheA, "./usr/local/A/file", "A1")
	a.stageForTest(t, []resolved{{pkg: &config.Package{ID: "nh"}, tag: "v1", cache: cacheA, manifest: []string{"usr/local/A/file"}}})

	// Second run stages B; A must be re-merged from the existing staged tgz.
	cacheB := filepath.Join(a.paths.CacheDir(), "bee-v1.tgz")
	tinyTgz(t, cacheB, "./usr/local/B/file", "B1")
	a.stageForTest(t, []resolved{{pkg: &config.Package{ID: "bee"}, tag: "v1", cache: cacheB, manifest: []string{"usr/local/B/file"}}})
	members := tgzMembers(t, a.paths.StagedTgz())
	if members["./usr/local/A/file"] != "A1" {
		t.Errorf("A payload lost from merged tgz: %v", members)
	}
	if members["./usr/local/B/file"] != "B1" {
		t.Errorf("B payload missing from merged tgz: %v", members)
	}

	// Both promote after a simulated reboot (committed tgz removed).
	os.Remove(a.paths.StagedTgz())
	if err := a.reconcile(); err != nil {
		t.Fatal(err)
	}
	if a.state.Get("nh").InstalledVersion != "v1" || a.state.Get("bee").InstalledVersion != "v1" {
		t.Errorf("both packages should promote: nh=%q bee=%q",
			a.state.Get("nh").InstalledVersion, a.state.Get("bee").InstalledVersion)
	}
}

// A1: re-staging the same package at a newer tag: new payload wins, no false dup.
func TestStageRemergeNewerTagNoFalseDup(t *testing.T) {
	a := newTestApp(t)
	cacheV1 := filepath.Join(a.paths.CacheDir(), "nh-v1.tgz")
	tinyTgz(t, cacheV1, "./usr/local/A/file", "old")
	a.stageForTest(t, []resolved{{pkg: &config.Package{ID: "nh"}, tag: "v1", cache: cacheV1}})

	cacheV2 := filepath.Join(a.paths.CacheDir(), "nh-v2.tgz")
	tinyTgz(t, cacheV2, "./usr/local/A/file", "new")
	dups := a.stageForTest(t, []resolved{{pkg: &config.Package{ID: "nh"}, tag: "v2", cache: cacheV2}})
	if len(dups) != 0 {
		t.Errorf("re-stage collision against the re-merged source must be suppressed, got %v", dups)
	}
	if m := tgzMembers(t, a.paths.StagedTgz()); m["./usr/local/A/file"] != "new" {
		t.Errorf("newer payload should win: %q", m["./usr/local/A/file"])
	}
}

// B6: a foreign tgz occupying the slot means our committed staging was consumed
// by the boot installer, so promotion proceeds (no more foreign-tgz freeze).
func TestReconcilePromotesOnForeignTgz(t *testing.T) {
	a := newTestApp(t)
	ps := a.state.Get("nh")
	ps.StagedVersion = "v2"
	ps.StagedManifest = []string{"usr/x"}
	a.state.StagedSHA256 = "hash-of-our-staged-tgz"
	a.state.StagedSize = 999
	a.state.StagedCommitted = true
	// A different (foreign) tgz now sits in the slot — hash won't match ours.
	if err := os.WriteFile(a.paths.StagedTgz(), []byte("a foreign manual tgz"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcile(); err != nil {
		t.Fatal(err)
	}
	if a.state.Get("nh").InstalledVersion != "v2" {
		t.Errorf("foreign tgz must not freeze promotion; installed = %q", a.state.Get("nh").InstalledVersion)
	}
}

// B6: an uncommitted staging (a crash before the tgz went live) that has since
// vanished is rolled back, never promoted — nothing was actually installed.
func TestReconcileRollsBackUncommitted(t *testing.T) {
	a := newTestApp(t)
	ps := a.state.Get("nh")
	ps.InstalledVersion = "v1"
	ps.StagedVersion = "v2"
	ps.StagedManifest = []string{"usr/x"}
	a.state.StagedSHA256 = "hash"
	a.state.StagedSize = 10
	a.state.StagedCommitted = false // never committed live
	// No tgz on disk (crash between recording intent and the live move).
	if err := a.reconcile(); err != nil {
		t.Fatal(err)
	}
	if got := a.state.Get("nh").InstalledVersion; got != "v1" {
		t.Errorf("uncommitted staging must not promote; installed = %q", got)
	}
	if a.state.Get("nh").StagedVersion != "" {
		t.Errorf("uncommitted staged fields should be rolled back: %+v", a.state.Get("nh"))
	}
}

// A1/§7.3: a non-kpm package whose archive writes kpm's reserved paths is caught.
func TestFirstReservedPath(t *testing.T) {
	if firstReservedPath([]string{"usr/local/x", "mnt/onboard/.adds/kpm/bin/kpm"}) == "" {
		t.Error("kpm's install tree must be reserved")
	}
	if firstReservedPath([]string{"mnt/onboard/.adds/NM/kpm"}) == "" {
		t.Error("kpm's NickelMenu drop-in must be reserved (case-insensitively)")
	}
	if hit := firstReservedPath([]string{"mnt/onboard/.adds/nickelhardcover/x", "usr/lib/y"}); hit != "" {
		t.Errorf("a normal package path must not be reserved, got %q", hit)
	}
}

// B1: with the lock held, a mutating command errors but a read-only one runs.
func TestLockBlocksMutatingAllowsReadOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KPM_ROOT", root)
	if err := os.MkdirAll(filepath.Join(root, ".adds", "kpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, ".adds", "kpm", "lock")
	if err := os.WriteFile(lock, []byte("999 held"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newApp(true); err == nil {
		t.Error("mutating command should fail while the lock is held")
	}
	a, err := newApp(false)
	if err != nil {
		t.Fatalf("read-only command should proceed without the lock: %v", err)
	}
	if !a.readOnly {
		t.Error("read-only app should be flagged readOnly")
	}
}

// B1: a stale lock (older than staleLockAge) is broken and retaken.
func TestStaleLockBroken(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KPM_ROOT", root)
	if err := os.MkdirAll(filepath.Join(root, ".adds", "kpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, ".adds", "kpm", "lock")
	if err := os.WriteFile(lock, []byte("1 old"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-staleLockAge - time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(true)
	if err != nil {
		t.Fatalf("stale lock should be broken and retaken: %v", err)
	}
	if !a.locked {
		t.Error("app should hold the lock after breaking a stale one")
	}
	a.releaseLock()
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Error("releaseLock should remove the lock file")
	}
}

// B4: unstage removes the staged tgz and clears all staged fields.
func TestUnstageClearsFields(t *testing.T) {
	a := newTestApp(t)
	cache := filepath.Join(a.paths.CacheDir(), "nh-v1.tgz")
	tinyTgz(t, cache, "./usr/local/A/file", "A")
	a.stageForTest(t, []resolved{{pkg: &config.Package{ID: "nh"}, tag: "v1", cache: cache, manifest: []string{"usr/local/A/file"}}})
	ps := a.state.Get("nh")

	if code := a.cmdUnstage(nil); code != exitOK {
		t.Fatalf("unstage exit %d, want 0", code)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); !os.IsNotExist(err) {
		t.Error("staged tgz should be removed")
	}
	if ps.StagedVersion != "" || ps.StagedManifest != nil {
		t.Errorf("staged fields should be cleared: %+v", ps)
	}
	if a.state.StagedSHA256 != "" || a.state.StagedSize != 0 {
		t.Error("staged hash/size should be cleared")
	}
}

// B4: unstage refuses to remove a foreign (hash-mismatch) tgz.
func TestUnstageRefusesForeign(t *testing.T) {
	a := newTestApp(t)
	if err := os.WriteFile(a.paths.StagedTgz(), []byte("manual install"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := a.cmdUnstage(nil); code != exitError {
		t.Errorf("unstage of a foreign tgz should error, got %d", code)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err != nil {
		t.Error("foreign tgz must not be removed")
	}
}

// F1: a future last-check timestamp is treated as stale.
func TestFreshFutureTimestampStale(t *testing.T) {
	future := time.Now().Add(1 * time.Hour).UTC().Format(state.TimeFormat)
	if fresh(future) {
		t.Error("a future timestamp must be treated as stale")
	}
	recent := time.Now().Add(-1 * time.Minute).UTC().Format(state.TimeFormat)
	if !fresh(recent) {
		t.Error("a recent timestamp should be fresh")
	}
}

// F3: cleanCache drops .part files and week-old tgzs, keeps recent tgzs.
func TestCleanCache(t *testing.T) {
	a := newTestApp(t)
	oldTgz := filepath.Join(a.paths.CacheDir(), "old.tgz")
	part := filepath.Join(a.paths.CacheDir(), "half.part")
	recent := filepath.Join(a.paths.CacheDir(), "recent.tgz")
	// A registry cache in cache/ must never be swept (REGISTRY.md §7.5/§9.11).
	regCache := a.paths.RegistryCache("main")
	for _, p := range []string{oldTgz, part, recent, regCache} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(oldTgz, old, old)
	os.Chtimes(regCache, old, old) // even an old registry cache survives

	a.cleanCache()
	if _, err := os.Stat(oldTgz); !os.IsNotExist(err) {
		t.Error("week-old tgz should be removed")
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Error(".part file should always be removed")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("recent tgz should be kept")
	}
	if _, err := os.Stat(regCache); err != nil {
		t.Error("registry cache (registry-<name>.toml) must survive the sweep")
	}
}

// F6: tagsEqual ignores a single leading 'v'; updateTarget uses it.
func TestTagsEqual(t *testing.T) {
	if !tagsEqual("v1.2.0", "1.2.0") {
		t.Error("v1.2.0 should equal 1.2.0")
	}
	if tagsEqual("v1", "v2") {
		t.Error("v1 must not equal v2")
	}
	pkg := &config.Package{ID: "nh"}
	_, avail := updateTarget(pkg, &state.PackageState{InstalledVersion: "v1.2.0", LatestSeen: "1.2.0"}, "")
	if avail {
		t.Error("installed v1.2.0 vs latest 1.2.0 should be up to date")
	}
}

// F7: an unconfigured package (empty source) is skipped, not an error.
func TestUnconfiguredSkipped(t *testing.T) {
	a := newTestApp(t)
	p := &config.Package{ID: "self", Source: ""}
	if p.Configured() {
		t.Fatal("empty source should be unconfigured")
	}
	_, skip, err := a.resolveAndDownload(p)
	if err != nil {
		t.Errorf("unconfigured package must not error: %v", err)
	}
	if !skip {
		t.Error("unconfigured package should be skipped")
	}

	// update --all over only-unconfigured packages: nothing to stage, exit 0.
	if err := config.Save(a.paths.PackageFile("kpm"), &config.Package{Name: "kpm", Source: "", Forge: "forgejo", Asset: "KoboRoot.tgz"}); err != nil {
		t.Fatal(err)
	}
	if code := a.cmdUpdate([]string{"--all"}); code != exitOK {
		t.Errorf("update over unconfigured packages should exit 0, got %d", code)
	}
}

// G4: read-only commands reject unexpected positional args.
func TestUnexpectedPositionals(t *testing.T) {
	a := newTestApp(t)
	if code := a.cmdList([]string{"bogus"}); code != exitError {
		t.Errorf("list with positional: exit %d, want %d", code, exitError)
	}
	if code := a.cmdStatus([]string{"bogus"}); code != exitError {
		t.Errorf("status with positional: exit %d, want %d", code, exitError)
	}
	if code := a.cmdLog([]string{"bogus"}); code != exitError {
		t.Errorf("log with positional: exit %d, want %d", code, exitError)
	}
}

// G1: log clamps a non-positive -n and does not panic.
func TestLogClampNoPanic(t *testing.T) {
	a := newTestApp(t)
	for i := 0; i < 5; i++ {
		a.paths.Log("CHECK", "x")
	}
	if code := a.cmdLog([]string{"-n", "-5"}); code != exitOK {
		t.Errorf("log -n -5 should clamp and exit 0, got %d", code)
	}
}

// G5: off-device with no KPM_ROOT is refused.
func TestOffDeviceGuard(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("guard only applies off Linux")
	}
	t.Setenv("KPM_ROOT", "")
	if _, err := newApp(false); err == nil {
		t.Error("newApp must refuse to run off-device without KPM_ROOT")
	}
}

// E3: an id that is not [a-z0-9-]+ (e.g. a traversal) is rejected.
func TestRemoveRejectsInvalidID(t *testing.T) {
	a := newTestApp(t)
	if code := a.cmdRemove([]string{"../../foo"}); code != exitError {
		t.Errorf("remove of a traversal id should error, got %d", code)
	}
}

// C8: if removing the packages.d file fails, state is kept intact for retry and
// the command exits 2 (partial). Uses a held file handle, which blocks deletion
// on Windows; skipped elsewhere.
func TestUninstallRegistrationRemovalFailureKeepsState(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("relies on Windows open-file delete semantics")
	}
	a, sysroot := newUninstallApp(t)
	mkfile(t, sysroot, "usr/local/pkg/lib.so")
	registerPkg(t, a, "pkg", config.Uninstall{}, []string{"usr/local/pkg/lib.so"})

	// Hold the registration file open so os.Remove fails.
	h, err := os.Open(a.paths.PackageFile("pkg"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	if code := a.cmdUninstall([]string{"--yes", "pkg"}); code != exitPartial {
		t.Errorf("registration-removal failure should exit %d, got %d", exitPartial, code)
	}
	if a.state.Packages["pkg"] == nil {
		t.Error("state entry must be retained when registration removal fails")
	}
}
