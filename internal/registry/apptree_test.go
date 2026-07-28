package registry

// apptree_test.go pins the 0.11.0 [install] schema's interaction with the def
// hash — the single most dangerous part of the change (APP-TREE.md §12/§13).

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"kpm/internal/config"
)

// goldenDefHashes0100 is HashDef's output for every package in the FROZEN
// registry snapshot at testdata/registry-0.10.0.toml, captured from the 0.10.x
// encoder (a standalone replica of registry.HashDef with no Install field at
// all) BEFORE the field was added.
//
// DO NOT REGENERATE EITHER FILE. The snapshot and these hashes are a fixed
// historical record, not a mirror of the live registry: their whole purpose is to
// prove that adding `Install *config.Install` with omitempty left the canonical
// encoding of every pre-0.11.0 def byte-identical. If a toml bump (or a value
// receiver, or a lost omitempty) ever starts emitting an empty [install] table,
// every synced_def_sha256 on every user's device stops matching, kpm sync reports
// permanent drift and silently skips every package — the 0.5.0 bug, which by
// 0.11.0 had recurred twice. This test is the tripwire, and it must fail HERE
// rather than on 14 packages across every installed device.
var goldenDefHashes0100 = map[string]string{
	"kobo-tweaks":        "4cccda0376561f3b8f27b40e4356caf9054b891f0d58552809b139e60a0641a6",
	"kobocloud":          "7cabddeaa43839f08c1e9215cfa1c8998423c1ccc7e8d8194da11f47a53188cc",
	"kobowifiremote":     "15db94bc882dcd706358174d3c950526736ba3c55f60ca6c1d21559bd63e9f83",
	"kpm":                "321c3b78c933469f8612c388c7c4ce6ef637c0fd7bc62613ead6c60a11b656b5",
	"kscribbler":         "07859dbeaa0d7096f3158d196c9dbee5fb2393a393cb1631e564108879c036c1",
	"nickel-dissolve":    "64ebd5d56406396759c26b3eb7d28fa8978474e49c6cb783a9cff5736b89a9df",
	"nickel-home":        "6d74be4050b5987ffd65af99b81fc1cd9ddd294d1566e4384db66348c5ed8ede",
	"nickel-screensaver": "f77652d5e496b28e57e9dea5081d39b1dd60cf61ed2fad2bfb4cde45734584c5",
	"nickelclock":        "8c524c214be2fffe89d1a3606487f45ce4f903a9ace0639d599be30b98a58a37",
	"nickeldbus":         "d204ea92cdefba757aaf38281d71531f63fa546b722891178dc7184cb490a942",
	"nickelhardcover":    "2055c3e17db990bdc0a0d16fac65c40a8b6efdc78081e674b1dce902fbeb2925",
	"nickelmenu":         "fedca012f60e578bbf1e876f13b71f5460f7b39251717af6142d83cfee443301",
	"nickelnote":         "35cefe76375d1f7c014e7f4e79e0282cfee0fb2f06bfce76e5046a228d025735",
	"nickeltypefix":      "d8de37179ac7c8699bbef344d64449eb7fe9780a7c5599241c15cdb9cca710fc",
}

