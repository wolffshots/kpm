package main

// content_test.go drives the 0.12.0 content-directory work end to end (fonts
// plan §3.3, §3.4.1, §3.6, §3.7): per-file ownership inside a shared directory,
// the pre-download space refusal, the reboot rule, and the derived `kind`.
//
// The invariant every ownership case asks about is the one that buys the
// shared-root exception: kpm never writes, and therefore never deletes, a file it
// does not own.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/forge"
)

const fontsDir = "/mnt/onboard/fonts"

// fontPkg registers a font package: app-tree, flat zip, dest = the content dir.
// It declares NO purge_paths — a recursive purge of a shared content dir is
// refused by policy, and a font package owns no directory to purge.
func fontPkg(t *testing.T, a *App, host, id string) {
	t.Helper()
	pkg := &config.Package{
		Name: id, Source: host + "/o/" + id, Forge: config.ForgeForgejo,
		Asset:     id + "-app.zip",
		Install:   &config.Install{Method: config.MethodAppTree, Dest: fontsDir},
		Uninstall: config.Uninstall{Method: config.MethodManifest},
	}
	if err := config.Save(a.paths.PackageFile(id), pkg); err != nil {
		t.Fatal(err)
	}
}

// ---- 1. per-file ownership (§3.3, D4) -----------------------------------

// A second font package shipping a file the first one installed is refused, by
// name. Without this rule it would overwrite the file, record it, and delete it
// at ITS uninstall — carrying off a file that belongs to the other package.
func TestContentOwnershipRefusesAnotherPackagesFile(t *testing.T) {
	shared := map[string]string{"Shared.ttf": "from-a"}
	a, sysroot, host := appTreeApp(t, map[string][]byte{
		"fonts-a": zipAsset(t, shared),
		"fonts-b": zipAsset(t, map[string]string{"Shared.ttf": "from-b"}),
	})
	fontPkg(t, a, host, "fonts-a")
	if code := a.cmdUpdate([]string{"fonts-a"}); code != exitOK {
		t.Fatalf("fonts-a exit = %d", code)
	}

	fontPkg(t, a, host, "fonts-b")
	errOut := captureStderr(t, func() {
		if code := a.cmdUpdate([]string{"fonts-b"}); code == exitOK {
			t.Fatal("a package that ships another package's file must fail")
		}
	})
	if !strings.Contains(errOut, "is installed by fonts-a") {
		t.Errorf("the refusal must name the owner:\n%s", errOut)
	}
	if got, _ := readDev(t, sysroot, fontsDir+"/Shared.ttf"); got != "from-a" {
		t.Errorf("the owner's file was overwritten: %q", got)
	}
	// Nothing is written, so nothing is recorded either.
	if len(a.state.Get("fonts-b").Manifest) != 0 {
		t.Errorf("a refused install must record nothing: %v", a.state.Get("fonts-b").Manifest)
	}
}

