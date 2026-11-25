package main

// staged_view_test.go covers the read-only view of a staged install after the
// boot that installed it (bug of 2026-11-25: the UI showed "reboot pending"
// after the reboot). The UI opens with `kpm search --json`, a read-only
// command. Read-only commands do not run reconcile, so they must still report
// the outcome of the boot, without a write.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kpm/internal/config"
)

// stageOnDisk stages nh v2 over v1 through the real merge and commit path and
// saves state.json, as `kpm update nh` does before the reboot.
// A non-empty fw is written to .kobo/version and recorded in state first.
func stageOnDisk(t *testing.T, fw string) *App {
	t.Helper()
	a := newTestApp(t)
	if fw != "" {
		writeFirmware(t, a, fw)
		a.state.Firmware = fw
	}
	a.state.Get("nh").InstalledVersion = "v1"
	cache := filepath.Join(a.paths.CacheDir(), "nh-v2.tgz")
	tinyTgz(t, cache, "./usr/local/nh/file", "v2")
	a.stageForTest(t, []resolved{{pkg: &config.Package{ID: "nh"}, tag: "v2", cache: cache, manifest: []string{"usr/local/nh/file"}}})
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	return a
}

// After the boot installer consumed the tgz, a read-only command sees the
// package as installed, and state.json stays as it was (B1).
func TestReadOnlyViewPromotesConsumedStaging(t *testing.T) {
	a := stageOnDisk(t, "")
	os.Remove(a.paths.StagedTgz()) // the boot installer ran
	before, _ := os.ReadFile(a.paths.StateFile())

	b, err := newApp(false)
	if err != nil {
		t.Fatal(err)
	}
	ps := b.state.Get("nh")
	if ps.StagedVersion != "" || ps.InstalledVersion != "v2" {
		t.Errorf("after the install boot: staged %q installed %q, want \"\" and v2", ps.StagedVersion, ps.InstalledVersion)
	}
	if s := b.stagedSummary(); s.Count != 0 {
		t.Errorf("staged summary count = %d, want 0", s.Count)
	}
	after, _ := os.ReadFile(a.paths.StateFile())
	if !bytes.Equal(before, after) {
		t.Error("a read-only command wrote state.json")
	}
	if _, err := os.Stat(a.paths.LogFile()); err == nil {
		if log, _ := os.ReadFile(a.paths.LogFile()); bytes.Contains(log, []byte("INSTALLED")) {
			t.Error("a read-only command logged INSTALLED")
		}
	}

	// The next mutating command records the promotion for real.
	c, err := newApp(true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.releaseLock()
	if ps := c.state.Get("nh"); ps.StagedVersion != "" || ps.InstalledVersion != "v2" {
		t.Errorf("mutating run: staged %q installed %q", ps.StagedVersion, ps.InstalledVersion)
	}
}

// Before the reboot the tgz is still in the slot: the view keeps it staged.
func TestReadOnlyViewKeepsPendingStaging(t *testing.T) {
	stageOnDisk(t, "")
	b, err := newApp(false)
	if err != nil {
		t.Fatal(err)
	}
	if ps := b.state.Get("nh"); ps.StagedVersion != "v2" || ps.InstalledVersion != "v1" {
		t.Errorf("before the reboot: staged %q installed %q, want v2 and v1", ps.StagedVersion, ps.InstalledVersion)
	}
}

// After a firmware line change the staged slot moves (QT6-FIRMWARE6.md section
// 3f), so a missing new-line tgz says nothing about the old one. The view must
// not promote: only a mutating run (dropOldLineStaged) settles it.
func TestReadOnlyViewSkipsFirmwareChange(t *testing.T) {
	a := stageOnDisk(t, fw4) // KoboRoot.tgz, the 4.x slot, stays in place
	writeFirmware(t, a, fw6) // the 6.x slot Kobo.tgz is absent
	b, err := newApp(false)
	if err != nil {
		t.Fatal(err)
	}
	if ps := b.state.Get("nh"); ps.StagedVersion != "v2" || ps.InstalledVersion != "v1" {
		t.Errorf("after a line change: staged %q installed %q, want v2 and v1 (not promoted)", ps.StagedVersion, ps.InstalledVersion)
	}
}

// The text of `kpm status` is the summary that the last check or update wrote.
// After the install boot that summary still says STAGED. When the view settled
// the staging, status renders a fresh summary from the viewed state.
func TestStatusAfterInstallBootIsFresh(t *testing.T) {
	a := stageOnDisk(t, "")
	def := &config.Package{Name: "nh", Source: "localhost/o/nh", Forge: config.ForgeForgejo, Asset: "KoboRoot.tgz"}
	if err := config.Save(a.paths.PackageFile("nh"), def); err != nil {
		t.Fatal(err)
	}
	pkgs, err := a.loadPackages()
	if err != nil {
		t.Fatal(err)
	}
	a.paths.WriteStatus(a.buildStatus(pkgs)) // as update writes it
	if !strings.Contains(a.paths.ReadStatus(), "STAGED") {
		t.Fatalf("setup: summary has no STAGED line:\n%s", a.paths.ReadStatus())
	}
	os.Remove(a.paths.StagedTgz()) // the boot installer ran
	before, _ := os.ReadFile(a.paths.StatusFile())

	b, err := newApp(false)
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { b.cmdStatus(nil) })
	if strings.Contains(out, "STAGED") || !strings.Contains(out, "up to date") {
		t.Errorf("status after the install boot:\n%s", out)
	}
	if after, _ := os.ReadFile(a.paths.StatusFile()); !bytes.Equal(before, after) {
		t.Error("a read-only command wrote the status file")
	}
}

// Without a settled staging, status prints the stored summary as before: it
// can hold a message that state cannot rebuild, for example a refusal.
func TestStatusKeepsStoredSummary(t *testing.T) {
	a := newTestApp(t)
	a.paths.WriteStatus("kpm: refusing to stage — test")
	b, err := newApp(false)
	if err != nil {
		t.Fatal(err)
	}
	if out := captureStdout(t, func() { b.cmdStatus(nil) }); !strings.Contains(out, "refusing to stage") {
		t.Errorf("status lost the stored summary:\n%s", out)
	}
}
