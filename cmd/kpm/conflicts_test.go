package main

// conflicts_test.go pins feature B (INTEGRITY.md §4): the install/update-time
// hard refusal for a package that would collide with something already on the
// device. Before it, installing kobo-tweaks would have deleted NickelClock's
// files behind kpm's back, leaving a state entry claiming an installed version
// and a manifest of files that no longer exist — whose uninstall recipe is then
// a silent no-op.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/forge"
)

// tweaksManifest offers kobo-tweaks declaring a conflict with nickelclock, plus
// a nickelclock entry so both are installable from the same registry.
const tweaksManifest = `schema_version = 1
[packages.kobo-tweaks]
name = "Kobo Tweaks"
source = "github.com/o/kobo-tweaks"
forge = "github"
asset = "KoboRoot.tgz"
conflicts = ["nickelclock"]
min_kpm = "0.10.0"

[packages.nickelclock]
name = "NickelClock"
source = "github.com/shermp/NickelClock"
forge = "github"
asset = "KoboRoot.tgz"
`

// registerLocal writes a packages.d def carrying the given conflicts and marks
// it installed (or staged) in state.
func registerLocal(t *testing.T, a *App, id string, conflicts []string, installed, staged string) {
	t.Helper()
	if err := config.Save(a.paths.PackageFile(id), &config.Package{
		Name: id, Source: "github.com/o/" + id, Forge: config.ForgeGitHub,
		Asset: "KoboRoot.tgz", Conflicts: conflicts,
	}); err != nil {
		t.Fatal(err)
	}
	ps := a.state.Get(id)
	ps.InstalledVersion = installed
	ps.StagedVersion = staged
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
}

// installConflictApp seeds a registry offering kobo-tweaks (declaring the
// conflict) with nickelclock already installed locally.
func installConflictApp(t *testing.T) *App {
	t.Helper()
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	seedRegistry(t, a, "main", tweaksManifest)
	registerLocal(t, a, "nickelclock", nil, "v0.4.0", "")
	return a
}

// exit 1, the exact sentence, and NOTHING written to packages.d.
func TestInstallRefusedByConflict(t *testing.T) {
	a := installConflictApp(t)

	var code int
	var stderr string
	out := captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = a.cmdInstall([]string{"kobo-tweaks", "--yes", "--json"}) })
	})
	if code != exitError {
		t.Fatalf("install of a conflicting package must exit %d, got %d\n%s", exitError, code, out)
	}
	const want = `kpm install: "kobo-tweaks" conflicts with installed package "nickelclock" — uninstall it first: kpm uninstall nickelclock`
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q\nwant it to contain %q", stderr, want)
	}
	if _, err := os.Stat(a.paths.PackageFile("kobo-tweaks")); !os.IsNotExist(err) {
		t.Errorf("a refused install must write no def, stat err = %v", err)
	}
	// The refusal rides the existing mutation shape (§6.4).
	var m jsonMutation
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Changed) != 0 || len(m.Failed) != 1 || m.Failed[0].ID != "kobo-tweaks" {
		t.Fatalf("mutation payload = %+v", m)
	}
	// The substring the hook matches on (§4.6/§6.5).
	if !strings.Contains(m.Failed[0].Error, "conflicts with installed package") {
		t.Errorf("failed[0].error = %q", m.Failed[0].Error)
	}
}

// §4.3's central decision: --yes means "I have read the def", never "override a
// safety rule". The hook ALWAYS passes --yes, so a --yes-able refusal would be
// bypassed by the only surface most users have.
func TestInstallConflictNotBypassedByYes(t *testing.T) {
	for _, args := range [][]string{
		{"kobo-tweaks", "--yes"},
		{"kobo-tweaks"},
	} {
		a := installConflictApp(t)
		var code int
		captureStdout(t, func() {
			captureStderr(t, func() { code = a.cmdInstall(args) })
		})
		if code == exitConfirm {
			t.Errorf("%v: a conflict must not be a confirmable prompt", args)
		}
		if code != exitError {
			t.Errorf("%v: exit = %d, want %d", args, code, exitError)
		}
		if _, err := os.Stat(a.paths.PackageFile("kobo-tweaks")); !os.IsNotExist(err) {
			t.Errorf("%v: a refused install must write no def", args)
		}
	}
}

