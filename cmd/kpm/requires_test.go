package main

// requires_test.go pins package dependencies (docs/dependencies.md): install
// registers missing requirements, update installs requirements first, and
// uninstall refuses while an installed package needs the target.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/forge"
)

// uncagedManifest offers kobo-uncaged, which requires nickeldbus, which
// requires nickelmenu.
const uncagedManifest = `schema_version = 1
[packages.kobo-uncaged]
name = "Kobo-UNCaGED"
source = "github.com/o/kobo-uncaged"
forge = "github"
asset = "KoboRoot.tgz"
requires = ["nickeldbus"]

[packages.nickeldbus]
name = "NickelDBus"
source = "github.com/o/nickeldbus"
forge = "github"
asset = "KoboRoot.tgz"
requires = ["nickelmenu"]

[packages.nickelmenu]
name = "NickelMenu"
source = "github.com/pgaskin/NickelMenu"
forge = "github"
asset = "KoboRoot.tgz"
`

func runInstall(t *testing.T, a *App, args ...string) (int, string, string) {
	t.Helper()
	var code int
	var stderr string
	out := captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = a.cmdInstall(args) })
	})
	return code, out, stderr
}

// install registers every missing requirement, requirements first, and the
// JSON payload names all of them.
func TestInstallRegistersRequirements(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", uncagedManifest)

	code, out, stderr := runInstall(t, a, "kobo-uncaged", "--yes", "--json")
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	for _, id := range []string{"kobo-uncaged", "nickeldbus", "nickelmenu"} {
		if _, err := os.Stat(a.paths.PackageFile(id)); err != nil {
			t.Errorf("%s def not written: %v", id, err)
		}
	}
	if !strings.Contains(out, "also installs nickelmenu, nickeldbus (required by kobo-uncaged)") {
		t.Errorf("the confirm text must name the requirements in install order:\n%s", out)
	}
	if !strings.Contains(out, "  requires nickeldbus\n") {
		t.Errorf("the def review must list requires:\n%s", out)
	}
	var m jsonMutation
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &m); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.Changed, ","); got != "kobo-uncaged,nickeldbus,nickelmenu" {
		t.Errorf("changed = %s", got)
	}
	p, err := a.loadPackage("kobo-uncaged")
	if err != nil || len(p.Requires) != 1 || p.Requires[0] != "nickeldbus" {
		t.Errorf("requires not projected into packages.d: %+v %v", p, err)
	}
}

// Without --yes, nothing is written, requirements included.
func TestInstallRequirementsNeedYes(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", uncagedManifest)
	code, _, _ := runInstall(t, a, "kobo-uncaged")
	if code != exitConfirm {
		t.Fatalf("exit %d, want %d", code, exitConfirm)
	}
	for _, id := range []string{"kobo-uncaged", "nickeldbus", "nickelmenu"} {
		if _, err := os.Stat(a.paths.PackageFile(id)); !os.IsNotExist(err) {
			t.Errorf("%s: no def may be written without --yes", id)
		}
	}
}

// An installed requirement, or one that already has a def, is not registered
// again.
func TestInstallSkipsMetRequirements(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", uncagedManifest)
	registerLocal(t, a, "nickelmenu", nil, "v0.6.0", "")
	if err := config.Save(a.paths.PackageFile("nickeldbus"), &config.Package{
		Name: "NickelDBus", Source: "github.com/o/nickeldbus", Forge: config.ForgeGitHub, Asset: "KoboRoot.tgz",
	}); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runInstall(t, a, "kobo-uncaged", "--yes", "--json")
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	if strings.Contains(out, "also installs") {
		t.Errorf("no requirement needs a new def:\n%s", out)
	}
	var m jsonMutation
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &m); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.Changed, ","); got != "kobo-uncaged" {
		t.Errorf("changed = %s", got)
	}
}

