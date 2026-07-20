package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/state"
	"kpm/internal/uninstall"
)

// newUninstallApp builds an App with KPM_SYSROOT and a consistent KPM_ROOT so
// deletions and kpm's own data both land inside one sandbox.
func newUninstallApp(t *testing.T) (*App, string) {
	t.Helper()
	sysroot := t.TempDir()
	t.Setenv("KPM_SYSROOT", sysroot)
	root := filepath.Join(sysroot, "mnt", "onboard")
	t.Setenv("KPM_ROOT", root)
	p := device.New()
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p.StagedTgz()), 0o755); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Load(p.StateFile())
	return &App{paths: p, state: st}, sysroot
}

// registerPkg writes a package TOML and seeds its manifest + installed version.
func registerPkg(t *testing.T, a *App, id string, u config.Uninstall, manifest []string) {
	t.Helper()
	pkg := &config.Package{
		Name:      id,
		Source:    "codeberg.org/o/" + id,
		Forge:     config.ForgeForgejo,
		Asset:     "KoboRoot.tgz",
		Uninstall: u,
	}
	if err := config.Save(a.paths.PackageFile(id), pkg); err != nil {
		t.Fatal(err)
	}
	ps := a.state.Get(id)
	ps.InstalledVersion = "v1"
	ps.Manifest = manifest
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
}

