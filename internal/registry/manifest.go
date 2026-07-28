package registry

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"kpm/internal/config"
	"kpm/internal/uninstall"
)

// SchemaVersion is the only registry.toml schema this kpm understands.
const SchemaVersion = 1

// PackageDef is one [packages.<id>] entry in a registry.toml. Its fields are
// identical to the local packages.d schema except there is no pin (pins are a
// local decision, never distributed — REGISTRY.md §2) and the optional min_kpm.
//
// Description/Homepage are optional, purely-presentational metadata surfaced by
// "kpm search --json" for the Nickel UI (JSON-OUTPUT.md §3). They are registry-
// only: they are never copied into a local packages.d def and are deliberately
// excluded from HashDef, so adding/changing them never registers as a def
// update or local drift (the sync machinery ignores them).
//
// TestedFw is the newest firmware the curator has positively confirmed this
// release working on. Advisory only — it never gates install/update. Like
// Description/Homepage it is registry-only: excluded from HashDef and never
// copied into packages.d, so a curator edit is never sync drift (REGISTRY.md §2).
//
// NhName is the NickelHook info name (`nh_info.name`) — the string NickelHook
// stamps into its crash dump-log filename `/mnt/onboard/<nh_name>_<date>.log`.
// It is NOT the package id or display name, so `kpm doctor` needs it to find a
// package's load-failure dump. Optional and registry-only (excluded from HashDef,
// never projected to packages.d) exactly like TestedFw; doctor falls back to the
// display Name with spaces stripped when it is absent (DOCTOR.md).
//
// UpdateCheck/Conflicts/Checksums are the 0.10.0 integrity fields
// (INTEGRITY.md §2). Unlike the four above they are FUNCTIONAL — they change
// what install/update do — so all three are projected into packages.d and
// included in HashDef. Every one carries omitempty: a def that omits them must
// encode to the same canonical bytes as before 0.10.0, or its hash changes and
// it reads as spurious drift (§2.2 hazard 2).
type PackageDef struct {
	Name        string             `toml:"name"`
	Source      string             `toml:"source"`
	Forge       string             `toml:"forge"`
	Asset       string             `toml:"asset"`
	MinKpm      string             `toml:"min_kpm"`
	Description string             `toml:"description,omitempty"`  // JSON-OUTPUT.md §3
	Homepage    string             `toml:"homepage,omitempty"`     // JSON-OUTPUT.md §3
	TestedFw    string             `toml:"tested_fw,omitempty"`    // REGISTRY.md §2
	NhName      string             `toml:"nh_name,omitempty"`      // DOCTOR.md
	UpdateCheck string             `toml:"update_check,omitempty"` // INTEGRITY.md §3
	Conflicts   []string           `toml:"conflicts,omitempty"`    // INTEGRITY.md §4
	Uninstall   config.Uninstall   `toml:"uninstall"`
	Configs     []config.ModConfig `toml:"configs,omitempty"` // CONFIG.md §2
	// Checksums is last: TOML requires tables after scalars, and BurntSushi's
	// encoder writes struct fields in declaration order.
	Checksums map[string]string `toml:"checksums,omitempty"` // INTEGRITY.md §5
}

// EffectiveUpdateCheck returns UpdateCheck with the default ("tag") applied,
// mirroring config.Package.EffectiveUpdateCheck. The default is applied only
// here — never stored (INTEGRITY.md §2.2 hazard 1).
func (d *PackageDef) EffectiveUpdateCheck() string {
	if d.UpdateCheck == "" {
		return config.UpdateCheckTag
	}
	return d.UpdateCheck
}

// DroppedDef records a package def ParseManifest refused, with the reason. A
// dropped def is INVISIBLE: the package simply is not in the registry, which
// from the outside is indistinguishable from one the curator never published —
// a disproportionate outcome for (say) one mistyped checksum. Callers report
// these so the cause is discoverable (m3). Not part of the TOML.
type DroppedDef struct {
	ID     string
	Reason string
}

// Manifest is a parsed registry.toml.
type Manifest struct {
	SchemaVersion int                    `toml:"schema_version"`
	Packages      map[string]*PackageDef `toml:"packages"`
	// Dropped lists the ids ParseManifest refused, sorted. Populated at parse,
	// never decoded from the file.
	Dropped []DroppedDef `toml:"-"`
}

