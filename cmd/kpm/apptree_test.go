package main

// apptree_test.go drives app-tree installs end to end (APP-TREE.md §13): a real
// forge fixture serving a real zip, through the real cmdUpdate, into a
// KPM_ROOT/KPM_SYSROOT sandbox. The invariant under test throughout is that an
// app-tree package is installed the moment `update` returns — it never sets a
// staged version, never writes KoboRoot.tgz, and never asks for a reboot.

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"kpm/internal/apptree"
	"kpm/internal/config"
	"kpm/internal/forge"
	"kpm/internal/state"
)

// zipAsset builds a release zip: files maps entry name -> content, dirs are
// explicit (possibly empty) directory entries.
func zipAsset(t *testing.T, files map[string]string, dirs ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, d := range dirs {
		if _, err := zw.Create(strings.TrimSuffix(d, "/") + "/"); err != nil {
			t.Fatal(err)
		}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[n])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// mixedServer is releaseServer's app-tree-aware sibling: a repo named in zips
// publishes a single <repo>-app.zip asset, every other repo publishes the usual
// KoboRoot.tgz, so one server can drive a mixed `update --all`.
func mixedServer(t *testing.T, tag string, zips map[string][]byte) *httptest.Server {
	t.Helper()
	tgzBody := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/pkg/file": "payload"})
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/releases/latest"):
			parts := strings.Split(r.URL.Path, "/")
			repo := ""
			for i, p := range parts {
				if p == "repos" && i+2 < len(parts) {
					repo = parts[i+2]
				}
			}
			if z, ok := zips[repo]; ok {
				fmt.Fprintf(w, `{"tag_name":%q,"draft":false,"prerelease":false,`+
					`"assets":[{"name":%q,"size":%d,"browser_download_url":%q}]}`,
					tag, repo+"-app.zip", len(z), srv.URL+"/download/"+repo+"/asset")
				return
			}
			fmt.Fprintf(w, `{"tag_name":%q,"draft":false,"prerelease":false,`+
				`"assets":[{"name":"KoboRoot.tgz","size":%d,"browser_download_url":%q}]}`,
				tag, len(tgzBody), srv.URL+"/download/"+repo+"/asset")
		case strings.HasPrefix(r.URL.Path, "/download/"):
			repo := strings.Split(strings.TrimPrefix(r.URL.Path, "/download/"), "/")[0]
			if z, ok := zips[repo]; ok {
				w.Write(z)
				return
			}
			w.Write(tgzBody)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// appTreePkg registers an app-tree package def in packages.d.
func appTreePkg(t *testing.T, a *App, host, id, root, dest string, launcher *config.Launcher) {
	t.Helper()
	pkg := &config.Package{
		Name: id, Source: host + "/o/" + id, Forge: config.ForgeForgejo,
		Asset: id + "-app.zip",
		Install: &config.Install{
			Method: config.MethodAppTree, Root: root, Dest: dest, Launcher: launcher,
		},
		Uninstall: config.Uninstall{
			Method: config.MethodManifest, PurgePaths: []string{dest + "/**"},
		},
	}
	if err := config.Save(a.paths.PackageFile(id), pkg); err != nil {
		t.Fatal(err)
	}
}

// appTreeApp is newUninstallApp plus a wired-up forge client.
func appTreeApp(t *testing.T, zips map[string][]byte) (*App, string, string) {
	t.Helper()
	a, sysroot := newUninstallApp(t)
	setVersion(t, "0.11.0")
	srv := mixedServer(t, "v1", zips)
	a.client = forge.NewClientWithHTTP(srv.Client())
	return a, sysroot, strings.TrimPrefix(srv.URL, "https://")
}

func hostFile(t *testing.T, sysroot, devAbs string) string {
	t.Helper()
	return filepath.Join(sysroot, filepath.FromSlash(strings.TrimPrefix(devAbs, "/")))
}

func readDev(t *testing.T, sysroot, devAbs string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(hostFile(t, sysroot, devAbs))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func logText(t *testing.T, a *App) string {
	t.Helper()
	b, _ := os.ReadFile(a.paths.LogFile())
	return string(b)
}

// ---- 1. no staging, no reboot -------------------------------------------

func TestAppTreeInstallNoStaging(t *testing.T) {
	z := zipAsset(t, map[string]string{
		"koreader/reader.lua":         "v1-reader",
		"koreader/plugins/x/main.lua": "v1-plugin",
	})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"koreader": z})
	appTreePkg(t, a, host, "koreader", "koreader", "/mnt/onboard/.adds/koreader", nil)

	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("update exit = %d, want %d", code, exitOK)
	}
	ps := a.state.Get("koreader")
	if ps.InstalledVersion != "v1" {
		t.Errorf("installed_version = %q, want v1", ps.InstalledVersion)
	}
	if ps.StagedVersion != "" {
		t.Errorf("staged_version = %q — an app-tree package must NEVER stage", ps.StagedVersion)
	}
	if len(ps.Manifest) == 0 {
		t.Error("manifest must be recorded so uninstall works")
	}
	if a.state.StagedSHA256 != "" || a.state.StagedSize != 0 || a.state.StagedCommitted {
		t.Errorf("the staging identity must be untouched: sha=%q size=%d committed=%v",
			a.state.StagedSHA256, a.state.StagedSize, a.state.StagedCommitted)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err == nil {
		t.Error("no KoboRoot.tgz may be written for an app-tree install")
	}
	if got, ok := readDev(t, sysroot, "/mnt/onboard/.adds/koreader/reader.lua"); !ok || got != "v1-reader" {
		t.Errorf("tree not extracted: %q %v", got, ok)
	}
	lg := logText(t, a)
	if !strings.Contains(lg, "INSTALLED") {
		t.Errorf("log must record INSTALLED:\n%s", lg)
	}
	if strings.Contains(lg, "STAGE ") {
		t.Errorf("log must NOT record STAGE:\n%s", lg)
	}
	// The cached zip is cleared once it has been extracted.
	ents, _ := os.ReadDir(a.paths.CacheDir())
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".zip") {
			t.Errorf("cached zip %s should be removed after a successful install", e.Name())
		}
	}
}