// §4.2: the relation is symmetric — the block fires when only the OTHER package
// declares it, so a curator declares it once on whichever side is natural.
func TestConflictIsSymmetric(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	// The registry def for kobo-tweaks declares NOTHING.
	seedRegistry(t, a, "main", `schema_version = 1
[packages.kobo-tweaks]
name = "Kobo Tweaks"
source = "github.com/o/kobo-tweaks"
forge = "github"
asset = "KoboRoot.tgz"
`)
	// The installed local nickelclock names it instead.
	registerLocal(t, a, "nickelclock", []string{"kobo-tweaks"}, "v0.4.0", "")

	var code int
	var stderr string
	captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = a.cmdInstall([]string{"kobo-tweaks", "--yes"}) })
	})
	if code != exitError {
		t.Fatalf("a reverse-declared conflict must still block, exit = %d", code)
	}
	if !strings.Contains(stderr, `conflicts with installed package "nickelclock"`) {
		t.Errorf("stderr = %q", stderr)
	}
}

// §4.4: the block is a pure function of live state, so uninstalling the blocker
// clears it with no bookkeeping and no state fixup.
func TestConflictClearedByUninstall(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	setVersion(t, "0.10.0")
	seedRegistry(t, a, "main", tweaksManifest)
	mkfile(t, sysroot, "usr/local/nickelclock/lib.so")
	registerPkg(t, a, "nickelclock", config.Uninstall{}, []string{"usr/local/nickelclock/lib.so"})

	var code int
	captureStdout(t, func() {
		captureStderr(t, func() { code = a.cmdInstall([]string{"kobo-tweaks", "--yes"}) })
	})
	if code != exitError {
		t.Fatalf("install should be blocked first, exit = %d", code)
	}

	captureStdout(t, func() { code = a.cmdUninstall([]string{"nickelclock", "--yes"}) })
	if code != exitOK {
		t.Fatalf("uninstall exit = %d, want %d", code, exitOK)
	}
	if _, ok := a.state.Packages["nickelclock"]; ok {
		t.Fatal("uninstall should have deleted the state entry")
	}

	captureStdout(t, func() { code = a.cmdInstall([]string{"kobo-tweaks", "--yes"}) })
	if code != exitOK {
		t.Fatalf("install must succeed once the blocker is uninstalled, exit = %d", code)
	}
	if _, err := os.Stat(a.paths.PackageFile("kobo-tweaks")); err != nil {
		t.Errorf("the def should now be written: %v", err)
	}
}

// §4.3: staged counts as "on the device" — those files land at the next reboot.
func TestStagedConflictBlocksInstall(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	seedRegistry(t, a, "main", tweaksManifest)
	registerLocal(t, a, "nickelclock", nil, "", "v0.4.0") // staged, never installed

	var code int
	captureStdout(t, func() {
		captureStderr(t, func() { code = a.cmdInstall([]string{"kobo-tweaks", "--yes"}) })
	})
	if code != exitError {
		t.Fatalf("a staged blocker must still block, exit = %d", code)
	}
}

// §4.3: `kpm remove` unregisters but leaves the files (and state) in place, so
// the block must persist — the collision is still real.
//
// §4.4: and the printed remedy must be one that WORKS. `kpm remove` leaves state
// while deleting the def, and cmdUninstall requires a local def, so the plain
// "kpm uninstall nickelclock" the refusal used to print dead-ends with
// `package "nickelclock" not registered` — the user follows the instruction
// straight into an error.
func TestRemovedButNotUninstalledStillBlocks(t *testing.T) {
	a := installConflictApp(t)
	var code int
	captureStdout(t, func() { code = a.cmdRemove([]string{"nickelclock"}) })
	if code != exitOK {
		t.Fatalf("remove exit = %d", code)
	}
	if _, err := os.Stat(a.paths.PackageFile("nickelclock")); !os.IsNotExist(err) {
		t.Fatal("remove should have deleted the def")
	}
	if _, ok := a.state.Packages["nickelclock"]; !ok {
		t.Fatal("remove must leave state intact (that is the point of this test)")
	}
	var stderr string
	captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = a.cmdInstall([]string{"kobo-tweaks", "--yes"}) })
	})
	if code != exitError {
		t.Fatalf("a removed-but-not-uninstalled blocker must still block, exit = %d", code)
	}
	// The dead-end command must not be the remedy on offer.
	if strings.Contains(stderr, "uninstall it first: kpm uninstall nickelclock") {
		t.Errorf("the refusal still prints a command that fails with `not registered`:\n%s", stderr)
	}
	for _, want := range []string{
		"kpm install nickelclock --yes",
		`unregistered by "kpm remove" but the files remain`,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal must name the real recovery (%q):\n%s", want, stderr)
		}
	}
	// Proof the printed recipe is real: re-register, then uninstall works.
	captureStdout(t, func() { code = a.cmdInstall([]string{"nickelclock", "--yes"}) })
	if code != exitOK {
		t.Fatalf("re-registering the orphaned blocker must work, exit = %d", code)
	}
}