// The user's own font — hand-copied over USB, in no manifest at all — is the
// case a per-DIRECTORY ownership rule cannot express. kpm refuses and says what
// to do about it, because overwriting it would also mean deleting it later.
func TestContentOwnershipRefusesUserFile(t *testing.T) {
	a, sysroot, host := appTreeApp(t, map[string][]byte{
		"fonts-a": zipAsset(t, map[string]string{"Mine.ttf": "from-a"}),
	})
	fontPkg(t, a, host, "fonts-a")
	mine := hostFile(t, sysroot, fontsDir+"/Mine.ttf")
	if err := os.MkdirAll(filepath.Dir(mine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("the user's own"), 0o644); err != nil {
		t.Fatal(err)
	}

	errOut := captureStderr(t, func() {
		if code := a.cmdUpdate([]string{"fonts-a"}); code == exitOK {
			t.Fatal("a file kpm did not install must never be overwritten")
		}
	})
	if !strings.Contains(errOut, "kpm did not install it") {
		t.Errorf("the refusal must explain itself:\n%s", errOut)
	}
	if b, _ := os.ReadFile(mine); string(b) != "the user's own" {
		t.Errorf("the user's font was overwritten: %q", b)
	}
}

// The package's OWN file from the previous version is its to replace — otherwise
// no font package could ever be updated, and a crash-resume could never retry.
func TestContentOwnershipAllowsOwnUpdate(t *testing.T) {
	a, sysroot, host := appTreeApp(t, map[string][]byte{
		"fonts-a": zipAsset(t, map[string]string{"A.ttf": "v1"}),
	})
	fontPkg(t, a, host, "fonts-a")
	if code := a.cmdUpdate([]string{"fonts-a"}); code != exitOK {
		t.Fatalf("v1 exit = %d", code)
	}

	srv := mixedServer(t, "v2", map[string][]byte{"fonts-a": zipAsset(t, map[string]string{"A.ttf": "v2"})})
	a.client = forge.NewClientWithHTTP(srv.Client())
	fontPkg(t, a, strings.TrimPrefix(srv.URL, "https://"), "fonts-a")
	a.state.Get("fonts-a").LastChecked = ""
	if code := a.cmdUpdate([]string{"fonts-a"}); code != exitOK {
		t.Fatalf("v2 exit = %d — a package must be able to replace its own file", code)
	}
	if got, _ := readDev(t, sysroot, fontsDir+"/A.ttf"); got != "v2" {
		t.Errorf("A.ttf = %q, want v2", got)
	}
}

// The collision check must run at INSTALL time, not at resolve time: an
// `update --all` resolves every package before it installs any, so a
// resolve-time check would clear both halves of this pair.
func TestContentOwnershipUpdateAllSecondFails(t *testing.T) {
	a, sysroot, host := appTreeApp(t, map[string][]byte{
		"fonts-a": zipAsset(t, map[string]string{"Shared.ttf": "from-a", "OnlyA.ttf": "a"}),
		"fonts-b": zipAsset(t, map[string]string{"Shared.ttf": "from-b"}),
	})
	fontPkg(t, a, host, "fonts-a")
	fontPkg(t, a, host, "fonts-b")

	if code := a.cmdUpdate([]string{"--all"}); code == exitOK {
		t.Fatal("the second package must fail, so the run is not clean")
	}
	// The first package installed in full...
	if got, _ := readDev(t, sysroot, fontsDir+"/OnlyA.ttf"); got != "a" {
		t.Error("the first package must still install")
	}
	if a.state.Get("fonts-a").InstalledVersion != "v1" {
		t.Error("the first package must be recorded")
	}
	// ...and the second wrote nothing at all.
	if got, _ := readDev(t, sysroot, fontsDir+"/Shared.ttf"); got != "from-a" {
		t.Errorf("Shared.ttf = %q, want the first package's copy", got)
	}
	if a.state.Get("fonts-b").InstalledVersion != "" {
		t.Error("the refused package must not be recorded as installed")
	}
}

// ---- 2. uninstall inside a shared directory (D4) ------------------------

func TestContentUninstallKeepsUserFileAndDir(t *testing.T) {
	a, sysroot, host := appTreeApp(t, map[string][]byte{
		"fonts-a": zipAsset(t, map[string]string{"A.ttf": "a", "B.ttf": "b"}),
	})
	fontPkg(t, a, host, "fonts-a")
	if code := a.cmdUpdate([]string{"fonts-a"}); code != exitOK {
		t.Fatalf("update exit = %d", code)
	}
	user := hostFile(t, sysroot, fontsDir+"/UserFont-Regular.ttf")
	if err := os.WriteFile(user, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := a.cmdUninstall([]string{"fonts-a", "--yes"}); code != exitOK {
		t.Fatalf("uninstall exit = %d", code)
	}
	for _, f := range []string{"A.ttf", "B.ttf"} {
		if _, ok := readDev(t, sysroot, fontsDir+"/"+f); ok {
			t.Errorf("%s must be removed", f)
		}
	}
	if b, err := os.ReadFile(user); err != nil || string(b) != "mine" {
		t.Errorf("the user's own font must survive: %q %v", b, err)
	}
	if _, err := os.Stat(hostFile(t, sysroot, fontsDir)); err != nil {
		t.Error("the content dir itself must survive — Nickel reads it and it is shared")
	}
}

// A tgz package that ships mnt/onboard/fonts/X.ttf used to uninstall "cleanly"
// while leaving the file on disk with nothing tracking it: the member classified
// vDenied and went to plan.Skipped. It is now removed like any other member.
func TestContentTgzFontMemberNowUninstalls(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	f := mkfile(t, sysroot, "mnt/onboard/fonts/Shipped.ttf")
	registerPkg(t, a, "oldpkg", config.Uninstall{}, []string{"mnt/onboard/fonts/Shipped.ttf"})

	if code := a.cmdUninstall([]string{"oldpkg", "--yes"}); code != exitOK {
		t.Fatalf("uninstall exit = %d", code)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Error("a font shipped by a tgz package must now be removed by its uninstall")
	}
	if _, err := os.Stat(hostFile(t, sysroot, fontsDir)); err != nil {
		t.Error("the content dir itself must survive an uninstall that emptied it")
	}
}

// ---- 3. the pre-download space check (§3.4.1) ---------------------------

// The refusal must land BEFORE the .part file is created: a download that fills
// the partition leaves Nickel unable to write its database, and deleting the
// .part afterwards does not undo that.
func TestFetchAssetRefusesWithoutSpace(t *testing.T) {
	a, _, host := appTreeApp(t, map[string][]byte{"fonts-a": zipAsset(t, map[string]string{"A.ttf": "a"})})
	fontPkg(t, a, host, "fonts-a")
	// A host cannot pose a full FAT32 partition, so pose the statfs answer.
	old := cacheAvailableBytes
	cacheAvailableBytes = func(string) (int64, bool) { return 1024, true }
	t.Cleanup(func() { cacheAvailableBytes = old })

	errOut := captureStderr(t, func() {
		if code := a.cmdUpdate([]string{"fonts-a"}); code == exitOK {
			t.Fatal("the download must be refused when the partition is full")
		}
	})
	if !strings.Contains(errOut, "bytes free") {
		t.Errorf("the refusal must say what it needs:\n%s", errOut)
	}
	ents, err := os.ReadDir(a.paths.CacheDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		t.Errorf("nothing may be written to the cache: found %s", e.Name())
	}
}

// An unknown filesystem is not a full one: off-device (and on any host statfs
// cannot answer for) the check passes and the install proceeds.
func TestFetchAssetUnknownSpacePasses(t *testing.T) {
	a, sysroot, host := appTreeApp(t, map[string][]byte{"fonts-a": zipAsset(t, map[string]string{"A.ttf": "a"})})
	fontPkg(t, a, host, "fonts-a")
	old := cacheAvailableBytes
	cacheAvailableBytes = func(string) (int64, bool) { return 0, false }
	t.Cleanup(func() { cacheAvailableBytes = old })

	if code := a.cmdUpdate([]string{"fonts-a"}); code != exitOK {
		t.Fatalf("an unknown filesystem must not block an install: exit %d", code)
	}
	if got, _ := readDev(t, sysroot, fontsDir+"/A.ttf"); got != "a" {
		t.Errorf("A.ttf = %q, want a", got)
	}
}

// A forge that reports no asset size gives the check nothing to compare, so it
// passes — even on a near-full partition — rather than refusing every download
// from that forge. Called directly: the fixture server always reports a size.
func TestFetchAssetUnknownSizeSkipsCheck(t *testing.T) {
	a, _, host := appTreeApp(t, map[string][]byte{"fonts-a": zipAsset(t, map[string]string{"A.ttf": "a"})})
	old := cacheAvailableBytes
	cacheAvailableBytes = func(string) (int64, bool) { return 1024, true }
	t.Cleanup(func() { cacheAvailableBytes = old })

	asset := forge.Asset{Name: "fonts-a-app.zip", DownloadURL: "https://" + host + "/download/fonts-a/asset"}
	if _, _, err := a.fetchAsset("fonts-a", "v1", ".zip", "", false, false, asset); err != nil {
		t.Errorf("an unknown size must skip the space check: %v", err)
	}
	asset.Size = 100
	if _, _, err := a.fetchAsset("fonts-a", "v1", ".zip", "", false, false, asset); err == nil {
		t.Error("a known size with 1 KiB free must be refused")
	}
}

// ---- 4. reboot semantics (§3.6, the §3.7 truth table) -------------------

// noReboot makes rebootDevice a recorded no-op, so a test can assert the reboot
// decision without restarting the machine running the suite.
func noReboot(t *testing.T) *bool {
	t.Helper()
	called := false
	old := rebootDevice
	rebootDevice = func() error { called = true; return nil }
	t.Cleanup(func() { rebootDevice = old })
	return &called
}

// update <font-id>: staged=false, reboot_required=true. The files are on disk,
// but Nickel renders the old font until it restarts — a missing prompt here is a
// SILENT failure.
func TestUIContractUpdateFont(t *testing.T) {
	a, _, host := appTreeApp(t, map[string][]byte{"fonts-a": zipAsset(t, map[string]string{"A.ttf": "a"})})
	fontPkg(t, a, host, "fonts-a")

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"fonts-a", "--json"}) })
	if code != exitOK {
		t.Fatalf("update --json exit = %d", code)
	}
	m := decodeJSON(t, lastJSON(t, out))
	if wantBool(t, m, "staged") != false || wantBool(t, m, "reboot_required") != true {
		t.Errorf("a font install stages nothing but needs a reboot: %v", m)
	}
}

