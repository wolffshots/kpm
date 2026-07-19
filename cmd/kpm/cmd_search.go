package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"kpm/internal/config"
	"kpm/internal/registry"
	"kpm/internal/version"
)

// cmdSearch lists/filters packages available across cached registries, marking
// installed ones, available def updates, and min_kpm gates (§4, §9.2). Read-only:
// it reads caches exclusively and never touches the network (§9.2).
func (a *App) cmdSearch(args []string) int {
	flags, pos := splitArgs(args, nil)
	if len(flags) > 0 || len(pos) > 1 {
		fmt.Fprintln(os.Stderr, "usage: kpm search [<term>]")
		return exitError
	}
	term := ""
	if len(pos) == 1 {
		term = strings.ToLower(pos[0])
	}

	cfg, err := a.loadRegistryConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm search:", err)
		return exitError
	}
	mans, missing := a.cachedManifests(cfg)
	entries, _ := registry.Merge(mans)

	// Index manifests by registry name so def-update checks can compare against
	// the package's recorded PROVENANCE registry def (matching sync), not just
	// the shadowing winner (C6).
	manByName := map[string]*registry.Manifest{}
	for _, m := range mans {
		manByName[m.Name] = m.Manifest
	}

	ids := make([]string, 0, len(entries))
	for id, e := range entries {
		if term != "" && !strings.Contains(strings.ToLower(id), term) && !strings.Contains(strings.ToLower(e.Def.Name), term) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	if len(ids) == 0 {
		if len(entries) == 0 {
			fmt.Println("no packages available — add a registry and run \"kpm registry refresh\"")
		} else {
			fmt.Printf("no packages match %q\n", term)
		}
		a.printSearchNotes(cfg, missing)
		return exitOK
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tREGISTRY\tINSTALLED\tSTATUS")
	for _, id := range ids {
		e := entries[id]
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", id, e.Registry, dash(a.state.Get(id).InstalledVersion), a.searchStatus(id, e, manByName))
	}
	w.Flush()
	a.printSearchNotes(cfg, missing)
	return exitOK
}

// searchStatus computes the STATUS cell for one available package (C6):
//   - invalid def (missing source/forge/asset) → "invalid def";
//   - installed only when installed_version != "", else a local def is
//     "registered";
//   - the min_kpm gate is annotated only when the package is not installed;
//   - a def update is measured against the PROVENANCE registry def (matching
//     sync), falling back to the winner only when provenance is absent.
func (a *App) searchStatus(id string, e *registry.Entry, manByName map[string]*registry.Manifest) string {
	if len(missingDefFields(e.Def)) > 0 {
		return "invalid def"
	}
	local, err := a.loadPackage(id)
	installed := err == nil && a.state.Get(id).InstalledVersion != ""

	if !installed && !registry.MinKpmSatisfied(version.Version, e.Def.MinKpm) {
		return "requires kpm >= " + e.Def.MinKpm
	}
	if err != nil {
		return "available" // no local def
	}

	defUpdate := a.defUpdateAvailable(id, local, e, manByName)
	if !installed {
		if defUpdate {
			return "registered, def update (run: kpm sync)"
		}
		return "registered"
	}
	if defUpdate {
		return "installed, def update (run: kpm sync)"
	}
	return "installed"
}

// defUpdateAvailable reports whether a registry-managed package's def has moved
// past the last-synced hash. It compares against the def from the package's
// PROVENANCE registry (local.Registry) so shadowing does not make search
// disagree with sync; it falls back to the winning entry only when provenance is
// absent or that registry's cached def is unavailable (C6).
func (a *App) defUpdateAvailable(id string, local *config.Package, e *registry.Entry, manByName map[string]*registry.Manifest) bool {
	ref := e.Def // fallback: the winner
	if local.Registry != "" {
		if m := manByName[local.Registry]; m != nil {
			if d, ok := m.Packages[id]; ok {
				ref = d
			}
		}
	}
	h, err := registry.HashDef(ref)
	return err == nil && h != a.state.Get(id).SyncedDefSHA256
}

// printSearchNotes prints per-registry cache staleness, flagging registries whose
// cache is missing or unparseable (they contribute nothing to the results).
func (a *App) printSearchNotes(cfg *registry.Config, missing []string) {
	miss := map[string]bool{}
	for _, name := range missing {
		miss[name] = true
	}
	now := time.Now()
	for _, r := range cfg.Registries {
		if miss[r.Name] {
			fmt.Printf("registry %s: no usable cache — run \"kpm registry refresh %s\"\n", r.Name, r.Name)
			continue
		}
		fetched := ""
		if a.state.Registries != nil {
			if rs := a.state.Registries[r.Name]; rs != nil {
				fetched = rs.LastFetched
			}
		}
		fmt.Printf("registry %s: %s\n", r.Name, registry.Staleness(fetched, now))
	}
}