// §1.2 item 3 / §4.5: once BOTH are installed the collision has already
// happened. Refusing to update either would strand both and undo nothing, so the
// pair is reported — by status and search — and never blocked.
func TestExistingPairIsReportedNotBlocked(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	srv := releaseServer(t, "v2.0.0")
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")
	if err := config.Save(a.paths.PackageFile("kobo-tweaks"), &config.Package{
		Name: "Kobo Tweaks", Source: host + "/o/kobo-tweaks", Forge: config.ForgeForgejo,
		Asset: "KoboRoot.tgz", Conflicts: []string{"nickelclock"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(a.paths.PackageFile("nickelclock"), &config.Package{
		Name: "NickelClock", Source: host + "/o/nickelclock", Forge: config.ForgeForgejo,
		Asset: "KoboRoot.tgz",
	}); err != nil {
		t.Fatal(err)
	}
	a.state.Get("kobo-tweaks").InstalledVersion = "v1.0.0"
	a.state.Get("nickelclock").InstalledVersion = "v1.0.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	// Neither update is blocked; both stage.
	var code int
	captureStdout(t, func() { code = a.cmdUpdate([]string{"--all"}) })
	if code != exitOK {
		t.Fatalf("an already-installed pair must not block update, exit = %d", code)
	}

	// status reports the pair, in both the human summary and --json.
	pkgs, _ := a.loadPackages()
	if status := a.buildStatus(pkgs); !strings.Contains(status,
		"CONFLICT: kobo-tweaks and nickelclock cannot both be installed — uninstall one.") {
		t.Errorf("status should report the pair:\n%s", status)
	}
	out := captureStdout(t, func() { a.cmdStatus([]string{"--json"}) })
	got := lastJSON(t, out)
	if !strings.Contains(got, `"id":"kobo-tweaks","installed":"v1.0.0"`) ||
		!strings.Contains(got, `"conflicts_active":["nickelclock"]`) {
		t.Errorf("status --json should carry conflicts_active: %s", got)
	}
	if !strings.Contains(got, `"conflicts_active":["kobo-tweaks"]`) {
		t.Errorf("status --json must report the pair in BOTH directions: %s", got)
	}
	// search reports both the declaration and the active set.
	out = captureStdout(t, func() { a.cmdSearch([]string{"--json"}) })
	got = lastJSON(t, out)
	if !strings.Contains(got, `"conflicts":["nickelclock"],"conflicts_active":["nickelclock"]`) {
		t.Errorf("search --json should carry both arrays: %s", got)
	}
}

// blockedUpdateApp registers three packages against one fake forge: victim
// (already at the served tag), good (behind, so it stages) and blocked
// (registered, never installed, conflicting with victim).
func blockedUpdateApp(t *testing.T) *App {
	t.Helper()
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	srv := releaseServer(t, "v2.0.0")
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")
	fakeForgePkg(t, a, host, "victim", "v2.0.0")
	fakeForgePkg(t, a, host, "good", "v1.0.0")
	if err := config.Save(a.paths.PackageFile("blocked"), &config.Package{
		Name: "blocked", Source: host + "/o/blocked", Forge: config.ForgeForgejo,
		Asset: "KoboRoot.tgz", Conflicts: []string{"victim"},
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

const wantConflictRefusal = `conflicts with installed package "victim" — uninstall it first: kpm uninstall victim`

// §4.3 second call site: `update` enforces too (a def can be hand-added, or can
// predate the field). Under --all the refusal is a SKIP, not a failure: it is
// permanent — no retry can clear it — so counting it as a failure made every
// "Update all" tap end in an error dialog forever once one registered-but-not-
// installed def existed, with no UI route to drop it.
func TestUpdateAllSkipsConflictWithoutFailing(t *testing.T) {
	a := blockedUpdateApp(t)

	var code int
	var stderr string
	out := captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = a.cmdUpdate([]string{"--all", "--json"}) })
	})
	if code != exitOK {
		t.Fatalf("a conflict skip must not poison an otherwise-successful run (exit %d), got %d\n%s\n%s", exitOK, code, out, stderr)
	}
	var m jsonMutation
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Changed) != 1 || m.Changed[0] != "good" {
		t.Errorf("changed = %v, want [good]", m.Changed)
	}
	if len(m.Failed) != 0 {
		t.Errorf("a permanent refusal must not ride failed[]: %+v", m.Failed)
	}
	if len(m.Skipped) != 1 || m.Skipped[0].ID != "blocked" {
		t.Fatalf("skipped = %+v, want one entry for blocked", m.Skipped)
	}
	if m.Skipped[0].Reason != wantConflictRefusal {
		t.Errorf("skipped[0].reason = %q, want %q", m.Skipped[0].Reason, wantConflictRefusal)
	}
	// Still visible to a human reading the run.
	if !strings.Contains(out, "blocked: skipped — conflicts with installed package") {
		t.Errorf("the skip must be reported in the human output:\n%s", out)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err != nil {
		t.Errorf("the sibling package must still stage: %v", err)
	}
}

// The complement: when the user NAMES the package, the same refusal is a hard
// failure — a direct request that cannot be satisfied, where silence is wrong.
func TestUpdateNamedPackageFailsOnConflict(t *testing.T) {
	a := blockedUpdateApp(t)

	var code int
	out := captureStdout(t, func() {
		captureStderr(t, func() { code = a.cmdUpdate([]string{"blocked", "--json"}) })
	})
	if code != exitError {
		t.Fatalf("an explicitly named blocked package must fail, exit = %d\n%s", code, out)
	}
	var m jsonMutation
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Failed) != 1 || m.Failed[0].ID != "blocked" {
		t.Fatalf("failed = %+v, want one entry for blocked", m.Failed)
	}
	if m.Failed[0].Error != wantConflictRefusal {
		t.Errorf("failed[0].error = %q, want %q", m.Failed[0].Error, wantConflictRefusal)
	}
	if len(m.Skipped) != 0 {
		t.Errorf("a named package must not be silently skipped: %+v", m.Skipped)
	}
	// Mixed explicit run: one staged, one refused → the partial exit 2.
	b := blockedUpdateApp(t)
	captureStdout(t, func() {
		captureStderr(t, func() { code = b.cmdUpdate([]string{"good", "blocked"}) })
	})
	if code != exitPartial {
		t.Errorf("one staged + one named-and-refused must be partial (exit %d), got %d", exitPartial, code)
	}
}

// C1 — THE NEW-DEVICE CASE. Installing both defs is legal (neither package's
// software is on the device yet), so `kpm update --all` is the first moment the
// pair can collide. ps.StagedVersion is only written after the whole resolve
// loop, so without the in-run pending set neither half saw the other: both were
// merged into ONE KoboRoot.tgz, the reboot installed both, and kobo-tweaks
// deleted NickelClock's binary — while kpm printed the CONFLICT line and exited
// 0. This is exactly what "Update all" and KpmProcess::updateAll issue.
func TestUpdateAllStagesOnlyOneHalfOfAConflictingPair(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	srv := releaseServer(t, "v2.0.0")
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")
	// Both registered, NEITHER installed or staged. kobo-tweaks sorts first, so
	// selectPackages hands it over first and the outcome is deterministic.
	for _, id := range []string{"kobo-tweaks", "nickelclock"} {
		var conflicts []string
		if id == "kobo-tweaks" {
			conflicts = []string{"nickelclock"}
		}
		if err := config.Save(a.paths.PackageFile(id), &config.Package{
			Name: id, Source: host + "/o/" + id, Forge: config.ForgeForgejo,
			Asset: "KoboRoot.tgz", Conflicts: conflicts,
		}); err != nil {
			t.Fatal(err)
		}
	}

	var code int
	out := captureStdout(t, func() {
		captureStderr(t, func() { code = a.cmdUpdate([]string{"--all", "--json"}) })
	})
	if code != exitOK {
		t.Fatalf("update --all exit = %d, want %d\n%s", code, exitOK, out)
	}
	var m jsonMutation
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Changed) != 1 || m.Changed[0] != "kobo-tweaks" {
		t.Fatalf("exactly one half of the pair may stage: changed = %v", m.Changed)
	}
	if len(m.Skipped) != 1 || m.Skipped[0].ID != "nickelclock" {
		t.Fatalf("the second half must be skipped, not staged: skipped = %+v", m.Skipped)
	}
	if got := a.state.Get("nickelclock").StagedVersion; got != "" {
		t.Errorf("nickelclock must not be staged, staged_version = %q", got)
	}
	// The merged tgz must carry ONE package's payload, not both.
	if got := a.state.Get("kobo-tweaks").StagedVersion; got != "v2.0.0" {
		t.Errorf("kobo-tweaks staged_version = %q, want v2.0.0", got)
	}
}

