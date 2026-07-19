package main

import (
	"flag"
	"fmt"
	"os"

	"kpm/internal/config"
	"kpm/internal/registry"
)

// cmdSync re-copies registry defs for registry-managed packages, propagating
// curated uninstall-recipe fixes and changed asset globs (§5/§9.7). It reads
// caches only. pin is never read from or written to a registry def. Local drift
// is reported and skipped unless --overwrite.
func (a *App) cmdSync(args []string) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	overwrite := fs.Bool("overwrite", false, "overwrite locally-drifted defs")
	flags, pos := splitArgs(args, nil)
	if err := fs.Parse(flags); err != nil {
		return exitError
	}

	all, err := a.loadPackages()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm sync:", err)
		return exitError
	}
	byID := map[string]*config.Package{}
	for _, p := range all {
		byID[p.ID] = p
	}

	// Select targets: named ids, or every registry-managed package.
	var targets []*config.Package
	issues := 0
	if len(pos) > 0 {
		for _, id := range pos {
			if !config.ValidID(id) {
				fmt.Fprintf(os.Stderr, "kpm sync: invalid package id %q\n", id)
				return exitError
			}
			p, ok := byID[id]
			if !ok {
				// Not registered at all: a usage error (exit 1), not a partial
				// failure among valid targets (C7).
				fmt.Fprintf(os.Stderr, "kpm sync: %q not registered\n", id)
				return exitError
			}
			if p.Registry == "" {
				fmt.Fprintf(os.Stderr, "kpm sync: %q is hand-added (no registry) — nothing to sync\n", id)
				issues++
				continue
			}
			targets = append(targets, p)
		}
	} else {
		for _, p := range all {
			if p.Registry != "" {
				targets = append(targets, p)
			}
		}
	}

	if len(targets) == 0 && issues == 0 {
		fmt.Println("no registry-managed packages to sync")
		return exitOK
	}

	cfg, err := a.loadRegistryConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm sync:", err)
		return exitError
	}

	applied, upToDate := 0, 0
	changed := false
	for _, p := range targets {
		outcome, stateChanged := a.syncOne(cfg, p, *overwrite)
		if stateChanged {
			changed = true
		}
		switch outcome {
		case syncApplied:
			applied++
		case syncUpToDate:
			upToDate++
		default:
			issues++
		}
	}

	// Persist only when something actually changed — a hash backfill or an
	// applied def (C7). A pure "up to date" run writes nothing.
	if changed {
		if err := a.state.Save(); err != nil {
			fmt.Fprintln(os.Stderr, "kpm sync: state:", err)
			return exitError
		}
	}
	fmt.Printf("sync: %d applied, %d up to date, %d skipped/failed\n", applied, upToDate, issues)
	if issues > 0 {
		return exitPartial
	}
	return exitOK
}

type syncOutcome int

const (
	syncApplied syncOutcome = iota
	syncUpToDate
	syncSkipped
)

// syncOne syncs a single registry-managed package. It uses the package's recorded
// registry (provenance) as the def source. It returns the outcome and whether it
// mutated state (an applied def or a healed/backfilled hash), so cmdSync can skip
// state.Save when nothing changed (C7).
func (a *App) syncOne(cfg *registry.Config, p *config.Package, overwrite bool) (syncOutcome, bool) {
	r, ok := cfg.Find(p.Registry)
	if !ok {
		a.paths.Log("WARN", fmt.Sprintf("%s  registry %q no longer configured", p.ID, p.Registry))
		fmt.Printf("%s: registry %q no longer configured — left intact\n", p.ID, p.Registry)
		return syncSkipped, false
	}
	m, err := a.cachedManifest(r)
	if err != nil {
		fmt.Printf("%s: %v\n", p.ID, err)
		return syncSkipped, false
	}
	remoteDef, ok := m.Packages[p.ID]
	if !ok {
		// Id disappeared from its registry: WARN, leave the local def intact (§9.7).
		a.paths.Log("WARN", fmt.Sprintf("%s  no longer in registry %q", p.ID, p.Registry))
		fmt.Printf("%s: no longer offered by registry %q — left intact\n", p.ID, p.Registry)
		return syncSkipped, false
	}

	localDef := registry.DefFromPackage(p)
	localHash, _ := registry.HashDef(localDef)
	remoteHash, _ := registry.HashDef(remoteDef)
	ps := a.state.Get(p.ID)
	synced := ps.SyncedDefSHA256

	apply := func() (syncOutcome, bool) {
		diffs := registry.FieldDiffs(localDef, remoteDef)
		newPkg := remoteDef.ToPackage(p.ID, p.Registry, p.Pin)
		if err := config.SaveReplace(a.paths.PackageFile(p.ID), newPkg); err != nil {
			fmt.Printf("%s: write failed: %v\n", p.ID, err)
			return syncSkipped, false
		}
		ps.SyncedDefSHA256 = remoteHash
		a.paths.Log("SYNC", fmt.Sprintf("%s  from %s (%d field change(s))", p.ID, p.Registry, len(diffs)))
		if len(diffs) == 0 {
			fmt.Printf("%s: synced (no visible field changes)\n", p.ID)
		} else {
			fmt.Printf("%s: synced from %s\n", p.ID, p.Registry)
			for _, d := range diffs {
				fmt.Printf("    %s\n", d)
			}
		}
		return syncApplied, true
	}

	// Decision tree (A2):
	// 1. Local content already equals the registry def → up to date. Heal an
	//    empty/stale/legacy synced hash (content-equal defs are always up to
	//    date regardless of the stored hash — survives a canonical-encoding
	//    change as one transparent backfill).
	if localHash == remoteHash {
		if synced != remoteHash {
			ps.SyncedDefSHA256 = remoteHash
			return syncUpToDate, true
		}
		return syncUpToDate, false
	}
	// 2. No baseline recorded → apply (nothing to compare against).
	if synced == "" {
		return apply()
	}
	// 3. Local still matches the last sync → clean upstream change → apply.
	if localHash == synced {
		return apply()
	}
	// 4. Local drift: the def was edited since the last sync. Skip unless
	//    --overwrite restores it from the registry.
	if !overwrite {
		a.paths.Log("WARN", fmt.Sprintf("%s  local drift — skipped (use --overwrite)", p.ID))
		fmt.Printf("%s: local def has been edited since last sync — skipped (use --overwrite to replace)\n", p.ID)
		return syncSkipped, false
	}
	return apply()
}
