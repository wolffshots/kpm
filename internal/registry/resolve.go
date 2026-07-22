package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"kpm/internal/config"
)

// NamedManifest pairs a registry name with its parsed manifest, in config order.
type NamedManifest struct {
	Name     string
	Manifest *Manifest
}

// Entry is a package available from the merged registry view: its winning
// definition and the registry it came from.
type Entry struct {
	ID       string
	Registry string
	Def      *PackageDef
}

// Conflict records a package id offered by more than one registry: the winner
// (earliest in config order) and the shadowed registries (REGISTRY.md §9.6).
type Conflict struct {
	ID       string
	Winner   string
	Shadowed []string
}

// Merge resolves the merged package view across registries in config order:
// the earliest registry offering an id wins; later ones are shadowed and
// invisible to search/install. Returns the winning entries by id and the
// conflicts (sorted by id) for a WARN-once report.
func Merge(mans []NamedManifest) (map[string]*Entry, []Conflict) {
	entries := map[string]*Entry{}
	shadowed := map[string][]string{}
	for _, nm := range mans {
		if nm.Manifest == nil {
			continue
		}
		for _, id := range nm.Manifest.SortedIDs() {
			def := nm.Manifest.Packages[id]
			if _, ok := entries[id]; ok {
				shadowed[id] = append(shadowed[id], nm.Name)
				continue
			}
			entries[id] = &Entry{ID: id, Registry: nm.Name, Def: def}
		}
	}
	var conflicts []Conflict
	for id, sh := range shadowed {
		conflicts = append(conflicts, Conflict{ID: id, Winner: entries[id].Registry, Shadowed: sh})
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].ID < conflicts[j].ID })
	return entries, conflicts
}