// TestHashDefNilInstallUnchanged is the critical one (§13). Every shipped def has
// no [install] block, so every one must hash exactly as it did in 0.10.x.
func TestHashDefNilInstallUnchanged(t *testing.T) {
	data, err := os.ReadFile("testdata/registry-0.10.0.toml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(data)
	if err != nil {
		t.Fatalf("the frozen 0.10.0 snapshot must still parse: %v", err)
	}
	if len(m.Dropped) > 0 {
		t.Fatalf("the frozen 0.10.0 snapshot must not drop any def, dropped %v", m.Dropped)
	}
	if len(m.Packages) != len(goldenDefHashes0100) {
		t.Fatalf("snapshot has %d defs, goldens cover %d — the snapshot was regenerated; see this file's comment",
			len(m.Packages), len(goldenDefHashes0100))
	}
	for _, id := range m.SortedIDs() {
		def := m.Packages[id]
		if def.Install != nil {
			t.Fatalf("%s: the frozen snapshot must carry no [install] block", id)
		}
		got, err := HashDef(def)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if want := goldenDefHashes0100[id]; got != want {
			t.Errorf("%s: HashDef = %s, want the 0.10.x golden %s\n"+
				"  a nil *Install must encode to NOTHING; an empty [install] table here means "+
				"every installed package reads as permanently drifted", id, got, want)
		}
	}
}

// TestHashDefNilInstallEmitsNothing is the same invariant stated directly against
// the encoder, so the failure is legible even if the goldens are ever doubted.
func TestHashDefNilInstallEmitsNothing(t *testing.T) {
	bare := &PackageDef{Name: "x", Source: "h/o/r", Forge: "github", Asset: "KoboRoot.tgz"}
	withNil := *bare
	withNil.Install = nil
	h1, err := HashDef(bare)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashDef(&withNil)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("an explicitly-nil Install changed the hash: %s != %s", h1, h2)
	}
	// And the canonical bytes themselves must carry no install key: this is the
	// encoder property HashDef relies on, asserted directly.
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(bare); err != nil {
		t.Fatal(err)
	}
	// "[uninstall]" contains "install", so match the table header exactly.
	if strings.Contains(buf.String(), "[install]") {
		t.Errorf("canonical encoding of an install-less def emits an [install] table:\n%s", buf.String())
	}
}

func TestHashDefIncludesInstall(t *testing.T) {
	base := &PackageDef{Name: "x", Source: "h/o/r", Forge: "github", Asset: "a.zip"}
	withInstall := *base
	withInstall.Install = &config.Install{
		Method: config.MethodAppTree, Root: "koreader", Dest: "/mnt/onboard/.adds/koreader",
	}
	moved := *base
	moved.Install = &config.Install{
		Method: config.MethodAppTree, Root: "koreader", Dest: "/mnt/onboard/.adds/koreader2",
	}
	launcher := *base
	launcher.Install = &config.Install{
		Method: config.MethodAppTree, Root: "koreader", Dest: "/mnt/onboard/.adds/koreader",
		Launcher: &config.Launcher{NmFile: "/mnt/onboard/.adds/nm/koreader", NmEntry: "menu_item:main:K:cmd_spawn:x\n"},
	}

	hBase, _ := HashDef(base)
	hInst, _ := HashDef(&withInstall)
	hMoved, _ := HashDef(&moved)
	hLaunch, _ := HashDef(&launcher)
	if hBase == hInst {
		t.Error("adding an [install] block must change the hash (it is functional, not presentational)")
	}
	if hInst == hMoved {
		t.Error("changing install.dest must change the hash — a dest fix has to reach installed devices")
	}
	if hInst == hLaunch {
		t.Error("adding install.launcher must change the hash — it decides a file kpm writes")
	}

	// normInstall collapses an all-zero table to nil, so a bare `[install]` in a
	// registry hashes identically to omitting it.
	zero := *base
	zero.Install = &config.Install{}
	hZero, _ := HashDef(&zero)
	if hZero != hBase {
		t.Errorf("an all-zero [install] must normalize to nil: %s != %s", hZero, hBase)
	}
	// HashDef must not MUTATE its argument while normalizing.
	if zero.Install == nil {
		t.Error("HashDef normalized in place; it must work on a copy")
	}
}

func TestFieldDiffsReportsInstall(t *testing.T) {
	local := &PackageDef{Name: "x"}
	remote := &PackageDef{Name: "x", Install: &config.Install{
		Method: config.MethodAppTree, Dest: "/mnt/onboard/.adds/koreader",
	}}
	diffs := FieldDiffs(local, remote)
	found := false
	for _, d := range diffs {
		if d == "install: (changed)" {
			found = true
		}
	}
	if !found {
		t.Errorf("FieldDiffs = %v, want an \"install: (changed)\" line", diffs)
	}
	if got := FieldDiffs(remote, remote); len(got) != 0 {
		t.Errorf("identical defs must diff empty, got %v", got)
	}
}