// M3 — THE REPORTING/ENFORCING SPLIT, direction 1 (false allow). The registry
// declares the conflict on nickelclock, which was installed BEFORE that curator
// edit and has not been synced. `search --json` read the merged view and greyed
// out Install; `install` read packages.d only and happily wrote the def. The UI
// and the CLI disagreed, and the enforcing side was the weaker one.
func TestInstallHonoursRegistryDeclaredConflict(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	// The curator moved the declaration onto nickelclock (§4.2 says either side
	// is fine); kobo-tweaks declares nothing.
	seedRegistry(t, a, "main", `schema_version = 1
[packages.kobo-tweaks]
name = "Kobo Tweaks"
source = "github.com/o/kobo-tweaks"
forge = "github"
asset = "KoboRoot.tgz"

[packages.nickelclock]
name = "NickelClock"
source = "github.com/shermp/NickelClock"
forge = "github"
asset = "KoboRoot.tgz"
conflicts = ["kobo-tweaks"]
min_kpm = "0.10.0"
`)
	// The STALE local def predates the edit and carries no conflicts.
	registerLocal(t, a, "nickelclock", nil, "v0.4.0", "")

	// search reports it blocked...
	out := captureStdout(t, func() { a.cmdSearch([]string{"--json"}) })
	if !strings.Contains(lastJSON(t, out), `"conflicts_active":["nickelclock"]`) {
		t.Fatalf("search must report the registry-declared conflict: %s", lastJSON(t, out))
	}
	// ...so install must refuse it.
	var code int
	captureStdout(t, func() {
		captureStderr(t, func() { code = a.cmdInstall([]string{"kobo-tweaks", "--yes"}) })
	})
	if code != exitError {
		t.Fatalf("install must enforce the same graph search reports (exit %d)", code)
	}
	if _, err := os.Stat(a.paths.PackageFile("kobo-tweaks")); !os.IsNotExist(err) {
		t.Error("a refused install must write no def")
	}
	// And so must update, reading the same CACHED registry (no network call: the
	// def below points at a local test forge that would happily serve a release).
	srv := releaseServer(t, "v2.0.0")
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")
	if err := config.Save(a.paths.PackageFile("kobo-tweaks"), &config.Package{
		Name: "Kobo Tweaks", Source: host + "/o/kobo-tweaks", Forge: config.ForgeForgejo,
		Asset: "KoboRoot.tgz", Registry: "main",
	}); err != nil {
		t.Fatal(err)
	}
	var stderr string
	out = captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = a.cmdUpdate([]string{"kobo-tweaks", "--json"}) })
	})
	if code != exitError {
		t.Fatalf("update must enforce the registry-declared conflict too, exit = %d\n%s", code, out)
	}
	if !strings.Contains(stderr, "conflicts with installed package") {
		t.Errorf("update should name the conflict:\n%s", stderr)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err == nil {
		t.Error("a blocked package must never stage")
	}
}

