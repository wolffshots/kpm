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

// Update-check modes (INTEGRITY.md §3). Empty means the default, "tag" — see
// EffectiveUpdateCheck. The zero value is NEVER materialized into a stored def:
// a def that omits the key must hash identically to one that does, or every
// package reads as permanently drifted (INTEGRITY.md §2.2 hazard 1).
const (
	UpdateCheckTag     = "tag"
	UpdateCheckRolling = "rolling"
)

// NormalizeTag strips a single leading "v" so "v1.2.0" and "1.2.0" name the
// same release. It is the one place that rule lives: tagsEqual and the
// checksums lookup both delegate here (INTEGRITY.md §5.2).
func NormalizeTag(tag string) string { return strings.TrimPrefix(tag, "v") }

// sha256HexLen is the length of a hex-encoded SHA-256 digest.
const sha256HexLen = 64

// NormalizeChecksum trims a declared checksum and strips an optional "sha256:"
// prefix (case-insensitive). §5.5 steers a curator straight at GitHub's asset
// digest, which is published in exactly that `sha256:<hex>` form — pasting it
// verbatim used to fail validation and take the WHOLE package out of the
// registry, silently. It normalizes at USE only: the stored bytes are untouched,
// so HashDef is unaffected and a def can never read as drifted for this.
func NormalizeChecksum(s string) string {
	s = strings.TrimSpace(s)
	const prefix = "sha256:"
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		s = strings.TrimSpace(s[len(prefix):])
	}
	return strings.ToLower(s)
}

// isSHA256Hex reports whether s is exactly 64 hex characters (either case).
func isSHA256Hex(s string) bool {
	if len(s) != sha256HexLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

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

	// UpdateCheck opts a package into rolling-release detection: "" (default,
	// meaning "tag") or "rolling" (INTEGRITY.md §3). Read through
	// EffectiveUpdateCheck — the default is never stored.
	UpdateCheck string `toml:"update_check,omitempty"`
	// Conflicts names package ids that cannot be installed alongside this one.
	// The relation is treated as symmetric: kpm checks both directions, so a
	// curator declares it once (INTEGRITY.md §4.2).
	Conflicts []string `toml:"conflicts,omitempty"`
	// Checksums maps a release tag to the curator-attested lowercase sha256 of
	// that release's asset, verified at download time (INTEGRITY.md §5). An
	// absent tag is simply unverified.
	Checksums map[string]string `toml:"checksums,omitempty"`

	Uninstall Uninstall   `toml:"uninstall"`
	Configs   []ModConfig `toml:"configs,omitempty"` // editable config declarations (CONFIG.md §2)

	// ID is the filename stem; not serialized into the TOML body.
	ID string `toml:"-"`
}

// EffectiveUpdateCheck returns UpdateCheck with the default ("tag") applied.
// The default is applied HERE and nowhere else: materializing it into a stored
// def would make every package that omits the key hash differently from its
// registry entry and read as permanently drifted (INTEGRITY.md §2.2 hazard 1).
func (p Package) EffectiveUpdateCheck() string {
	if p.UpdateCheck == "" {
		return UpdateCheckTag
	}
	return p.UpdateCheck
}

// ChecksumFor returns the declared lowercase sha256 for a release tag and
// whether one was declared. Lookup is tag-normalized the same way tagsEqual
// normalizes (a single leading "v" is insignificant), so a curator writing
// "0.4.0" against an upstream tag of "v0.4.0" still gets verification.
//
// A declared value that is not a 64-character hex sha256 yields ("", false):
// a fat-fingered local edit disables verification rather than making the
// package permanently un-installable (INTEGRITY.md §9.2). Use
// ChecksumDeclaredFor to tell that case apart from "no attestation".
func (p Package) ChecksumFor(tag string) (string, bool) {
	// A rolling package's asset changes under a permanently-constant tag, so any
	// declared checksum is correct exactly once and then hard-fails every
	// download after — with wording that reads like a security incident. The
	// combination is a parse-time error for a registry def
	// (ValidateDefCombination), but packages.d is user-editable, so enforce it
	// again at use and degrade to unverified, matching §9.2's pattern.
	if p.EffectiveUpdateCheck() == UpdateCheckRolling {
		return "", false
	}
	sum, ok := lookupChecksum(p.Checksums, tag)
	if !ok {
		return "", false
	}
	sum = NormalizeChecksum(sum)
	if !isSHA256Hex(sum) {
		return "", false
	}
	return sum, true
}

