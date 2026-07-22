package uninstall

import (
	"os"
	"path/filepath"
	"testing"

	"kpm/internal/config"
	"kpm/internal/device"
)

// sandbox sets KPM_SYSROOT to a temp dir and returns (paths, sysroot).
func sandbox(t *testing.T) (device.Paths, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("KPM_SYSROOT", root)
	return device.New(), root
}

// mkfile creates a file (and parents) at the host path sysroot/rel.
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

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestExecuteSharedDirSurvival(t *testing.T) {
	p, sysroot := sandbox(t)
	// Two packages share /usr/local/Shared.
	mkfile(t, sysroot, "usr/local/Shared/a") // pkg A
	mkfile(t, sysroot, "usr/local/Shared/b") // pkg B (must survive)
	mkfile(t, sysroot, "usr/local/keepme")   // keeps /usr/local non-empty

	plan, err := Compute([]string{"usr/local/Shared/a"}, config.Uninstall{}, false)
	if err != nil {
		t.Fatal(err)
	}
	res := Execute(p, plan)
	if !res.OK() {
		t.Fatalf("unexpected failures: %+v", res.Failed)
	}
	if exists(filepath.Join(sysroot, "usr/local/Shared/a")) {
		t.Error("pkg A file should be deleted")
	}
	if !exists(filepath.Join(sysroot, "usr/local/Shared/b")) {
		t.Error("shared dir sibling (pkg B) must survive")
	}
	if !exists(filepath.Join(sysroot, "usr/local/Shared")) {
		t.Error("shared dir must survive (non-empty)")
	}
}