// TestAppTreeReconcileIgnoresAppTree: PromoteStaged/RollbackStaged both iterate
// on StagedVersion != "", so an app-tree package is invisible to reconcile —
// which is what lets internal/state stay completely unchanged.
func TestAppTreeReconcileIgnoresAppTree(t *testing.T) {
	z := zipAsset(t, map[string]string{"koreader/reader.lua": "v1"})
	a, _, host := appTreeApp(t, map[string][]byte{"koreader": z})
	appTreePkg(t, a, host, "koreader", "koreader", "/mnt/onboard/.adds/koreader", nil)
	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("update exit = %d", code)
	}
	before := *a.state.Get("koreader")

	// A later mutating command runs reconcile; the app-tree package must be
	// neither promoted nor rolled back.
	if err := a.reconcile(); err != nil {
		t.Fatal(err)
	}
	after := a.state.Get("koreader")
	if after.InstalledVersion != before.InstalledVersion || after.StagedVersion != "" {
		t.Errorf("reconcile disturbed an app-tree package: %+v -> %+v", before, after)
	}
	if len(after.Manifest) != len(before.Manifest) {
		t.Error("reconcile must not touch an app-tree package's manifest")
	}
}

// ---- 2. update semantics ------------------------------------------------

func TestAppTreeUpdatePrunesStaleFiles(t *testing.T) {
	v1 := zipAsset(t, map[string]string{"r/a": "1a", "r/b": "1b", "r/c": "1c"})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"koreader": v1})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("v1 update exit = %d", code)
	}

	// v2 drops c and rewrites a/b.
	v2 := zipAsset(t, map[string]string{"r/a": "2a", "r/b": "2b"})
	srv := mixedServer(t, "v2", map[string][]byte{"koreader": v2})
	a.client = forge.NewClientWithHTTP(srv.Client())
	newHost := strings.TrimPrefix(srv.URL, "https://")
	appTreePkg(t, a, newHost, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	a.state.Get("koreader").LastChecked = "" // force a fresh resolve

	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("v2 update exit = %d", code)
	}
	if _, ok := readDev(t, sysroot, "/mnt/onboard/.adds/koreader/c"); ok {
		t.Error("a file the new version dropped must be pruned")
	}
	for name, want := range map[string]string{"a": "2a", "b": "2b"} {
		if got, ok := readDev(t, sysroot, "/mnt/onboard/.adds/koreader/"+name); !ok || got != want {
			t.Errorf("%s = %q (%v), want %q", name, got, ok, want)
		}
	}
	man := strings.Join(a.state.Get("koreader").Manifest, ",")
	if strings.Contains(man, "/c") {
		t.Errorf("manifest still lists the dropped file: %s", man)
	}
}

func TestAppTreeUpdatePreservesUserData(t *testing.T) {
	v1 := zipAsset(t, map[string]string{"r/reader.lua": "1"})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"koreader": v1})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("v1 exit = %d", code)
	}
	// The user's own settings, created inside dest by the app itself.
	userFile := hostFile(t, sysroot, "/mnt/onboard/.adds/koreader/settings/foo.lua")
	if err := os.MkdirAll(filepath.Dir(userFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userFile, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	v2 := zipAsset(t, map[string]string{"r/reader.lua": "2"})
	srv := mixedServer(t, "v2", map[string][]byte{"koreader": v2})
	a.client = forge.NewClientWithHTTP(srv.Client())
	appTreePkg(t, a, strings.TrimPrefix(srv.URL, "https://"), "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	a.state.Get("koreader").LastChecked = ""
	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("v2 exit = %d", code)
	}

	if b, err := os.ReadFile(userFile); err != nil || string(b) != "mine" {
		t.Errorf("user data inside dest must survive an update: %q %v", b, err)
	}
	for _, m := range a.state.Get("koreader").Manifest {
		if strings.Contains(m, "settings/foo.lua") {
			t.Error("a file kpm never installed must never enter the manifest")
		}
	}
}

// TestAppTreeUpdatePreservesUserDataUnderShippedDir is the case a directory-based
// preserve list gets wrong: the user's file sits INSIDE a directory the zip also
// ships, so only "delete exactly what I installed" gets it right.
func TestAppTreeUpdatePreservesUserDataUnderShippedDir(t *testing.T) {
	v1 := zipAsset(t, map[string]string{"r/plugins/ships.koplugin/main.lua": "1"})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"koreader": v1})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("v1 exit = %d", code)
	}
	mine := hostFile(t, sysroot, "/mnt/onboard/.adds/koreader/plugins/mine.koplugin/main.lua")
	if err := os.MkdirAll(filepath.Dir(mine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("user plugin"), 0o644); err != nil {
		t.Fatal(err)
	}

	// v2 drops the shipped plugin entirely — the user's must still survive.
	v2 := zipAsset(t, map[string]string{"r/reader.lua": "2"})
	srv := mixedServer(t, "v2", map[string][]byte{"koreader": v2})
	a.client = forge.NewClientWithHTTP(srv.Client())
	appTreePkg(t, a, strings.TrimPrefix(srv.URL, "https://"), "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	a.state.Get("koreader").LastChecked = ""
	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("v2 exit = %d", code)
	}
	if b, err := os.ReadFile(mine); err != nil || string(b) != "user plugin" {
		t.Errorf("a user plugin under a shipped directory must survive: %q %v", b, err)
	}
	if _, ok := readDev(t, sysroot, "/mnt/onboard/.adds/koreader/plugins/ships.koplugin/main.lua"); ok {
		t.Error("the shipped plugin the new version dropped must be pruned")
	}
}

// TestAppTreeCrashResumeIsIdempotent simulates a crash after extraction but
// before state.Save: installed_version stays at the old value, so the next
// update re-extracts and prunes against the still-correct OLD manifest.
func TestAppTreeCrashResumeIsIdempotent(t *testing.T) {
	v1 := zipAsset(t, map[string]string{"r/a": "1a", "r/c": "1c"})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"koreader": v1})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("v1 exit = %d", code)
	}
	v1Manifest := append([]string(nil), a.state.Get("koreader").Manifest...)

	// Simulate the crash window: v2's files are already on disk (extraction
	// finished) but state still describes v1.
	v2 := zipAsset(t, map[string]string{"r/a": "2a", "r/b": "2b"})
	if err := os.WriteFile(hostFile(t, sysroot, "/mnt/onboard/.adds/koreader/a"), []byte("2a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostFile(t, sysroot, "/mnt/onboard/.adds/koreader/b"), []byte("2b"), 0o644); err != nil {
		t.Fatal(err)
	}
	ps := a.state.Get("koreader")
	ps.InstalledVersion, ps.Manifest = "v1", v1Manifest
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	srv := mixedServer(t, "v2", map[string][]byte{"koreader": v2})
	a.client = forge.NewClientWithHTTP(srv.Client())
	appTreePkg(t, a, strings.TrimPrefix(srv.URL, "https://"), "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	a.state.Get("koreader").LastChecked = ""
	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("resume exit = %d", code)
	}
	if got, _ := readDev(t, sysroot, "/mnt/onboard/.adds/koreader/a"); got != "2a" {
		t.Errorf("a = %q, want 2a", got)
	}
	if got, _ := readDev(t, sysroot, "/mnt/onboard/.adds/koreader/b"); got != "2b" {
		t.Errorf("b = %q, want 2b", got)
	}
	if _, ok := readDev(t, sysroot, "/mnt/onboard/.adds/koreader/c"); ok {
		t.Error("the resumed run must still prune v1's dropped file")
	}
	if a.state.Get("koreader").InstalledVersion != "v2" {
		t.Errorf("installed_version = %q, want v2", a.state.Get("koreader").InstalledVersion)
	}
}