// ChecksumDeclaredFor reports whether checksums carries an entry for tag at
// all, valid or not — the signal that a malformed local edit deserves a WARN.
func (p Package) ChecksumDeclaredFor(tag string) bool {
	_, ok := lookupChecksum(p.Checksums, tag)
	return ok
}

// lookupChecksum finds the entry for tag, comparing tags through NormalizeTag.
// An exact key match wins over a normalized one. Keys are trimmed before
// comparison: a stray-whitespace key like " v1.0.0" is accepted by
// ValidateChecksums but could never match, so the release installed unverified
// with no diagnostic at all.
func lookupChecksum(sums map[string]string, tag string) (string, bool) {
	if len(sums) == 0 {
		return "", false
	}
	if v, ok := sums[tag]; ok {
		return v, true
	}
	want := NormalizeTag(strings.TrimSpace(tag))
	for k, v := range sums {
		if NormalizeTag(strings.TrimSpace(k)) == want {
			return v, true
		}
	}
	return "", false
}

// ValidateUpdateCheck checks the update_check enum. Empty is valid (it means
// the default) — see EffectiveUpdateCheck.
func ValidateUpdateCheck(v string) error {
	switch v {
	case "", UpdateCheckTag, UpdateCheckRolling:
		return nil
	}
	return fmt.Errorf("invalid update_check %q (want %q or %q)", v, UpdateCheckTag, UpdateCheckRolling)
}

// ValidateConflicts checks that every entry is a valid package id and that the
// package does not conflict with itself. Duplicates are not an error — they are
// collapsed at use by the consumer's normalization — but a self-reference is,
// because it signals a curator mistake rather than a harmless redundancy.
func ValidateConflicts(id string, conflicts []string) error {
	for _, c := range conflicts {
		if c == id {
			return fmt.Errorf("conflicts must not list the package's own id %q", id)
		}
		if !ValidID(c) {
			return fmt.Errorf("conflicts entry %q is not a valid package id", c)
		}
	}
	return nil
}

// ValidateChecksums checks that every key is a non-empty release tag and every
// value is a 64-character hex sha256, after NormalizeChecksum strips an optional
// "sha256:" prefix (the form GitHub publishes and §5.5 points a curator at).
// Keys are visited in sorted order so the reported error is deterministic.
func ValidateChecksums(sums map[string]string) error {
	tags := make([]string, 0, len(sums))
	for tag := range sums {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		if strings.TrimSpace(tag) == "" {
			return fmt.Errorf("checksums has an empty release tag key")
		}
		if !isSHA256Hex(NormalizeChecksum(sums[tag])) {
			return fmt.Errorf("checksums entry for tag %q is not a 64-character hex sha256", tag)
		}
	}
	return nil
}

