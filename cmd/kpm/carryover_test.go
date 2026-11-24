package main

// carryover_test.go covers the bundle for the upgrade from 4.x to 6.x
// (QT6-FIRMWARE6.md section 3h).

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/forge"
	"kpm/internal/state"
	"kpm/internal/tgz"
)

const rcKTest = "#!/bin/sh\n\nplatform_detect() { :; }\numount -a -r\n"

type carryFailRT struct{}

func (carryFailRT) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("offline")
}

// carryoverSetup makes a 4.x device with kpm, a package with a 6.x build, a
// package without one, a package that 6.x has built in, and a 4.x rcK.
func carryoverSetup(t *testing.T) (*App, string) {
	t.Helper()
	a := newTestApp(t)
	sys := t.TempDir()
	t.Setenv("KPM_SYSROOT", sys)
	writeFirmware(t, a, fw4)
	host := assetsServer(t, a, "v2.0.0", map[string][]byte{
		"Kobo.tgz":     tgzBytes(t, map[string]string{"imageformats/libpkg-qt6.so": "qt6"}),
		"kpm-Kobo.tgz": tgzBytes(t, map[string]string{"imageformats/libnkpm-qt6.so": "hook", "kpm-seed/VERSION": "v2.0.0"}),
	})
	defs := map[string]*config.Package{
		selfID:  {Name: "kpm", Source: host + "/o/kpm", Forge: config.ForgeForgejo, Asset: "KoboRoot.tgz", Fw6: &config.Fw6{Asset: "kpm-Kobo.tgz"}},
		"pkg":   {Name: "pkg", Source: host + "/o/pkg", Forge: config.ForgeForgejo, Asset: "KoboRoot.tgz", Fw6: &config.Fw6{Asset: "Kobo.tgz"}},
		"old":   {Name: "old", Source: host + "/o/old", Forge: config.ForgeForgejo, Asset: "KoboRoot.tgz"},
		"clock": {Name: "clock", Source: host + "/o/clock", Forge: config.ForgeForgejo, Asset: "KoboRoot.tgz", BuiltinFw6: "the reader clock", Fw6: &config.Fw6{Asset: "Kobo.tgz"}},
	}
	for id, p := range defs {
		if err := config.Save(a.paths.PackageFile(id), p); err != nil {
			t.Fatal(err)
		}
		a.state.Get(id).InstalledVersion = "v1"
	}
	mustWrite(t, filepath.Join(sys, "etc", "init.d", "rcK"), rcKTest)
	if err := os.Chmod(filepath.Join(sys, "etc", "init.d", "rcK"), 0o755); err != nil {
		t.Fatal(err)
	}
	return a, sys
}

func bundleNames(t *testing.T, a *App) []string {
	t.Helper()
	res, err := tgz.Verify(a.paths.CarryoverBundle())
	if err != nil {
		t.Fatal(err)
	}
	return res.Manifest
}

// The bundle holds the 6.x build of kpm and of each installed package that has
// one, kpm last. The state records each carried package with its device paths,
// and the rcK gets the block right after the "#!" line.
func TestCarryoverBuildsBundle(t *testing.T) {
	a, sys := carryoverSetup(t)
	if err := a.refreshCarryover(true); err != nil {
		t.Fatal(err)
	}
	names := bundleNames(t, a)
	want := []string{"imageformats/libpkg-qt6.so", "imageformats/libnkpm-qt6.so", "kpm-seed/VERSION"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("bundle = %v, want %v (kpm last, no old, no clock)", names, want)
	}
	co := a.state.Carryover
	if co == nil || len(co.Packages) != 2 {
		t.Fatalf("carryover = %+v, want kpm and pkg", co)
	}
	if cp := co.Packages["pkg"]; cp.Version != "v2.0.0" || len(cp.Manifest) != 1 || cp.Manifest[0] != "usr/local/Kobo/imageformats/libpkg-qt6.so" {
		t.Errorf("pkg = %+v", cp)
	}
	if sum, size, _ := sha256AndSize(a.paths.CarryoverBundle()); sum != co.SHA256 || size != co.Size {
		t.Error("the record must name the bundle on disk")
	}
	b, _ := os.ReadFile(filepath.Join(sys, "etc", "init.d", "rcK"))
	if string(b) != "#!/bin/sh\n"+rcKBlock+"\nplatform_detect() { :; }\numount -a -r\n" {
		t.Errorf("rcK =\n%s", b)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(filepath.Join(sys, "etc", "init.d", "rcK")); info.Mode().Perm() != 0o755 {
			t.Errorf("rcK mode = %v, want 0755", info.Mode().Perm())
		}
	}
	saved, err := state.Load(a.paths.StateFile())
	if err != nil || saved.Carryover == nil || saved.Carryover.SHA256 != co.SHA256 {
		t.Error("the record must be saved")
	}
}