// ---- 3. reboot semantics ------------------------------------------------

// TestAppTreeRebootFlagDoesNotReboot: the NickelMenu "Update all" entry always
// passes --reboot, so an app-tree-only run must complete WITHOUT rebooting.
// device.Reboot is always preceded by a REBOOT log line, so its absence is an
// exact proxy for "not called" (and on this host Reboot would fail, so a call
// would also flip the exit code).
func TestAppTreeRebootFlagDoesNotReboot(t *testing.T) {
	z := zipAsset(t, map[string]string{"r/a": "1"})
	a, _, host := appTreeApp(t, map[string][]byte{"koreader": z})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)

	if code := a.cmdUpdate([]string{"koreader", "--reboot"}); code != exitOK {
		t.Fatalf("update --reboot exit = %d, want %d (an app-tree run must not reboot)", code, exitOK)
	}
	if lg := logText(t, a); strings.Contains(lg, "REBOOT") {
		t.Errorf("an app-tree-only run must not reboot:\n%s", lg)
	}
	if a.state.Get("koreader").InstalledVersion != "v1" {
		t.Error("the package must still be installed")
	}
}

// TestAppTreeMixedUpdateAll is the normal case once this ships: one package of
// each shape in one run.
func TestAppTreeMixedUpdateAll(t *testing.T) {
	z := zipAsset(t, map[string]string{"r/a": "1"})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"koreader": z})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	fakeForgePkg(t, a, host, "nickelclock", "")

	out := captureStdout(t, func() {
		if code := a.cmdUpdate([]string{"--all"}); code != exitOK {
			t.Fatalf("mixed update exit = %d", code)
		}
	})
	// The tree is extracted AND a tgz is staged, in one run.
	if got, _ := readDev(t, sysroot, "/mnt/onboard/.adds/koreader/a"); got != "1" {
		t.Errorf("app-tree package not extracted: %q", got)
	}
	if a.state.Get("koreader").StagedVersion != "" {
		t.Error("the app-tree package must not stage")
	}
	if a.state.Get("nickelclock").StagedVersion != "v1" {
		t.Error("the tgz package must stage")
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err != nil {
		t.Error("the merged KoboRoot.tgz must exist for the tgz package")
	}
	if !strings.Contains(out, "1 package(s) installed, 1 staged — reboot to install") {
		t.Errorf("human tail wrong:\n%s", out)
	}
}

// TestAppTreeMixedOrdering: app-tree installs complete BEFORE the merge, so a
// merge failure cannot strand a package that was going to succeed. The merge is
// made to fail by planting a foreign KoboRoot.tgz, which guardExistingTgz refuses.
func TestAppTreeMixedOrdering(t *testing.T) {
	z := zipAsset(t, map[string]string{"r/a": "1"})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"koreader": z})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	fakeForgePkg(t, a, host, "nickelclock", "")
	// A tgz kpm did not stage: the guard refuses to overwrite it, so the merge
	// fails before anything is staged.
	if err := os.WriteFile(a.paths.StagedTgz(), []byte("foreign"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := a.cmdUpdate([]string{"--all"}); code != exitError {
		t.Fatalf("the merge must fail, exit = %d", code)
	}
	// ...and the app-tree package is nonetheless fully installed and recorded.
	if got, _ := readDev(t, sysroot, "/mnt/onboard/.adds/koreader/a"); got != "1" {
		t.Error("a merge failure must not strand the app-tree extraction")
	}
	if a.state.Get("koreader").InstalledVersion != "v1" {
		t.Error("the app-tree install must be recorded before the merge is attempted")
	}
	if a.state.Get("nickelclock").StagedVersion != "" {
		t.Error("the tgz package must not be recorded as staged when the merge failed")
	}
}

// ---- 4. uninstall -------------------------------------------------------

func TestAppTreeUninstallRemovesTree(t *testing.T) {
	z := zipAsset(t, map[string]string{"r/a": "1", "r/sub/b": "2"})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"koreader": z})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("update exit = %d", code)
	}
	userFile := hostFile(t, sysroot, "/mnt/onboard/.adds/koreader/settings.lua")
	if err := os.WriteFile(userFile, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := a.cmdUninstall([]string{"koreader", "--yes"}); code != exitOK {
		t.Fatalf("uninstall exit = %d", code)
	}
	if _, ok := readDev(t, sysroot, "/mnt/onboard/.adds/koreader/a"); ok {
		t.Error("installed files must be removed")
	}
	if b, err := os.ReadFile(userFile); err != nil || string(b) != "mine" {
		t.Errorf("user data must survive a plain uninstall: %q %v", b, err)
	}
	if _, err := os.Stat(hostFile(t, sysroot, "/mnt/onboard/.adds/koreader")); err != nil {
		t.Error("dest must remain while user data is still in it")
	}
}