func TestInstallProjectionDeepCopies(t *testing.T) {
	def := &PackageDef{Name: "x", Source: "h/o/r", Forge: "github", Asset: "a.zip",
		Install: &config.Install{
			Method: config.MethodAppTree, Root: "koreader", Dest: "/mnt/onboard/.adds/koreader",
			Launcher: &config.Launcher{NmFile: "/mnt/onboard/.adds/nm/koreader", NmEntry: "menu_item:main:K:cmd_spawn:x\n"},
		}}
	before, _ := HashDef(def)

	pkg := def.ToPackage("koreader", "main", "")
	if pkg.Install == def.Install {
		t.Fatal("ToPackage aliased the Install pointer; it must deep-copy")
	}
	if pkg.Install.Launcher == def.Install.Launcher {
		t.Fatal("ToPackage aliased the Launcher pointer; it must deep-copy")
	}
	pkg.Install.Dest = "/mnt/onboard/.adds/hijacked"
	pkg.Install.Launcher.NmFile = "/mnt/onboard/.adds/nm/hijacked"
	if after, _ := HashDef(def); after != before {
		t.Error("mutating the projected package changed the registry def's hash")
	}

	back := DefFromPackage(pkg)
	if back.Install == pkg.Install || back.Install.Launcher == pkg.Install.Launcher {
		t.Fatal("DefFromPackage aliased the Install/Launcher pointer; it must deep-copy")
	}
	if back.Install.Dest != "/mnt/onboard/.adds/hijacked" {
		t.Errorf("DefFromPackage lost the install block: %+v", back.Install)
	}
	// A nil Install round-trips as nil, never as an empty table.
	if DefFromPackage(&config.Package{}).Install != nil {
		t.Error("a nil Install must project as nil")
	}
	if (&PackageDef{}).ToPackage("x", "r", "").Install != nil {
		t.Error("a nil Install must materialize as nil")
	}
}

