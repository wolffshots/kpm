package registry

import (
	"strings"
	"testing"

	"kpm/internal/config"
)

const fw6Registry = `
schema_version = 1

[packages.nickelmenu]
name = "NickelMenu"
source = "github.com/pgaskin/NickelMenu"
forge = "github"
asset = "KoboRoot.tgz"
min_kpm = "0.13.0"
tested_fw = "4.46.23852"

  [packages.nickelmenu.fw6]
  source = "github.com/nicoverbruggen/NickelMenu"
  asset = "Kobo.tgz"
  tested_fw = "6.0.279225"

[packages.bad]
name = "Bad"
source = "github.com/o/bad"
forge = "github"
asset = "KoboRoot.tgz"
min_kpm = "0.13.0"

  [packages.bad.fw6]
  source = "github.com/o/bad"
`

// QT6-FIRMWARE6.md section 3c: the [fw6] table parses, a table without an asset
// drops the def, and tested_fw of the line is chosen.
func TestParseManifestFw6(t *testing.T) {
	m, err := ParseManifest([]byte(fw6Registry))
	if err != nil {
		t.Fatal(err)
	}
	def := m.Packages["nickelmenu"]
	if def == nil || def.Fw6 == nil || def.Fw6.Asset != "Kobo.tgz" {
		t.Fatalf("nickelmenu fw6 = %+v", def)
	}
	if len(m.Dropped) != 1 || m.Dropped[0].ID != "bad" || !strings.Contains(m.Dropped[0].Reason, "fw6 requires asset") {
		t.Errorf("dropped = %v, want bad for the missing asset", m.Dropped)
	}
	if got := def.TestedFwFor(config.Line6); got != "6.0.279225" {
		t.Errorf("TestedFwFor(6) = %q", got)
	}
	if got := def.TestedFwFor(config.Line4); got != "4.46.23852" {
		t.Errorf("TestedFwFor(4) = %q", got)
	}
	def.Fw6.TestedFw = ""
	if got := def.TestedFwFor(config.Line6); got != "4.46.23852" {
		t.Errorf("TestedFwFor(6) without fw6.tested_fw = %q, want the base value", got)
	}
}

// The [fw6] table is hashed, its tested_fw is not, and the local projection
// drops tested_fw and never shares the pointer.
func TestHashDefFw6(t *testing.T) {
	base := &PackageDef{Name: "x", Source: "h/o/r", Forge: "github", Asset: "KoboRoot.tgz"}
	withFw6 := *base
	withFw6.Fw6 = &config.Fw6{Asset: "Kobo.tgz", TestedFw: "6.0.279225"}
	h0, _ := HashDef(base)
	h1, _ := HashDef(&withFw6)
	if h0 == h1 {
		t.Error("an [fw6] table must change the hash")
	}
	other := withFw6
	other.Fw6 = &config.Fw6{Asset: "Kobo.tgz", TestedFw: "6.1.0", Checksums: map[string]string{}}
	h2, _ := HashDef(&other)
	if h1 != h2 {
		t.Error("fw6 tested_fw and empty checksums must not change the hash")
	}
	if withFw6.Fw6.TestedFw != "6.0.279225" {
		t.Error("HashDef must not change its input")
	}

	p := withFw6.ToPackage("x", "main", "")
	if p.Fw6 == withFw6.Fw6 || p.Fw6.TestedFw != "" || p.Fw6.Asset != "Kobo.tgz" {
		t.Errorf("ToPackage fw6 = %+v, want a copy without tested_fw", p.Fw6)
	}
	back, _ := HashDef(DefFromPackage(p))
	if back != h1 {
		t.Error("a projected def must hash as its registry def (no drift)")
	}
	if d := FieldDiffs(DefFromPackage(p), &other); len(d) != 0 {
		t.Errorf("FieldDiffs = %v, want none", d)
	}
	other.Fw6 = &config.Fw6{Asset: "Other.tgz"}
	if d := FieldDiffs(DefFromPackage(p), &other); len(d) != 1 || d[0] != "fw6: (changed)" {
		t.Errorf("FieldDiffs = %v, want the fw6 line", d)
	}
}

// builtin_fw6 (QT6-FIRMWARE6.md section 3g) parses, an invalid text drops the
// def, it changes the hash, and a def without it hashes as before.
func TestBuiltinFw6Def(t *testing.T) {
	m, err := ParseManifest([]byte(`schema_version = 1
[packages.nickelclock]
name = "NickelClock"
source = "github.com/shermp/NickelClock"
forge = "github"
asset = "NickelClock-*.zip"
builtin_fw6 = "Nickel shows a clock"

[packages.bad]
name = "Bad"
source = "github.com/o/bad"
forge = "github"
asset = "KoboRoot.tgz"
builtin_fw6 = "two\nlines"
`))
	if err != nil {
		t.Fatal(err)
	}
	def := m.Packages["nickelclock"]
	if def == nil || def.BuiltinFw6 != "Nickel shows a clock" {
		t.Fatalf("nickelclock = %+v", def)
	}
	if len(m.Dropped) != 1 || m.Dropped[0].ID != "bad" {
		t.Errorf("dropped = %v, want bad", m.Dropped)
	}
	plain := *def
	plain.BuiltinFw6 = ""
	h0, _ := HashDef(&plain)
	h1, _ := HashDef(def)
	if h0 == h1 {
		t.Error("builtin_fw6 must change the hash")
	}
	if h, _ := HashDef(DefFromPackage(plain.ToPackage("nickelclock", "main", ""))); h != h0 {
		t.Error("a def without builtin_fw6 must hash as before")
	}
	p := def.ToPackage("nickelclock", "main", "")
	if p.BuiltinFw6 != def.BuiltinFw6 {
		t.Errorf("ToPackage builtin_fw6 = %q", p.BuiltinFw6)
	}
	if h, _ := HashDef(DefFromPackage(p)); h != h1 {
		t.Error("a projected def must hash as its registry def (no drift)")
	}
	if d := FieldDiffs(DefFromPackage(p), &plain); len(d) != 1 || !strings.HasPrefix(d[0], "builtin_fw6:") {
		t.Errorf("FieldDiffs = %v, want the builtin_fw6 line", d)
	}
}