// The human tail of a live-only run names the reboot when a content package
// changed (§3.6 site 5). It is suppressed under --json, where BEGIN_JSON must be
// the last line, so this is a plain run.
func TestContentHumanTail(t *testing.T) {
	a, _, host := appTreeApp(t, map[string][]byte{"fonts-a": zipAsset(t, map[string]string{"A.ttf": "a"})})
	fontPkg(t, a, host, "fonts-a")

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"fonts-a"}) })
	if code != exitOK {
		t.Fatalf("update exit = %d", code)
	}
	if !strings.Contains(out, "1 package(s) installed - reboot to use them") {
		t.Errorf("the human tail must say a reboot is needed:\n%s", out)
	}
}

// update --all with one app-tree MOD and one font: still nothing staged, but the
// font pulls reboot_required true for the run.
func TestUIContractUpdateModPlusFont(t *testing.T) {
	a, _, host := appTreeApp(t, map[string][]byte{
		"fonts-a":  zipAsset(t, map[string]string{"A.ttf": "a"}),
		"koreader": zipAsset(t, map[string]string{"r/a": "1"}),
	})
	fontPkg(t, a, host, "fonts-a")
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"--all", "--json"}) })
	if code != exitOK {
		t.Fatalf("update --all --json exit = %d", code)
	}
	m := decodeJSON(t, lastJSON(t, out))
	if wantBool(t, m, "staged") != false || wantBool(t, m, "reboot_required") != true {
		t.Errorf("a live run with a font in it needs a reboot and stages nothing: %v", m)
	}
	if len(wantArray(t, m, "changed")) != 2 {
		t.Errorf("changed = %v, want both packages", m["changed"])
	}
}