// An unchanged package set uses no network. An uninstall rebuilds the bundle
// offline from the kept builds. A changed package needs the network, so the
// offline refresh fails and keeps the old bundle.
func TestCarryoverOfflineReuse(t *testing.T) {
	a, _ := carryoverSetup(t)
	if err := a.refreshCarryover(true); err != nil {
		t.Fatal(err)
	}
	a.client = forge.NewClientWithHTTP(&http.Client{Transport: carryFailRT{}})
	first := a.state.Carryover.SHA256
	if err := a.refreshCarryover(true); err != nil {
		t.Fatalf("unchanged set must not need the network: %v", err)
	}
	if a.state.Carryover.SHA256 != first {
		t.Error("an unchanged set must keep the bundle")
	}

	delete(a.state.Packages, "pkg")
	if err := a.refreshCarryover(false); err != nil {
		t.Fatalf("uninstall rebuild: %v", err)
	}
	if names := bundleNames(t, a); strings.Join(names, ",") != "imageformats/libnkpm-qt6.so,kpm-seed/VERSION" {
		t.Errorf("bundle after uninstall = %v", names)
	}
	if _, err := os.Stat(filepath.Join(a.paths.CarryoverDir(), "pkg.tgz")); err == nil {
		t.Error("the build of an uninstalled package must go")
	}

	kept := a.state.Carryover.SHA256
	a.state.Get("pkg").InstalledVersion = "v3"
	err := a.refreshCarryover(false)
	if err == nil || !strings.Contains(err.Error(), "needs a download") {
		t.Fatalf("offline with a change: err = %v", err)
	}
	if sum, _, _ := sha256AndSize(a.paths.CarryoverBundle()); sum != kept || a.state.Carryover.SHA256 != kept {
		t.Error("a failed refresh must keep the old bundle and record")
	}
}

// Without a 6.x build of kpm there is no bundle: nothing could start kpm on 6.x.
func TestCarryoverNeedsKpm(t *testing.T) {
	a, _ := carryoverSetup(t)
	p, _ := config.Load(a.paths.PackageFile(selfID), selfID)
	p.Fw6 = nil
	os.Remove(a.paths.PackageFile(selfID)) // Save keeps the tables of an existing file
	if err := config.Save(a.paths.PackageFile(selfID), p); err != nil {
		t.Fatal(err)
	}
	if err := a.refreshCarryover(true); err == nil {
		t.Fatal("want an error without a kpm 6.x build")
	}
	if _, err := os.Stat(a.paths.CarryoverBundle()); err == nil {
		t.Error("no bundle may be written")
	}
}

// The rcK edit is idempotent, replaces an old block, keeps the rest, and does
// nothing without an rcK.
func TestCarryoverRcKBlock(t *testing.T) {
	a, sys := carryoverSetup(t)
	rck := filepath.Join(sys, "etc", "init.d", "rcK")
	mustWrite(t, rck, "#!/bin/sh\n"+rcKBegin+" old\necho old\n"+rcKEnd+"\numount -a -r\n")
	for i := 0; i < 2; i++ {
		if err := a.ensureRcKBlock(); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(rck)
	got := string(b)
	if strings.Count(got, rcKBegin) != 1 || strings.Contains(got, "echo old") || !strings.HasSuffix(got, "umount -a -r\n") || !strings.Contains(got, rcKBlock) {
		t.Errorf("rcK =\n%s", got)
	}
	if ok, _ := a.rcKHasBlock(); !ok {
		t.Error("rcKHasBlock = false")
	}
	os.Remove(rck)
	if err := a.ensureRcKBlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rck); err == nil {
		t.Error("kpm must not create an rcK")
	}
}

