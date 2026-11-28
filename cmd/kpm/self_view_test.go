package main

// self_view_test.go covers three device findings of 2026-11-26 (Libra Colour,
// kpm 0.16.0-rc2): the read-only views showed the old self version after a
// manual KoboRoot.tgz install, the `kpm status` header time was UTC, and kpm
// offered an older release of itself as an update.

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"kpm/internal/config"
	"kpm/internal/state"
)

// selfAt registers kpm with the given stored version and saves state, as a
// device has it after kpm 0.15.0 ran and before a manual install of a newer kpm.
func selfAt(t *testing.T, stored string) *App {
	t.Helper()
	a := newTestApp(t)
	registerSelf(t, a)
	a.state.Get(selfID).InstalledVersion = stored
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	return a
}

// Finding 1: after a manual install, a read-only command shows the running
// version for kpm, in memory only: no write and no log (B1).
func TestReadOnlyViewUsesRunningSelfVersion(t *testing.T) {
	a := selfAt(t, "0.15.0")
	setVersion(t, "0.16.0-rc2")
	before, _ := os.ReadFile(a.paths.StateFile())

	b, err := newApp(false)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.state.Get(selfID).InstalledVersion; got != "0.16.0-rc2" {
		t.Errorf("read-only view: kpm installed %q, want 0.16.0-rc2", got)
	}
	if !b.viewChanged {
		t.Error("viewChanged not set, so status prints the stale summary")
	}
	if after, _ := os.ReadFile(a.paths.StateFile()); !bytes.Equal(before, after) {
		t.Error("a read-only command wrote state.json")
	}
	if log, err := os.ReadFile(a.paths.LogFile()); err == nil && bytes.Contains(log, []byte("reconciled")) {
		t.Errorf("a read-only command logged the reconcile: %q", log)
	}
}

// The mutating run reconciles the self version after any firmware change too,
// so the view does not take the staged-slot guard of viewStaged.
func TestReadOnlyViewSelfAfterFirmwareChange(t *testing.T) {
	a := selfAt(t, "0.15.0")
	writeFirmware(t, a, fw4)
	a.state.Firmware = fw4
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	writeFirmware(t, a, fw6)
	setVersion(t, "0.16.0-rc2")
	b, err := newApp(false)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.state.Get(selfID).InstalledVersion; got != "0.16.0-rc2" {
		t.Errorf("after a line change: kpm installed %q, want 0.16.0-rc2", got)
	}
}

// A "dev" binary never overwrites a record (SELF-VERSION section 1.5), and an
// equal record is no change.
func TestReadOnlyViewSelfKeepsRecord(t *testing.T) {
	for _, c := range []struct{ stored, running string }{
		{"0.15.0", "dev"},
		{"0.16.0", "0.16.0"},
	} {
		selfAt(t, c.stored)
		setVersion(t, c.running)
		b, err := newApp(false)
		if err != nil {
			t.Fatal(err)
		}
		if got := b.state.Get(selfID).InstalledVersion; got != c.stored || b.viewChanged {
			t.Errorf("running %s over %s: installed %q changed %v, want %q unchanged", c.running, c.stored, got, b.viewChanged, c.stored)
		}
	}
}

// A self-update records the tag form "v0.16.0". The view shows the running
// version "0.16.0", as seedSelf records it, and writes nothing.
func TestReadOnlyViewSelfDropsTagPrefix(t *testing.T) {
	a := selfAt(t, "v0.16.0")
	setVersion(t, "0.16.0")
	before, _ := os.ReadFile(a.paths.StateFile())
	b, err := newApp(false)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.state.Get(selfID).InstalledVersion; got != "0.16.0" {
		t.Errorf("installed %q, want 0.16.0", got)
	}
	if after, _ := os.ReadFile(a.paths.StateFile()); string(before) != string(after) {
		t.Error("read-only view wrote state.json")
	}
}

// Finding 1, status text: the stored summary says kpm v0.15.0. The view
// changed the self row, so status renders it again and writes nothing.
func TestStatusShowsRunningSelfVersion(t *testing.T) {
	a := selfAt(t, "0.15.0")
	pkgs, err := a.loadPackages()
	if err != nil {
		t.Fatal(err)
	}
	a.paths.WriteStatus(a.buildStatus(pkgs)) // as 0.15.0 check wrote it
	setVersion(t, "0.16.0-rc2")
	before, _ := os.ReadFile(a.paths.StatusFile())

	b, err := newApp(false)
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { b.cmdStatus(nil) })
	if strings.Contains(out, "0.15.0") || !strings.Contains(out, "kpm  0.16.0-rc2") {
		t.Errorf("status after a manual install:\n%s", out)
	}
	if after, _ := os.ReadFile(a.paths.StatusFile()); !bytes.Equal(before, after) {
		t.Error("a read-only command wrote the status file")
	}
}

// Finding 6: state stores UTC, and the status header shows local time like
// kpm.log and the details page.
func TestHumanTimeIsLocal(t *testing.T) {
	orig := time.Local
	time.Local = time.FixedZone("SAST", 2*3600)
	t.Cleanup(func() { time.Local = orig })
	if got := humanTime("2026-11-26T12:09:00Z"); got != "2026-11-26 14:09" {
		t.Errorf("humanTime = %q, want 2026-11-26 14:09", got)
	}
	if got := humanTime(""); got != "never" {
		t.Errorf("humanTime(\"\") = %q, want never", got)
	}
}

// kpm never offers an older release of itself. Other packages and a pin keep
// the old behavior: their tags need not be versions, and a source can move back.
func TestUpdateTargetNeverOffersOlderSelf(t *testing.T) {
	self := &config.Package{ID: selfID}
	other := &config.Package{ID: "nh"}
	cases := []struct {
		name      string
		pkg       *config.Package
		ps        *state.PackageState
		pin       string
		wantAvail bool
	}{
		{"self rc over older latest", self, &state.PackageState{InstalledVersion: "0.16.0-rc2", LatestSeen: "v0.15.0"}, "", false},
		{"self newer latest", self, &state.PackageState{InstalledVersion: "0.15.0", LatestSeen: "v0.16.0"}, "", true},
		{"self rc to its final", self, &state.PackageState{InstalledVersion: "0.16.0-rc2", LatestSeen: "v0.16.0"}, "", true},
		{"self older latest, files missing", self, &state.PackageState{InstalledVersion: "0.16.0-rc2", LatestSeen: "v0.15.0", MissingFiles: []string{"x"}}, "", false},
		{"self pinned older", self, &state.PackageState{InstalledVersion: "0.16.0-rc2", LatestSeen: "v0.15.0"}, "v0.15.0", true},
		{"other package older latest", other, &state.PackageState{InstalledVersion: "v1.0.0", LatestSeen: "v0.9.0"}, "", true},
	}
	for _, c := range cases {
		if _, avail := updateTarget(c.pkg, c.ps, c.pin); avail != c.wantAvail {
			t.Errorf("%s: available %v, want %v", c.name, avail, c.wantAvail)
		}
	}
}

// `kpm update` (and Update all) must skip the older self release too, or
// Update all stages a downgrade that check did not offer.
func TestUpdateSkipsOlderSelf(t *testing.T) {
	a := selfAt(t, "0.16.0-rc2")
	stub := newStubForge(t, "v0.15.0")
	a.client = stub.client
	adoptInState(t, a, stub.host+"/o/r", "forgejo")
	p, err := a.loadPackage(selfID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, skip, _, err := a.resolveAndDownload(p, nil, nil); err != nil || !skip {
		t.Errorf("older self release: skip=%v err=%v, want a skip", skip, err)
	}
}
