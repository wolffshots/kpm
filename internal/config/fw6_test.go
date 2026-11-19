package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// ForLine and AvailableOn (QT6-FIRMWARE6.md section 3c rules 3 to 5).
func TestForLineAndAvailableOn(t *testing.T) {
	p := Package{ID: "nm", Source: "github.com/a/nm", Forge: ForgeGitHub, Asset: "KoboRoot.tgz",
		Checksums: map[string]string{"v1": strings.Repeat("a", 64)}}
	if !p.AvailableOn(Line4) || p.AvailableOn(Line6) {
		t.Error("a tgz package without fw6 is available on line 4 only")
	}
	if got := p.ForLine(Line6); got.Asset != "KoboRoot.tgz" {
		t.Error("ForLine without fw6 must not change the package")
	}
	p.Fw6 = &Fw6{Source: "github.com/b/nm", Asset: "Kobo.tgz"}
	if !p.AvailableOn(Line6) {
		t.Error("fw6 makes the package available on line 6")
	}
	got := p.ForLine(Line6)
	if got.Source != "github.com/b/nm" || got.Forge != ForgeGitHub || got.Asset != "Kobo.tgz" || got.Checksums != nil {
		t.Errorf("ForLine(6) = %+v", got)
	}
	if p.Asset != "KoboRoot.tgz" || p.ForLine(Line4).Asset != "KoboRoot.tgz" {
		t.Error("ForLine must not change the receiver, and line 4 keeps the base")
	}
	live := Package{Install: &Install{Method: MethodFile, Dest: "/mnt/onboard/fonts/x.ttf"}}
	if !live.AvailableOn(Line6) {
		t.Error("a file package installs on line 6 without fw6")
	}
}

func TestFw6Validate(t *testing.T) {
	sum := strings.Repeat("a", 64)
	for _, c := range []struct {
		f       *Fw6
		wantErr string
	}{
		{nil, ""},
		{&Fw6{Asset: "Kobo.tgz"}, ""},
		{&Fw6{Asset: "Kobo.tgz", Forge: ForgeForgejo, Source: "h/o/r", Checksums: map[string]string{"v1": sum}}, ""},
		{&Fw6{}, "requires asset"},
		{&Fw6{Asset: "Kobo.tgz", Source: "nope"}, "host/owner/repo"},
		{&Fw6{Asset: "Kobo.tgz", Forge: "gitlab"}, "forge"},
		{&Fw6{Asset: "Kobo.tgz", Checksums: map[string]string{"v1": "short"}}, "fw6"},
	} {
		err := c.f.Validate()
		if (c.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%+v: err = %v, want %q", c.f, err, c.wantErr)
		}
	}
	rolling := Package{UpdateCheck: UpdateCheckRolling, Fw6: &Fw6{Asset: "K", Checksums: map[string]string{"v1": sum}}}
	if ValidateDefCombination(rolling) == nil {
		t.Error("rolling with fw6 checksums must be refused")
	}
}

// Save writes the [fw6] table and Load reads it back.
func TestSaveLoadFw6(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nm.toml")
	in := &Package{Name: "NickelMenu", Source: "github.com/a/nm", Forge: ForgeGitHub, Asset: "KoboRoot.tgz",
		Fw6: &Fw6{Asset: "Kobo.tgz", Checksums: map[string]string{"v1": strings.Repeat("b", 64)}}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path, "nm")
	if err != nil {
		t.Fatal(err)
	}
	if out.Fw6 == nil || out.Fw6.Asset != "Kobo.tgz" || out.Fw6.Checksums["v1"] != in.Fw6.Checksums["v1"] {
		t.Errorf("round trip fw6 = %+v", out.Fw6)
	}
}
