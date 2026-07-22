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
	pkgs, unreadable, err := LoadAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(unreadable) != 0 {
		t.Errorf("unexpected unreadable: %v", unreadable)
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
	pkgs, unreadable, err := LoadAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 || pkgs[0].ID != "nickeldbus" {
		t.Errorf("only the valid def should load: %+v", pkgs)
	}
	if len(unreadable) != 1 || unreadable[0] != "Bad_Name.toml" {
		t.Errorf("only Bad_Name.toml should be unreadable, got %v", unreadable)
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
	pkgs, unreadable, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll must not fail on bad files: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].ID != "good" {
		t.Errorf("only the good def should load: %+v", pkgs)
	}
	if len(unreadable) != 2 {
		t.Errorf("both bad files should be reported unreadable: %v", unreadable)
	}
}
