package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const selfRegistryManifest = `schema_version = 1
[packages.kpm]
name = "kpm"
source = "github.com/wolffshots/kpm"
forge = "github"
asset = "KoboRoot.tgz"
`

// listSelfRegistry runs "kpm list --json" and returns the kpm row's registry.
func listSelfRegistry(t *testing.T, a *App) any {
	t.Helper()
	var code int
	out := captureStdout(t, func() { code = a.cmdList([]string{"--json"}) })
	if code != exitOK {
		t.Fatalf("list --json exit %d:\n%s", code, out)
	}
	var payload struct {
		Packages []map[string]any `json:"packages"`
	}
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &payload); err != nil {
		t.Fatalf("decode list json: %v\n%s", err, out)
	}
	for _, p := range payload.Packages {
		if p["id"] == selfID {
			return p["registry"]
		}
	}
	t.Fatalf("no kpm row in list output:\n%s", out)
	return nil
}

// adoptSelfForTest adopts kpm from registry "main" with install --adopt.
func adoptSelfForTest(t *testing.T, a *App) {
	t.Helper()
	registerSelf(t, a)
	seedRegistry(t, a, "main", selfRegistryManifest)
	var code int
	captureStdout(t, func() { code = a.cmdInstall([]string{"kpm", "--adopt", "--yes"}) })
	if code != exitOK {
		t.Fatalf("install kpm --adopt --yes exit %d", code)
	}
}

// A self-update clobbers kpm.toml with the shipped template, which has no
// registry key. list --json must still name the adopted registry.
func TestSelfRegistrySurvivesTarballClobber(t *testing.T) {
	a := newTestApp(t)
	adoptSelfForTest(t, a)

	if got := a.state.Get(selfID).Registry; got != "main" {
		t.Fatalf("state registry = %q, want main", got)
	}
	if got := listSelfRegistry(t, a); got != "main" {
		t.Fatalf("before clobber: registry = %v, want main", got)
	}

	clobberSelfToml(t, a)
	if got := listSelfRegistry(t, a); got != "main" {
		t.Errorf("after clobber: registry = %v, want main", got)
	}
}

// After the clobber, kpm must still count as registry-managed for sync.
func TestSelfSyncAfterTarballClobber(t *testing.T) {
	a := newTestApp(t)
	adoptSelfForTest(t, a)
	clobberSelfToml(t, a)

	var code int
	out := captureStdout(t, func() { code = a.cmdSync([]string{"kpm"}) })
	if code != exitOK {
		t.Fatalf("sync kpm exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "no registry") || strings.Contains(out, "hand-added") {
		t.Errorf("clobbered kpm must stay registry-managed:\n%s", out)
	}
}

// A device that self-updated under v0.16.0 has the source in state but no
// registry anywhere. seedSelf infers it from the cached registry.
func TestSeedSelfBackfillsRegistryAfterClobber(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.16.1")
	adoptSelfForTest(t, a)
	clobberSelfToml(t, a)
	a.state.Get(selfID).Registry = "" // the v0.16.0 state shape
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	if got := listSelfRegistry(t, a); got != nil {
		t.Fatalf("precondition: registry = %v, want null", got)
	}

	if err := a.seedSelf(); err != nil {
		t.Fatal(err)
	}
	if got := a.state.Get(selfID).Registry; got != "main" {
		t.Errorf("backfilled state registry = %q, want main", got)
	}
	if got := listSelfRegistry(t, a); got != "main" {
		t.Errorf("after backfill: registry = %v, want main", got)
	}
}

// The backfill never guesses: a cached kpm def with another source is not
// the registry kpm was adopted from.
func TestSeedSelfBackfillSkipsMismatchedSource(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.16.1")
	registerSelf(t, a)
	seedRegistry(t, a, "main", selfRegistryManifest)
	adoptInState(t, a, "codeberg.org/other/kpm", "forgejo")

	if err := a.seedSelf(); err != nil {
		t.Fatal(err)
	}
	if got := a.state.Get(selfID).Registry; got != "" {
		t.Errorf("state registry = %q, want empty for a mismatched source", got)
	}
}

// A kpm.toml that still names the registry (adopted before this fix, not yet
// clobbered) is copied into state by seedSelf.
func TestSeedSelfMigratesRegistryFromToml(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.16.1")
	adoptSelfForTest(t, a)
	a.state.Get(selfID).Registry = ""
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	if err := a.seedSelf(); err != nil {
		t.Fatal(err)
	}
	if got := a.state.Get(selfID).Registry; got != "main" {
		t.Errorf("state registry = %q, want main", got)
	}
}