// Direction 2 (false block), which a naive union makes WORSE: the curator
// REMOVES the conflict while the local def still carries it. `search` reports
// clear so the hook offers Install — and a local-only enforcement hard-refused,
// giving a generic error dialog with no blocker route, and routing the user
// toward uninstalling a package to satisfy a constraint kpm no longer enforces.
func TestInstallDoesNotBlockOnConflictTheRegistryRemoved(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	seedRegistry(t, a, "main", `schema_version = 1
[packages.kobo-tweaks]
name = "Kobo Tweaks"
source = "github.com/o/kobo-tweaks"
forge = "github"
asset = "KoboRoot.tgz"

[packages.nickelclock]
name = "NickelClock"
source = "github.com/shermp/NickelClock"
forge = "github"
asset = "KoboRoot.tgz"
`)
	// The local def still carries the withdrawn declaration.
	registerLocal(t, a, "nickelclock", []string{"kobo-tweaks"}, "v0.4.0", "")

	out := captureStdout(t, func() { a.cmdSearch([]string{"--json"}) })
	if strings.Contains(lastJSON(t, out), `"conflicts_active":["nickelclock"]`) {
		t.Fatalf("search must follow the registry entry, which no longer declares it: %s", lastJSON(t, out))
	}
	var code int
	captureStdout(t, func() {
		captureStderr(t, func() { code = a.cmdInstall([]string{"kobo-tweaks", "--yes"}) })
	})
	if code != exitOK {
		t.Fatalf("install must not refuse what search says is installable, exit = %d", code)
	}
	if _, err := os.Stat(a.paths.PackageFile("kobo-tweaks")); err != nil {
		t.Errorf("the def should have been written: %v", err)
	}
}