// update --all with a font and a tgz package: the mixed branch is unchanged —
// the run staged something, so both bools are true.
func TestUIContractUpdateFontPlusTgz(t *testing.T) {
	a, _, host := appTreeApp(t, map[string][]byte{"fonts-a": zipAsset(t, map[string]string{"A.ttf": "a"})})
	fontPkg(t, a, host, "fonts-a")
	fakeForgePkg(t, a, host, "nickelclock", "")

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"--all", "--json"}) })
	if code != exitOK {
		t.Fatalf("mixed update exit = %d", code)
	}
	m := decodeJSON(t, lastJSON(t, out))
	if wantBool(t, m, "staged") != true || wantBool(t, m, "reboot_required") != true {
		t.Errorf("a mixed run stages and reboots: %v", m)
	}
}

// uninstall <font-id> --yes: the files are gone now, but the old font stays in
// use until the restart.
func TestUIContractUninstallFont(t *testing.T) {
	a, _, host := appTreeApp(t, map[string][]byte{"fonts-a": zipAsset(t, map[string]string{"A.ttf": "a"})})
	fontPkg(t, a, host, "fonts-a")
	if code := a.cmdUpdate([]string{"fonts-a"}); code != exitOK {
		t.Fatalf("setup update exit = %d", code)
	}

	var code int
	out := captureStdout(t, func() { code = a.cmdUninstall([]string{"fonts-a", "--yes", "--json"}) })
	if code != exitOK {
		t.Fatalf("uninstall --json exit = %d", code)
	}
	m := decodeJSON(t, lastJSON(t, out))
	if wantBool(t, m, "staged") != false || wantBool(t, m, "reboot_required") != true {
		t.Errorf("a font uninstall stages nothing and needs a reboot: %v", m)
	}
}

// rebootToApply is true for EVERY tgz package, so the uninstall site must ask it
// only for a live package. A plain manifest uninstall of a tgz package deletes
// its files now and still needs no reboot — the 0.11.0 answer, unchanged.
func TestUIContractUninstallTgzStillNoReboot(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	mkfile(t, sysroot, "usr/local/pkg/lib.so")
	registerPkg(t, a, "pkg", config.Uninstall{}, []string{"usr/local/pkg/lib.so"})

	var code int
	out := captureStdout(t, func() { code = a.cmdUninstall([]string{"pkg", "--yes", "--json"}) })
	if code != exitOK {
		t.Fatalf("uninstall exit = %d", code)
	}
	m := decodeJSON(t, lastJSON(t, out))
	if wantBool(t, m, "reboot_required") != false {
		t.Errorf("a tgz manifest uninstall needs no reboot: %v", m)
	}
}

