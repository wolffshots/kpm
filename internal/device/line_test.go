package device

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLineOf(t *testing.T) {
	for fw, want := range map[string]int{
		"4.46.23852": Line4, "6.0.279225": Line6, "7.1.0": Line6,
		"5.0.0": 0, "": 0, "x.1": 0, " 4.45.1 ": Line4,
	} {
		if got := LineOf(fw); got != want {
			t.Errorf("LineOf(%q) = %d, want %d", fw, got, want)
		}
	}
}

// Line (QT6-FIRMWARE6.md section 3a): the version file first, then the Qt 6
// core library, then line 4.
func TestLine(t *testing.T) {
	root := t.TempDir()
	sys := t.TempDir()
	t.Setenv("KPM_SYSROOT", sys)
	p := Paths{Root: root}
	if p.Line() != Line4 {
		t.Error("the default must be line 4")
	}
	if filepath.Base(p.StagedTgz()) != "KoboRoot.tgz" {
		t.Errorf("line 4 slot = %s", p.StagedTgz())
	}
	qt6 := p.HostPath(Qt6Core)
	if err := os.MkdirAll(filepath.Dir(qt6), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(qt6, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if p.Line() != Line6 {
		t.Error("a Qt 6 core library without a version file must give line 6")
	}
	if filepath.Base(p.StagedTgz()) != "Kobo.tgz" {
		t.Errorf("line 6 slot = %s", p.StagedTgz())
	}
	if err := os.MkdirAll(filepath.Join(root, ".kobo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.VersionFile(), []byte("N0,4.1.15,4.46.23852,4.1.15\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p.Line() != Line4 {
		t.Error("the version file must decide over the library")
	}
}