// M5: an unparseable per-file TOML is skipped with err == nil — and nobody
// consulted the skip list. The blocker simply vanished from the graph, so the
// install it should have refused exited 0 with only a WARN in the log.
// Hand-damaged local state is a live scenario here.
func TestConflictGuardRefusesOnUnparseableDef(t *testing.T) {
	a := installConflictApp(t)
	// Truncate the installed blocker's def mid-key.
	if err := os.WriteFile(a.paths.PackageFile("nickelclock"), []byte("name = \"NickelClock\"\nsource = \n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var code int
	var stderr string
	captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = a.cmdInstall([]string{"kobo-tweaks", "--yes"}) })
	})
	if code != exitError {
		t.Fatalf("a partial view of packages.d must not answer the conflict question, exit = %d", code)
	}
	if !strings.Contains(stderr, "nickelclock.toml") || !strings.Contains(stderr, "unreadable") {
		t.Errorf("the refusal must name the unreadable file:\n%s", stderr)
	}
	if _, err := os.Stat(a.paths.PackageFile("kobo-tweaks")); !os.IsNotExist(err) {
		t.Error("nothing may be written while the graph is untrustworthy")
	}
	// update refuses for the same reason — and does it before the network wait,
	// so a torn packages.d never costs a 30s stall first. A second, perfectly
	// readable package is registered so the run has something to select.
	srv := releaseServer(t, "v2.0.0")
	a.client = forge.NewClientWithHTTP(srv.Client())
	fakeForgePkg(t, a, strings.TrimPrefix(srv.URL, "https://"), "other", "v1.0.0")
	out := captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = a.cmdUpdate([]string{"--all"}) })
	})
	if code != exitError {
		t.Fatalf("update must refuse too, exit = %d\n%s", code, out)
	}
	if !strings.Contains(stderr, "nickelclock.toml") {
		t.Errorf("update's refusal must name the unreadable file:\n%s", stderr)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err == nil {
		t.Error("nothing may stage while the graph is untrustworthy")
	}
	// It is one repair from resolution: with a readable def the ordinary conflict
	// refusal applies again — the guard is loud, not wedged.
	registerLocal(t, a, "nickelclock", nil, "v0.4.0", "")
	captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = a.cmdInstall([]string{"kobo-tweaks", "--yes"}) })
	})
	if code != exitError {
		t.Fatalf("with a readable def the ordinary conflict refusal applies, exit = %d", code)
	}
	if strings.Contains(stderr, "unreadable") {
		t.Errorf("the unreadable refusal must clear once the def parses:\n%s", stderr)
	}
	if !strings.Contains(stderr, "conflicts with installed package") {
		t.Errorf("the ordinary refusal must be back:\n%s", stderr)
	}
}