// A font that FAILED changed nothing, so it must not pull a reboot into a run
// whose only success is a plain app-tree mod.
func TestContentFailedFontNeedsNoReboot(t *testing.T) {
	a, sysroot, host := appTreeApp(t, map[string][]byte{
		"fonts-a":  zipAsset(t, map[string]string{"Mine.ttf": "from-a"}),
		"koreader": zipAsset(t, map[string]string{"r/a": "1"}),
	})
	fontPkg(t, a, host, "fonts-a")
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	// The user's own file makes the font install fail.
	mkfile(t, sysroot, "mnt/onboard/fonts/Mine.ttf")

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"--all", "--json"}) })
	if code == exitOK {
		t.Fatal("fixture: the font install must fail")
	}
	m := decodeJSON(t, lastJSON(t, out))
	if wantBool(t, m, "reboot_required") != false {
		t.Errorf("only the mod changed, so no reboot is needed: %v", m)
	}
	if changed := wantArray(t, m, "changed"); len(changed) != 1 || changed[0] != "koreader" {
		t.Errorf("changed = %v, want [koreader]", changed)
	}
}

// The NickelMenu "Update all" entry always passes --reboot. A font-only run must
// take it; the app-tree-mod-only run next door must not.
func TestContentRebootFlag(t *testing.T) {
	a, _, host := appTreeApp(t, map[string][]byte{"fonts-a": zipAsset(t, map[string]string{"A.ttf": "a"})})
	fontPkg(t, a, host, "fonts-a")
	called := noReboot(t)

	if code := a.cmdUpdate([]string{"fonts-a", "--reboot"}); code != exitOK {
		t.Fatalf("update --reboot exit = %d", code)
	}
	if !*called {
		t.Error("a font-only run with --reboot must reboot: the new font is not in use until it does")
	}
	if !strings.Contains(logText(t, a), "REBOOT") {
		t.Error("the reboot must be logged")
	}
}

func TestContentRebootFlagSkipsPlainAppTree(t *testing.T) {
	a, _, host := appTreeApp(t, map[string][]byte{"koreader": zipAsset(t, map[string]string{"r/a": "1"})})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	called := noReboot(t)

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"koreader", "--reboot"}) })
	if code != exitOK {
		t.Fatalf("update --reboot exit = %d", code)
	}
	if *called {
		t.Error("an app-tree mod is usable immediately — --reboot must NOT reboot for it")
	}
	if strings.Contains(out, "reboot to use them") {
		t.Errorf("a plain app-tree run must not claim a reboot:\n%s", out)
	}
}

// The status summary claims a reboot whenever a pending update needs one. The
// app-tree-mod case ("(no reboot)") is pinned by TestAppTreeStatusNoRebootWording.
func TestContentStatusRebootWording(t *testing.T) {
	a, _ := newUninstallApp(t)
	setVersion(t, "0.11.0")
	fontPkg(t, a, "example.com", "fonts-a")
	p, err := a.loadPackage("fonts-a")
	if err != nil {
		t.Fatal(err)
	}
	a.state.Get("fonts-a").InstalledVersion = "v1"
	a.state.Get("fonts-a").LatestSeen = "v2"

	got := a.buildStatus([]*config.Package{p})
	if !strings.Contains(got, "(reboots)") {
		t.Errorf("a pending font update must claim a reboot:\n%s", got)
	}
}

// ---- 5. kind on search --json (§3.7, D1) --------------------------------

