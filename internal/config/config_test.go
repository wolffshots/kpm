package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nickelhardcover.toml")
	p := &Package{
		Name:   "NickelHardcover",
		Source: "codeberg.org/StrayRose/NickelHardcover",
		Forge:  ForgeForgejo,
		Asset:  "KoboRoot.tgz",
		Pin:    "v0.5.0",
	}
	if err := Save(path, p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path, "nickelhardcover")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "nickelhardcover" || got.Source != p.Source || got.Pin != "v0.5.0" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Host() != "codeberg.org" || got.Owner() != "StrayRose" || got.Repo() != "NickelHardcover" {
		t.Errorf("source split wrong: %s/%s/%s", got.Host(), got.Owner(), got.Repo())
	}
}

func TestSavePreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.toml")
	original := `name = "X"
source = "codeberg.org/o/r"
forge = "forgejo"
asset = "KoboRoot.tgz"
pin = ""
registry = "https://example.test/registry"
min_kpm = "0.2.0"

[uninstall]
method = "manifest"
custom_key = "keepme"

[extra_table]
foo = "bar"
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate `kpm pin`: Load, mutate pin, Save.
	p, err := Load(path, "x")
	if err != nil {
		t.Fatal(err)
	}
	p.Pin = "v1.0.0"
	if err := Save(path, p); err != nil {
		t.Fatal(err)
	}
	// Re-read raw and assert unknown keys survived.
	var raw map[string]any
	b, _ := os.ReadFile(path)
	if _, err := toml.Decode(string(b), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["registry"] != "https://example.test/registry" {
		t.Errorf("registry not preserved: %v", raw["registry"])
	}
	if raw["min_kpm"] != "0.2.0" {
		t.Errorf("min_kpm not preserved: %v", raw["min_kpm"])
	}
	if raw["pin"] != "v1.0.0" {
		t.Errorf("pin not updated: %v", raw["pin"])
	}
	et, _ := raw["extra_table"].(map[string]any)
	if et == nil || et["foo"] != "bar" {
		t.Errorf("unknown table not preserved: %v", raw["extra_table"])
	}
	uni, _ := raw["uninstall"].(map[string]any)
	if uni == nil || uni["custom_key"] != "keepme" {
		t.Errorf("unknown [uninstall] key not preserved: %v", raw["uninstall"])
	}
}

// A1: SaveReplace treats known keys authoritatively (dropped fields disappear)
// while preserving unknown top-level keys and unknown [uninstall] keys.
func TestSaveReplaceRemovesKnownPreservesUnknown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.toml")
	// A def with marker-method hooks plus an unknown [uninstall] key and an
	// unknown top-level table.
	original := `name = "X"
source = "codeberg.org/o/r"
forge = "forgejo"
asset = "KoboRoot.tgz"
pin = "v1"
registry = "main"

[uninstall]
method = "marker"
marker_file = "/mnt/onboard/.adds/nm/uninstall"
needs_reboot = true
run_before = "/etc/init.d/x stop"
custom_key = "keepme"

[extra_table]
foo = "bar"
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	// Replace with a manifest-method def carrying no hooks (as a v2 registry def
	// would), preserving the pin/registry provenance.
	repl := &Package{
		Name: "X", Source: "codeberg.org/o/r", Forge: "forgejo", Asset: "KoboRoot.tgz",
		Pin: "v1", Registry: "main",
		Uninstall: Uninstall{Method: MethodManifest, PurgePaths: []string{"/mnt/onboard/x/**"}},
	}
	if err := SaveReplace(path, repl); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	b, _ := os.ReadFile(path)
	if _, err := toml.Decode(string(b), &raw); err != nil {
		t.Fatal(err)
	}
	uni, _ := raw["uninstall"].(map[string]any)
	if uni == nil {
		t.Fatalf("uninstall table missing:\n%s", b)
	}
	// Dropped known keys must be gone.
	for _, k := range []string{"marker_file", "needs_reboot", "run_before"} {
		if _, ok := uni[k]; ok {
			t.Errorf("dropped known key %q must be removed:\n%s", k, b)
		}
	}
	if uni["method"] != "manifest" {
		t.Errorf("method should be replaced to manifest: %v", uni["method"])
	}
	// Unknown keys survive.
	if uni["custom_key"] != "keepme" {
		t.Errorf("unknown [uninstall] key must survive: %v", uni["custom_key"])
	}
	et, _ := raw["extra_table"].(map[string]any)
	if et == nil || et["foo"] != "bar" {
		t.Errorf("unknown top-level table must survive: %v", raw["extra_table"])
	}
	// Provenance preserved.
	if raw["registry"] != "main" || raw["pin"] != "v1" {
		t.Errorf("pin/registry provenance lost: %v", raw)
	}
}

