package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"kpm/internal/config"
)

// §9.8: min_kpm numeric dotted compare table.
func TestMinKpmSatisfied(t *testing.T) {
	cases := []struct {
		running, required string
		want              bool
	}{
		{"0.3.0", "", true},       // no requirement
		{"0.3.0", "0.2.0", true},  // newer
		{"0.2.0", "0.2.0", true},  // equal
		{"0.1.9", "0.2.0", false}, // older
		{"v0.3.0", "0.2.0", true}, // leading v stripped
		{"0.3.0", "v0.3.0", true}, // leading v on requirement
		{"1.0", "1.0.0", true},    // missing segment = 0
		{"1.0.0", "1.0.1", false}, // patch older
		{"0.10.0", "0.9.0", true}, // numeric, not lexical (10 > 9)
		{"2", "1.5.0", true},      // major dominates
	}
	for _, c := range cases {
		if got := MinKpmSatisfied(c.running, c.required); got != c.want {
			t.Errorf("MinKpmSatisfied(%q, %q) = %v, want %v", c.running, c.required, got, c.want)
		}
	}
}

// §9.3: schema_version gate rejects missing and unsupported versions.
func TestParseManifestSchemaGate(t *testing.T) {
	if _, err := ParseManifest([]byte(`[packages.x]` + "\n")); err == nil {
		t.Error("missing schema_version must be refused")
	}
	if _, err := ParseManifest([]byte("schema_version = 2\n")); err == nil {
		t.Error("unsupported schema_version must be refused")
	}
	m, err := ParseManifest([]byte("schema_version = 1\n[packages.nickelmenu]\nname = \"NickelMenu\"\n"))
	if err != nil {
		t.Fatalf("valid schema should parse: %v", err)
	}
	if _, ok := m.Packages["nickelmenu"]; !ok {
		t.Error("package should be parsed")
	}
}

