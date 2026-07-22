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
type PackageDef struct {
	Name        string             `toml:"name"`
	Source      string             `toml:"source"`
	Forge       string             `toml:"forge"`
	Asset       string             `toml:"asset"`
	MinKpm      string             `toml:"min_kpm"`
	Description string             `toml:"description,omitempty"` // JSON-OUTPUT.md §3
	Homepage    string             `toml:"homepage,omitempty"`    // JSON-OUTPUT.md §3
	Uninstall   config.Uninstall   `toml:"uninstall"`
	Configs     []config.ModConfig `toml:"configs,omitempty"` // CONFIG.md §2
}

// Manifest is a parsed registry.toml.
type Manifest struct {
	SchemaVersion int                    `toml:"schema_version"`
	Packages      map[string]*PackageDef `toml:"packages"`
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
	for id := range m.Packages {
		if !config.ValidID(id) {
			delete(m.Packages, id)
			continue
		}
		// A def whose config declarations are invalid (bad format, unsafe path, or
		// duplicate name) is dropped, exactly like an invalid id — a malformed
		// [[configs]] block must not reach the editor (CONFIG.md §2).
		if def := m.Packages[id]; def != nil {
			if err := uninstall.ValidateConfigDecls(def.Configs); err != nil {
				delete(m.Packages, id)
			}
		}
	}
	if m.Packages == nil {
		m.Packages = map[string]*PackageDef{}
	}
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