// The other half of the M5 rule, and the reason it is not simply "anything
// LoadAll skipped": a file whose STEM is not a valid package id was never
// registered, so it can carry no `conflicts` declaration and cannot weaken the
// guard. Refusing on it protects against nothing while blocking every update —
// and a stray `NickelMenu.toml` or `README.toml` in packages.d is a far likelier
// accident than a truncated def.
func TestStrayFilenameDoesNotBlockConflictGatedCommands(t *testing.T) {
	a := installConflictApp(t)
	srv := releaseServer(t, "v2.0.0")
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")
	fakeForgePkg(t, a, host, "other", "v1.0.0")
	// The fixture registers nickelclock against github.com, and this is the one
	// conflict test whose update actually RUNS (the others refuse first). Re-point
	// it at the fake forge — already at the tag the server serves, so it resolves
	// and reports no update — or the run waits out the 30s network budget against
	// a host the test client's cert pool cannot verify, and then fails.
	fakeForgePkg(t, a, host, "nickelclock", "v2.0.0")
	// Perfectly valid TOML under a filename that can never be a package id.
	// Neither stem may case-fold onto a registered def: on a case-INsensitive
	// filesystem (Windows, default macOS) a stray `NickelClock.toml` written here
	// would land on top of `nickelclock.toml` and quietly replace the def whose
	// inertness this test exists to prove.
	for _, name := range []string{"NickelMenu.toml", "README.toml"} {
		if err := os.WriteFile(a.paths.PackagesDir()+"/"+name,
			[]byte("name = \"stray\"\nconflicts = [\"other\"]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Guard the guard: on either filesystem the registered def must have survived
	// the strays, or the assertions below prove nothing.
	if p, err := config.Load(a.paths.PackageFile("nickelclock"), "nickelclock"); err != nil ||
		!config.SourceConfigured(p.Source) {
		t.Fatalf("a stray filename must not displace nickelclock.toml (err = %v, pkg = %+v)", err, p)
	}

	// update proceeds: the stray files are inert, not a hole in the graph. (Note
	// they even declare a conflict — which must be ignored, because an
	// unregistered file's declarations were never in force to begin with.)
	var code int
	var stderr string
	out := captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = a.cmdUpdate([]string{"--all"}) })
	})
	if code != exitOK {
		t.Fatalf("a stray filename must not block update, exit = %d\n%s\n%s", code, out, stderr)
	}
	if strings.Contains(stderr, "cannot check conflicts") {
		t.Errorf("no refusal may mention an unregistered file:\n%s", stderr)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err != nil {
		t.Errorf("the registered package should still stage: %v", err)
	}

	// install proceeds to its ORDINARY answer too — here the real conflict
	// refusal, which proves the guard is running rather than short-circuited.
	captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = a.cmdInstall([]string{"kobo-tweaks", "--yes"}) })
	})
	if code != exitError || !strings.Contains(stderr, "conflicts with installed package") {
		t.Fatalf("install must reach the real conflict check, exit = %d\n%s", code, stderr)
	}
	if strings.Contains(stderr, "cannot check conflicts") {
		t.Errorf("install must not refuse over an unregistered file:\n%s", stderr)
	}
	// The files are still surfaced — ignored is not silent.
	if !logContains(t, a, "not a package definition") {
		t.Error("a stray .toml must still be WARNed, just not treated as a gap")
	}
}

// §4.6: the exact multi-blocker wording, and normalizeConflicts' contract.
func TestConflictHelpers(t *testing.T) {
	if got := conflictRefusalText([]string{"nickelclock"}, nil); got !=
		`conflicts with installed package "nickelclock" — uninstall it first: kpm uninstall nickelclock` {
		t.Errorf("single = %q", got)
	}
	if got := conflictRefusalText([]string{"nickelclock", "nickelnote"}, nil); got !=
		`conflicts with installed packages "nickelclock", "nickelnote" — uninstall them first: kpm uninstall nickelclock; kpm uninstall nickelnote` {
		t.Errorf("plural = %q", got)
	}
	got := normalizeConflicts([]string{"b", "a", "b", "self", "Bad_Id", ""}, "self")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("normalizeConflicts = %v, want [a b] (sorted, deduped, self and invalid dropped)", got)
	}
	if normalizeConflicts(nil, "x") == nil {
		t.Error("normalizeConflicts must never return nil (the JSON array must not be null)")
	}
}
