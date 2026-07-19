// Package config handles packages.d/*.toml: the on-disk package definitions,
// their load/save, and parsing of "kpm add" URLs into those definitions.
package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Forge identifiers.
const (
	ForgeGitHub  = "github"
	ForgeForgejo = "forgejo"
)

// DefaultAsset is used when the user does not pass --asset.
const DefaultAsset = "KoboRoot.tgz"

// Uninstall methods.
const (
	MethodManifest = "manifest"
	MethodMarker   = "marker"
)

// Uninstall is the per-package [uninstall] table (UNINSTALL.md §2). All fields
// are optional; a bad block only surfaces errors when "kpm uninstall" runs, so
// registration/check/update never fail on it.
type Uninstall struct {
	Method      string   `toml:"method"`       // "manifest" (default) | "marker"
	ExtraPaths  []string `toml:"extra_paths"`  // always-delete software artifacts
	PurgePaths  []string `toml:"purge_paths"`  // user data/config; deleted only with --purge
	KeepPaths   []string `toml:"keep_paths"`   // subtract from the deletion set
	AllowPaths  []string `toml:"allow_paths"`  // per-package allowlist extension (§4)
	MarkerFile  string   `toml:"marker_file"`  // method="marker": file to create
	NeedsReboot *bool    `toml:"needs_reboot"` // nil => default (true for marker, else false)
	RunBefore   string   `toml:"run_before"`   // /bin/sh -c before removal; nonzero aborts
	RunAfter    string   `toml:"run_after"`    // /bin/sh -c after removal; nonzero is WARN
}

// EffectiveMethod returns Method with the default ("manifest") applied.
func (u Uninstall) EffectiveMethod() string {
	if u.Method == "" {
		return MethodManifest
	}
	return u.Method
}

// RebootRequired reports whether removal needs a reboot to complete: the
// explicit needs_reboot if set, otherwise true for the marker method.
func (u Uninstall) RebootRequired() bool {
	if u.NeedsReboot != nil {
		return *u.NeedsReboot
	}
	return u.EffectiveMethod() == MethodMarker
}

// Validate checks the method and marker constraints. Path-policy validation of
// allow_paths lives in internal/uninstall (it needs the denylist). Called only
// at uninstall time so a bad block never breaks other commands.
func (u Uninstall) Validate() error {
	switch u.EffectiveMethod() {
	case MethodManifest, MethodMarker:
	default:
		return fmt.Errorf("invalid uninstall method %q (want %q or %q)", u.Method, MethodManifest, MethodMarker)
	}
	if u.EffectiveMethod() == MethodMarker && strings.TrimSpace(u.MarkerFile) == "" {
		return fmt.Errorf("uninstall method %q requires marker_file", MethodMarker)
	}
	return nil
}

// Package is one packages.d/<id>.toml file.
type Package struct {
	Name     string `toml:"name"`
	Source   string `toml:"source"`   // host/owner/repo
	Forge    string `toml:"forge"`    // "github" | "forgejo"
	Asset    string `toml:"asset"`    // release asset name; glob allowed
	Pin      string `toml:"pin"`      // empty = latest; else exact tag
	Registry string `toml:"registry"` // provenance: source registry name; absent on hand-added
	MinKpm   string `toml:"min_kpm"`  // optional minimum kpm version (REGISTRY.md §2)

	Uninstall Uninstall `toml:"uninstall"`

	// ID is the filename stem; not serialized into the TOML body.
	ID string `toml:"-"`
}

// Host/Owner/Repo split the "source" field.
func (p Package) Host() string  { return field(p.Source, 0) }
func (p Package) Owner() string { return field(p.Source, 1) }
func (p Package) Repo() string  { return field(p.Source, 2) }

// Configured reports whether the package has a usable host/owner/repo source.
// An empty/partial source marks self-update (or any package) "unconfigured":
// check/update skip it silently instead of erroring (F7).
func (p Package) Configured() bool {
	return p.Host() != "" && p.Owner() != "" && p.Repo() != ""
}

func field(source string, i int) string {
	parts := strings.Split(source, "/")
	if i < len(parts) {
		return parts[i]
	}
	return ""
}

var idRE = regexp.MustCompile(`^[a-z0-9-]+$`)

// ValidID reports whether id matches the [a-z0-9-]+ package-id rule.
func ValidID(id string) bool { return idRE.MatchString(id) }

// Load reads and parses a single package TOML at path, setting ID from filename.
func Load(path, id string) (*Package, error) {
	var p Package
	if _, err := toml.DecodeFile(path, &p); err != nil {
		return nil, err
	}
	p.ID = id
	return &p, nil
}

