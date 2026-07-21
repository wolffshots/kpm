package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// package_test.go covers the build tool's tgz assembly and the A2 nm-label check.

func TestWriteAndVerifyTgzMembers(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "KoboRoot.tgz")
	members := []tgzFile{
		{memBin, 0o755, []byte("\x7fELF fake arm binary")},
		{memToml, 0o644, []byte(selfToml)},
		{memNm, 0o644, []byte("menu_item :main :kpm: Status\n")},
		{memSo, 0o755, []byte("\x7fELF fake arm shared object")},
	}
	if err := writeTgz(out, members); err != nil {
		t.Fatal(err)
	}
	// The build tool's own self-check must pass.
	if err := verifyTgz(out); err != nil {
		t.Fatalf("verifyTgz: %v", err)
	}

	// Independently assert exact member list, modes, and ownership.
	want := map[string]int64{memBin: 0o755, memToml: 0o644, memNm: 0o644, memSo: 0o755}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, _ := gzip.NewReader(f)
	tr := tar.NewReader(gr)
	got := map[string]int64{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got[hdr.Name] = hdr.Mode & 0o777
		if hdr.Uid != 0 || hdr.Gid != 0 || hdr.Uname != "root" || hdr.Gname != "root" {
			t.Errorf("%s not root:root (uid %d gid %d)", hdr.Name, hdr.Uid, hdr.Gid)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("member count %d, want %d: %v", len(got), len(want), got)
	}
	for name, mode := range want {
		if got[name] != mode {
			t.Errorf("%s mode %#o, want %#o", name, got[name], mode)
		}
	}
}

// A2: the real res/nm-config must have colon-free menu_item labels.
func TestNmConfigLabelsColonFree(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "res", "nm-config"))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyNmLabels(data); err != nil {
		t.Errorf("shipped res/nm-config fails the label check: %v", err)
	}
}

func TestVerifyNmLabelsRejectsColonLabel(t *testing.T) {
	// The original bug: label "kpm: Check for updates" contains a colon.
	bad := []byte("menu_item :main :kpm: Check for updates :nickel_wifi :autoconnect\n")
	if err := verifyNmLabels(bad); err == nil {
		t.Error("a colon in the label must be rejected")
	}
	// A valid cmd_spawn menu_item and a valid cmd_output menu_item both pass.
	good := []byte("menu_item :main :kpm - Check :nickel_wifi :autoconnect\n" +
		"menu_item :main :kpm - Status :cmd_output :9000 :/bin/kpm status\n")
	if err := verifyNmLabels(good); err != nil {
		t.Errorf("valid labels rejected: %v", err)
	}
}

func TestVerifyTgzRejectsWrongMembers(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "bad.tgz")
	// Missing the nm member.
	if err := writeTgz(out, []tgzFile{
		{memBin, 0o755, []byte("x")},
		{memToml, 0o644, []byte("y")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := verifyTgz(out); err == nil {
		t.Error("verifyTgz should reject an incomplete member list")
	}
}
