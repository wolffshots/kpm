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

// D: the major.minor firmware comparator. Warn iff the device is newer than the
// tested firmware by major or minor; the third (build) segment is ignored, and
// any unknown firmware never warns.
func TestFirmwareUntested(t *testing.T) {
	cases := []struct {
		name               string
		deviceFw, testedFw string
		want               bool
	}{
		{"equal", "4.45.23697", "4.45.23697", false},
		{"same minor, newer build", "4.45.23697", "4.45.10000", false}, // build counter ignored
		{"same minor, older build", "4.45.10000", "4.45.23697", false},
		{"newer minor warns", "4.45.23697", "4.38.23429", true},
		{"newer major warns", "5.0.1", "4.45.23697", true},
		{"older device no warn", "4.38.23429", "4.45.23697", false},
		{"older minor no warn", "4.44.99999", "4.45.1", false},
		{"missing device fw", "", "4.45.23697", false},
		{"missing tested fw", "4.45.23697", "", false},
		{"both missing", "", "", false},
		{"garbage device", "garbage", "4.45.23697", false}, // parses to 0.0 -> not newer
		{"garbage tested", "4.45.23697", "garbage", true},  // tested 0.0 -> device newer
		{"partial tested (major only)", "4.45.1", "4", true},
	}
	for _, c := range cases {
		if got := FirmwareUntested(c.deviceFw, c.testedFw); got != c.want {
			t.Errorf("%s: FirmwareUntested(%q, %q) = %v, want %v", c.name, c.deviceFw, c.testedFw, got, c.want)
		}
	}
}