// A1: a zero-value known field disappears entirely under SaveReplace.
func TestSaveReplaceDropsZeroValueKnownField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.toml")
	original := `name = "X"
source = "codeberg.org/o/r"
forge = "forgejo"
asset = "KoboRoot.tgz"
pin = "v1"
min_kpm = "0.3.0"
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	// Replace with a def that has no pin and no min_kpm — both keys must vanish.
	repl := &Package{Name: "X", Source: "codeberg.org/o/r", Forge: "forgejo", Asset: "KoboRoot.tgz"}
	if err := SaveReplace(path, repl); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	b, _ := os.ReadFile(path)
	toml.Decode(string(b), &raw)
	if _, ok := raw["pin"]; ok {
		t.Errorf("empty pin should be dropped by SaveReplace:\n%s", b)
	}
	if _, ok := raw["min_kpm"]; ok {
		t.Errorf("empty min_kpm should be dropped by SaveReplace:\n%s", b)
	}
}

// CONFIG.md §2: config declarations round-trip through Save/Load, and a fresh
// package with no configs never emits an empty configs array.
func TestSaveLoadConfigsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nickelnote.toml")
	p := &Package{
		Name: "NickelNote", Source: "github.com/onatbas/nickelnote", Forge: ForgeGitHub, Asset: "KoboRoot.tgz",
		Configs: []ModConfig{
			{Name: "Note content", Path: "/mnt/onboard/.adds/nickelnote/content.template", Format: FormatText, Reload: ReloadAuto, Create: true},
			{Name: "Style", Path: "/mnt/onboard/.adds/nickelnote/style.template", Format: FormatText, Reload: ReloadAuto, Create: true, Description: "Stylesheet."},
		},
	}
	if err := Save(path, p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path, "nickelnote")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Configs) != 2 || got.Configs[0].Name != "Note content" || !got.Configs[0].Create {
		t.Fatalf("configs round-trip mismatch: %+v", got.Configs)
	}
	if got.Configs[1].Description != "Stylesheet." {
		t.Errorf("description lost: %+v", got.Configs[1])
	}
}

// A1: SaveReplace drops a configs array the registry no longer declares.
func TestSaveReplaceDropsConfigs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.toml")
	withCfg := &Package{
		Name: "X", Source: "codeberg.org/o/r", Forge: "forgejo", Asset: "KoboRoot.tgz",
		Configs: []ModConfig{{Name: "C", Path: "/mnt/onboard/.adds/x/c.ini", Format: FormatINI}},
	}
	if err := Save(path, withCfg); err != nil {
		t.Fatal(err)
	}
	// Re-save with no configs via replace-semantics: the array must vanish.
	if err := SaveReplace(path, &Package{Name: "X", Source: "codeberg.org/o/r", Forge: "forgejo", Asset: "KoboRoot.tgz"}); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	b, _ := os.ReadFile(path)
	toml.Decode(string(b), &raw)
	if _, ok := raw["configs"]; ok {
		t.Errorf("SaveReplace must drop a removed configs array:\n%s", b)
	}
}

func TestModConfigValidate(t *testing.T) {
	ok := ModConfig{Name: "N", Path: "/mnt/onboard/.adds/x/c.ini", Format: FormatINI}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid decl rejected: %v", err)
	}
	bad := []ModConfig{
		{Name: "", Path: "/p", Format: FormatINI},                // no name
		{Name: "N", Path: "", Format: FormatINI},                 // no path
		{Name: "N", Path: "/p", Format: "env"},                   // deferred format
		{Name: "N", Path: "/p", Format: FormatText, Reload: "x"}, // bad reload
		{Name: "2", Path: "/p", Format: FormatINI},               // numeric name collides with index selection
		{Name: "-x", Path: "/p", Format: FormatINI},              // leading '-' is eaten by flag parsing
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d should be invalid: %+v", i, c)
		}
	}
	// Duplicate names (case-insensitive) are rejected at the slice level.
	dup := []ModConfig{
		{Name: "Same", Path: "/mnt/onboard/.adds/x/a.ini", Format: FormatINI},
		{Name: "same", Path: "/mnt/onboard/.adds/x/b.ini", Format: FormatINI},
	}
	if err := ValidateConfigs(dup); err == nil {
		t.Error("duplicate config names must be rejected")
	}
}

// CONFIG.md §2: a seed template is bounded and text — an oversized or NUL-bearing
// template is rejected by Validate (so ParseManifest drops the def), while a
// modestly-sized text template passes.
func TestModConfigValidateTemplate(t *testing.T) {
	ok := ModConfig{Name: "N", Path: "/mnt/onboard/.adds/x/c.template", Format: FormatText, Template: "hello\nworld\n"}
	if err := ok.Validate(); err != nil {
		t.Errorf("a normal template must be accepted: %v", err)
	}
	oversize := ModConfig{Name: "N", Path: "/mnt/onboard/.adds/x/c.template", Format: FormatText, Template: strings.Repeat("A", MaxTemplate+1)}
	if err := oversize.Validate(); err == nil {
		t.Error("a template over MaxTemplate must be rejected")
	}
	binary := ModConfig{Name: "N", Path: "/mnt/onboard/.adds/x/c.template", Format: FormatText, Template: "a\x00b"}
	if err := binary.Validate(); err == nil {
		t.Error("a template with a NUL byte must be rejected")
	}
}

// CONFIG.md §2: Createable is true when create=true OR a template is declared —
// either lets `config init`/`config set` create a missing file.
func TestModConfigCreateable(t *testing.T) {
	cases := []struct {
		c    ModConfig
		want bool
	}{
		{ModConfig{}, false},
		{ModConfig{Create: true}, true},
		{ModConfig{Template: "x\n"}, true},
		{ModConfig{Create: true, Template: "x\n"}, true},
	}
	for i, tc := range cases {
		if got := tc.c.Createable(); got != tc.want {
			t.Errorf("case %d Createable() = %v, want %v", i, got, tc.want)
		}
	}
}

func TestSaveFreshHasNoUninstallNoise(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.toml")
	p := &Package{
		Name:   "Fresh",
		Source: "github.com/o/r",
		Forge:  ForgeGitHub,
		Asset:  "KoboRoot.tgz",
	}
	if err := Save(path, p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "[uninstall]") {
		t.Errorf("fresh add must not write an empty [uninstall] table:\n%s", b)
	}
	if !strings.Contains(string(b), `pin = ""`) {
		t.Errorf("fresh add should keep pin explicit:\n%s", b)
	}
}

func TestLoadUninstallTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.toml")
	content := `name = "X"