func TestAppTreeUninstallPurgeRemovesDest(t *testing.T) {
	z := zipAsset(t, map[string]string{"r/a": "1"})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"koreader": z})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("update exit = %d", code)
	}
	if err := os.WriteFile(hostFile(t, sysroot, "/mnt/onboard/.adds/koreader/settings.lua"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := a.cmdUninstall([]string{"koreader", "--yes", "--purge"}); code != exitOK {
		t.Fatalf("purge exit = %d", code)
	}
	if _, err := os.Stat(hostFile(t, sysroot, "/mnt/onboard/.adds/koreader")); err == nil {
		t.Error("--purge must remove dest entirely")
	}
	// The shared .adds root must survive.
	if _, err := os.Stat(hostFile(t, sysroot, "/mnt/onboard/.adds")); err != nil {
		t.Error("the shared .adds root must never be removed")
	}
}

func TestAppTreeUninstallRemovesNmEntry(t *testing.T) {
	z := zipAsset(t, map[string]string{"r/a": "1"})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"koreader": z})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", &config.Launcher{
		NmFile:  "/mnt/onboard/.adds/nm/koreader",
		NmEntry: "menu_item:main:KOReader:cmd_spawn:quiet:exec /mnt/onboard/.adds/koreader/koreader.sh",
	})
	// Another package's drop-in shares the directory and must survive.
	other := hostFile(t, sysroot, "/mnt/onboard/.adds/nm/other")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("menu_item:main:Other:cmd_spawn:x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("update exit = %d", code)
	}
	body, ok := readDev(t, sysroot, "/mnt/onboard/.adds/nm/koreader")
	if !ok {
		t.Fatal("the launcher drop-in must be written")
	}
	if !strings.HasSuffix(body, "\n") || strings.Contains(body, "\r") {
		t.Errorf("the drop-in must be LF with exactly one trailing newline: %q", body)
	}
	inManifest := false
	for _, m := range a.state.Get("koreader").Manifest {
		if m == "mnt/onboard/.adds/nm/koreader" {
			inManifest = true
		}
	}
	if !inManifest {
		t.Errorf("the drop-in must be in the manifest so uninstall removes it: %v",
			a.state.Get("koreader").Manifest)
	}

	if code := a.cmdUninstall([]string{"koreader", "--yes"}); code != exitOK {
		t.Fatalf("uninstall exit = %d", code)
	}
	if _, ok := readDev(t, sysroot, "/mnt/onboard/.adds/nm/koreader"); ok {
		t.Error("the drop-in must be removed by uninstall")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error(".adds/nm is shared — another package's drop-in must survive")
	}
}

// ---- 5. launcher --------------------------------------------------------

func TestAppTreeLauncherNotOverwritten(t *testing.T) {
	z := zipAsset(t, map[string]string{"r/a": "1"})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"koreader": z})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", &config.Launcher{
		NmFile:  "/mnt/onboard/.adds/nm/koreader",
		NmEntry: "menu_item:main:KOReader:cmd_spawn:quiet:exec /mnt/onboard/.adds/koreader/koreader.sh",
	})
	// The user has customised their entry.
	custom := "menu_item:reader:My KOReader:cmd_spawn:quiet:exec /mnt/onboard/.adds/koreader/koreader.sh\n"
	nm := hostFile(t, sysroot, "/mnt/onboard/.adds/nm/koreader")
	if err := os.MkdirAll(filepath.Dir(nm), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nm, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("update exit = %d", code)
	}
	if b, _ := os.ReadFile(nm); string(b) != custom {
		t.Errorf("an existing drop-in must be left byte-identical, got %q", b)
	}

	// --force-launcher rewrites it.
	a.state.Get("koreader").LastChecked = ""
	a.state.Get("koreader").InstalledVersion = "" // force a re-install of the same tag
	if code := a.cmdUpdate([]string{"koreader", "--force-launcher"}); code != exitOK {
		t.Fatalf("forced update exit = %d", code)
	}
	if b, _ := os.ReadFile(nm); string(b) == custom {
		t.Error("--force-launcher must rewrite the drop-in")
	}
}

func TestAppTreeLauncherWarnsWithoutNickelMenu(t *testing.T) {
	z := zipAsset(t, map[string]string{"r/a": "1"})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"koreader": z})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", &config.Launcher{
		NmFile:  "/mnt/onboard/.adds/nm/koreader",
		NmEntry: "menu_item:main:KOReader:cmd_spawn:quiet:exec /mnt/onboard/.adds/koreader/koreader.sh",
	})

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"koreader"}) })
	if code != exitOK {
		t.Fatalf("a missing NickelMenu is a WARNING, not a failure: exit %d", code)
	}
	if _, ok := readDev(t, sysroot, "/mnt/onboard/.adds/nm/koreader"); !ok {
		t.Error("the drop-in is written anyway — it is inert without NickelMenu and becomes live when it arrives")
	}
	if !strings.Contains(out, "NickelMenu not detected") {
		t.Errorf("the warning must be printed:\n%s", out)
	}
	if !strings.Contains(logText(t, a), "NickelMenu not detected") {
		t.Error("the warning must reach kpm.log")
	}
	if !strings.Contains(a.paths.ReadStatus(), "NickelMenu not detected") {
		t.Errorf("the warning must reach the status file:\n%s", a.paths.ReadStatus())
	}

	// With NickelMenu's payload present, there is nothing to warn about.
	so := hostFile(t, sysroot, "/usr/local/Kobo/imageformats/libnm.so")
	if err := os.MkdirAll(filepath.Dir(so), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(so, []byte("so"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.state.Get("koreader").InstalledVersion = ""
	a.state.Get("koreader").LastChecked = ""
	out = captureStdout(t, func() { code = a.cmdUpdate([]string{"koreader"}) })
	if strings.Contains(out, "NickelMenu not detected") {
		t.Errorf("no warning when NickelMenu is present:\n%s", out)
	}
}

// TestAppTreeLauncherRefusesKpmDropIn: no package but kpm may claim kpm's own
// NickelMenu entry — overwriting it would replace every kpm action on the device.
func TestAppTreeLauncherRefusesKpmDropIn(t *testing.T) {
	z := zipAsset(t, map[string]string{"r/a": "1"})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"evil": z})
	appTreePkg(t, a, host, "evil", "r", "/mnt/onboard/.adds/evil", &config.Launcher{
		NmFile:  "/mnt/onboard/.adds/nm/kpm",
		NmEntry: "menu_item:main:Pwned:cmd_spawn:x",
	})
	if code := a.cmdUpdate([]string{"evil"}); code != exitOK {
		t.Fatalf("the install still succeeds; only the launcher is refused: exit %d", code)
	}
	if _, ok := readDev(t, sysroot, "/mnt/onboard/.adds/nm/kpm"); ok {
		t.Fatal("a package must never be able to write kpm's own NickelMenu drop-in")
	}
	if !strings.Contains(logText(t, a), "kpm's own NickelMenu drop-in") {
		t.Errorf("the refusal must be logged:\n%s", logText(t, a))
	}
}

// ---- 6. status wording --------------------------------------------------