// The first run on 6.x after the upgrade: the bundle was installed, so kpm
// records the carried packages with their 6.x files, the firmware-change check
// keeps them, and a package that was not carried is forgotten as before.
func TestCarryoverAdoptOnUpgrade(t *testing.T) {
	a, sys := carryoverSetup(t)
	if err := a.refreshCarryover(true); err != nil {
		t.Fatal(err)
	}
	a.state.Get("pkg").Manifest = []string{"usr/local/Kobo/imageformats/libpkg.so"}
	a.state.Get("old").Manifest = []string{"usr/local/Kobo/imageformats/libold.so"}
	a.state.Firmware = fw4
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	// The first 6.x boot installed the bundle.
	mustWrite(t, filepath.Join(sys, "usr", "local", "Kobo", "imageformats", "libpkg-qt6.so"), "qt6")
	mustWrite(t, filepath.Join(sys, "usr", "local", "Kobo", "imageformats", "libnkpm-qt6.so"), "hook")
	mustWrite(t, filepath.Join(sys, "usr", "local", "Kobo", "kpm-seed", "VERSION"), "v2.0.0")

	writeFirmware(t, a, fw6)
	b, err := newApp(true)
	if err != nil {
		t.Fatal(err)
	}
	defer b.releaseLock()
	ps := b.state.Get("pkg")
	if ps.InstalledVersion != "v2.0.0" || len(ps.Manifest) != 1 || ps.Manifest[0] != "usr/local/Kobo/imageformats/libpkg-qt6.so" {
		t.Errorf("pkg: installed %q manifest %v, want the carried 6.x build", ps.InstalledVersion, ps.Manifest)
	}
	if ps := b.state.Get("old"); ps.InstalledVersion != "" {
		t.Errorf("old: installed %q, want forgotten", ps.InstalledVersion)
	}
	if ps := b.state.Get(selfID); len(ps.MissingFiles) != 0 {
		t.Errorf("kpm: missing %v, want none", ps.MissingFiles)
	}
	if b.state.Carryover != nil {
		t.Error("the record must go after the adoption")
	}
	if _, err := os.Stat(b.paths.CarryoverDir()); err == nil {
		t.Error("the bundle folder must go")
	}
	if !logContains(t, b, "pkg  carried over to firmware 6.x: v2.0.0") {
		t.Error("the adoption must be logged")
	}
}

// While the bundle still waits in .kobo, kpm keeps the record. A later run
// adopts a package that the firmware-change check forgot in the meantime.
func TestCarryoverAdoptWaitsForInstall(t *testing.T) {
	a, sys := carryoverSetup(t)
	if err := a.refreshCarryover(true); err != nil {
		t.Fatal(err)
	}
	writeFirmware(t, a, fw6)
	pending := filepath.Join(a.paths.Root, ".kobo", "Kobo.tgz")
	data, _ := os.ReadFile(a.paths.CarryoverBundle())
	mustWrite(t, pending, string(data))
	if a.adoptCarryover() || a.state.Carryover == nil {
		t.Fatal("a pending bundle must keep the record")
	}

	os.Remove(pending)
	a.state.Get("pkg").InstalledVersion = "" // forgotten by the firmware-change check
	mustWrite(t, filepath.Join(sys, "usr", "local", "Kobo", "imageformats", "libpkg-qt6.so"), "qt6")
	if !a.adoptCarryover() {
		t.Fatal("want an adoption")
	}
	if ps := a.state.Get("pkg"); ps.InstalledVersion != "v2.0.0" {
		t.Errorf("pkg installed %q, want v2.0.0", ps.InstalledVersion)
	}
	if !logContains(t, a, "kpm  not carried over to firmware 6.x") {
		t.Error("kpm's missing files must be logged")
	}
}