source = "codeberg.org/o/r"
forge = "forgejo"
asset = "KoboRoot.tgz"
pin = ""

[uninstall]
method = "marker"
extra_paths = ["/mnt/onboard/.adds/x"]
purge_paths = ["/mnt/onboard/.adds/x/config/**"]
keep_paths = ["/mnt/onboard/.adds/x/edited.cfg"]
allow_paths = ["/srv/x"]
marker_file = "/mnt/onboard/.adds/nm/uninstall"
needs_reboot = false
run_before = "/etc/init.d/x stop"
run_after = "echo done"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path, "x")
	if err != nil {
		t.Fatal(err)
	}
	u := p.Uninstall
	if u.EffectiveMethod() != MethodMarker {
		t.Errorf("method = %q", u.EffectiveMethod())
	}
	if len(u.ExtraPaths) != 1 || u.ExtraPaths[0] != "/mnt/onboard/.adds/x" {
		t.Errorf("extra_paths not parsed: %+v", u.ExtraPaths)
	}
	if len(u.PurgePaths) != 1 || len(u.KeepPaths) != 1 || len(u.AllowPaths) != 1 {
		t.Errorf("purge/keep/allow not parsed: %+v", u)
	}
	if u.MarkerFile != "/mnt/onboard/.adds/nm/uninstall" {
		t.Errorf("marker_file = %q", u.MarkerFile)
	}
	// needs_reboot = false explicitly overrides the marker default of true.
	if u.NeedsReboot == nil || *u.NeedsReboot != false || u.RebootRequired() {
		t.Errorf("needs_reboot override not honored: %v", u.NeedsReboot)
	}
	if u.RunBefore == "" || u.RunAfter == "" {
		t.Errorf("run hooks not parsed: %+v", u)
	}
}