// D: tested_fw is advisory registry-only metadata, excluded from HashDef like
// description/homepage — two defs differing only in tested_fw hash identically,
// so a curator edit never registers as a def update or local drift.
func TestHashDefExcludesTestedFw(t *testing.T) {
	base := PackageDef{Name: "X", Source: "h/o/r", Forge: "forgejo", Asset: "KoboRoot.tgz"}
	a := base
	a.TestedFw = "4.45.23697"
	b := base
	b.TestedFw = "4.38.23429"
	ha, err := HashDef(&a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := HashDef(&b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Errorf("tested_fw must be excluded from HashDef: %q != %q", ha, hb)
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

// ---- INTEGRITY.md §12: the 0.10.0 integrity fields ----------------------

// integrityBaseDef is a representative pre-0.10.0 def (the real NickelClock
// entry's shape) used by the hash-invariance guards below.
func integrityBaseDef() *PackageDef {
	return &PackageDef{
		Name:   "NickelClock",
		Source: "github.com/shermp/NickelClock",
		Forge:  "github",
		Asset:  "NickelClock-*.zip",
		MinKpm: "0.4.0",
		Uninstall: config.Uninstall{
			Method:     config.MethodMarkerRemove,
			MarkerFile: "/mnt/onboard/.adds/nickelclock/uninstall",
		},
	}
}

// THE 0.5.0 GUARD (§2.2 hazard 2). A def using none of the three new fields
// must hash to exactly what it hashed to at 0.9.2 — the hex below was captured
// from the tree before the fields were added. A new field missing `,omitempty`
// changes the canonical TOML bytes, changes this hash, and makes every one of
// the 12 registry packages read as drifted and get skipped from every sync.
func TestHashDefUnchangedForDefsWithoutNewFields(t *testing.T) {
	const want = "f44e5af3192cb31733e30fa9c237df4aacfcfa31c36d22d3e2190858794623b3"
	got, err := HashDef(integrityBaseDef())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("HashDef of a def with no 0.10.0 fields = %s, want the 0.9.2 value %s\n"+
			"(a new field is missing `,omitempty` — every package would read as drifted)", got, want)
	}
}

// §2.2 hazard 3: an explicitly-empty conflicts list or checksums table must hash
// identically to omitting the key, or `conflicts = []` reads as drift.
func TestHashDefEmptyConflictsAndChecksumsAreNotDrift(t *testing.T) {
	absent, err := HashDef(integrityBaseDef())
	if err != nil {
		t.Fatal(err)
	}
	empty := integrityBaseDef()
	empty.Conflicts = []string{}
	empty.Checksums = map[string]string{}
	got, err := HashDef(empty)
	if err != nil {
		t.Fatal(err)
	}
	if got != absent {
		t.Errorf("empty conflicts/checksums must hash like absent: %s != %s", got, absent)
	}
}

// §2.1: all three fields are functional, so each one must be part of the hash —
// a curator flipping a package to rolling, adding a conflict, or attesting a new
// release must reach devices where the package is already installed via sync.
func TestHashDefIncludesNewFields(t *testing.T) {
	base, err := HashDef(integrityBaseDef())
	if err != nil {
		t.Fatal(err)
	}
	sum := strings.Repeat("a", 64)
	cases := []struct {
		name  string
		mutow func(*PackageDef)
	}{
		{"update_check", func(d *PackageDef) { d.UpdateCheck = config.UpdateCheckRolling }},
		{"conflicts", func(d *PackageDef) { d.Conflicts = []string{"nickelmenu"} }},
		{"checksums", func(d *PackageDef) { d.Checksums = map[string]string{"v0.4.0": sum} }},
	}
	for _, c := range cases {
		d := integrityBaseDef()
		c.mutow(d)
		h, err := HashDef(d)
		if err != nil {
			t.Fatal(err)
		}
		if h == base {
			t.Errorf("changing %s must change the def hash", c.name)
		}
	}
	// And a changed checksum VALUE (not just presence) must move the hash too.
	d1, d2 := integrityBaseDef(), integrityBaseDef()
	d1.Checksums = map[string]string{"v0.4.0": strings.Repeat("a", 64)}
	d2.Checksums = map[string]string{"v0.4.0": strings.Repeat("b", 64)}
	h1, _ := HashDef(d1)
	h2, _ := HashDef(d2)
	if h1 == h2 {
		t.Error("changing a checksums value must change the def hash")
	}
}

// §2.2 hazard 3: the canonical encoding of a map must not depend on insertion
// order (guards the BurntSushi sorted-map-key assumption).
func TestChecksumsHashIsOrderIndependent(t *testing.T) {
	a := integrityBaseDef()
	a.Checksums = map[string]string{}
	b := integrityBaseDef()
	b.Checksums = map[string]string{}
	tags := []string{"v0.1.0", "v0.2.0", "v0.3.0", "v0.4.0", "v0.5.0"}
	for i, tag := range tags {
		a.Checksums[tag] = strings.Repeat(string(rune('a'+i)), 64)
	}
	for i := len(tags) - 1; i >= 0; i-- {
		b.Checksums[tags[i]] = strings.Repeat(string(rune('a'+i)), 64)
	}
	ha, err := HashDef(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := HashDef(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Errorf("checksums hash must not depend on map insertion order: %s != %s", ha, hb)
	}
}

// §2.2 hazard 4: THE round-trip test. A field missing from any of ToPackage,
// DefFromPackage, or the HashDef normalization produces silent permanent drift.
func TestDefRoundTripsAllNewFields(t *testing.T) {
	def := integrityBaseDef()
	def.UpdateCheck = config.UpdateCheckRolling
	def.Conflicts = []string{"nickelmenu", "nickelnote"}
	// rolling+checksums is a parse error, so exercise checksums on a second def.
	want, err := HashDef(def)
	if err != nil {
		t.Fatal(err)
	}
	got, err := HashDef(DefFromPackage(def.ToPackage("nickelclock", "main", "v0.4.0")))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("update_check/conflicts lost in the ToPackage->DefFromPackage round trip: %s != %s", got, want)
	}

	sums := integrityBaseDef()
	sums.Checksums = map[string]string{"v0.4.0": strings.Repeat("3b", 32)}
	want2, _ := HashDef(sums)
	got2, _ := HashDef(DefFromPackage(sums.ToPackage("nickelclock", "main", "")))
	if got2 != want2 {
		t.Errorf("checksums lost in the ToPackage->DefFromPackage round trip: %s != %s", got2, want2)
	}
}

// §2.2 hazard 1, pinned directly: a registry def with NO update_check, taken
// all the way through ToPackage -> SaveReplace -> config.Load -> DefFromPackage,
// must still hash equal to the registry def. If ToPackage materialized the "tag"
// default, the local def would carry "tag" while the remote carries "" and every
// package would read as permanently drifted.
func TestUpdateCheckDefaultIsNeverMaterialized(t *testing.T) {
	def := integrityBaseDef()
	if def.UpdateCheck != "" {
		t.Fatal("fixture must omit update_check")
	}
	want, err := HashDef(def)
	if err != nil {
		t.Fatal(err)
	}

	pkg := def.ToPackage("nickelclock", "main", "")
	if pkg.UpdateCheck != "" {
		t.Fatalf("ToPackage materialized the update_check default (%q) — the 0.5.0 bug", pkg.UpdateCheck)
	}
	path := filepath.Join(t.TempDir(), "nickelclock.toml")
	if err := config.SaveReplace(path, pkg); err != nil {
		t.Fatal(err)
	}
	if b, rerr := os.ReadFile(path); rerr == nil && strings.Contains(string(b), "update_check") {
		t.Errorf("SaveReplace wrote an update_check key for a def that omits it:\n%s", b)
	}
	loaded, err := config.Load(path, "nickelclock")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UpdateCheck != "" {
		t.Errorf("loaded def carries update_check = %q, want the zero value", loaded.UpdateCheck)
	}
	if loaded.EffectiveUpdateCheck() != config.UpdateCheckTag {
		t.Errorf("EffectiveUpdateCheck() = %q, want %q", loaded.EffectiveUpdateCheck(), config.UpdateCheckTag)
	}
	got, err := HashDef(DefFromPackage(loaded))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("a def omitting update_check reads as drifted after a full round trip: %s != %s", got, want)
	}
}

// §9: a def whose integrity declarations are malformed or jointly incoherent is
// dropped from the manifest, exactly like an invalid id — and its siblings
// survive.
func TestParseManifestDropsInvalidUpdateCheck(t *testing.T) {
	assertDefDropped(t, `update_check = "semver"`)
}

func TestParseManifestDropsInvalidConflicts(t *testing.T) {
	assertDefDropped(t, `conflicts = ["Not_An_Id"]`)
	assertDefDropped(t, `conflicts = ["bad"]`) // self-reference: the def's id is "bad"
}

func TestParseManifestDropsInvalidChecksums(t *testing.T) {
	assertDefDropped(t, "[packages.bad.checksums]\n\"v1\" = \"deadbeef\"")
	assertDefDropped(t, "[packages.bad.checksums]\n\"\" = \""+strings.Repeat("a", 64)+"\"")
}

func TestParseManifestDropsRollingWithChecksums(t *testing.T) {
	assertDefDropped(t, "update_check = \"rolling\"\n[packages.bad.checksums]\n\"latest\" = \""+strings.Repeat("a", 64)+"\"")
}

// §3.8: `update_check` REQUIRES min_kpm, and nothing enforced it. An older kpm
// ignores the unknown key and compares tags, so a curator omission silently
// reproduces the exact bug feature A fixes on every 0.9.x device — the entry
// lies about being up to date forever.
func TestParseManifestDropsRollingWithoutMinKpm(t *testing.T) {
	assertDefDropped(t, `update_check = "rolling"`)
	// With min_kpm it is coherent and survives.
	m, err := ParseManifest([]byte("schema_version = 1\n[packages.ok]\nname = \"Ok\"\nupdate_check = \"rolling\"\nmin_kpm = \"0.10.0\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Packages["ok"]; !ok {
		t.Error("a rolling entry declaring min_kpm must survive")
	}
}

// m3: a dropped def vanishes from the registry, which is indistinguishable from
// one the curator never published. Every drop must be reportable, with a reason.
func TestParseManifestReportsDroppedDefs(t *testing.T) {
	man := `schema_version = 1
[packages.good]
name = "Good"

[packages.bad]
name = "Bad"
update_check = "semver"

[packages.Bad_Id]
name = "Nope"
`
	m, err := ParseManifest([]byte(man))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Dropped) != 2 {
		t.Fatalf("both drops must be reported, got %+v", m.Dropped)
	}
	// Sorted by id, so the assertion is deterministic.
	if m.Dropped[0].ID != "Bad_Id" || m.Dropped[1].ID != "bad" {
		t.Errorf("dropped ids = %+v, want sorted [Bad_Id bad]", m.Dropped)
	}
	if !strings.Contains(m.Dropped[1].Reason, "invalid update_check") {
		t.Errorf("the reason must name the cause, got %q", m.Dropped[1].Reason)
	}
	if !strings.Contains(m.Dropped[0].Reason, "valid package id") {
		t.Errorf("the reason must name the cause, got %q", m.Dropped[0].Reason)
	}
	if len(ParseManifestDropped(t, "schema_version = 1\n[packages.good]\nname = \"Good\"\n")) != 0 {
		t.Error("a clean manifest must report no drops")
	}
}

// ParseManifestDropped is a tiny helper so the clean-manifest assertion above
// reads as one line.
func ParseManifestDropped(t *testing.T, man string) []DroppedDef {
	t.Helper()
	m, err := ParseManifest([]byte(man))
	if err != nil {
		t.Fatal(err)
	}
	return m.Dropped
}

// §5.5 points a curator straight at GitHub's asset digest, which is published as
// `sha256:<hex>`. Pasting it verbatim used to fail validation and take the WHOLE
// package out of the registry, silently.
func TestParseManifestAcceptsSha256PrefixedChecksum(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	man := "schema_version = 1\n[packages.nickelclock]\nname = \"NickelClock\"\n" +
		"[packages.nickelclock.checksums]\n\"v0.4.0\" = \"sha256:" + sum + "\"\n"
	m, err := ParseManifest([]byte(man))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Packages["nickelclock"]; !ok {
		t.Fatalf("a sha256:-prefixed digest must not drop the package: %+v", m.Dropped)
	}
	// And it verifies at use, prefix stripped.
	p := m.Packages["nickelclock"].ToPackage("nickelclock", "main", "")
	if got, ok := p.ChecksumFor("v0.4.0"); !ok || got != sum {
		t.Errorf("ChecksumFor = (%q,%v), want (%q,true)", got, ok, sum)
	}
	// The stored bytes are untouched, so the def's hash is unaffected: normalizing
	// at store would make the local copy hash differently from the registry's.
	if got := m.Packages["nickelclock"].Checksums["v0.4.0"]; got != "sha256:"+sum {
		t.Errorf("the stored value must be verbatim, got %q", got)
	}
}

// assertDefDropped parses a two-package manifest where "bad" carries the given
// extra TOML and asserts it is dropped (with a recorded reason) while the
// sibling "good" survives.
func assertDefDropped(t *testing.T, badExtra string) {
	t.Helper()
	man := "schema_version = 1\n[packages.good]\nname = \"Good\"\n\n[packages.bad]\nname = \"Bad\"\n" + badExtra + "\n"
	m, err := ParseManifest([]byte(man))
	if err != nil {
		t.Fatalf("manifest should still parse: %v\n%s", err, man)
	}
	if _, ok := m.Packages["bad"]; ok {
		t.Errorf("malformed def must be dropped:\n%s", man)
	}
	if _, ok := m.Packages["good"]; !ok {
		t.Errorf("sibling def must survive:\n%s", man)
	}
	if len(m.Dropped) != 1 || m.Dropped[0].ID != "bad" || m.Dropped[0].Reason == "" {
		t.Errorf("the drop must be reported with a reason, got %+v:\n%s", m.Dropped, man)
	}
}

// A well-formed integrity block parses and lands on the def.
func TestParseManifestIntegrityFields(t *testing.T) {
	sum := strings.Repeat("3b1f8c", 10) + "abcd"
	man := `schema_version = 1
[packages.kobocloud]
name = "KoboCloud"
update_check = "rolling"
min_kpm = "0.10.0"

[packages.kobo-tweaks]
name = "Kobo Tweaks"
conflicts = ["nickelclock"]
min_kpm = "0.10.0"

[packages.nickelclock]
name = "NickelClock"

  [packages.nickelclock.checksums]
  "v0.4.0" = "` + sum + `"
`
	m, err := ParseManifest([]byte(man))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Packages["kobocloud"]; got == nil || got.UpdateCheck != config.UpdateCheckRolling {
		t.Fatalf("kobocloud update_check not parsed: %+v", got)
	}
	if got := m.Packages["kobocloud"].EffectiveUpdateCheck(); got != config.UpdateCheckRolling {
		t.Errorf("EffectiveUpdateCheck = %q", got)
	}
	if got := m.Packages["kobo-tweaks"]; got == nil || len(got.Conflicts) != 1 || got.Conflicts[0] != "nickelclock" {
		t.Fatalf("kobo-tweaks conflicts not parsed: %+v", got)
	}
	if got := m.Packages["nickelclock"]; got == nil || got.Checksums["v0.4.0"] != sum {
		t.Fatalf("nickelclock checksums not parsed: %+v", got)
	}
	// The default is not materialized by parsing either.
	if got := m.Packages["nickelclock"].UpdateCheck; got != "" {
		t.Errorf("parsed def carries update_check = %q, want the zero value", got)
	}
}

// m9: joining conflicts on ", " made a hand-edited ["a, b"] render identically
// to ["a","b"], so `add` compared equal strings and dropped the diff line for a
// change sync then applied anyway.
func TestFieldDiffsDistinguishesCommaBearingConflict(t *testing.T) {
	local := &PackageDef{Conflicts: []string{"a, b"}}
	remote := &PackageDef{Conflicts: []string{"a", "b"}}
	var found string
	for _, d := range FieldDiffs(local, remote) {
		if strings.HasPrefix(d, "conflicts:") {
			found = d
		}
	}
	if found == "" {
		t.Fatalf("a real conflicts change must produce a diff line, got %v", FieldDiffs(local, remote))
	}
	// Equal lists still produce nothing.
	same := &PackageDef{Conflicts: []string{"a", "b"}}
	for _, d := range FieldDiffs(same, remote) {
		if strings.HasPrefix(d, "conflicts:") {
			t.Errorf("identical conflicts must not diff: %q", d)
		}
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