func mkfile(t *testing.T, sysroot, rel string) string {
	t.Helper()
	host := filepath.Join(sysroot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(host), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(host, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return host
}

func TestUninstallConfirmRequired(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	f := mkfile(t, sysroot, "usr/local/pkg/lib.so")
	registerPkg(t, a, "pkg", config.Uninstall{}, []string{"usr/local/pkg/lib.so"})

	if code := a.cmdUninstall([]string{"pkg"}); code != exitConfirm {
		t.Errorf("without --yes: exit %d, want %d", code, exitConfirm)
	}
	if _, err := os.Stat(f); err != nil {
		t.Error("file must not be deleted without --yes")
	}
}

func TestUninstallDryRunMutatesNothing(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	f := mkfile(t, sysroot, "usr/local/pkg/lib.so")
	registerPkg(t, a, "pkg", config.Uninstall{}, []string{"usr/local/pkg/lib.so"})

	if code := a.cmdUninstall([]string{"--dry-run", "pkg"}); code != exitOK {
		t.Errorf("--dry-run exit %d, want 0", code)
	}
	if _, err := os.Stat(f); err != nil {
		t.Error("--dry-run must not delete files")
	}
	if _, err := os.Stat(a.paths.PackageFile("pkg")); err != nil {
		t.Error("--dry-run must not unregister")
	}
	if a.state.Packages["pkg"] == nil {
		t.Error("--dry-run must not clear state")
	}
}

func TestUninstallSuccessClearsStateAndRegistration(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	f := mkfile(t, sysroot, "usr/local/pkg/lib.so")
	mkfile(t, sysroot, "usr/local/keepme")
	registerPkg(t, a, "pkg", config.Uninstall{}, []string{"usr/local/pkg/lib.so"})

	if code := a.cmdUninstall([]string{"--yes", "pkg"}); code != exitOK {
		t.Fatalf("--yes exit %d, want 0", code)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Error("file should be deleted")
	}
	if _, err := os.Stat(a.paths.PackageFile("pkg")); !os.IsNotExist(err) {
		t.Error("packages.d file should be removed")
	}
	if a.state.Packages["pkg"] != nil {
		t.Error("state entry should be cleared")
	}
}

func TestUninstallKeepRegistration(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	mkfile(t, sysroot, "usr/local/pkg/lib.so")
	registerPkg(t, a, "pkg", config.Uninstall{}, []string{"usr/local/pkg/lib.so"})

	if code := a.cmdUninstall([]string{"--yes", "--keep-registration", "pkg"}); code != exitOK {
		t.Fatalf("exit %d, want 0", code)
	}
	if _, err := os.Stat(a.paths.PackageFile("pkg")); err != nil {
		t.Error("--keep-registration should keep the TOML")
	}
	if a.state.Packages["pkg"] != nil {
		t.Error("state entry should still be cleared on success")
	}
}

func TestUninstallStagedPendingRefused(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	f := mkfile(t, sysroot, "usr/local/pkg/lib.so")
	registerPkg(t, a, "pkg", config.Uninstall{}, []string{"usr/local/pkg/lib.so"})
	// Simulate a pending staged update.
	a.state.Get("pkg").StagedVersion = "v2"
	if err := os.WriteFile(a.paths.StagedTgz(), []byte("staged"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := a.cmdUninstall([]string{"--yes", "pkg"}); code != exitError {
		t.Errorf("staged-pending: exit %d, want %d", code, exitError)
	}
	if _, err := os.Stat(f); err != nil {
		t.Error("staged-pending refusal must not delete files")
	}
}

func TestUninstallSelfRefused(t *testing.T) {
	a, _ := newUninstallApp(t)
	if code := a.cmdUninstall([]string{"--yes", "kpm"}); code != exitError {
		t.Errorf("self-uninstall: exit %d, want %d", code, exitError)
	}
}

func TestUninstallPartialFailureExit2(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	// Marker method whose parent path is occupied by a regular file, so
	// creating the marker fails -> partial.
	blocker := filepath.Join(sysroot, filepath.FromSlash("mnt/onboard/.adds/nm"))
	if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	registerPkg(t, a, "pkg", config.Uninstall{
		Method:     config.MethodMarker,
		MarkerFile: "/mnt/onboard/.adds/nm/uninstall",
	}, nil)

	if code := a.cmdUninstall([]string{"--yes", "pkg"}); code != exitPartial {
		t.Errorf("partial failure: exit %d, want %d", code, exitPartial)
	}
	// State entry retained for retry.
	if a.state.Packages["pkg"] == nil {
		t.Error("partial failure must retain state entry")
	}
	if _, err := os.Stat(a.paths.PackageFile("pkg")); err != nil {
		t.Error("partial failure must keep the registration")
	}
}

// finding 2: an execution-time safety skip (symlinked parent, C7) leaves a file
// on disk, so the package must NOT be unregistered — registration and state are
// kept for retry and the exit is partial.
func TestUninstallExecSkipRetainsRegistration(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	// The real directory the symlink points at — its file must survive.
	real := filepath.Join(sysroot, "opt", "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(real, "secret")
	if err := os.WriteFile(secret, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(sysroot, "opt", "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(pkgDir, "link")); err != nil {
		t.Skipf("symlink creation unsupported on this host: %v", err)
	}
	registerPkg(t, a, "pkg", config.Uninstall{}, []string{"opt/pkg/link/secret"})

	if code := a.cmdUninstall([]string{"--yes", "pkg"}); code != exitPartial {
		t.Errorf("exec-skip: exit %d, want %d", code, exitPartial)
	}
	if !exists(secret) {
		t.Error("delete must not follow the symlinked parent")
	}
	if a.state.Packages["pkg"] == nil {
		t.Error("exec-skip must retain state entry")
	}
	if _, err := os.Stat(a.paths.PackageFile("pkg")); err != nil {
		t.Error("exec-skip must keep the registration")
	}
}

// A plan-time policy skip (denylist) is the user's accepted policy: the package
// still unregisters cleanly, deleting the allowlisted paths.
func TestUninstallPlanSkipStillUnregisters(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	f := mkfile(t, sysroot, "usr/local/pkg/lib.so")
	mkfile(t, sysroot, "usr/local/keepme")
	// A denylisted manifest entry (/etc/passwd) is skipped at plan time.
	registerPkg(t, a, "pkg", config.Uninstall{}, []string{"etc/passwd", "usr/local/pkg/lib.so"})

	if code := a.cmdUninstall([]string{"--yes", "pkg"}); code != exitOK {
		t.Fatalf("plan-skip: exit %d, want %d", code, exitOK)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Error("allowlisted file should be deleted")
	}
	if _, err := os.Stat(a.paths.PackageFile("pkg")); !os.IsNotExist(err) {
		t.Error("plan-time skip must still allow unregistration")
	}
	if a.state.Packages["pkg"] != nil {
		t.Error("state entry should be cleared despite the plan-time skip")
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// MARKER-REMOVE: end to end — the shipped trigger file is deleted and the
// package unregisters cleanly; --purge composes with the trigger delete.
func TestUninstallMarkerRemoveEndToEnd(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	trigger := mkfile(t, sysroot, "mnt/onboard/.adds/nickelclock/uninstall")
	cfgFile := mkfile(t, sysroot, "mnt/onboard/.adds/nickelclock/settings.ini")
	registerPkg(t, a, "nickelclock", config.Uninstall{
		Method:     config.MethodMarkerRemove,
		MarkerFile: "/mnt/onboard/.adds/nickelclock/uninstall",
		PurgePaths: []string{"/mnt/onboard/.adds/nickelclock/**"},
	}, []string{"usr/local/Kobo/imageformats/libnickelclock.so"})

	if code := a.cmdUninstall([]string{"--yes", "--purge", "nickelclock"}); code != exitOK {
		t.Fatalf("exit %d, want %d", code, exitOK)
	}
	if exists(trigger) {
		t.Error("trigger file should be deleted")
	}
	if exists(cfgFile) {
		t.Error("--purge should remove the config dir alongside the trigger delete")
	}
	if _, err := os.Stat(a.paths.PackageFile("nickelclock")); !os.IsNotExist(err) {
		t.Error("registration should be removed")
	}
	if a.state.Packages["nickelclock"] != nil {
		t.Error("state entry should be cleared")
	}
}

// MARKER-REMOVE §2: an already-absent trigger file is an idempotent success —
// the package still unregisters.
func TestUninstallMarkerRemoveAbsentSucceeds(t *testing.T) {
	a, _ := newUninstallApp(t)
	registerPkg(t, a, "nickelclock", config.Uninstall{
		Method:     config.MethodMarkerRemove,
		MarkerFile: "/mnt/onboard/.adds/nickelclock/uninstall",
	}, nil)

	if code := a.cmdUninstall([]string{"--yes", "nickelclock"}); code != exitOK {
		t.Fatalf("absent trigger: exit %d, want %d", code, exitOK)
	}
	if _, err := os.Stat(a.paths.PackageFile("nickelclock")); !os.IsNotExist(err) {
		t.Error("idempotent no-op should still unregister")
	}
}

// MARKER-REMOVE §2: the plan prints "delete marker" for marker-remove and
// "create marker" for marker.
func TestPrintPlanMarkerVerbs(t *testing.T) {
	rm, err := uninstall.Compute(nil, config.Uninstall{
		Method: config.MethodMarkerRemove, MarkerFile: "/mnt/onboard/.adds/nickelclock/uninstall",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printPlan(&buf, "nickelclock", rm, false, false)
	if !strings.Contains(buf.String(), "delete marker /mnt/onboard/.adds/nickelclock/uninstall") {
		t.Errorf("marker-remove plan should print the delete verb:\n%s", buf.String())
	}

	cr, err := uninstall.Compute(nil, config.Uninstall{
		Method: config.MethodMarker, MarkerFile: "/mnt/onboard/.adds/nm/uninstall",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	printPlan(&buf, "nickelmenu", cr, false, false)
	if !strings.Contains(buf.String(), "create marker /mnt/onboard/.adds/nm/uninstall") {
		t.Errorf("marker plan should print the create verb:\n%s", buf.String())
	}
}

func TestListToleratesBadUninstallBlock(t *testing.T) {
	a, _ := newUninstallApp(t)
	// A package with an invalid method must not break list.
	registerPkg(t, a, "pkg", config.Uninstall{Method: "bogus"}, []string{"usr/local/pkg/f"})
	if code := a.cmdList(nil); code != exitOK {
		t.Errorf("cmdList with bad [uninstall] block: exit %d, want 0", code)
	}
}