func TestUninstallDefaults(t *testing.T) {
	// Manifest is the default method; reboot only defaults on for marker.
	var u Uninstall
	if u.EffectiveMethod() != MethodManifest {
		t.Errorf("default method = %q, want manifest", u.EffectiveMethod())
	}
	if u.RebootRequired() {
		t.Error("manifest method should default needs_reboot=false")
	}
	marker := Uninstall{Method: MethodMarker, MarkerFile: "/x"}
	if !marker.RebootRequired() {
		t.Error("marker method should default needs_reboot=true")
	}
}

func TestUninstallValidate(t *testing.T) {
	if err := (Uninstall{}).Validate(); err != nil {
		t.Errorf("empty (manifest) should be valid: %v", err)
	}
	if err := (Uninstall{Method: "bogus"}).Validate(); err == nil {
		t.Error("bad method should be invalid")
	}
	if err := (Uninstall{Method: MethodMarker}).Validate(); err == nil {
		t.Error("marker without marker_file should be invalid")
	}
	if err := (Uninstall{Method: MethodMarker, MarkerFile: "/x"}).Validate(); err != nil {
		t.Errorf("marker with marker_file should be valid: %v", err)
	}
}

// MARKER-REMOVE §1/§4.5: the new method validates like marker (marker_file
// required) and defaults needs_reboot=true; explicit needs_reboot still wins.
func TestUninstallMarkerRemove(t *testing.T) {
	if err := (Uninstall{Method: MethodMarkerRemove}).Validate(); err == nil {
		t.Error("marker-remove without marker_file should be invalid")
	}
	u := Uninstall{Method: MethodMarkerRemove, MarkerFile: "/mnt/onboard/.adds/nickelclock/uninstall"}
	if err := u.Validate(); err != nil {
		t.Errorf("marker-remove with marker_file should be valid: %v", err)
	}
	if !u.RebootRequired() {
		t.Error("marker-remove should default needs_reboot=true")
	}
	off := false
	u.NeedsReboot = &off
	if u.RebootRequired() {
		t.Error("explicit needs_reboot=false must override the marker-remove default")
	}
}

// MARKER-REMOVE §4.7: a def with the new method parses from TOML, validates,
// and survives install's def write (SaveReplace) round-trip.
func TestUninstallMarkerRemoveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nickelclock.toml")
	def := &Package{
		Name:   "NickelClock",
		Source: "github.com/shermp/NickelClock",
		Forge:  ForgeGitHub,
		Asset:  "NickelClock-*.zip",
		Uninstall: Uninstall{
			Method:     MethodMarkerRemove,
			MarkerFile: "/mnt/onboard/.adds/nickelclock/uninstall",
			PurgePaths: []string{"/mnt/onboard/.adds/nickelclock/**"},
		},
	}
	if err := SaveReplace(path, def); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path, "nickelclock")
	if err != nil {
		t.Fatal(err)
	}
	if p.Uninstall.Method != MethodMarkerRemove || p.Uninstall.MarkerFile != def.Uninstall.MarkerFile {
		t.Errorf("round-trip lost the method/marker_file: %+v", p.Uninstall)
	}
	if err := p.Uninstall.Validate(); err != nil {
		t.Errorf("round-tripped def must validate: %v", err)
	}
}