// ValidateDefCombination rejects field combinations that are individually valid
// but jointly incoherent. A rolling tag's asset changes without any registry
// edit, so a checksum pinned to that permanently-constant tag would be correct
// exactly once and hard-fail every download after (INTEGRITY.md §5.6).
func ValidateDefCombination(p Package) error {
	if p.UpdateCheck == UpdateCheckRolling && len(p.Checksums) > 0 {
		return fmt.Errorf(`update_check = "rolling" cannot be combined with checksums (a rolling tag's asset changes without a def edit)`)
	}
	return nil
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

// IsSidecar reports whether name is a filesystem sidecar kpm must never treat
// as package data: AppleDouble files (._foo the Kobo's FAT partition collects
// from macOS/Finder), .DS_Store, and any other dotfile. A valid package id is
// [a-z0-9-]+, which can never start with '.', so a dot-prefixed entry is never a
// real def and never a payload file — it is inert metadata to skip or sweep
// (SELF-SOURCE §3a).
func IsSidecar(name string) bool { return strings.HasPrefix(name, ".") }

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
	// The three integrity fields are optional and functional: write them only
	// when set, so a def that does not use them is byte-identical to before
	// 0.10.0 and its HashDef is unchanged (INTEGRITY.md §2.2 hazard 2).
	setOptional(m, "update_check", p.UpdateCheck)
	setOptionalSlice(m, "conflicts", p.Conflicts)
	setOptionalMap(m, "checksums", p.Checksums)

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
	knownTopKeys       = []string{"name", "source", "forge", "asset", "pin", "registry", "min_kpm", "configs", "update_check", "conflicts", "checksums"}
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
	setOptional(m, "update_check", p.UpdateCheck)
	setOptionalSlice(m, "conflicts", p.Conflicts)
	setOptionalMap(m, "checksums", p.Checksums)

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

// setOptionalSlice/setOptionalMap are setOptional for the collection-valued
// optional fields: an empty collection removes the key, so `conflicts = []` or
// `checksums = {}` is never written and never reads as drift (§2.2 hazard 3).
func setOptionalSlice(m map[string]any, key string, val []string) {
	if len(val) > 0 {
		m[key] = val
	} else {
		delete(m, key)
	}
}

func setOptionalMap(m map[string]any, key string, val map[string]string) {
	if len(val) > 0 {
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

// SkippedDefs names the packages.d entries LoadAll did not return, SPLIT BY
// CAUSE. The two are not interchangeable and callers must not treat them alike:
//
//   - Unparseable: the stem IS a valid package id, so this file may well be a
//     registered package — kpm simply cannot read what it declares. Anything
//     derived from the whole snapshot (notably the conflict graph) is therefore
//     incomplete in a way kpm cannot bound.
//   - InvalidName: the stem is not a valid package id, so the file was never
//     registered and can declare nothing. Its absence from the snapshot costs
//     nothing; it is inert clutter (a stray `NickelClock.toml`, a `README.toml`).
//
// Conflating them made a stray filename refuse every conflict-gated command
// while protecting against nothing.
type SkippedDefs struct {
	Unparseable []string
	InvalidName []string
}

// All returns every skipped filename — the count and WARN surface, where the
// cause does not matter. InvalidName first, then Unparseable; each is already in
// directory order.
func (s SkippedDefs) All() []string {
	if len(s.Unparseable) == 0 {
		return s.InvalidName
	}
	if len(s.InvalidName) == 0 {
		return s.Unparseable
	}
	out := make([]string, 0, len(s.InvalidName)+len(s.Unparseable))
	out = append(out, s.InvalidName...)
	return append(out, s.Unparseable...)
}

// LoadAll reads every <id>.toml in dir, sorted by id. A file that fails to
// decode, or whose stem is not a valid package id, is skipped and its name
// returned in skipped (the caller WARNs) rather than failing the whole load
// (E2/E3) — one bad def must not make every package invisible. See SkippedDefs
// for why the two causes are reported separately.
func LoadAll(dir string) (pkgs []*Package, skipped SkippedDefs, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, SkippedDefs{}, nil
		}
		return nil, SkippedDefs{}, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		// AppleDouble sidecars (._foo.toml the Kobo's FAT partition collects from
		// macOS/Finder) and any other dotfile are never package defs — a valid id
		// is [a-z0-9-]+, which can't start with '.'. Skip silently so they don't
		// masquerade as unreadable definitions (SELF-SOURCE §3a).
		if IsSidecar(e.Name()) {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".toml")
		if !ValidID(id) {
			skipped.InvalidName = append(skipped.InvalidName, e.Name())
			continue
		}
		p, lerr := Load(dir+"/"+e.Name(), id)
		if lerr != nil {
			skipped.Unparseable = append(skipped.Unparseable, e.Name())
			continue
		}
		pkgs = append(pkgs, p)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ID < pkgs[j].ID })
	return pkgs, skipped, nil
}
