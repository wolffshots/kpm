// Package config handles packages.d/*.toml: the on-disk package definitions,
// their load/save, and parsing of "kpm add" URLs into those definitions.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	MethodManifest     = "manifest"
	MethodMarker       = "marker"
	MethodMarkerRemove = "marker-remove"
)

// Uninstall is the per-package [uninstall] table (UNINSTALL.md §2). All fields
// are optional; a bad block only surfaces errors when "kpm uninstall" runs, so
// registration/check/update never fail on it.
type Uninstall struct {
	Method      string   `toml:"method"`       // "manifest" (default) | "marker" | "marker-remove"
	ExtraPaths  []string `toml:"extra_paths"`  // always-delete software artifacts
	PurgePaths  []string `toml:"purge_paths"`  // user data/config; deleted only with --purge
	KeepPaths   []string `toml:"keep_paths"`   // subtract from the deletion set
	AllowPaths  []string `toml:"allow_paths"`  // per-package allowlist extension (§4)
	MarkerFile  string   `toml:"marker_file"`  // method="marker": file to create; "marker-remove": file to delete
	NeedsReboot *bool    `toml:"needs_reboot"` // nil => default (true for the marker methods, else false)
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
// explicit needs_reboot if set, otherwise true for the marker methods (the
// package only acts on its trigger at the next boot — MARKER-REMOVE §1).
func (u Uninstall) RebootRequired() bool {
	if u.NeedsReboot != nil {
		return *u.NeedsReboot
	}
	m := u.EffectiveMethod()
	return m == MethodMarker || m == MethodMarkerRemove
}

// Validate checks the method and marker constraints. Path-policy validation of
// allow_paths lives in internal/uninstall (it needs the denylist). Called only
// at uninstall time so a bad block never breaks other commands.
func (u Uninstall) Validate() error {
	m := u.EffectiveMethod()
	switch m {
	case MethodManifest, MethodMarker, MethodMarkerRemove:
	default:
		return fmt.Errorf("invalid uninstall method %q (want %q, %q, or %q)", u.Method, MethodManifest, MethodMarker, MethodMarkerRemove)
	}
	if (m == MethodMarker || m == MethodMarkerRemove) && strings.TrimSpace(u.MarkerFile) == "" {
		return fmt.Errorf("uninstall method %q requires marker_file", m)
	}
	return nil
}

// Config formats (CONFIG.md §2). Only ini and text are editable in this
// release; env/keyvalue are reserved for a future additive extension and are
// deliberately rejected by Validate for now (forward-compat: bump min_kpm when a
// new format is added).
const (
	FormatINI  = "ini"
	FormatText = "text"
)

// Config reload modes (CONFIG.md §2). Empty means auto (see EffectiveReload).
const (
	ReloadAuto   = "auto"
	ReloadReboot = "reboot"
)

// MaxTemplate caps a config declaration's seed template (16 KiB). A template is
// starter content a mod ships so `kpm config init` can create the file from an
// example; these are short hand-authored files, not data blobs (CONFIG.md §2).
const MaxTemplate = 16 * 1024

// ModConfig is one [[configs]] entry: a config file a package declares so kpm
// can view and edit it on-device (CONFIG.md §2). All fields are optional except
// name/path/format. Like Uninstall, a bad block only surfaces errors when
// "kpm config" runs, so registration/check/update never fail on it.
type ModConfig struct {
	Name          string   `toml:"name"`                     // display label + CLI/UI selector (unique per package)
	Path          string   `toml:"path"`                     // device-absolute path; must pass the uninstall path policy
	Format        string   `toml:"format"`                   // "ini" | "text" (env/keyvalue reserved)
	Reload        string   `toml:"reload,omitempty"`         // "auto" (default) | "reboot"
	Create        bool     `toml:"create,omitempty"`         // create the file on first edit when missing
	SensitiveKeys []string `toml:"sensitive_keys,omitempty"` // keys whose values are masked in human output/logs
	Description   string   `toml:"description,omitempty"`    // one-liner for the UI; presentational
	Template      string   `toml:"template,omitempty"`       // starter content for `kpm config init` (CONFIG.md §2)
}

// Createable reports whether `kpm config init`/`config set` may create this
// file when it is missing: either create=true (seed from empty) or a template is
// declared (seed from the example). Mirrors EffectiveReload's derived-getter style.
func (c ModConfig) Createable() bool { return c.Create || c.Template != "" }

// EffectiveReload returns Reload with the default ("auto") applied.
func (c ModConfig) EffectiveReload() string {
	if c.Reload == "" {
		return ReloadAuto
	}
	return c.Reload
}

// RebootRequired reports whether an edit to this config needs a reboot to take
// effect (reload == "reboot") — feeds the reboot_required JSON field (CONFIG.md §3).
func (c ModConfig) RebootRequired() bool { return c.EffectiveReload() == ReloadReboot }

// IsSensitive reports whether key is listed in sensitive_keys (case-insensitive);
// such values are masked in human output and logs but never in --json (CONFIG.md §2).
func (c ModConfig) IsSensitive(key string) bool {
	for _, k := range c.SensitiveKeys {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// Validate checks a single declaration's format and required fields. Path-policy
// validation lives in internal/uninstall (it needs the denylist), mirroring how
// Uninstall.Validate defers allow_paths policy. Called at parse/sync time and
// before every read/write so a bad block never breaks other commands.
func (c ModConfig) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("config declaration requires name")
	}
	// The name doubles as the CLI/UI selector, passed as a bare argv token. A name
	// that parses as an integer would be hijacked by 1-based-index selection (so it
	// could silently resolve to the wrong file), and a name starting with "-" would
	// be eaten by flag parsing — same reasoning as ValidID for package ids. Reject
	// both at declaration time so a def can never carry an unusable selector.
	trimmedName := strings.TrimSpace(c.Name)
	if _, err := strconv.Atoi(trimmedName); err == nil {
		return fmt.Errorf("config name %q must not be a number (it would collide with index selection)", c.Name)
	}
	if strings.HasPrefix(trimmedName, "-") {
		return fmt.Errorf("config name %q must not start with '-'", c.Name)
	}
	if strings.TrimSpace(c.Path) == "" {
		return fmt.Errorf("config %q requires path", c.Name)
	}
	switch c.Format {
	case FormatINI, FormatText:
	default:
		return fmt.Errorf("config %q has invalid format %q (want %q or %q)", c.Name, c.Format, FormatINI, FormatText)
	}
	switch c.Reload {
	case "", ReloadAuto, ReloadReboot:
	default:
		return fmt.Errorf("config %q has invalid reload %q (want %q or %q)", c.Name, c.Reload, ReloadAuto, ReloadReboot)
	}
	// A seed template is bounded and text: an oversized or NUL-bearing template is
	// dropped at parse like a bad format/path, so it can never reach the seed
	// engine (CONFIG.md §2). The 16 KiB cap keeps registry defs small.
	if len(c.Template) > MaxTemplate {
		return fmt.Errorf("config %q template is %d bytes, over the %d-byte limit", c.Name, len(c.Template), MaxTemplate)
	}
	if strings.IndexByte(c.Template, 0) >= 0 {
		return fmt.Errorf("config %q template contains a NUL byte (binary), refusing", c.Name)
	}
	return nil
}

// ValidateConfigs validates every declaration and enforces name uniqueness
// (case-insensitive) across the slice — the name is the CLI/UI selector, so two
// configs sharing a name are ambiguous (CONFIG.md §2).
func ValidateConfigs(cfgs []ModConfig) error {
	seen := map[string]bool{}
	for _, c := range cfgs {
		if err := c.Validate(); err != nil {
			return err
		}
		key := strings.ToLower(strings.TrimSpace(c.Name))
		if seen[key] {
			return fmt.Errorf("duplicate config name %q", c.Name)
		}
		seen[key] = true
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

	Uninstall Uninstall   `toml:"uninstall"`
	Configs   []ModConfig `toml:"configs,omitempty"` // editable config declarations (CONFIG.md §2)

	// ID is the filename stem; not serialized into the TOML body.
	ID string `toml:"-"`
}

// Host/Owner/Repo split the "source" field. They delegate to the package-level
// Source* helpers so there is a single split implementation that kpm can also
// apply to an effective (state-resident) source string (SELF-SOURCE §3).
func (p Package) Host() string  { return SourceHost(p.Source) }
func (p Package) Owner() string { return SourceOwner(p.Source) }
func (p Package) Repo() string  { return SourceRepo(p.Source) }

// Configured reports whether the package has a usable host/owner/repo source.
// An empty/partial source marks self-update (or any package) "unconfigured":
// check/update skip it silently instead of erroring (F7).
func (p Package) Configured() bool {
	return SourceConfigured(p.Source)
}

// SourceHost/Owner/Repo split a "host/owner/repo" source string. kpm's own
// source lives in state.json (SELF-SOURCE §1), so the forge call sites split
// the effective source string through these rather than reading p.Source
// directly, which would bypass the state override (SELF-SOURCE §3).
func SourceHost(src string) string  { return field(src, 0) }
func SourceOwner(src string) string { return field(src, 1) }
func SourceRepo(src string) string  { return field(src, 2) }

// SourceConfigured reports whether src has a usable host/owner/repo triple (F7).
func SourceConfigured(src string) bool {
	return SourceHost(src) != "" && SourceOwner(src) != "" && SourceRepo(src) != ""
}

func field(source string, i int) string {
	parts := strings.Split(source, "/")
	if i < len(parts) {
		return parts[i]
	}
	return ""
}

// Ids must start with an alphanumeric so an id can never begin with "-" and
// read as a flag when passed as a bare argv token (defense in depth: callers
// already pass ids list-form, never through a shell).
var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidID reports whether id matches the package-id rule: lowercase
// alphanumerics and dashes, starting with an alphanumeric.
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

	// Config declarations are registry-projected as a whole (never hand-merged
	// like [uninstall]'s unknown keys), so overlay the array or drop the key.
	overlayConfigs(m, p.Configs)

	return encodeTOMLFile(path, m)
}

// knownTopKeys / knownUninstallKeys are the keys SaveReplace treats
// authoritatively: any of them absent from the written Package is deleted from
// the on-disk file. Every other key (unknown top-level tables/fields, unknown
// [uninstall] keys) is preserved (A1).
var (
	knownTopKeys       = []string{"name", "source", "forge", "asset", "pin", "registry", "min_kpm", "configs"}
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

	// configs was cleared by the knownTopKeys loop; re-set it authoritatively so
	// a config the registry dropped disappears locally (A1).
	overlayConfigs(m, p.Configs)

	return encodeTOMLFile(path, m)
}

// overlayConfigs writes p's config declarations as the "configs" array-of-tables
// or removes the key when there are none, so a fresh package never emits an empty
// configs array (E1/A1).
func overlayConfigs(m map[string]any, cfgs []ModConfig) {
	if len(cfgs) > 0 {
		m["configs"] = cfgs
	} else {
		delete(m, "configs")
	}
}

// encodeTOMLFile writes m to path as TOML atomically (encode to a buffer, then
// temp file + fsync + rename + directory fsync). A power loss mid-write on the
// Kobo's FAT32 partition can no longer leave a truncated <id>.toml that LoadAll
// would skip (making the package silently vanish).
func encodeTOMLFile(path string, m map[string]any) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(m); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".kpm-toml-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	if d, derr := os.Open(dir); derr == nil { // best-effort directory fsync
		_ = d.Sync()
		d.Close()
	}
	return nil
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
		// AppleDouble sidecars (._foo.toml the Kobo's FAT partition collects from
		// macOS/Finder) and any other dotfile are never package defs — a valid id
		// is [a-z0-9-]+, which can't start with '.'. Skip silently so they don't
		// masquerade as unreadable definitions (SELF-SOURCE §3a).
		if strings.HasPrefix(e.Name(), ".") {
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