// A requirement that no registry offers refuses the install, and writes nothing.
func TestInstallRefusesUnknownRequirement(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", `schema_version = 1
[packages.a]
name = "A"
source = "github.com/o/a"
forge = "github"
asset = "KoboRoot.tgz"
requires = ["ghost"]
`)
	code, out, stderr := runInstall(t, a, "a", "--yes", "--json")
	if code != exitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(stderr, `"a" requires package "ghost", which no registry offers`) {
		t.Errorf("stderr = %q", stderr)
	}
	if _, err := os.Stat(a.paths.PackageFile("a")); !os.IsNotExist(err) {
		t.Error("a refused install must write no def")
	}
}

// A loop has no install order, so install refuses it and names the loop.
func TestInstallRefusesRequiresLoop(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", `schema_version = 1
[packages.a]
name = "A"
source = "github.com/o/a"
forge = "github"
asset = "KoboRoot.tgz"
requires = ["b"]

[packages.b]
name = "B"
source = "github.com/o/b"
forge = "github"
asset = "KoboRoot.tgz"
requires = ["a"]
`)
	code, _, stderr := runInstall(t, a, "a", "--yes")
	if code != exitError || !strings.Contains(stderr, "requires makes a loop: a -> b -> a") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

// A requirement that conflicts with an installed package refuses the install.
func TestInstallRefusesConflictingRequirement(t *testing.T) {
	a := newTestApp(t)
	seedRegistry(t, a, "main", `schema_version = 1
[packages.a]
name = "A"
source = "github.com/o/a"
forge = "github"
asset = "KoboRoot.tgz"
requires = ["b"]

[packages.b]
name = "B"
source = "github.com/o/b"
forge = "github"
asset = "KoboRoot.tgz"
conflicts = ["c"]
`)
	registerLocal(t, a, "c", nil, "v1", "")
	code, _, stderr := runInstall(t, a, "a", "--yes")
	if code != exitError || !strings.Contains(stderr, `requires package "b", which conflicts with installed package "c"`) {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, id := range []string{"a", "b"} {
		if _, err := os.Stat(a.paths.PackageFile(id)); !os.IsNotExist(err) {
			t.Errorf("%s: a refused install must write no def", id)
		}
	}
}

// The registry drops a def that requires itself or requires what it conflicts
// with.
func TestManifestDropsBadRequires(t *testing.T) {
	if err := config.ValidateRequires("a", []string{"a"}, nil); err == nil {
		t.Error("self requirement must fail")
	}
	if err := config.ValidateRequires("a", []string{"Bad_Id"}, nil); err == nil {
		t.Error("invalid id must fail")
	}
	if err := config.ValidateRequires("a", []string{"b"}, []string{"b"}); err == nil {
		t.Error("requires and conflicts on the same id must fail")
	}
	if err := config.ValidateRequires("a", []string{"b", "b"}, []string{"c"}); err != nil {
		t.Errorf("valid list failed: %v", err)
	}
}

// update of a named package brings its registered requirement into the run and
// stages the requirement too.
func TestUpdateNamedAddsRequirement(t *testing.T) {
	a := newTestApp(t)
	srv := releaseServer(t, "v2.0.0")
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")
	fakeForgePkg(t, a, host, "base", "")
	if err := config.Save(a.paths.PackageFile("app"), &config.Package{
		Name: "app", Source: host + "/o/app", Forge: config.ForgeForgejo,
		Asset: "KoboRoot.tgz", Requires: []string{"base"},
	}); err != nil {
		t.Fatal(err)
	}
	var code int
	out := captureStdout(t, func() {
		captureStderr(t, func() { code = a.cmdUpdate([]string{"app", "--json"}) })
	})
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	var m jsonMutation
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &m); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.Changed, ","); got != "app,base" {
		t.Errorf("changed = %s, want app,base", got)
	}
	for _, id := range []string{"app", "base"} {
		if a.state.Get(id).StagedVersion != "v2.0.0" {
			t.Errorf("%s not staged", id)
		}
	}
}

// update blocks a new package whose requirement has no def: a skip under --all,
// a failure when named. The message says how to install the requirement.
func TestUpdateBlocksOnMissingRequirement(t *testing.T) {
	setup := func() *App {
		a := newTestApp(t)
		srv := releaseServer(t, "v2.0.0")
		a.client = forge.NewClientWithHTTP(srv.Client())
		host := strings.TrimPrefix(srv.URL, "https://")
		fakeForgePkg(t, a, host, "good", "v1.0.0")
		if err := config.Save(a.paths.PackageFile("app"), &config.Package{
			Name: "app", Source: host + "/o/app", Forge: config.ForgeForgejo,
			Asset: "KoboRoot.tgz", Requires: []string{"ghost"},
		}); err != nil {
			t.Fatal(err)
		}
		return a
	}
	const want = `requires package "ghost", which is not installed — install it first: kpm install ghost --yes; kpm update ghost`

	a := setup()
	var code int
	out := captureStdout(t, func() {
		captureStderr(t, func() { code = a.cmdUpdate([]string{"--all", "--json"}) })
	})
	if code != exitOK {
		t.Fatalf("--all exit %d\n%s", code, out)
	}
	var m jsonMutation
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Skipped) != 1 || m.Skipped[0].ID != "app" || m.Skipped[0].Reason != want {
		t.Errorf("skipped = %+v", m.Skipped)
	}
	if got := strings.Join(m.Changed, ","); got != "good" {
		t.Errorf("changed = %s, want good", got)
	}

	b := setup()
	out = captureStdout(t, func() {
		captureStderr(t, func() { code = b.cmdUpdate([]string{"app", "--json"}) })
	})
	if code != exitError {
		t.Fatalf("named exit %d\n%s", code, out)
	}
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Failed) != 1 || m.Failed[0].Error != want {
		t.Errorf("failed = %+v", m.Failed)
	}
}

// orderByRequires puts each requirement before the packages that need it and
// keeps the order of unrelated packages.
func TestOrderByRequires(t *testing.T) {
	g := depGraph{req: map[string][]string{"a": {"c"}, "c": {"d"}}}
	var pkgs []*config.Package
	for _, id := range []string{"a", "b", "c", "d"} {
		pkgs = append(pkgs, &config.Package{ID: id})
	}
	var got []string
	for _, p := range orderByRequires(pkgs, g) {
		got = append(got, p.ID)
	}
	if strings.Join(got, ",") != "d,c,a,b" {
		t.Errorf("order = %v, want d,c,a,b", got)
	}
	// A loop keeps every member once.
	g = depGraph{req: map[string][]string{"a": {"b"}, "b": {"a"}}}
	got = nil
	for _, p := range orderByRequires(pkgs[:2], g) {
		got = append(got, p.ID)
	}
	if strings.Join(got, ",") != "b,a" {
		t.Errorf("loop order = %v, want b,a", got)
	}
}

// uninstall refuses while an installed package requires the target, and the
// refusal clears once the dependent is gone.
func TestUninstallRefusedWhileRequired(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	f := mkfile(t, sysroot, "usr/local/base/lib.so")
	registerPkg(t, a, "base", config.Uninstall{}, []string{"usr/local/base/lib.so"})
	registerPkg(t, a, "app", config.Uninstall{}, nil)
	p, _ := a.loadPackage("app")
	p.Requires = []string{"base"}
	if err := config.Save(a.paths.PackageFile("app"), p); err != nil {
		t.Fatal(err)
	}

	var code int
	var stderr string
	out := captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = a.cmdUninstall([]string{"base", "--yes", "--json"}) })
	})
	if code != exitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	const want = `"base" is required by installed package "app" — uninstall it first: kpm uninstall app`
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q\nwant %q", stderr, want)
	}
	if _, err := os.Stat(f); err != nil {
		t.Error("a refused uninstall must delete nothing")
	}

	// Remove the dependent, then the uninstall goes through.
	delete(a.state.Packages, "app")
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() {
		captureStderr(t, func() { code = a.cmdUninstall([]string{"base", "--yes"}) })
	})
	if code != exitOK {
		t.Errorf("uninstall after the dependent is gone: exit %d", code)
	}
}

// status --json reports requires, requires_missing and required_by.
func TestStatusJSONRequires(t *testing.T) {
	a := newTestApp(t)
	registerLocal(t, a, "base", nil, "v1", "")
	if err := config.Save(a.paths.PackageFile("app"), &config.Package{
		Name: "app", Source: "github.com/o/app", Forge: config.ForgeGitHub,
		Asset: "KoboRoot.tgz", Requires: []string{"base", "ghost"},
	}); err != nil {
		t.Fatal(err)
	}
	a.state.Get("app").InstalledVersion = "v1"
	out := captureStdout(t, func() { a.cmdStatus([]string{"--json"}) })
	got := lastJSON(t, out)
	for _, want := range []string{
		`"id":"app","installed":"v1"`,
		`"requires":["base","ghost"],"requires_missing":["ghost"],"required_by":[]`,
		`"requires":[],"requires_missing":[],"required_by":["app"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status --json lacks %s:\n%s", want, got)
		}
	}
}
