package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/state"
)

func TestUpdateTarget(t *testing.T) {
	pkg := &config.Package{ID: "nh"}
	cases := []struct {
		name       string
		ps         *state.PackageState
		pin        string
		wantTarget string
		wantAvail  bool
	}{
		{"latest available", &state.PackageState{InstalledVersion: "v1", LatestSeen: "v2"}, "", "v2", true},
		{"up to date", &state.PackageState{InstalledVersion: "v2", LatestSeen: "v2"}, "", "v2", false},
		{"already staged", &state.PackageState{InstalledVersion: "v1", LatestSeen: "v2", StagedVersion: "v2"}, "", "v2", false},
		{"pin differs (downgrade)", &state.PackageState{InstalledVersion: "v2", LatestSeen: "v2"}, "v1", "v1", true},
		{"no info", &state.PackageState{}, "", "", false},
	}
	for _, c := range cases {
		target, avail := updateTarget(pkg, c.ps, c.pin)
		if target != c.wantTarget || avail != c.wantAvail {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", c.name, target, avail, c.wantTarget, c.wantAvail)
		}
	}
}

// tinyTgz writes a minimal valid tgz with one entry.
func tinyTgz(t *testing.T, path, name, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}
	tw.WriteHeader(hdr)
	tw.Write([]byte(data))
	tw.Close()
	gw.Close()
	f.Close()
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	t.Setenv("KPM_ROOT", root)
	p := device.New()
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p.StagedTgz()), 0o755); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Load(p.StateFile())
	return &App{paths: p, state: st}
}

func TestStageMergesKpmLast(t *testing.T) {
	a := newTestApp(t)
	cacheA := filepath.Join(a.paths.CacheDir(), "nh-v1.tgz")
	cacheK := filepath.Join(a.paths.CacheDir(), "kpm-v1.tgz")
	tinyTgz(t, cacheA, "./usr/shared", "nh")
	tinyTgz(t, cacheK, "./usr/shared", "kpm")

	targets := []resolved{
		{pkg: &config.Package{ID: "nh"}, tag: "v1", cache: cacheA},
		{pkg: &config.Package{ID: selfID}, tag: "v1", cache: cacheK},
	}
	dups, err := a.stage(targets)
	if err != nil {
		t.Fatal(err)
	}
	if len(dups) != 1 || dups[0] != "usr/shared" {
		t.Errorf("dups = %v", dups)
	}
	// kpm must be written last so it wins.
	f, err := os.Open(a.paths.StagedTgz())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, _ := gzip.NewReader(f)
	tr := tar.NewReader(gr)
	var last string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		buf := make([]byte, hdr.Size)
		tr.Read(buf)
		last = string(buf)
	}
	if last != "kpm" {
		t.Errorf("kpm should be staged last, got %q", last)
	}
}

func TestGuardExistingTgz(t *testing.T) {
	a := newTestApp(t)
	// No tgz -> ok.
	if err := a.guardExistingTgz(); err != nil {
		t.Fatalf("no tgz should be ok: %v", err)
	}
	// Foreign tgz whose content doesn't match the recorded hash -> refuse (B4).
	os.WriteFile(a.paths.StagedTgz(), []byte("manual"), 0o644)
	if err := a.guardExistingTgz(); err == nil {
		t.Error("foreign tgz should be refused")
	}
	// Record the actual hash/size of the on-disk tgz -> now it's "ours".
	sum, size, err := sha256AndSize(a.paths.StagedTgz())
	if err != nil {
		t.Fatal(err)
	}
	a.state.StagedSHA256 = sum
	a.state.StagedSize = size
	if err := a.guardExistingTgz(); err != nil {
		t.Errorf("our own staged tgz (hash match) should be allowed: %v", err)
	}
	// A hash mismatch (content changed) -> refuse again (B4).
	os.WriteFile(a.paths.StagedTgz(), []byte("tampered-or-foreign"), 0o644)
	if err := a.guardExistingTgz(); err == nil {
		t.Error("hash mismatch should be refused")
	}
}