// kind is DERIVED from the install destination, and the local packages.d
// snapshot wins over the registry def — the same precedence install_method uses,
// out of the same helper, so the two can never disagree about which def a row
// describes.
func TestUIContractSearchKind(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.11.0")
	seedRegistry(t, a, "main", `schema_version = 1

[packages.nickelclock]
name = "NickelClock"
source = "github.com/shermp/NickelClock"
forge = "github"
asset = "KoboRoot.tgz"

[packages.koreader]
name = "KOReader"
source = "github.com/koreader/koreader"
forge = "github"
asset = "koreader-kobo-*.zip"

  [packages.koreader.install]
  method = "app-tree"
  root = "koreader"
  dest = "/mnt/onboard/.adds/koreader"

[packages.kf-core-fonts]
name = "KF Core fonts"
source = "github.com/nicoverbruggen/ebook-fonts"
forge = "github"
asset = "kobo-core-fonts.zip"

  [packages.kf-core-fonts.install]
  method = "app-tree"
  dest = "/mnt/onboard/fonts"

[packages.moved]
name = "Moved"
source = "github.com/o/moved"
forge = "github"
asset = "moved.zip"

  [packages.moved.install]
  method = "app-tree"
  dest = "/mnt/onboard/.adds/moved"
`)
	// `moved` also has a LOCAL def, which is the one `update` acts on: it installs
	// into the content dir, so the row must read "font" despite the registry.
	fontPkg(t, a, "example.com", "moved")

	var code int
	out := captureStdout(t, func() { code = a.cmdSearch([]string{"--json"}) })
	if code != exitOK {
		t.Fatalf("search --json exit = %d", code)
	}
	m := decodeJSON(t, lastJSON(t, out))
	got := map[string]string{}
	for _, raw := range wantArray(t, m, "packages") {
		pkg := raw.(map[string]any)
		assertSearchPkg(t, pkg)
		got[wantStr(t, pkg, "id")] = wantStr(t, pkg, "kind")
	}
	want := map[string]string{
		"nickelclock":   "mod", // no [install] block at all
		"koreader":      "mod", // app-tree, but into its own .adds tree
		"kf-core-fonts": "font",
		"moved":         "font", // the LOCAL snapshot wins
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s kind = %q, want %q", id, got[id], w)
		}
	}
}

// Every def in the frozen pre-content registry snapshot reports "mod". Nothing
// in the shipped registry installs into a content dir, so the browse view keeps
// exactly the rows it has today under the Mods tab.
func TestSearchKindDefaultsToModForShippedDefs(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "registry", "testdata", "registry-0.10.0.toml"))
	if err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t)
	setVersion(t, "0.11.0")
	seedRegistry(t, a, "main", string(raw))

	var code int
	out := captureStdout(t, func() { code = a.cmdSearch([]string{"--json"}) })
	if code != exitOK {
		t.Fatalf("search --json exit = %d", code)
	}
	rows := wantArray(t, decodeJSON(t, lastJSON(t, out)), "packages")
	if len(rows) == 0 {
		t.Fatal("the snapshot must produce rows")
	}
	for _, r := range rows {
		pkg := r.(map[string]any)
		if k := wantStr(t, pkg, "kind"); k != "mod" {
			t.Errorf("%s kind = %q, want mod — no shipped def installs into a content dir",
				wantStr(t, pkg, "id"), k)
		}
	}
}

// ---- 6. wave 2 review fixes ---------------------------------------------

// CRITICAL 1, the tgz route: a KoboRoot.tgz member can carry a padded name, and
// there is no ownership check on that path at all. Trimming it at uninstall
// deleted the USER's unpadded file and left the package's own file behind.
func TestContentPaddedManifestMemberSparesUserFile(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	user := mkfile(t, sysroot, "mnt/onboard/fonts/Mine.ttf")
	padded := mkfile(t, sysroot, "mnt/onboard/fonts/Mine.ttf\u00a0")
	registerPkg(t, a, "oldpkg", config.Uninstall{}, []string{"mnt/onboard/fonts/Mine.ttf\u00a0"})

	if code := a.cmdUninstall([]string{"oldpkg", "--yes"}); code != exitOK {
		t.Fatalf("uninstall exit = %d", code)
	}
	if _, err := os.Stat(user); err != nil {
		t.Error("the user's own font must survive: the manifest never named it")
	}
	// The padded member itself is skipped as malformed rather than deleted. That
	// is the safe end of the trade: kpm leaves a file it cannot name unambiguously.
	if _, err := os.Stat(padded); err != nil {
		t.Error("fixture: the padded file should be left alone, not deleted")
	}
}

// MINOR 5: a manifest spelled with a leading slash must still find its owner.
// Keyed as-is it missed the owner map, and kpm told the user to delete a file
// another package installs.
func TestContentOwnershipFindsSlashPrefixedOwner(t *testing.T) {
	a, sysroot, host := appTreeApp(t, map[string][]byte{
		"fonts-b": zipAsset(t, map[string]string{"Shared.ttf": "from-b"}),
	})
	// A package whose manifest uses the absolute spelling.
	registerPkg(t, a, "fonts-a", config.Uninstall{}, []string{"/mnt/onboard/fonts/Shared.ttf"})
	mkfile(t, sysroot, "mnt/onboard/fonts/Shared.ttf")
	fontPkg(t, a, host, "fonts-b")

	errOut := captureStderr(t, func() {
		if code := a.cmdUpdate([]string{"fonts-b"}); code == exitOK {
			t.Fatal("the install must be refused")
		}
	})
	if !strings.Contains(errOut, "is installed by fonts-a") {
		t.Errorf("the owner must be found whatever the manifest spelling:\n%s", errOut)
	}
}