func TestAppTreeStatusNoRebootWording(t *testing.T) {
	a, _ := newUninstallApp(t)
	setVersion(t, "0.11.0")
	appTreePkg(t, a, "example.com", "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	fakeForgePkg(t, a, "example.com", "nickelclock", "v1")

	kor, err := a.loadPackage("koreader")
	if err != nil {
		t.Fatal(err)
	}
	clock, err := a.loadPackage("nickelclock")
	if err != nil {
		t.Fatal(err)
	}
	// An update is available for the app-tree package only.
	a.state.Get("koreader").InstalledVersion = "v1"
	a.state.Get("koreader").LatestSeen = "v2"

	if got := a.buildStatus([]*config.Package{kor}); !strings.Contains(got, "(no reboot)") {
		t.Errorf("an app-tree-only pending update must not claim a reboot:\n%s", got)
	}
	// Add a tgz package with a pending update: the reboot claim comes back.
	a.state.Get("nickelclock").LatestSeen = "v2"
	if got := a.buildStatus([]*config.Package{kor, clock}); !strings.Contains(got, "(reboots)") {
		t.Errorf("a mixed pending set must keep the reboot claim:\n%s", got)
	}
}

// ---- 7. contract --------------------------------------------------------

// TestMinKpmGatesAppTree: an app-tree def carries min_kpm = "0.11.0", so a 0.10.0
// kpm refuses it up front instead of downloading a zip it cannot unwrap.
func TestMinKpmGatesAppTree(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	seedRegistry(t, a, "main", `schema_version = 1

[packages.koreader]
name = "KOReader"
source = "github.com/koreader/koreader"
forge = "github"
asset = "koreader-kobo-*.zip"
min_kpm = "0.11.0"

  [packages.koreader.install]
  method = "app-tree"
  root = "koreader"
  dest = "/mnt/onboard/.adds/koreader"
`)
	errOut := captureStderr(t, func() {
		if code := a.cmdInstall([]string{"koreader", "--yes"}); code != exitError {
			t.Errorf("install exit = %d, want %d", code, exitError)
		}
	})
	want := `"koreader" requires kpm >= 0.11.0 (running 0.10.0)`
	if !strings.Contains(errOut, want) {
		t.Errorf("stderr = %q, want it to contain %q", errOut, want)
	}
	if _, err := os.Stat(a.paths.PackageFile("koreader")); err == nil {
		t.Error("no def may be written when min_kpm gates the install")
	}
}

// TestInstallDefRoundTrip: the [install] block survives registry -> packages.d
// -> registry, SaveReplace removes a block the registry dropped, and unknown
// keys inside [install] are preserved.
func TestInstallDefRoundTrip(t *testing.T) {
	a := newTestApp(t)
	path := a.paths.PackageFile("koreader")
	pkg := &config.Package{
		ID: "koreader", Name: "KOReader", Source: "github.com/koreader/koreader",
		Forge: "github", Asset: "koreader-kobo-*.zip",
		Install: &config.Install{
			Method: config.MethodAppTree, Root: "koreader", Dest: "/mnt/onboard/.adds/koreader",
			Launcher: &config.Launcher{
				NmFile:  "/mnt/onboard/.adds/nm/koreader",
				NmEntry: "menu_item:main:KOReader:cmd_spawn:quiet:exec /mnt/onboard/.adds/koreader/koreader.sh",
			},
		},
	}
	if err := config.SaveReplace(path, pkg); err != nil {
		t.Fatal(err)
	}
	back, err := config.Load(path, "koreader")
	if err != nil {
		t.Fatal(err)
	}
	if back.Install == nil || back.Install.Dest != pkg.Install.Dest || back.Install.Root != "koreader" {
		t.Fatalf("install block did not round-trip: %+v", back.Install)
	}
	if back.Install.Launcher == nil || back.Install.Launcher.NmFile != pkg.Install.Launcher.NmFile {
		t.Fatalf("launcher did not round-trip: %+v", back.Install.Launcher)
	}
	if !back.IsAppTree() {
		t.Error("IsAppTree must be true after a round trip")
	}

	// An unknown key inside [install] (a future field) survives a rewrite.
	raw, _ := os.ReadFile(path)
	withUnknown := strings.Replace(string(raw), "[install]", "[install]\n  future_key = \"keep me\"", 1)
	if err := os.WriteFile(path, []byte(withUnknown), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveReplace(path, pkg); err != nil {
		t.Fatal(err)
	}
	if raw, _ = os.ReadFile(path); !strings.Contains(string(raw), "keep me") {
		t.Errorf("an unknown key inside [install] must be preserved:\n%s", raw)
	}

	// A registry that DROPS the block removes it locally on the next sync.
	pkg.Install = nil
	if err := config.SaveReplace(path, pkg); err != nil {
		t.Fatal(err)
	}
	back, err = config.Load(path, "koreader")
	if err != nil {
		t.Fatal(err)
	}
	if back.Install != nil && back.Install.Method != "" {
		t.Errorf("a dropped [install] must not persist locally: %+v", back.Install)
	}
	if back.IsAppTree() {
		t.Error("a package with no install block is tgz")
	}
	// A package with NO install block never grows an empty table.
	plain := &config.Package{ID: "x", Name: "x", Source: "h/o/r", Forge: "github", Asset: "a.tgz"}
	p2 := a.paths.PackageFile("x")
	if err := config.SaveReplace(p2, plain); err != nil {
		t.Fatal(err)
	}
	if raw, _ = os.ReadFile(p2); strings.Contains(string(raw), "[install]") {
		t.Errorf("a def with no install block must not emit one:\n%s", raw)
	}
}

// TestMatchAssetKoreaderGlob: the hyphen after "kobo" is load-bearing. Verified
// against the real 23-asset list of KOReader v2026.07.
func TestMatchAssetKoreaderGlob(t *testing.T) {
	names := []string{
		"koreader-android-arm64-v2026.07.apk", "koreader-android-arm-v2026.07.apk",
		"koreader-appimage-x86_64-v2026.07.AppImage", "koreader-cervantes-v2026.07.zip",
		"koreader-debian-x86_64-v2026.07.deb", "koreader-kindle-v2026.07.zip",
		"koreader-kindlepw2-v2026.07.zip", "koreader-kobo-v2026.07.zip",
		"koreader-kobov5-v2026.07.zip", "koreader-macos-v2026.07.dmg",
		"koreader-pocketbook-v2026.07.zip", "koreader-remarkable-v2026.07.zip",
		"koreader-sony-prstux-v2026.07.zip", "koreader-ubuntu-touch-v2026.07.click",
	}
	rel := forge.Release{Tag: "v2026.07"}
	for _, n := range names {
		rel.Assets = append(rel.Assets, forge.Asset{Name: n, DownloadURL: "https://x/" + n})
	}
	got, err := rel.MatchAsset("koreader-kobo-*.zip")
	if err != nil {
		t.Fatalf("the hyphenated glob must match exactly one: %v", err)
	}
	if got.Name != "koreader-kobo-v2026.07.zip" {
		t.Errorf("matched %q", got.Name)
	}
	// Drop the hyphen and it becomes ambiguous — kpm refuses rather than guessing
	// which of the platform builds the user meant.
	if _, err := rel.MatchAsset("koreader-kobo*.zip"); err == nil {
		t.Error("the un-hyphenated glob is ambiguous and must error")
	} else if !strings.Contains(err.Error(), "narrow the pattern") {
		t.Errorf("error = %q, want the narrow-the-pattern refusal", err)
	}
}

// TestAppTreeStateHasNoNewFields: state.json gains nothing for app-tree — the
// strongest evidence the model fits (§12.4).
func TestAppTreeStateHasNoNewFields(t *testing.T) {
	z := zipAsset(t, map[string]string{"r/a": "1"})
	a, _, host := appTreeApp(t, map[string][]byte{"koreader": z})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("update exit = %d", code)
	}
	raw, err := os.ReadFile(a.paths.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"install_method", "app_tree", "apptree", "dest"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("state.json must gain no new field, found %q:\n%s", forbidden, raw)
		}
	}
	ps := a.state.Get("koreader")
	if ps.VerifiedAt == "" {
		t.Error("verified_at must be stamped, so the hook's files-missing badge works")
	}
	if len(ps.MissingFiles) != 0 {
		t.Errorf("a freshly-extracted tree must verify clean, missing: %v", ps.MissingFiles)
	}
	_ = state.TimeFormat
}