func TestLoadToleratesBadUninstallBlock(t *testing.T) {
	// A bad [uninstall] block must not fail Load (so check/update/list keep
	// working); it only surfaces when Validate() is called at uninstall time.
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	content := `name = "Bad"
source = "codeberg.org/o/bad"
forge = "forgejo"
asset = "KoboRoot.tgz"

[uninstall]
method = "totally-invalid"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path, "bad")
	if err != nil {
		t.Fatalf("Load must tolerate a bad [uninstall] block: %v", err)
	}
	if err := p.Uninstall.Validate(); err == nil {
		t.Error("Validate must reject the bad block at uninstall time")
	}
}

func TestLoadAllSorted(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"zeta", "alpha", "mid"} {
		if err := Save(filepath.Join(dir, id+".toml"), &Package{Source: "h/o/" + id}); err != nil {
			t.Fatal(err)
		}
	}
	// a non-toml file should be ignored
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	pkgs, skipped, err := LoadAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped.All()) != 0 {
		t.Errorf("unexpected skipped: %+v", skipped)
	}
	if len(pkgs) != 3 || pkgs[0].ID != "alpha" || pkgs[2].ID != "zeta" {
		t.Errorf("LoadAll not sorted: %+v", pkgs)
	}
}

// SELF-SOURCE §3a: AppleDouble sidecars (._foo.toml) and other dotfiles are
// silently skipped — never reported as unreadable — while a genuinely invalid
// name (invalid chars, no dot prefix) still lands in unreadable.
func TestLoadAllSkipsAppleDoubleAndHidden(t *testing.T) {
	dir := t.TempDir()
	// A valid def.
	if err := os.WriteFile(filepath.Join(dir, "nickeldbus.toml"), []byte("source = \"h/o/nickeldbus\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// AppleDouble sidecar + a hidden dotfile: both silently ignored.
	if err := os.WriteFile(filepath.Join(dir, "._nickeldbus.toml"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden.toml"), []byte("source = \"h/o/x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A genuinely invalid name (no dot prefix, invalid chars) still reported.
	if err := os.WriteFile(filepath.Join(dir, "Bad_Name.toml"), []byte("source = \"h/o/x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgs, skipped, err := LoadAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 || pkgs[0].ID != "nickeldbus" {
		t.Errorf("only the valid def should load: %+v", pkgs)
	}
	if len(skipped.All()) != 1 || skipped.All()[0] != "Bad_Name.toml" {
		t.Errorf("only Bad_Name.toml should be skipped, got %+v", skipped)
	}
	// And it is reported as an invalid NAME, not as unparseable: the file
	// decodes fine, it simply was never a registration (see SkippedDefs).
	if len(skipped.InvalidName) != 1 || len(skipped.Unparseable) != 0 {
		t.Errorf("Bad_Name.toml must be an invalid-name skip, got %+v", skipped)
	}
}

func TestIsSidecar(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"._nickeldbus.toml", true}, // AppleDouble sidecar
		{".DS_Store", true},         // Finder metadata
		{".hidden.toml", true},      // any dotfile
		{".", true},
		{"nickeldbus.toml", false},
		{"nickel-home", false},
		{"Bad_Name.toml", false}, // invalid id but not a dotfile
		{"a._b", false},          // dot not at the start
		{"", false},
	}
	for _, c := range cases {
		if got := IsSidecar(c.name); got != c.want {
			t.Errorf("IsSidecar(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLoadAllSkipsBadAndInvalidId(t *testing.T) {
	dir := t.TempDir()
	// A valid def.
	if err := Save(filepath.Join(dir, "good.toml"), &Package{Source: "h/o/good"}); err != nil {
		t.Fatal(err)
	}
	// A file that fails to decode.
	if err := os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("this is = not = valid toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file whose stem is not a valid id.
	if err := os.WriteFile(filepath.Join(dir, "Bad_Id.toml"), []byte("source = \"h/o/x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgs, skipped, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll must not fail on bad files: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].ID != "good" {
		t.Errorf("only the good def should load: %+v", pkgs)
	}
	if len(skipped.All()) != 2 {
		t.Errorf("both bad files should be reported: %+v", skipped)
	}
	// SPLIT BY CAUSE. These two are not interchangeable: broken.toml has a valid
	// stem so it may be a registered package whose declarations kpm cannot read,
	// while Bad_Id.toml was never registered and can declare nothing. The
	// conflict-gated refusal keys on the first and must ignore the second.
	if len(skipped.Unparseable) != 1 || skipped.Unparseable[0] != "broken.toml" {
		t.Errorf("broken.toml must be the unparseable one, got %+v", skipped)
	}
	if len(skipped.InvalidName) != 1 || skipped.InvalidName[0] != "Bad_Id.toml" {
		t.Errorf("Bad_Id.toml must be the invalid-name one, got %+v", skipped)
	}
}

// ---- INTEGRITY.md §3/§5/§9: the 0.10.0 def fields ------------------------

// §2.2 hazard 1: the "tag" default lives only in the derived getter.
func TestEffectiveUpdateCheck(t *testing.T) {
	cases := []struct{ stored, want string }{
		{"", UpdateCheckTag},
		{UpdateCheckTag, UpdateCheckTag},
		{UpdateCheckRolling, UpdateCheckRolling},
	}
	for _, c := range cases {
		p := Package{UpdateCheck: c.stored}
		if got := p.EffectiveUpdateCheck(); got != c.want {
			t.Errorf("EffectiveUpdateCheck(%q) = %q, want %q", c.stored, got, c.want)
		}
		if p.UpdateCheck != c.stored {
			t.Errorf("the getter must not mutate the stored value")
		}
	}
	// §9.2 defense in depth: a hand-edited garbage value degrades to tag
	// comparison (it is simply never equal to UpdateCheckRolling).
	if (Package{UpdateCheck: "semver"}).EffectiveUpdateCheck() == UpdateCheckRolling {
		t.Error("a garbage update_check must not read as rolling")
	}
}

// §5.2: tagsEqual's normalization is shared, and the checksums lookup uses it.
func TestNormalizeTag(t *testing.T) {
	cases := []struct{ in, want string }{
		{"v1.2.0", "1.2.0"}, {"1.2.0", "1.2.0"}, {"vv1", "v1"}, {"latest", "latest"}, {"", ""},
	}
	for _, c := range cases {
		if got := NormalizeTag(c.in); got != c.want {
			t.Errorf("NormalizeTag(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	sum := strings.Repeat("3b", 32)
	p := Package{Checksums: map[string]string{"v0.4.0": sum, "0.5.0": strings.ToUpper(sum)}}
	// Exact and normalized lookups both hit, in both directions (§5.2).
	for _, tag := range []string{"v0.4.0", "0.4.0", "v0.5.0", "0.5.0"} {
		got, ok := p.ChecksumFor(tag)
		if !ok || got != sum {
			t.Errorf("ChecksumFor(%q) = (%q,%v), want (%q,true)", tag, got, ok, sum)
		}
	}
	if _, ok := p.ChecksumFor("v9.9.9"); ok {
		t.Error("an undeclared tag must report no checksum")
	}
	if _, ok := (Package{}).ChecksumFor("v1"); ok {
		t.Error("a def with no checksums table must report no checksum")
	}
	// §9.2: a malformed local value disables verification instead of wedging the
	// package, and ChecksumDeclaredFor still reports the entry so it can WARN.
	bad := Package{Checksums: map[string]string{"v1": "deadbeef"}}
	if _, ok := bad.ChecksumFor("v1"); ok {
		t.Error("a non-64-hex value must not be used as a checksum")
	}
	if !bad.ChecksumDeclaredFor("v1") {
		t.Error("ChecksumDeclaredFor must see the malformed entry")
	}
	if bad.ChecksumDeclaredFor("v2") {
		t.Error("ChecksumDeclaredFor must be false for an absent tag")
	}
}

// m2: rolling + checksums is a parse-time error for a REGISTRY def, but
// packages.d is user-editable and a hand-edited def can carry both. The tag
// never moves while the asset does, so the recorded hash is correct exactly once
// and then hard-fails every download forever with wording that reads like a
// security incident. Degrade to unverified instead (§9.2's pattern).
func TestChecksumIgnoredForRollingDef(t *testing.T) {
	sum := strings.Repeat("3b", 32)
	rolling := Package{UpdateCheck: UpdateCheckRolling, Checksums: map[string]string{"latest": sum}}
	if got, ok := rolling.ChecksumFor("latest"); ok {
		t.Errorf("a rolling def must never verify against a pinned checksum, got %q", got)
	}
	// The same def without the rolling flag still verifies — proof the checksum
	// itself is well-formed and the rolling test is what suppressed it.
	tagged := Package{Checksums: map[string]string{"latest": sum}}
	if got, ok := tagged.ChecksumFor("latest"); !ok || got != sum {
		t.Errorf("a tag-mode def must still verify: (%q,%v)", got, ok)
	}
}

// m3: §5.5 steers a curator at GitHub's `sha256:<hex>` asset digest, so accept
// it verbatim at use. n2: a stray-whitespace key was accepted by
// ValidateChecksums but could never match, so the release installed unverified
// with no diagnostic at all.
func TestChecksumNormalization(t *testing.T) {
	sum := strings.Repeat("3b", 32)
	cases := []struct{ name, key, value string }{
		{"sha256 prefix", "v1.0.0", "sha256:" + sum},
		{"uppercase prefix", "v1.0.0", "SHA256:" + strings.ToUpper(sum)},
		{"padded value", "v1.0.0", "  " + sum + "  "},
		{"padded key", " v1.0.0 ", sum},
	}
	for _, c := range cases {
		p := Package{Checksums: map[string]string{c.key: c.value}}
		if err := ValidateChecksums(p.Checksums); err != nil {
			t.Errorf("%s: ValidateChecksums = %v, want nil", c.name, err)
		}
		got, ok := p.ChecksumFor("v1.0.0")
		if !ok || got != sum {
			t.Errorf("%s: ChecksumFor = (%q,%v), want (%q,true)", c.name, got, ok, sum)
		}
	}
	// Still rejected: a prefix around something that is not a digest.
	if err := ValidateChecksums(map[string]string{"v1": "sha256:deadbeef"}); err == nil {
		t.Error("stripping the prefix must not make a short value valid")
	}
}

func TestIntegrityValidators(t *testing.T) {
	sum := strings.Repeat("a", 64)
	if err := ValidateUpdateCheck(""); err != nil {
		t.Errorf("empty update_check is the default, not an error: %v", err)
	}
	if err := ValidateUpdateCheck("semver"); err == nil ||
		err.Error() != `invalid update_check "semver" (want "tag" or "rolling")` {
		t.Errorf("update_check error = %v", err)
	}
	if err := ValidateConflicts("a", []string{"Bad_Id"}); err == nil ||
		err.Error() != `conflicts entry "Bad_Id" is not a valid package id` {
		t.Errorf("conflicts id error = %v", err)
	}
	if err := ValidateConflicts("a", []string{"a"}); err == nil ||
		err.Error() != `conflicts must not list the package's own id "a"` {
		t.Errorf("self-conflict error = %v", err)
	}
	if err := ValidateConflicts("a", []string{"b", "b", "c"}); err != nil {
		t.Errorf("duplicates are collapsed at use, not an error: %v", err)
	}
	if err := ValidateChecksums(map[string]string{"": sum}); err == nil ||
		err.Error() != "checksums has an empty release tag key" {
		t.Errorf("empty tag error = %v", err)
	}
	if err := ValidateChecksums(map[string]string{"v1": "deadbeef"}); err == nil ||
		err.Error() != `checksums entry for tag "v1" is not a 64-character hex sha256` {
		t.Errorf("bad checksum error = %v", err)
	}
	if err := ValidateChecksums(map[string]string{"v1": strings.ToUpper(sum)}); err != nil {
		t.Errorf("uppercase hex is valid: %v", err)
	}
	if err := ValidateDefCombination(Package{UpdateCheck: UpdateCheckRolling, Checksums: map[string]string{"latest": sum}}); err == nil ||
		err.Error() != `update_check = "rolling" cannot be combined with checksums (a rolling tag's asset changes without a def edit)` {
		t.Errorf("rolling+checksums error = %v", err)
	}
	if err := ValidateDefCombination(Package{UpdateCheck: UpdateCheckRolling}); err != nil {
		t.Errorf("rolling alone is fine: %v", err)
	}
}

