package main

import (
	"os"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/state"
	"kpm/internal/version"
)

// registerSelf writes kpm's own package def so seedSelf's registration guard
// passes (SELF-VERSION §1).
func registerSelf(t *testing.T, a *App) {
	t.Helper()
	if err := config.Save(a.paths.PackageFile(selfID), &config.Package{Name: "kpm", Source: "", Forge: "forgejo", Asset: "KoboRoot.tgz"}); err != nil {
		t.Fatal(err)
	}
}

// setVersion overrides the compiled-in version for one test and restores it
// (version.Version is a package var — §3).
func setVersion(t *testing.T, v string) {
	t.Helper()
	orig := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = orig })
}

// §3.1: stale record + real binary -> reconcile, refresh InstalledAt, INFO log.
func TestSeedSelfReconcilesStaleRecord(t *testing.T) {
	a := newTestApp(t)
	registerSelf(t, a)
	setVersion(t, "0.4.1")
	ps := a.state.Get(selfID)
	ps.InstalledVersion = "0.3.1"
	ps.InstalledAt = "2020-01-01T00:00:00Z"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	if err := a.seedSelf(); err != nil {
		t.Fatal(err)
	}
	got := a.state.Get(selfID)
	if got.InstalledVersion != "0.4.1" {
		t.Errorf("installed_version = %q, want 0.4.1", got.InstalledVersion)
	}
	if got.InstalledAt == "2020-01-01T00:00:00Z" {
		t.Error("InstalledAt should be refreshed on reconcile")
	}
	log, _ := os.ReadFile(a.paths.LogFile())
	if !strings.Contains(string(log), "self version reconciled 0.3.1 -> 0.4.1 (running binary is authoritative)") {
		t.Errorf("missing reconcile INFO line; log = %q", log)
	}
}

// §3.2: normalized-equal record (v0.4.1 vs 0.4.1) -> the running version,
// with no log and the same InstalledAt. A self-update promotes the tag form.
func TestSeedSelfNormalizedEqualDropsTagPrefix(t *testing.T) {
	a := newTestApp(t)
	registerSelf(t, a)
	setVersion(t, "0.4.1")
	ps := a.state.Get(selfID)
	ps.InstalledVersion = "v0.4.1"
	ps.InstalledAt = "2020-01-01T00:00:00Z"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	if err := a.seedSelf(); err != nil {
		t.Fatal(err)
	}
	got := a.state.Get(selfID)
	if got.InstalledVersion != "0.4.1" {
		t.Errorf("installed_version = %q, want 0.4.1", got.InstalledVersion)
	}
	if got.InstalledAt != "2020-01-01T00:00:00Z" {
		t.Errorf("InstalledAt changed to %q", got.InstalledAt)
	}
	if log, _ := os.ReadFile(a.paths.LogFile()); strings.Contains(string(log), "self version reconciled") {
		t.Errorf("normalized-equal must not log; log = %q", log)
	}
	// A second run is a no-op: no write.
	before, _ := os.ReadFile(a.paths.StateFile())
	if err := a.seedSelf(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(a.paths.StateFile())
	if string(before) != string(after) {
		t.Errorf("second seedSelf changed state:\nbefore=%s\nafter=%s", before, after)
	}
}

// A self-update stages the tag "v0.4.1". After the reboot, reconcile promotes
// it and seedSelf records the running version "0.4.1". The read-only view
// shows the same value and writes nothing.
func TestSelfUpdatePromotionRecordsPlainVersion(t *testing.T) {
	a := newTestApp(t)
	registerSelf(t, a)
	setVersion(t, "0.4.1")
	ps := a.state.Get(selfID)
	ps.InstalledVersion = "0.4.0"
	ps.StagedVersion = "v0.4.1"
	a.state.StagedCommitted = true
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	// No staged tgz on disk: the boot consumed it.
	before, _ := os.ReadFile(a.paths.StateFile())
	a.viewStaged()
	if got := a.state.Get(selfID).InstalledVersion; got != "0.4.1" {
		t.Errorf("view installed_version = %q, want 0.4.1", got)
	}
	after, _ := os.ReadFile(a.paths.StateFile())
	if string(before) != string(after) {
		t.Error("viewStaged wrote state.json")
	}

	st, err := state.Load(a.paths.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	b := &App{paths: a.paths, state: st}
	if err := b.reconcile(); err != nil {
		t.Fatal(err)
	}
	if err := b.seedSelf(); err != nil {
		t.Fatal(err)
	}
	if got := b.state.Get(selfID).InstalledVersion; got != "0.4.1" {
		t.Errorf("installed_version = %q, want 0.4.1", got)
	}
}

// §3.3: dev binary + stale record -> untouched (host sandbox must not pollute).
func TestSeedSelfDevDoesNotReconcile(t *testing.T) {
	a := newTestApp(t)
	registerSelf(t, a)
	setVersion(t, "dev")
	a.state.Get(selfID).InstalledVersion = "0.3.1"
	if err := a.seedSelf(); err != nil {
		t.Fatal(err)
	}
	if got := a.state.Get(selfID).InstalledVersion; got != "0.3.1" {
		t.Errorf("dev binary must not reconcile; got %q", got)
	}
}

// §3.4: dev binary + empty record -> seeds "dev" (today's behavior, regression).
func TestSeedSelfDevSeedsEmptyRecord(t *testing.T) {
	a := newTestApp(t)
	registerSelf(t, a)
	setVersion(t, "dev")
	if err := a.seedSelf(); err != nil {
		t.Fatal(err)
	}
	if got := a.state.Get(selfID).InstalledVersion; got != "dev" {
		t.Errorf("empty record + dev must seed dev; got %q", got)
	}
}

// §3.5: empty record + real version -> seeds (today's behavior, regression).
func TestSeedSelfSeedsEmptyRecord(t *testing.T) {
	a := newTestApp(t)
	registerSelf(t, a)
	setVersion(t, "0.4.1")
	if err := a.seedSelf(); err != nil {
		t.Fatal(err)
	}
	ps := a.state.Get(selfID)
	if ps.InstalledVersion != "0.4.1" {
		t.Errorf("empty record must seed 0.4.1; got %q", ps.InstalledVersion)
	}
	if ps.InstalledAt == "" {
		t.Error("InstalledAt should be set on seed")
	}
}

// §3.6: kpm.toml not registered -> no-op (regression).
func TestSeedSelfUnregisteredNoOp(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.4.1")
	a.state.Get(selfID).InstalledVersion = "0.3.1"
	if err := a.seedSelf(); err != nil {
		t.Fatal(err)
	}
	if got := a.state.Get(selfID).InstalledVersion; got != "0.3.1" {
		t.Errorf("unregistered kpm must be a no-op; got %q", got)
	}
}