// HashDef returns the SHA-256 (hex) of the canonical TOML encoding of a package
// def, excluding pin/registry (which PackageDef does not carry). install/sync
// store this as synced_def_sha256 to detect drift and up-to-date state (§9.7).
//
// Before encoding, every zero-length (non-nil-but-empty) uninstall slice is
// normalized to nil so a registry def's `purge_paths = []` hashes identically to
// a local def that simply omits the key — the reproduced false-drift case (A3).
// needs_reboot is deliberately NOT normalized: nil vs explicit false differ
// semantically for the marker method and round-trip consistently.
func HashDef(def *PackageDef) (string, error) {
	norm := *def
	// Description/Homepage are presentational registry-only metadata (JSON-
	// OUTPUT.md §3) and TestedFw is advisory firmware metadata (REGISTRY.md §2):
	// exclude all three from the canonical hash so changing them never looks like
	// a def update or local drift to install/sync.
	norm.Description = ""
	norm.Homepage = ""
	norm.TestedFw = ""
	u := norm.Uninstall
	u.ExtraPaths = nilIfEmpty(u.ExtraPaths)
	u.PurgePaths = nilIfEmpty(u.PurgePaths)
	u.KeepPaths = nilIfEmpty(u.KeepPaths)
	u.AllowPaths = nilIfEmpty(u.AllowPaths)
	norm.Uninstall = u
	// Config declarations are functional (edits target them), so include them in
	// the hash like Uninstall (CONFIG.md §2); normalize empty slices to nil so a
	// def's `configs = []` or `sensitive_keys = []` hashes identically to omitting
	// the key (A3).
	norm.Configs = normConfigs(norm.Configs)

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(&norm); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

// nilIfEmpty collapses a zero-length slice to nil so it is omitted from the
// canonical encoding (A3).
func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

// normConfigs returns a copy with an empty slice collapsed to nil and each
// entry's sensitive_keys normalized the same way, so a def's config metadata
// hashes stably regardless of empty-vs-omitted spelling (A3).
func normConfigs(cfgs []config.ModConfig) []config.ModConfig {
	if len(cfgs) == 0 {
		return nil
	}
	out := make([]config.ModConfig, len(cfgs))
	for i, c := range cfgs {
		c.SensitiveKeys = nilIfEmpty(c.SensitiveKeys)
		out[i] = c
	}
	return out
}

// DefFromPackage projects a local package def onto the syncable payload, dropping
// pin/registry/id so its hash can be compared against a registry def (§9.7).
func DefFromPackage(p *config.Package) *PackageDef {
	return &PackageDef{
		Name:      p.Name,
		Source:    p.Source,
		Forge:     p.Forge,
		Asset:     p.Asset,
		MinKpm:    p.MinKpm,
		Uninstall: p.Uninstall,
		Configs:   p.Configs,
	}
}

// ToPackage materializes a registry def into a local package def, stamping the
// registry provenance and carrying over a locally-decided pin (§5).
func (d *PackageDef) ToPackage(id, registry, pin string) *config.Package {
	return &config.Package{
		ID:        id,
		Name:      d.Name,
		Source:    d.Source,
		Forge:     d.Forge,
		Asset:     d.Asset,
		MinKpm:    d.MinKpm,
		Registry:  registry,
		Pin:       pin,
		Uninstall: d.Uninstall,
		Configs:   d.Configs,
	}
}

// FieldDiffs returns human-readable "field: old -> new" lines for the fields that
// differ between a local def and a registry def, for sync's per-package summary.
func FieldDiffs(local, remote *PackageDef) []string {
	var out []string
	add := func(name, a, b string) {
		if a != b {
			out = append(out, name+": "+quote(a)+" -> "+quote(b))
		}
	}
	add("name", local.Name, remote.Name)
	add("source", local.Source, remote.Source)
	add("forge", local.Forge, remote.Forge)
	add("asset", local.Asset, remote.Asset)
	add("min_kpm", local.MinKpm, remote.MinKpm)
	lh, _ := HashDef(&PackageDef{Uninstall: local.Uninstall})
	rh, _ := HashDef(&PackageDef{Uninstall: remote.Uninstall})
	if lh != rh {
		out = append(out, "uninstall: (changed)")
	}
	lc, _ := HashDef(&PackageDef{Configs: local.Configs})
	rc, _ := HashDef(&PackageDef{Configs: remote.Configs})
	if lc != rc {
		out = append(out, "configs: (changed)")
	}
	return out
}

func quote(s string) string {
	if s == "" {
		return `""`
	}
	return s
}

// MinKpmSatisfied reports whether the running kpm version meets a def's min_kpm.
// Empty min_kpm is always satisfied. Comparison is numeric dotted (split on ".",
// missing segments = 0, a single leading "v" stripped) — REGISTRY.md §9.8.
func MinKpmSatisfied(running, required string) bool {
	if strings.TrimSpace(required) == "" {
		return true
	}
	return compareVersions(running, required) >= 0
}

func compareVersions(a, b string) int {
	as, bs := versionParts(a), versionParts(b)
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// FirmwareUntested reports whether a device running deviceFw is newer — by
// major.minor ONLY — than the firmware a package was last confirmed working on
// (testedFw): the advisory "untested on your firmware" signal (D). The third
// version segment is a per-release build counter and is deliberately ignored;
// comparing it would flip every package to "untested" on any firmware bump.
// Non-numeric segments count as 0 (reusing versionParts, §9.8). A missing
// deviceFw or missing testedFw never warns — firmware compatibility is advisory
// and silence is the safe default.
func FirmwareUntested(deviceFw, testedFw string) bool {
	if strings.TrimSpace(deviceFw) == "" || strings.TrimSpace(testedFw) == "" {
		return false
	}
	dMaj, dMin := majorMinor(deviceFw)
	tMaj, tMin := majorMinor(testedFw)
	if dMaj != tMaj {
		return dMaj > tMaj
	}
	return dMin > tMin
}

// majorMinor returns the first two dotted-numeric segments of v (missing
// segments = 0), the granularity FirmwareUntested compares at.
func majorMinor(v string) (major, minor int) {
	p := versionParts(v)
	if len(p) > 0 {
		major = p[0]
	}
	if len(p) > 1 {
		minor = p[1]
	}
	return major, minor
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil
	}
	segs := strings.Split(v, ".")
	out := make([]int, len(segs))
	for i, s := range segs {
		n, _ := strconv.Atoi(s) // non-numeric segment counts as 0 (§9.8)
		out[i] = n
	}
	return out
}

// Staleness renders a short cache-age note ("cached 3d ago", "never refreshed")
// for search/registry list output (REGISTRY.md §9.2).
func Staleness(lastFetched string, now time.Time) string {
	if lastFetched == "" {
		return "never refreshed"
	}
	t, err := time.Parse(time.RFC3339, lastFetched)
	if err != nil {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return "cached just now"
	case d < time.Minute:
		return "cached just now"
	case d < time.Hour:
		return "cached " + strconv.Itoa(int(d/time.Minute)) + "m ago"
	case d < 24*time.Hour:
		return "cached " + strconv.Itoa(int(d/time.Hour)) + "h ago"
	default:
		return "cached " + strconv.Itoa(int(d/(24*time.Hour))) + "d ago"
	}
}