// Save writes p to path as TOML, preserving any unknown keys already present in
// the file (e.g. future registry/min_kpm fields, unknown [uninstall] keys, or
// unknown tables): it decodes the existing file into a map, overlays the known
// struct fields, and writes the merge (E1). Zero-value [uninstall] fields are
// omitted so a fresh `add` never writes an empty [uninstall] table; pin is kept
// explicit (it ships in the v1 template). Comments are NOT preserved (a TOML
// library limitation).
func Save(path string, p *Package) error {
	m := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		_ = toml.Unmarshal(b, &m) // best-effort: preserve unknown keys
	}
	// Overlay the known top-level fields.
	m["name"] = p.Name
	m["source"] = p.Source
	m["forge"] = p.Forge
	m["asset"] = p.Asset
	m["pin"] = p.Pin // keep pin explicit, even when ""
	// registry/min_kpm are optional provenance: write only when set, so a
	// hand-added package never grows a `registry = ""` noise line (§7.1). An
	// unknown value already in the file is populated onto the struct by Load,
	// so this overlay preserves it too.
	setOptional(m, "registry", p.Registry)
	setOptional(m, "min_kpm", p.MinKpm)

	// Merge the [uninstall] table: overlay only non-zero known fields onto any
	// existing (possibly unknown-key-bearing) table; never inject empty keys.
	uni := asMap(m["uninstall"])
	overlayUninstall(uni, p.Uninstall)
	if len(uni) > 0 {
		m["uninstall"] = uni
	} else {
		delete(m, "uninstall")
	}

	return encodeTOMLFile(path, m)
}

// knownTopKeys / knownUninstallKeys are the keys SaveReplace treats
// authoritatively: any of them absent from the written Package is deleted from
// the on-disk file. Every other key (unknown top-level tables/fields, unknown
// [uninstall] keys) is preserved (A1).
var (
	knownTopKeys       = []string{"name", "source", "forge", "asset", "pin", "registry", "min_kpm"}
	knownUninstallKeys = []string{"method", "extra_paths", "purge_paths", "keep_paths", "allow_paths", "marker_file", "needs_reboot", "run_before", "run_after"}
)

// SaveReplace writes p with replace-semantics: unknown top-level keys and unknown
// [uninstall] keys are preserved, but every KNOWN key is authoritative — the
// existing known keys are cleared first, then only p's non-zero values are
// overlaid. So a field a registry dropped (e.g. marker_file, needs_reboot,
// run_before) actually disappears locally instead of persisting as a stale
// root-executed hook, and an empty slice / nil needs_reboot removes its key
// (A1). Used by sync apply and install/adopt def writes; add/pin/unpin keep the
// merge-only Save (they edit a def loaded from the same file).
func SaveReplace(path string, p *Package) error {
	m := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		_ = toml.Unmarshal(b, &m) // best-effort: preserve unknown keys
	}
	// Clear known top-level keys, then set only non-zero values (a zero-value
	// known field disappears).
	for _, k := range knownTopKeys {
		delete(m, k)
	}
	setOptional(m, "name", p.Name)
	setOptional(m, "source", p.Source)
	setOptional(m, "forge", p.Forge)
	setOptional(m, "asset", p.Asset)
	setOptional(m, "pin", p.Pin)
	setOptional(m, "registry", p.Registry)
	setOptional(m, "min_kpm", p.MinKpm)

	// Clear known [uninstall] keys (unknown keys in the table survive), overlay.
	uni := asMap(m["uninstall"])
	for _, k := range knownUninstallKeys {
		delete(uni, k)
	}
	overlayUninstall(uni, p.Uninstall)
	if len(uni) > 0 {
		m["uninstall"] = uni
	} else {
		delete(m, "uninstall")
	}

	return encodeTOMLFile(path, m)
}

// encodeTOMLFile writes m to path as TOML (create/truncate).
func encodeTOMLFile(path string, m map[string]any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(m)
}

// setOptional writes key=val when val is non-empty, else removes it, so
// optional provenance fields never appear as empty-string noise (E1/§7.1).
func setOptional(m map[string]any, key, val string) {
	if val != "" {
		m[key] = val
	} else {
		delete(m, key)
	}
}

// asMap coerces a decoded TOML table value into a map[string]any (a new empty
// one if absent or the wrong shape).
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// overlayUninstall writes only the non-zero fields of u into dst, so a fresh
// package never emits empty marker_file/run_before/run_after or an empty table,
// while unknown keys already in dst survive (E1).
func overlayUninstall(dst map[string]any, u Uninstall) {
	setStr := func(key, val string) {
		if val != "" {
			dst[key] = val
		}
	}
	setSlice := func(key string, val []string) {
		if len(val) > 0 {
			dst[key] = val
		}
	}
	setStr("method", u.Method)
	setSlice("extra_paths", u.ExtraPaths)
	setSlice("purge_paths", u.PurgePaths)
	setSlice("keep_paths", u.KeepPaths)
	setSlice("allow_paths", u.AllowPaths)
	setStr("marker_file", u.MarkerFile)
	if u.NeedsReboot != nil {
		dst["needs_reboot"] = *u.NeedsReboot
	}
	setStr("run_before", u.RunBefore)
	setStr("run_after", u.RunAfter)
}

// LoadAll reads every <id>.toml in dir, sorted by id. A file that fails to
// decode, or whose stem is not a valid package id, is skipped and its name
// returned in unreadable (the caller WARNs) rather than failing the whole load
// (E2/E3) — one bad def must not make every package invisible.
func LoadAll(dir string) (pkgs []*Package, unreadable []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".toml")
		if !ValidID(id) {
			unreadable = append(unreadable, e.Name())
			continue
		}
		p, lerr := Load(dir+"/"+e.Name(), id)
		if lerr != nil {
			unreadable = append(unreadable, e.Name())
			continue
		}
		pkgs = append(pkgs, p)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ID < pkgs[j].ID })
	return pkgs, unreadable, nil
}