func TestParseManifestDropsBadInstall(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"unknown method", `method = "app-tarball"` + "\n" + `dest = "/mnt/onboard/.adds/x"`},
		{"app-tree without dest", `method = "app-tree"`},
		{"dest outside allowlist", `method = "app-tree"` + "\n" + `dest = "/mnt/onboard/books/x"`},
		{"dest is a shared root", `method = "app-tree"` + "\n" + `dest = "/mnt/onboard/.adds"`},
		{"dest is kpm's own tree", `method = "app-tree"` + "\n" + `dest = "/mnt/onboard/.adds/kpm/x"`},
		{"root escapes", `method = "app-tree"` + "\n" + `root = "../etc"` + "\n" + `dest = "/mnt/onboard/.adds/x"`},
		{"root absolute", `method = "app-tree"` + "\n" + `root = "/etc"` + "\n" + `dest = "/mnt/onboard/.adds/x"`},
		{"root trailing slash", `method = "app-tree"` + "\n" + `root = "koreader/"` + "\n" + `dest = "/mnt/onboard/.adds/x"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := []byte(`schema_version = 1
[packages.good]
name = "Good"
source = "github.com/o/r"
forge = "github"
asset = "KoboRoot.tgz"

[packages.bad]
name = "Bad"
source = "github.com/o/r"
forge = "github"
asset = "bad.zip"

  [packages.bad.install]
  ` + strings.ReplaceAll(c.body, "\n", "\n  ") + "\n")
			m, err := ParseManifest(data)
			if err != nil {
				t.Fatalf("the manifest must still parse: %v", err)
			}
			if _, ok := m.Packages["bad"]; ok {
				t.Error("a def with a malformed [install] must be dropped")
			}
			if _, ok := m.Packages["good"]; !ok {
				t.Error("the rest of the manifest must still parse")
			}
			if len(m.Dropped) != 1 || m.Dropped[0].ID != "bad" || m.Dropped[0].Reason == "" {
				t.Errorf("the drop must be reported with a reason, got %+v", m.Dropped)
			}
		})
	}
}

func TestParseManifestDropsBadLauncher(t *testing.T) {
	cases := []struct{ name, nmFile, nmEntry string }{
		{"nm_file outside .adds/nm", "/mnt/onboard/.adds/koreader/nm", "menu_item:main:K:cmd_spawn:x"},
		{"nm_file is kpm's own", "/mnt/onboard/.adds/nm/kpm", "menu_item:main:K:cmd_spawn:x"},
		{"nm_file is kpm's own, FAT32 case", "/mnt/onboard/.adds/nm/KPM", "menu_item:main:K:cmd_spawn:x"},
		{"nm_file is the shared dir", "/mnt/onboard/.adds/nm", "menu_item:main:K:cmd_spawn:x"},
		{"nm_entry missing", "/mnt/onboard/.adds/nm/koreader", ""},
		{"colon in the label", "/mnt/onboard/.adds/nm/koreader", "menu_item:main:KOReader: the reader:cmd_spawn:x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := []byte(`schema_version = 1
[packages.bad]
name = "Bad"
source = "github.com/o/r"
forge = "github"
asset = "bad.zip"

  [packages.bad.install]
  method = "app-tree"
  dest = "/mnt/onboard/.adds/bad"

    [packages.bad.install.launcher]
    nm_file = "` + c.nmFile + `"
    nm_entry = "` + c.nmEntry + `"
`)
			m, err := ParseManifest(data)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if _, ok := m.Packages["bad"]; ok {
				t.Error("a def with a malformed [install.launcher] must be dropped")
			}
		})
	}
}

func TestParseManifestKeepsGoodInstall(t *testing.T) {
	data := []byte(`schema_version = 1
[packages.koreader]
name = "KOReader"
source = "github.com/koreader/koreader"
forge = "github"
asset = "koreader-kobo-*.zip"
min_kpm = "0.11.0"

  [packages.koreader.install]
  method = "app-tree"
  root = "koreader"
  dest = "/mnt/onboard/.adds/koreader"

    [packages.koreader.install.launcher]
    nm_file = "/mnt/onboard/.adds/nm/koreader"
    nm_entry = "menu_item:main:KOReader:cmd_spawn:quiet:exec /mnt/onboard/.adds/koreader/koreader.sh"

  [packages.koreader.uninstall]
  method = "manifest"
  purge_paths = ["/mnt/onboard/.adds/koreader/**"]

[packages.plato]
name = "Plato"
source = "github.com/baskerville/plato"
forge = "github"
asset = "plato-*.zip"
min_kpm = "0.11.0"

  [packages.plato.install]
  method = "app-tree"
  dest = "/mnt/onboard/.adds/plato"
`)
	m, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Dropped) != 0 {
		t.Fatalf("the §16 registry entries must parse cleanly, dropped %+v", m.Dropped)
	}
	k := m.Packages["koreader"]
	if k == nil || k.Install == nil || k.Install.Root != "koreader" ||
		k.Install.Dest != "/mnt/onboard/.adds/koreader" || k.Install.Launcher == nil {
		t.Fatalf("koreader install block not decoded: %+v", k)
	}
	// Plato omits root entirely; "" is the default (the whole zip).
	p := m.Packages["plato"]
	if p == nil || p.Install == nil || p.Install.Root != "" || p.Install.Launcher != nil {
		t.Fatalf("plato install block not decoded: %+v", p)
	}
	if got := k.ToPackage("koreader", "main", "").EffectiveInstallMethod(); got != config.MethodAppTree {
		t.Errorf("EffectiveInstallMethod = %q, want app-tree", got)
	}
}