// MINOR 8: the owner refusal is permanent until the user acts, so it has to say
// what to do. Owner-first is kept — it is the order that never takes a file from
// another package.
func TestContentOwnershipMessageNamesRemedy(t *testing.T) {
	a, sysroot, host := appTreeApp(t, map[string][]byte{
		"fonts-b": zipAsset(t, map[string]string{"Shared.ttf": "from-b"}),
	})
	registerPkg(t, a, "fonts-a", config.Uninstall{}, []string{"mnt/onboard/fonts/Shared.ttf"})
	mkfile(t, sysroot, "mnt/onboard/fonts/Shared.ttf")
	fontPkg(t, a, host, "fonts-b")

	errOut := captureStderr(t, func() { a.cmdUpdate([]string{"fonts-b"}) })
	if !strings.Contains(errOut, "Uninstall fonts-a") {
		t.Errorf("the refusal must name a remedy:\n%s", errOut)
	}
}

// MINOR 6: a tgz package that ships a font deletes it now, so the old font keeps
// rendering until a restart. The def declares no [install] block, so only the
// manifest can say so.
func TestUIContractUninstallTgzWithFontReboots(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	mkfile(t, sysroot, "mnt/onboard/fonts/Shipped.ttf")
	registerPkg(t, a, "oldpkg", config.Uninstall{}, []string{"mnt/onboard/fonts/Shipped.ttf"})

	var code int
	out := captureStdout(t, func() { code = a.cmdUninstall([]string{"oldpkg", "--yes", "--json"}) })
	if code != exitOK {
		t.Fatalf("uninstall exit = %d", code)
	}
	m := decodeJSON(t, lastJSON(t, out))
	if wantBool(t, m, "reboot_required") != true {
		t.Errorf("removing a font needs a reboot whatever shipped it: %v", m)
	}
}

// MINOR 7: the confirmation prompt must not offer to delete the font library.
func TestContentPlanDoesNotPrintContentDirDelete(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	mkfile(t, sysroot, "mnt/onboard/fonts/A.ttf")
	registerPkg(t, a, "oldpkg", config.Uninstall{},
		[]string{"mnt/onboard/fonts", "mnt/onboard/fonts/A.ttf"})

	out := captureStdout(t, func() { a.cmdUninstall([]string{"oldpkg", "--dry-run"}) })
	if strings.Contains(out, "delete      /mnt/onboard/fonts\n") {
		t.Errorf("the plan must not read as deleting the font library:\n%s", out)
	}
	if !strings.Contains(out, "delete      /mnt/onboard/fonts/A.ttf") {
		t.Errorf("the package's own font must still be listed:\n%s", out)
	}
	if _, err := os.Stat(hostFile(t, sysroot, fontsDir)); err != nil {
		t.Error("fixture: the content dir must exist")
	}
}

// MAJOR 4: app-tree installs land at step 2, before the tgz merge. When the merge
// then fails, the font is on disk and recorded — but the payload said nothing
// changed and no reboot was pending, so the hook never prompted, and the retry
// (finding the font up to date) said the same. The signal was lost for good.
func TestContentMixedStagingFailureKeepsRebootSignal(t *testing.T) {
	a, sysroot, host := appTreeApp(t, map[string][]byte{"fonts-a": zipAsset(t, map[string]string{"A.ttf": "a"})})
	fontPkg(t, a, host, "fonts-a")
	fakeForgePkg(t, a, host, "nickelclock", "")
	// A tgz kpm did not stage: guardExistingTgz refuses, so the merge fails after
	// the font has already been installed.
	if err := os.WriteFile(a.paths.StagedTgz(), []byte("foreign"), 0o644); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"--all", "--json"}) })
	if code != exitError {
		t.Fatalf("the staging half must fail, exit = %d", code)
	}
	// The font really did land.
	if got, _ := readDev(t, sysroot, fontsDir+"/A.ttf"); got != "a" {
		t.Fatal("fixture: the font must be installed before the merge fails")
	}
	m := decodeJSON(t, lastJSON(t, out))
	if wantBool(t, m, "reboot_required") != true {
		t.Errorf("the installed font still needs a reboot: %v", m)
	}
	if wantBool(t, m, "staged") != false {
		t.Errorf("nothing was staged: %v", m)
	}
	changed := wantArray(t, m, "changed")
	if len(changed) != 1 || changed[0] != "fonts-a" {
		t.Errorf("changed = %v, want [fonts-a]", changed)
	}
	if s, _ := m["error"].(string); s == "" {
		t.Errorf("the failure must still be reported in error: %v", m)
	}
}