// Invalid package ids are dropped from a parsed manifest.
func TestParseManifestDropsInvalidIDs(t *testing.T) {
	m, err := ParseManifest([]byte("schema_version = 1\n[packages.Bad_Id]\nname=\"x\"\n[packages.good]\nname=\"y\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Packages["Bad_Id"]; ok {
		t.Error("invalid id should be dropped")
	}
	if _, ok := m.Packages["good"]; !ok {
		t.Error("valid id should remain")
	}
}

// §9.6: conflict resolution — earliest registry wins; the rest are shadowed.
func TestMergeConflictResolution(t *testing.T) {
	main := &Manifest{SchemaVersion: 1, Packages: map[string]*PackageDef{
		"shared": {Name: "FromMain"}, "onlymain": {Name: "M"},
	}}
	extra := &Manifest{SchemaVersion: 1, Packages: map[string]*PackageDef{
		"shared": {Name: "FromExtra"}, "onlyextra": {Name: "E"},
	}}
	entries, conflicts := Merge([]NamedManifest{
		{Name: "main", Manifest: main},
		{Name: "extra", Manifest: extra},
	})
	if entries["shared"].Registry != "main" || entries["shared"].Def.Name != "FromMain" {
		t.Errorf("earliest registry should win: %+v", entries["shared"])
	}
	if entries["onlyextra"].Registry != "extra" {
		t.Error("non-conflicting entry from later registry should be present")
	}
	if len(conflicts) != 1 || conflicts[0].ID != "shared" || conflicts[0].Winner != "main" {
		t.Fatalf("conflicts = %+v", conflicts)
	}
	if len(conflicts[0].Shadowed) != 1 || conflicts[0].Shadowed[0] != "extra" {
		t.Errorf("shadowed = %v", conflicts[0].Shadowed)
	}
}

// §9.5/§9.13: config.toml round-trips unknown top-level and per-registry keys.
func TestConfigUnknownFieldRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := `github_token = "secret"

[[registries]]
name = "main"
url = "github.com/o/kobo-registry"
ref = "main"
path = "registry.toml"
forge = "github"
custom_reg_key = "keepme"
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Registries) != 1 || cfg.Registries[0].Name != "main" || cfg.Registries[0].Forge != "github" {
		t.Fatalf("load mismatch: %+v", cfg.Registries)
	}
	// Add a second registry and save.
	cfg.Registries = append(cfg.Registries, Registry{
		Name: "extra", URL: "codeberg.org/o/r", Ref: "main", Path: "registry.toml", Forge: "forgejo",
	})
	b, err := MarshalConfig(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if _, err := toml.Decode(string(b), &raw); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if raw["github_token"] != "secret" {
		t.Errorf("unknown top-level key not preserved: %v", raw["github_token"])
	}
	arr, _ := raw["registries"].([]map[string]any)
	if len(arr) != 2 {
		t.Fatalf("expected 2 registries, got %v", raw["registries"])
	}
	if arr[0]["custom_reg_key"] != "keepme" {
		t.Errorf("unknown per-registry key not preserved: %v", arr[0])
	}
	if arr[1]["name"] != "extra" {
		t.Errorf("appended registry missing: %v", arr[1])
	}
}

// §2: raw URL builder per forge.
func TestRawURL(t *testing.T) {
	gh, err := RawURL(config.ForgeGitHub, "github.com/o/r", "main", "registry.toml")
	if err != nil {
		t.Fatal(err)
	}
	if gh != "https://raw.githubusercontent.com/o/r/main/registry.toml" {
		t.Errorf("github raw url = %q", gh)
	}
	fj, err := RawURL(config.ForgeForgejo, "codeberg.org/o/r", "v1", "sub/registry.toml")
	if err != nil {
		t.Fatal(err)
	}
	if fj != "https://codeberg.org/o/r/raw/branch/v1/sub/registry.toml" {
		t.Errorf("forgejo raw url = %q", fj)
	}
	if _, err := RawURL(config.ForgeGitHub, "bad", "main", "registry.toml"); err == nil {
		t.Error("a non host/owner/repo url should error")
	}
}

// B2: Forgejo yields both a branch and a tag candidate (in that order); GitHub
// yields a single raw form.
func TestRawURLsCandidates(t *testing.T) {
	fj, err := RawURLs(config.ForgeForgejo, "codeberg.org/o/r", "v1", "registry.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(fj) != 2 {
		t.Fatalf("forgejo should yield 2 candidates, got %v", fj)
	}
	if fj[0] != "https://codeberg.org/o/r/raw/branch/v1/registry.toml" {
		t.Errorf("first candidate should be the branch form: %q", fj[0])
	}
	if fj[1] != "https://codeberg.org/o/r/raw/tag/v1/registry.toml" {
		t.Errorf("second candidate should be the tag form: %q", fj[1])
	}
	gh, err := RawURLs(config.ForgeGitHub, "github.com/o/r", "main", "registry.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(gh) != 1 {
		t.Errorf("github should yield a single candidate, got %v", gh)
	}
}

// HashDef is stable and pin-agnostic; ToPackage/DefFromPackage round-trip its hash.
func TestHashDefStableAndPinAgnostic(t *testing.T) {
	def := &PackageDef{Name: "X", Source: "h/o/r", Forge: "forgejo", Asset: "KoboRoot.tgz", MinKpm: "0.2.0"}
	h1, err := HashDef(def)
	if err != nil {
		t.Fatal(err)
	}
	// Convert to a local package (with a pin) and back; hash must be unchanged.
	pkg := def.ToPackage("x", "main", "v1.0.0")
	h2, err := HashDef(DefFromPackage(pkg))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("hash should ignore pin/registry: %q != %q", h1, h2)
	}
}

// CONFIG.md §2: a well-formed [[configs]] block parses and projects through
// ToPackage/DefFromPackage, and a bad format or unsafe path drops the whole def.
func TestParseManifestConfigs(t *testing.T) {
	man := `schema_version = 1
[packages.nickelclock]
name = "NickelClock"

  [[packages.nickelclock.configs]]
  name = "Settings"
  path = "/mnt/onboard/.adds/nickelclock/settings.ini"
  format = "ini"
  reload = "reboot"
`
	m, err := ParseManifest([]byte(man))
	if err != nil {
		t.Fatal(err)
	}
	def, ok := m.Packages["nickelclock"]
	if !ok {
		t.Fatal("nickelclock should parse")
	}
	if len(def.Configs) != 1 || def.Configs[0].Name != "Settings" || def.Configs[0].Format != "ini" {
		t.Fatalf("configs not parsed: %+v", def.Configs)
	}
}

func TestParseManifestDropsBadConfigFormat(t *testing.T) {
	man := `schema_version = 1
[packages.bad]
name = "Bad"

  [[packages.bad.configs]]
  name = "X"
  path = "/mnt/onboard/.adds/bad/x.conf"
  format = "yaml"
`
	m, err := ParseManifest([]byte(man))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Packages["bad"]; ok {
		t.Error("def with an unsupported config format must be dropped")
	}
}

func TestParseManifestDropsUnsafeConfigPath(t *testing.T) {
	man := `schema_version = 1
[packages.bad]
name = "Bad"

  [[packages.bad.configs]]
  name = "Shadow"
  path = "/etc/shadow"
  format = "ini"
`
	m, err := ParseManifest([]byte(man))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Packages["bad"]; ok {
		t.Error("def with a denylisted config path must be dropped")
	}
}

// Config declarations are functional metadata: they are part of HashDef and
// survive the ToPackage/DefFromPackage round trip.
func TestHashDefIncludesConfigs(t *testing.T) {
	base := &PackageDef{Name: "X", Source: "h/o/r", Forge: "forgejo", Asset: "KoboRoot.tgz"}
	withCfg := *base
	withCfg.Configs = []config.ModConfig{{
		Name: "Settings", Path: "/mnt/onboard/.adds/x/settings.ini", Format: config.FormatINI,
	}}
	hBase, _ := HashDef(base)
	hCfg, _ := HashDef(&withCfg)
	if hBase == hCfg {
		t.Error("adding a config declaration must change the def hash")
	}
	// Round trip through a local package preserves the config hash.
	pkg := withCfg.ToPackage("x", "main", "v1")
	if len(pkg.Configs) != 1 {
		t.Fatalf("ToPackage lost configs: %+v", pkg.Configs)
	}
	hBack, _ := HashDef(DefFromPackage(pkg))
	if hCfg != hBack {
		t.Errorf("config hash not stable across projection: %q != %q", hCfg, hBack)
	}
}

// A3: an empty configs slice hashes identically to omitting the key, so
// `configs = []` never reads as drift.
func TestHashDefConfigsEmptyEqualsAbsent(t *testing.T) {
	absent, _ := HashDef(&PackageDef{Name: "X"})
	empty, _ := HashDef(&PackageDef{Name: "X", Configs: []config.ModConfig{}})
	if absent != empty {
		t.Errorf("empty configs must hash like absent: %q != %q", absent, empty)
	}
}

func TestStaleness(t *testing.T) {
	now := time.Now()
	if Staleness("", now) != "never refreshed" {
		t.Error("empty last-fetched should say never refreshed")
	}
	threeDaysAgo := now.Add(-3 * 24 * time.Hour).Format(time.RFC3339)
	if got := Staleness(threeDaysAgo, now); got != "cached 3d ago" {
		t.Errorf("staleness = %q, want cached 3d ago", got)
	}
}

// CONFIG.md §2: a seed template on a [[configs]] block parses, is functional
// (part of HashDef), and survives the ToPackage/DefFromPackage projection.
func TestParseManifestConfigTemplate(t *testing.T) {
	man := `schema_version = 1
[packages.nickelnote]
name = "NickelNote"

  [[packages.nickelnote.configs]]
  name = "Note content"
  path = "/mnt/onboard/.adds/nickelnote/content.template"
  format = "text"
  create = true
  template = '''
<span>Your Name</span><br />
'''
`
	m, err := ParseManifest([]byte(man))
	if err != nil {
		t.Fatal(err)
	}
	def, ok := m.Packages["nickelnote"]
	if !ok {
		t.Fatal("nickelnote should parse")
	}
	if len(def.Configs) != 1 || def.Configs[0].Template != "<span>Your Name</span><br />\n" {
		t.Fatalf("template not parsed: %+v", def.Configs)
	}
}

// A template is functional metadata: adding/changing it changes the def hash, and
// the change round-trips through a local package projection.
func TestHashDefIncludesTemplate(t *testing.T) {
	base := &PackageDef{Name: "X", Source: "h/o/r", Forge: "forgejo", Asset: "KoboRoot.tgz",
		Configs: []config.ModConfig{{Name: "C", Path: "/mnt/onboard/.adds/x/c.template", Format: config.FormatText}}}
	withTpl := *base
	withTpl.Configs = []config.ModConfig{{Name: "C", Path: "/mnt/onboard/.adds/x/c.template", Format: config.FormatText, Template: "hello\n"}}
	hBase, _ := HashDef(base)
	hTpl, _ := HashDef(&withTpl)
	if hBase == hTpl {
		t.Error("adding a template must change the def hash")
	}
	// Round trip through a local package preserves the template + its hash.
	pkg := withTpl.ToPackage("x", "main", "v1")
	if len(pkg.Configs) != 1 || pkg.Configs[0].Template != "hello\n" {
		t.Fatalf("ToPackage lost the template: %+v", pkg.Configs)
	}
	hBack, _ := HashDef(DefFromPackage(pkg))
	if hTpl != hBack {
		t.Errorf("template hash not stable across projection: %q != %q", hTpl, hBack)
	}
}

// A [[configs]] block whose template is oversized drops the whole def, exactly
// like a bad format or unsafe path (config.Validate rejects it). A NUL byte is
// not representable in a TOML string at all, so ParseManifest never sees one —
// the NUL guard is defense-in-depth for programmatic defs, covered by
// config.TestModConfigValidateTemplate and modconfig.TestSeedContentRefusesBinary.
func TestParseManifestDropsOversizeTemplate(t *testing.T) {
	oversize := `schema_version = 1
[packages.bad]
name = "Bad"

  [[packages.bad.configs]]
  name = "X"
  path = "/mnt/onboard/.adds/bad/x.template"
  format = "text"
  template = "` + strings.Repeat("A", config.MaxTemplate+1) + "\"\n"
	m, err := ParseManifest([]byte(oversize))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Packages["bad"]; ok {
		t.Error("def with an oversized template must be dropped")
	}
}