// B: an AppleDouble sidecar (._plugin.so) the FAT partition collected from
// Finder, left inside a package's install dir, must never block a clean
// recursive removal — it is inert metadata, removed with the tree, no policy
// skip (SELF-SOURCE §3a).
func TestExecuteRemovesAppleDoubleSidecar(t *testing.T) {
	p, sysroot := sandbox(t)
	mkfile(t, sysroot, "opt/pkg/plugin.so")
	// Finder litter alongside the real payload.
	if err := os.WriteFile(filepath.Join(sysroot, "opt/pkg/._plugin.so"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "opt/pkg/.DS_Store"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := Plan{
		Method:  config.MethodManifest,
		Deletes: []Delete{{Path: "/opt/pkg", Recursive: true}},
	}
	res := Execute(p, plan)
	if !res.OK() {
		t.Fatalf("a Finder sidecar must not block uninstall: skipped=%+v failed=%+v", res.Skipped, res.Failed)
	}
	if exists(filepath.Join(sysroot, "opt/pkg")) {
		t.Error("package dir should be fully removed (no sidecar left to keep it alive)")
	}
}

// Defense-in-depth for the CRITICAL: even if a recursive delete of a shared
// root reached Execute (Compute now refuses it), removeTree re-applies the path
// policy at every node, so a denied descendant — the firmware's own libnickel.so
// under /usr/local/Kobo — is never deleted. The Plan is built by hand to bypass
// Compute's refusal and prove the net underneath it.
func TestExecuteRecursiveNeverDeletesDeniedDescendant(t *testing.T) {
	p, sysroot := sandbox(t)
	mkfile(t, sysroot, "usr/local/Kobo/libnickel.so.1.0.0") // firmware — must survive
	mkfile(t, sysroot, "usr/local/pkg/lib.so")              // allowed — may be removed

	plan := Plan{
		Method:  config.MethodManifest,
		Deletes: []Delete{{Path: "/usr/local", Recursive: true}},
	}
	res := Execute(p, plan)

	if !exists(filepath.Join(sysroot, "usr/local/Kobo/libnickel.so.1.0.0")) {
		t.Fatal("CRITICAL: firmware libnickel.so was deleted by a recursive uninstall")
	}
	if exists(filepath.Join(sysroot, "usr/local/pkg/lib.so")) {
		t.Error("allowed file under the recursive base should have been removed")
	}
	// A denied descendant was preserved, so the walk reports a safety skip and
	// the removal is not clean (registration is kept).
	if res.OK() {
		t.Error("result should not be OK when a denied path was preserved")
	}
}

func TestExecuteDeepestFirstCleanup(t *testing.T) {
	p, sysroot := sandbox(t)
	mkfile(t, sysroot, "usr/local/Foo/sub/f")
	mkfile(t, sysroot, "usr/local/keepme") // keep /usr/local alive

	plan, err := Compute([]string{"usr/local/Foo/sub/f"}, config.Uninstall{}, false)
	if err != nil {
		t.Fatal(err)
	}
	res := Execute(p, plan)
	if !res.OK() {
		t.Fatalf("unexpected failures: %+v", res.Failed)
	}
	if exists(filepath.Join(sysroot, "usr/local/Foo/sub")) {
		t.Error("emptied /usr/local/Foo/sub should be removed")
	}
	if exists(filepath.Join(sysroot, "usr/local/Foo")) {
		t.Error("emptied /usr/local/Foo should be removed")
	}
	if !exists(filepath.Join(sysroot, "usr/local")) {
		t.Error("/usr/local must survive (keepme remains)")
	}
}

func TestExecuteMissingFileTolerated(t *testing.T) {
	p, _ := sandbox(t)
	plan, err := Compute([]string{"usr/local/pkg/ghost"}, config.Uninstall{}, false)
	if err != nil {
		t.Fatal(err)
	}
	res := Execute(p, plan)
	if !res.OK() {
		t.Fatalf("missing file must not be a failure: %+v", res.Failed)
	}
	if len(res.AlreadyGone) != 1 || res.AlreadyGone[0] != "/usr/local/pkg/ghost" {
		t.Errorf("AlreadyGone = %v", res.AlreadyGone)
	}
}

func TestExecuteRecursiveDelete(t *testing.T) {
	p, sysroot := sandbox(t)
	mkfile(t, sysroot, "opt/pkg/a")
	mkfile(t, sysroot, "opt/pkg/sub/b")
	mkfile(t, sysroot, "opt/keep") // sibling keeps /opt alive

	cfg := config.Uninstall{ExtraPaths: []string{"/opt/pkg/**"}}
	plan, err := Compute(nil, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	res := Execute(p, plan)
	if !res.OK() {
		t.Fatalf("unexpected failures: %+v", res.Failed)
	}
	if exists(filepath.Join(sysroot, "opt/pkg")) {
		t.Error("recursive delete should remove /opt/pkg entirely")
	}
	if !exists(filepath.Join(sysroot, "opt/keep")) {
		t.Error("/opt/keep sibling must survive")
	}
}

func TestExecuteSymlinkNotTraversed(t *testing.T) {
	p, sysroot := sandbox(t)
	// A directory outside the delete subtree that must survive.
	outside := filepath.Join(sysroot, "opt", "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(sysroot, "opt", "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(pkgDir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink creation unsupported on this host: %v", err)
	}

	cfg := config.Uninstall{ExtraPaths: []string{"/opt/pkg/**"}}
	plan, err := Compute(nil, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	res := Execute(p, plan)
	if !res.OK() {
		t.Fatalf("unexpected failures: %+v", res.Failed)
	}
	if exists(pkgDir) {
		t.Error("/opt/pkg should be removed")
	}
	if !exists(keep) {
		t.Error("recursive delete must NOT follow the symlink out of the subtree")
	}
}

// C1: keep_paths are honored inside a recursive delete — the review scenario.
func TestExecuteKeepInsideRecursiveDelete(t *testing.T) {
	p, sysroot := sandbox(t)
	mkfile(t, sysroot, "mnt/onboard/.adds/Foo/a.txt")
	mkfile(t, sysroot, "mnt/onboard/.adds/Foo/b.txt")
	mkfile(t, sysroot, "mnt/onboard/.adds/Foo/user.cfg")  // must survive
	mkfile(t, sysroot, "mnt/onboard/.adds/keepdir_alive") // keeps .adds non-empty

	cfg := config.Uninstall{
		ExtraPaths: []string{"/mnt/onboard/.adds/Foo/**"},
		KeepPaths:  []string{"/mnt/onboard/.adds/Foo/user.cfg"},
	}
	plan, err := Compute(nil, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	res := Execute(p, plan)
	if !res.OK() {
		t.Fatalf("unexpected failures: %+v", res.Failed)
	}
	if exists(filepath.Join(sysroot, "mnt/onboard/.adds/Foo/a.txt")) {
		t.Error("a.txt should be deleted")
	}
	if exists(filepath.Join(sysroot, "mnt/onboard/.adds/Foo/b.txt")) {
		t.Error("b.txt should be deleted")
	}
	if !exists(filepath.Join(sysroot, "mnt/onboard/.adds/Foo/user.cfg")) {
		t.Error("kept user.cfg must survive inside the recursive delete")
	}
	if !exists(filepath.Join(sysroot, "mnt/onboard/.adds/Foo")) {
		t.Error("Foo dir must survive (non-empty: holds kept user.cfg)")
	}
	if len(res.Kept) != 1 || res.Kept[0] != "/mnt/onboard/.adds/Foo/user.cfg" {
		t.Errorf("Kept = %v", res.Kept)
	}
}

// C3: an existing marker file is left untouched (never truncated).
func TestExecuteMarkerIdempotent(t *testing.T) {
	p, sysroot := sandbox(t)
	host := mkfile(t, sysroot, "mnt/onboard/.adds/nm/uninstall")
	if err := os.WriteFile(host, []byte("pre-existing content"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Uninstall{Method: config.MethodMarker, MarkerFile: "/mnt/onboard/.adds/nm/uninstall"}
	plan, err := Compute(nil, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	res := Execute(p, plan)
	if !res.OK() {
		t.Fatalf("unexpected failures: %+v", res.Failed)
	}
	b, _ := os.ReadFile(host)
	if string(b) != "pre-existing content" {
		t.Errorf("existing marker must not be truncated/rewritten, got %q", b)
	}
	if res.Marker != "/mnt/onboard/.adds/nm/uninstall" {
		t.Errorf("existing marker should count as success: %q", res.Marker)
	}
}

// C7: a single-file delete through a symlinked parent is skipped, not followed.
func TestExecuteSymlinkedParentSkipped(t *testing.T) {
	p, sysroot := sandbox(t)
	// The real directory the symlink points at — its contents must survive.
	real := filepath.Join(sysroot, "opt", "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(real, "secret")
	if err := os.WriteFile(secret, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	optPkg := filepath.Join(sysroot, "opt", "pkg")
	if err := os.MkdirAll(optPkg, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(optPkg, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink creation unsupported on this host: %v", err)
	}
	// Manifest points a single-file delete through the symlinked parent.
	plan, err := Compute([]string{"opt/pkg/link/secret"}, config.Uninstall{}, false)
	if err != nil {
		t.Fatal(err)
	}
	res := Execute(p, plan)
	if !exists(secret) {
		t.Error("delete must not follow the symlinked parent out to /opt/real/secret")
	}
	if len(res.Skipped) != 1 {
		t.Errorf("expected a symlinked-parent skip, got %+v", res.Skipped)
	}
}

// MARKER-REMOVE §2: the shipped trigger file is deleted; its absence makes the
// package remove itself on the next boot.
func TestExecuteMarkerRemoveDeletes(t *testing.T) {
	p, sysroot := sandbox(t)
	host := mkfile(t, sysroot, "mnt/onboard/.adds/nickelclock/uninstall")
	cfg := config.Uninstall{Method: config.MethodMarkerRemove, MarkerFile: "/mnt/onboard/.adds/nickelclock/uninstall"}
	plan, err := Compute(nil, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	res := Execute(p, plan)
	if !res.OK() {
		t.Fatalf("unexpected failures: %+v", res.Failed)
	}
	if exists(host) {
		t.Error("trigger file should be deleted")
	}
	if res.Marker != "/mnt/onboard/.adds/nickelclock/uninstall" {
		t.Errorf("Marker = %q", res.Marker)
	}
}

// MARKER-REMOVE §2/§4.2: an already-absent trigger file is an idempotent
// success (the package is already uninstalling or was removed by hand).
func TestExecuteMarkerRemoveAlreadyAbsent(t *testing.T) {
	p, _ := sandbox(t)
	cfg := config.Uninstall{Method: config.MethodMarkerRemove, MarkerFile: "/mnt/onboard/.adds/nickelclock/uninstall"}
	plan, err := Compute(nil, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	res := Execute(p, plan)
	if !res.OK() {
		t.Fatalf("absent trigger must be a no-op success: %+v", res.Failed)
	}
	if res.Marker != "" {
		t.Errorf("no marker was deleted, Marker = %q", res.Marker)
	}
	if len(res.AlreadyGone) != 1 || res.AlreadyGone[0] != "/mnt/onboard/.adds/nickelclock/uninstall" {
		t.Errorf("AlreadyGone = %v", res.AlreadyGone)
	}
}

// MARKER-REMOVE §2/§4.3: a directory at the marker path is an error (mirror of
// the marker method's dir-in-the-way rule).
func TestExecuteMarkerRemoveDirInTheWay(t *testing.T) {
	p, sysroot := sandbox(t)
	dir := filepath.Join(sysroot, filepath.FromSlash("mnt/onboard/.adds/nickelclock/uninstall"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Uninstall{Method: config.MethodMarkerRemove, MarkerFile: "/mnt/onboard/.adds/nickelclock/uninstall"}
	plan, err := Compute(nil, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	res := Execute(p, plan)
	if res.OK() || len(res.Failed) != 1 {
		t.Fatalf("directory at the marker path must fail: %+v", res)
	}
	if !exists(dir) {
		t.Error("the directory must be left in place")
	}
}

func TestExecuteMarkerCreation(t *testing.T) {
	p, sysroot := sandbox(t)
	cfg := config.Uninstall{Method: config.MethodMarker, MarkerFile: "/mnt/onboard/.adds/nm/uninstall"}
	plan, err := Compute(nil, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	res := Execute(p, plan)
	if !res.OK() {
		t.Fatalf("unexpected failures: %+v", res.Failed)
	}
	host := filepath.Join(sysroot, filepath.FromSlash("mnt/onboard/.adds/nm/uninstall"))
	b, err := os.ReadFile(host)
	if err != nil {
		t.Fatalf("marker not created: %v", err)
	}
	if len(b) == 0 {
		t.Error("marker file is empty")
	}
}