// ParseManifest decodes registry.toml bytes and gates on schema_version: a
// missing (0) or unsupported version is refused so a newer registry format never
// silently mis-parses (REGISTRY.md §9.3). Unknown fields are ignored (forward
// compatibility). Package ids that are not [a-z0-9-]+ are dropped.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse registry.toml: %w", err)
	}
	if m.SchemaVersion == 0 {
		return nil, fmt.Errorf("registry has no schema_version — unsupported registry schema; update kpm")
	}
	if m.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported registry schema_version %d (this kpm supports %d); update kpm", m.SchemaVersion, SchemaVersion)
	}
	// drop removes a def and records why, so the loss is reportable instead of
	// silent (m3).
	drop := func(id, reason string) {
		delete(m.Packages, id)
		m.Dropped = append(m.Dropped, DroppedDef{ID: id, Reason: reason})
	}
	for id := range m.Packages {
		if !config.ValidID(id) {
			drop(id, "not a valid package id (want [a-z0-9-]+ starting with an alphanumeric)")
			continue
		}
		// A def whose declarations are invalid (bad config format, unsafe path,
		// duplicate name, bad update_check/conflicts/checksums, or an incoherent
		// combination) is dropped, exactly like an invalid id — a malformed block
		// must never reach the machinery that acts on it (CONFIG.md §2,
		// INTEGRITY.md §9).
		def := m.Packages[id]
		if def == nil {
			continue
		}
		if err := uninstall.ValidateConfigDecls(def.Configs); err != nil {
			drop(id, err.Error())
			continue
		}
		if err := config.ValidateUpdateCheck(def.UpdateCheck); err != nil {
			drop(id, err.Error())
			continue
		}
		if err := config.ValidateConflicts(id, def.Conflicts); err != nil {
			drop(id, err.Error())
			continue
		}
		if err := config.ValidateChecksums(def.Checksums); err != nil {
			drop(id, err.Error())
			continue
		}
		if err := config.ValidateDefCombination(config.Package{
			UpdateCheck: def.UpdateCheck, Checksums: def.Checksums,
		}); err != nil {
			drop(id, err.Error())
			continue
		}
		// §3.8: a rolling entry MUST declare min_kpm. Nothing enforced it, so a
		// curator omission silently reproduces the exact bug feature A fixes: an
		// older kpm ignores the unknown update_check key, compares tags, and
		// reports the package up to date forever on every 0.9.x device. Truthful
		// silence ("requires kpm >= …") beats a false all-clear, so the entry is
		// incoherent without it and joins the other dropped defs.
		if def.EffectiveUpdateCheck() == config.UpdateCheckRolling && strings.TrimSpace(def.MinKpm) == "" {
			drop(id, `update_check = "rolling" requires min_kpm (an older kpm would ignore it and report this package up to date forever)`)
			continue
		}
	}
	if m.Packages == nil {
		m.Packages = map[string]*PackageDef{}
	}
	sort.Slice(m.Dropped, func(i, j int) bool { return m.Dropped[i].ID < m.Dropped[j].ID })
	return &m, nil
}

// SortedIDs returns the package ids in a manifest, sorted.
func (m *Manifest) SortedIDs() []string {
	ids := make([]string, 0, len(m.Packages))
	for id := range m.Packages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// RawURLs builds the ordered candidate raw-file HTTPS URLs for a registry's
// manifest per forge (REGISTRY.md §2). url is host/owner/repo.
//
// GitHub uses a single raw.githubusercontent.com form (it resolves both branch
// and tag refs). Forgejo/Gitea distinguishes them: `/raw/branch/<ref>/` resolves
// branches only and `/raw/tag/<ref>/` tags only (verified live against
// Codeberg), so both are returned in order — refreshOne tries the branch form
// first and falls through to the tag form only on a 404 (B2).
func RawURLs(forge, url, ref, path string) ([]string, error) {
	host, owner, repo := splitURL(url)
	if host == "" || owner == "" || repo == "" {
		return nil, fmt.Errorf("registry url must be host/owner/repo, got %q", url)
	}
	if ref == "" {
		ref = DefaultRef
	}
	if path == "" {
		path = DefaultPath
	}
	switch forge {
	case config.ForgeGitHub:
		return []string{fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, repo, ref, path)}, nil
	case config.ForgeForgejo:
		return []string{
			fmt.Sprintf("https://%s/%s/%s/raw/branch/%s/%s", host, owner, repo, ref, path),
			fmt.Sprintf("https://%s/%s/%s/raw/tag/%s/%s", host, owner, repo, ref, path),
		}, nil
	default:
		return nil, fmt.Errorf("unknown forge %q", forge)
	}
}

// RawURL returns the primary candidate raw-file URL (the branch form for
// Forgejo). Used where a single representative URL is enough (e.g. deriving the
// host to poll for connectivity).
func RawURL(forge, url, ref, path string) (string, error) {
	urls, err := RawURLs(forge, url, ref, path)
	if err != nil {
		return "", err
	}
	return urls[0], nil
}

func splitURL(url string) (host, owner, repo string) {
	parts := strings.Split(strings.Trim(url, "/"), "/")
	if len(parts) >= 3 {
		return parts[0], parts[1], parts[2]
	}
	return "", "", ""
}