// TestPrintInstallDefShowsInstall: the confirm pause is where the user should see
// where kpm will write bulk payload as root, so the [install] block is printed
// above the uninstall recipe (§12.2).
func TestPrintInstallDefShowsInstall(t *testing.T) {
	pkg := &config.Package{
		ID: "koreader", Name: "KOReader", Source: "github.com/koreader/koreader",
		Forge: "github", Asset: "koreader-kobo-*.zip", MinKpm: "0.11.0",
		Install: &config.Install{
			Method: config.MethodAppTree, Root: "koreader", Dest: "/mnt/onboard/.adds/koreader",
			Launcher: &config.Launcher{
				NmFile:  "/mnt/onboard/.adds/nm/koreader",
				NmEntry: "menu_item:main:KOReader:cmd_spawn:quiet:exec /mnt/onboard/.adds/koreader/koreader.sh",
			},
		},
		Uninstall: config.Uninstall{Method: config.MethodManifest, PurgePaths: []string{"/mnt/onboard/.adds/koreader/**"}},
	}
	var b strings.Builder
	printInstallDef(&b, "koreader", "main", pkg, "", "", "")
	got := b.String()
	for _, want := range []string{
		"install  app-tree  ->  /mnt/onboard/.adds/koreader",
		"root      koreader",
		"nm_file   /mnt/onboard/.adds/nm/koreader",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("printInstallDef output missing %q:\n%s", want, got)
		}
	}
	// The destination must be disclosed BEFORE the uninstall recipe.
	if i, j := strings.Index(got, "install  app-tree"), strings.Index(got, "uninstall method"); i < 0 || (j >= 0 && i > j) {
		t.Errorf("the install block must print above the uninstall recipe:\n%s", got)
	}
	// A package with no [install] block prints nothing extra.
	plain := &config.Package{ID: "x", Name: "x", Source: "h/o/r", Forge: "github", Asset: "a.tgz"}
	b.Reset()
	printInstallDef(&b, "x", "main", plain, "", "", "")
	if strings.Contains(b.String(), "install  ") {
		t.Errorf("a tgz package must print no install block:\n%s", b.String())
	}
}

// ---- 8. doctor advisories (§8.1 / §10.3) --------------------------------