// §2.2 hazard 2/4: the three fields survive Save/Load, and a def that omits them
// writes no key at all (so its canonical bytes are unchanged from 0.9.2).
func TestIntegrityFieldsRoundTripThroughTOML(t *testing.T) {
	sum := strings.Repeat("3b", 32)
	dir := t.TempDir()

	full := filepath.Join(dir, "kobo-tweaks.toml")
	if err := Save(full, &Package{
		Name: "Kobo Tweaks", Source: "github.com/o/kobo-tweaks", Forge: ForgeGitHub, Asset: "KoboRoot.tgz",
		Conflicts: []string{"nickelclock"}, Checksums: map[string]string{"v1.0.0": sum},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(full, "kobo-tweaks")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0] != "nickelclock" {
		t.Errorf("conflicts lost: %+v", got.Conflicts)
	}
	if got.Checksums["v1.0.0"] != sum {
		t.Errorf("checksums lost: %+v", got.Checksums)
	}

	rolling := filepath.Join(dir, "kobocloud.toml")
	if err := SaveReplace(rolling, &Package{
		Name: "KoboCloud", Source: "github.com/f/KoboCloud", Forge: ForgeGitHub, Asset: "KoboRoot.tgz",
		UpdateCheck: UpdateCheckRolling, MinKpm: "0.10.0",
	}); err != nil {
		t.Fatal(err)
	}
	rp, err := Load(rolling, "kobocloud")
	if err != nil {
		t.Fatal(err)
	}
	if rp.UpdateCheck != UpdateCheckRolling {
		t.Errorf("update_check lost: %q", rp.UpdateCheck)
	}

	// A def using none of them writes none of them (no empty-key noise).
	plain := filepath.Join(dir, "plain.toml")
	if err := Save(plain, &Package{Name: "Plain", Source: "github.com/o/p", Forge: ForgeGitHub, Asset: "KoboRoot.tgz"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"update_check", "conflicts", "checksums"} {
		if strings.Contains(string(b), key) {
			t.Errorf("a def that does not use %s must not grow the key:\n%s", key, b)
		}
	}

	// SaveReplace is authoritative: a field the registry dropped disappears.
	if err := SaveReplace(full, &Package{
		Name: "Kobo Tweaks", Source: "github.com/o/kobo-tweaks", Forge: ForgeGitHub, Asset: "KoboRoot.tgz",
	}); err != nil {
		t.Fatal(err)
	}
	after, err := Load(full, "kobo-tweaks")
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Conflicts) != 0 || len(after.Checksums) != 0 {
		t.Errorf("SaveReplace must clear dropped integrity fields: %+v / %+v", after.Conflicts, after.Checksums)
	}
}