// CONDITIONAL 13: a user without NickelDBus reads only the status file, and
// buildStatus calls everything up to date the moment the files land.
func TestContentStatusFileMentionsPendingReboot(t *testing.T) {
	a, _, host := appTreeApp(t, map[string][]byte{"fonts-a": zipAsset(t, map[string]string{"A.ttf": "a"})})
	fontPkg(t, a, host, "fonts-a")
	if code := a.cmdUpdate([]string{"fonts-a"}); code != exitOK {
		t.Fatalf("update exit = %d", code)
	}
	if got := a.paths.ReadStatus(); !strings.Contains(got, "Reboot to use the newly installed files.") {
		t.Errorf("the status file must say a reboot is pending:\n%s", got)
	}
}

// ...and it must not say so for a plain app-tree mod, which is usable at once.
func TestContentStatusFileSilentForPlainAppTree(t *testing.T) {
	a, _, host := appTreeApp(t, map[string][]byte{"koreader": zipAsset(t, map[string]string{"r/a": "1"})})
	appTreePkg(t, a, host, "koreader", "r", "/mnt/onboard/.adds/koreader", nil)
	if code := a.cmdUpdate([]string{"koreader"}); code != exitOK {
		t.Fatalf("update exit = %d", code)
	}
	if got := a.paths.ReadStatus(); strings.Contains(got, "Reboot to use") {
		t.Errorf("an app-tree mod needs no reboot:\n%s", got)
	}
}

// foldMember keys the ownership and prune maps, so it must fold exactly what the
// device folds — and exactly what the policy layer folds. Both directions matter:
// under-folding misses an owner, over-folding lets a collision pass as this
// package's own previous file and skips the on-disk user-file check.
func TestFoldMemberFoldsASCIIOnly(t *testing.T) {
	// ASCII case is one file on FAT32 — and the leading-slash spelling is the
	// same member either way.
	if foldMember("mnt/onboard/fonts/SHARED.TTF") != foldMember("/mnt/onboard/fonts/shared.ttf") {
		t.Error("ASCII case (and a leading slash) must fold to one key on the onboard partition")
	}
	// Unicode simple folding merges these; vfat does not, so neither may kpm.
	for _, pair := range [][2]string{
		{"mnt/onboard/fonts/\u212A.ttf", "mnt/onboard/fonts/k.ttf"}, // Kelvin sign
		{"mnt/onboard/fonts/\u017F.ttf", "mnt/onboard/fonts/s.ttf"}, // long s
	} {
		if foldMember(pair[0]) == foldMember(pair[1]) {
			t.Errorf("%q and %q are different files on the device and must key differently", pair[0], pair[1])
		}
	}
	// The rootfs is case-SENSITIVE and must not fold at all.
	if foldMember("usr/local/kobo/X.so") == foldMember("usr/local/kobo/x.so") {
		t.Error("rootfs paths must stay case-sensitive")
	}
}

// The under-folding direction, end to end: an owner recorded in a different
// ASCII case is still the owner, because the device has only one such file.
func TestContentOwnershipMatchesOwnerAcrossASCIICase(t *testing.T) {
	a, sysroot, host := appTreeApp(t, map[string][]byte{
		"fonts-b": zipAsset(t, map[string]string{"Shared.ttf": "from-b"}),
	})
	registerPkg(t, a, "fonts-a", config.Uninstall{}, []string{"mnt/onboard/fonts/SHARED.TTF"})
	mkfile(t, sysroot, "mnt/onboard/fonts/SHARED.TTF")
	fontPkg(t, a, host, "fonts-b")

	errOut := captureStderr(t, func() {
		if code := a.cmdUpdate([]string{"fonts-b"}); code == exitOK {
			t.Fatal("SHARED.TTF and Shared.ttf are one file on FAT32: the install must be refused")
		}
	})
	if !strings.Contains(errOut, "is installed by fonts-a") {
		t.Errorf("the owner must be found across ASCII case:\n%s", errOut)
	}
}