// seedFw writes .kobo/version so Paths.Firmware() reports fw.
func seedFw(t *testing.T, a *App, fw string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(a.paths.VersionFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.paths.VersionFile(), []byte("N,N,"+fw+",N\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDoctorKfmonIconAdvice: kpm cannot write the partition root, so a KFMon
// user has to copy the launcher icon over USB. doctor says so — and says nothing
// at all when KFMon is absent or the icon is already there.
func TestDoctorKfmonIconAdvice(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	setVersion(t, "0.11.0")
	appTreePkg(t, a, "example.com", "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	kor, err := a.loadPackage("koreader")
	if err != nil {
		t.Fatal(err)
	}
	targets := []*config.Package{kor}

	// No KFMon: nothing to say.
	if got := a.appTreeAdvice(targets, ""); len(got) != 0 {
		t.Errorf("no KFMon means no icon advice, got %v", got)
	}
	// KFMon present, icon missing: one advisory naming the exact file.
	kf := hostFile(t, sysroot, "/usr/local/kfmon/bin/kfmon")
	if err := os.MkdirAll(filepath.Dir(kf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kf, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(a.appTreeAdvice(targets, ""), "\n")
	if !strings.Contains(got, "/mnt/onboard/koreader.png") || !strings.Contains(got, "KFMon is installed") {
		t.Errorf("expected the KFMon icon advisory, got:\n%s", got)
	}
	// Icon present: silence again.
	icon := hostFile(t, sysroot, "/mnt/onboard/koreader.png")
	if err := os.WriteFile(icon, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := a.appTreeAdvice(targets, ""); len(got) != 0 {
		t.Errorf("an existing icon means no advice, got %v", got)
	}
	// A TGZ package is never subject to any of this — even one whose id KFMon
	// happens to have an icon rule for. (plato is registered here WITHOUT an
	// [install] block, so it is a tgz package; KFMon is installed and
	// /mnt/onboard/icons/plato.png is missing, which is exactly the situation
	// that would advise for an app-tree package.)
	fakeForgePkg(t, a, "example.com", "plato", "v1")
	plato, err := a.loadPackage("plato")
	if err != nil {
		t.Fatal(err)
	}
	if plato.IsAppTree() {
		t.Fatal("fixture error: plato must be a tgz package here")
	}
	if got := a.appTreeAdvice([]*config.Package{plato}, "4.45.23697"); len(got) != 0 {
		t.Errorf("a tgz package gets no app-tree advice, got %v", got)
	}
	// ...and the identical package DOES advise once it is app-tree, proving the
	// assertion above is about the install method and not about the fixture.
	plato.Install = &config.Install{Method: config.MethodAppTree, Dest: "/mnt/onboard/.adds/plato"}
	if got := a.appTreeAdvice([]*config.Package{plato}, "4.45.23697"); len(got) == 0 {
		t.Error("the same package as app-tree must advise about the missing KFMon icon")
	}
}

// TestDoctorExcludeSyncFoldersAdvice: detect and tell. kpm must never WRITE
// Kobo eReader.conf — Nickel owns it and reverts edits made while it runs.
func TestDoctorExcludeSyncFoldersAdvice(t *testing.T) {
	write := func(t *testing.T, a *App, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(a.paths.NickelConf()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(a.paths.NickelConf(), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setup := func(t *testing.T) (*App, []*config.Package) {
		t.Helper()
		a, _ := newUninstallApp(t)
		setVersion(t, "0.11.0")
		appTreePkg(t, a, "example.com", "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
		kor, err := a.loadPackage("koreader")
		if err != nil {
			t.Fatal(err)
		}
		return a, []*config.Package{kor}
	}

	// absent (the conf exists but declares no ExcludeSyncFolders) -> advise, on
	// firmware >= 4.17.
	a, targets := setup(t)
	seedFw(t, a, "4.45.23697")
	write(t, a, "[FeatureSettings]\nSomethingElse=1\n")
	if v := excludeSyncVerdict(a.paths); v != syncExcludeAbsent {
		t.Errorf("verdict = %q, want %q", v, syncExcludeAbsent)
	}
	got := strings.Join(a.appTreeAdvice(targets, "4.45.23697"), "\n")
	if !strings.Contains(got, "ExcludeSyncFolders=") || !strings.Contains(got, "Nickel rewrites this file") {
		t.Errorf("an absent key must produce the advisory:\n%s", got)
	}

	// The advisory must NOT fire below 4.17 — the edit does nothing there.
	if got := a.appTreeAdvice(targets, "4.16.0"); len(got) != 0 {
		t.Errorf("below firmware 4.17 there is nothing to advise, got %v", got)
	}
	// ...nor when the firmware is unknown (silence is the safe default).
	if got := a.appTreeAdvice(targets, ""); len(got) != 0 {
		t.Errorf("an unknown firmware must stay silent, got %v", got)
	}

	// weak (top-level hidden folders only) -> still advise.
	a2, targets2 := setup(t)
	write(t, a2, "[FeatureSettings]\nExcludeSyncFolders=(\\.(?!kobo|adobe).+)\n")
	if got := a2.appTreeAdvice(targets2, "4.45.23697"); len(got) == 0 {
		t.Error("a value that misses nested hidden directories is weak and must advise")
	}
	if v := excludeSyncVerdict(a2.paths); v != syncExcludeWeak {
		t.Errorf("verdict = %q, want %q", v, syncExcludeWeak)
	}

	// ok (covers nested hidden dirs) -> silence.
	a3, targets3 := setup(t)
	write(t, a3, "[FeatureSettings]\nExcludeSyncFolders=(\\.(?!kobo|adobe).+|([^.][^/]*/)+\\..+)\n")
	if v := excludeSyncVerdict(a3.paths); v != syncExcludeOK {
		t.Errorf("verdict = %q, want %q", v, syncExcludeOK)
	}
	if got := a3.appTreeAdvice(targets3, "4.45.23697"); len(got) != 0 {
		t.Errorf("a correct value must produce no advice, got %v", got)
	}

	// unreadable/missing -> unknown, and SILENCE. kpm cannot tell whether the
	// setting is fine, so it says nothing rather than nagging about a file it
	// could not read — the same rule Paths.Firmware() follows.
	a4, targets4 := setup(t)
	if v := excludeSyncVerdict(a4.paths); v != syncExcludeUnknown {
		t.Errorf("verdict = %q, want %q for a missing conf", v, syncExcludeUnknown)
	}
	if got := a4.appTreeAdvice(targets4, "4.45.23697"); len(got) != 0 {
		t.Errorf("an unreadable conf must stay silent, got %v", got)
	}
}

// TestDoctorNeverWritesNickelConf is the §10.2 decision, asserted: no kpm command
// may create or modify Kobo eReader.conf. Nickel owns it and reverts edits made
// while it runs, and the documented recovery for a botched one is a factory reset.
func TestDoctorNeverWritesNickelConf(t *testing.T) {
	z := zipAsset(t, map[string]string{"r/a": "1"})
	a, _, host := appTreeApp(t, map[string][]byte{"koreader": z})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	seedFw(t, a, "4.45.23697")
	original := "[FeatureSettings]\nSomethingElse=1\n"
	if err := os.MkdirAll(filepath.Dir(a.paths.NickelConf()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.paths.NickelConf(), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("update exit = %d", code)
	}
	captureStdout(t, func() { a.cmdDoctor(nil) })

	b, err := os.ReadFile(a.paths.NickelConf())
	if err != nil || string(b) != original {
		t.Errorf("Kobo eReader.conf must be left byte-identical, got %q (%v)", b, err)
	}
}

// ---- 9. failure recovery and cache integrity ----------------------------

// zipAssetLying builds a release zip whose LAST entry declares a one-byte
// uncompressed size but streams a full body. Compute sees only the lie and
// plans both entries; Apply writes the first file and then aborts on the
// second — the ENOSPC / USB-pulled-mid-extraction shape, reproducibly.
func zipAssetLying(t *testing.T, good map[string]string, badName, badBody string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(good))
	for n := range good {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(good[n])); err != nil {
			t.Fatal(err)
		}
	}
	h := &zip.FileHeader{Name: badName, Method: zip.Store}
	h.SetMode(0o644)
	h.UncompressedSize64 = 1
	h.CompressedSize64 = uint64(len(badBody))
	w, err := zw.CreateRaw(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(badBody)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestAppTreeFailedInstallRecordsPartialManifest: an install that dies partway
// through extraction has already written real files. Returning before the state
// save discarded that fact entirely, so `kpm list` showed the package as not
// installed AND `kpm uninstall` refused it for having no manifest — ~80 MiB of
// files kpm could neither name nor remove. The stale-but-correct manifest is the
// safe state, exactly as it is for the crash-before-save window.
func TestAppTreeFailedInstallRecordsPartialManifest(t *testing.T) {
	z := zipAssetLying(t, map[string]string{"r/a": "1a"}, "r/zz", strings.Repeat("Z", 4096))
	a, sysroot, host := appTreeApp(t, map[string][]byte{"koreader": z})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)

	if code := a.cmdUpdate([]string{"koreader"}); code == exitOK {
		t.Fatal("the fixture must make the extraction fail")
	}
	// The file that DID land is really there...
	if got, ok := readDev(t, sysroot, "/mnt/onboard/.adds/koreader/a"); !ok || got != "1a" {
		t.Fatalf("fixture: the first file must have been written, got %q %v", got, ok)
	}
	// ...so state must name it.
	ps := a.state.Get("koreader")
	man := strings.Join(ps.Manifest, ",")
	if !strings.Contains(man, "mnt/onboard/.adds/koreader/a") {
		t.Errorf("a failed install must record what it wrote, manifest = %v", ps.Manifest)
	}
	// The version must NOT advance: the convergence property depends on the next
	// update re-running the whole extraction.
	if ps.InstalledVersion == "v1" {
		t.Error("a failed install must not record the new version")
	}
	// And the tree must now be removable by ordinary means.
	if code := a.cmdUninstall([]string{"koreader", "--yes"}); code != exitOK {
		t.Fatalf("uninstall of a half-extracted tree exit = %d, want %d", code, exitOK)
	}
	if _, ok := readDev(t, sysroot, "/mnt/onboard/.adds/koreader/a"); ok {
		t.Error("uninstall must remove the stranded file")
	}
}

// TestAppTreeFailedInstallPurgeableWithoutManifest is M1's other half: even with
// nothing recorded at all, the shipped def's purge_paths describes the tree, so
// `kpm uninstall <pkg> --purge` must be able to clean up rather than refusing
// for want of a manifest.
func TestAppTreeFailedInstallPurgeableWithoutManifest(t *testing.T) {
	z := zipAsset(t, map[string]string{"r/a": "1"})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"koreader": z})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	// A tree on disk that state knows nothing about (USB pulled before the save).
	stranded := hostFile(t, sysroot, "/mnt/onboard/.adds/koreader/a")
	if err := os.MkdirAll(filepath.Dir(stranded), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stranded, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := a.cmdUninstall([]string{"koreader", "--yes", "--purge"}); code != exitOK {
		t.Fatalf("--purge exit = %d, want %d", code, exitOK)
	}
	if _, err := os.Stat(stranded); err == nil {
		t.Error("--purge must remove a stranded tree that purge_paths describes")
	}
}

// TestAppTreeReVerifiesZipBeforeApply: Compute runs at download time and Apply
// re-opens the cached zip minutes later (an `update --all` over Wi-Fi resolves
// and downloads every package first), from a predictable path on a partition the
// user mounts over USB. The sha256 was checked once, at download. Swapping the
// file in between substituted attacker CONTENT into paths the plan had already
// cleared — a rewritten koreader.sh is root code execution through the
// NickelMenu launcher. The bytes must be re-verified immediately before Apply.
func TestAppTreeReVerifiesZipBeforeApply(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	setVersion(t, "0.11.0")
	appTreePkg(t, a, "example.com", "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	pkg, err := a.loadPackage("koreader")
	if err != nil {
		t.Fatal(err)
	}

	cache := filepath.Join(a.paths.CacheDir(), "koreader-v1.zip")
	if err := os.MkdirAll(a.paths.CacheDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, zipAsset(t, map[string]string{"r/koreader.sh": "genuine"}), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := apptree.Compute(cache, "r", "/mnt/onboard/.adds/koreader")
	if err != nil {
		t.Fatal(err)
	}
	sum, _, err := sha256AndSize(cache)
	if err != nil {
		t.Fatal(err)
	}

	// The swap: same entry name, attacker content. Paths still come from the
	// plan, so containment is untouched — only the bytes change.
	if err := os.WriteFile(cache, zipAsset(t, map[string]string{"r/koreader.sh": "pwned"}), 0o644); err != nil {
		t.Fatal(err)
	}

	r := resolvedApp{pkg: pkg, tag: "v1", cache: cache, plan: plan, sha256: sum}
	if _, err := a.installAppTree(r, false); err == nil {
		t.Fatal("installAppTree must refuse a cache file that changed after it was verified")
	}
	if got, ok := readDev(t, sysroot, "/mnt/onboard/.adds/koreader/koreader.sh"); ok {
		t.Errorf("substituted content was written: %q", got)
	}
}

// TestAppTreeRefusesNmDirDest: `dest = "/mnt/onboard/.adds/nm"` plus a zip entry
// named `kpm` replaced every kpm menu entry with attacker-chosen NickelMenu
// actions (cmd_spawn runs as root), and put kpm's own drop-in into the victim
// package's manifest so a later `kpm uninstall` deleted it. LauncherPath refused
// the same file when a def DECLARED it as nm_file; the extractor's own gates did
// not, and firstReservedPath — which does know the rule — is only reached on the
// tgz branch.
func TestAppTreeRefusesNmDirDest(t *testing.T) {
	z := zipAsset(t, map[string]string{"kpm": "menu_item:main:Pwned:cmd_spawn:quiet:exec /tmp/x"})
	a, sysroot, host := appTreeApp(t, map[string][]byte{"evil": z})
	appTreePkg(t, a, host, "evil", "", "/mnt/onboard/.adds/nm", nil)
	// kpm's own drop-in, as the tgz install leaves it.
	kpmNm := hostFile(t, sysroot, "/mnt/onboard/.adds/nm/kpm")
	if err := os.MkdirAll(filepath.Dir(kpmNm), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kpmNm, []byte("menu_item:main:kpm - Status:cmd_output:9000:/bin/kpm status\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := a.cmdUpdate([]string{"evil"}); code == exitOK {
		t.Error("an app-tree install onto the shared NickelMenu directory must be refused")
	}
	b, err := os.ReadFile(kpmNm)
	if err != nil || !strings.Contains(string(b), "kpm - Status") {
		t.Errorf("kpm's own NickelMenu drop-in was replaced: %q (%v)", b, err)
	}
	for _, m := range a.state.Get("evil").Manifest {
		if strings.EqualFold(m, "mnt/onboard/.adds/nm/kpm") {
			t.Error("kpm's drop-in must never enter another package's manifest")
		}
	}
}
