package main

import (
	"fmt"
	"os"
	"strings"

	"kpm/internal/device"
	"kpm/internal/registry"
)

// cmdAdoptSelf enrols kpm's own self-update from a configured registry — the one
// mutating, network step the graphical UI's "Enable self-update" button calls
// (kpm-self-enrol-plan §2). It funnels kpm's source/forge into state.json
// (durable across the self-update that clobbers kpm.toml, SELF-SOURCE §5) and
// preserves any existing pin. It requires a configured registry that offers a kpm
// package and never hardcodes a source (§2 registry bootstrap) — the source comes
// from whatever registry the user configured.
//
// Best-effort refresh (§3): it attempts a network refresh of the configured
// registries first, but if the network is down and the cache already offers a kpm
// def it proceeds against the cache with a logged note, only surfacing the network
// error when the cache cannot satisfy the adopt at all. It calls refreshOne /
// persistDef / state.Save directly — never re-entering run() — so it never
// deadlocks on the single-instance lock newApp already holds (§4).
func (a *App) cmdAdoptSelf(args []string) int {
	flags, pos := splitArgs(args, nil)
	flags, jsonMode := takeJSON(flags)
	if len(flags) > 0 || len(pos) > 0 {
		fmt.Fprintln(os.Stderr, "usage: kpm adopt-self [--json]")
		return exitError
	}
	fail := func(msg string) int {
		if jsonMode {
			jsonError(msg)
		}
		fmt.Fprintln(os.Stderr, "kpm adopt-self:", msg)
		return exitError
	}

	cfg, err := a.loadRegistryConfig()
	if err != nil {
		return fail(err.Error())
	}
	if len(cfg.Registries) == 0 {
		return fail("no registry configured — add one first (kpm registry add <url>)")
	}

	// Best-effort refresh (§3): freshen the cache when the network is up, but never
	// fail the adopt on a refresh error — fall through to whatever the cache holds.
	netUp := true
	if host := firstRegistryHost(cfg.Registries); host != "" {
		netUp = a.netUp(host)
	}
	if netUp {
		for _, r := range cfg.Registries {
			if rerr := a.refreshOne(r); rerr != nil {
				a.paths.Log("ADOPT-SELF", fmt.Sprintf("%s  refresh error (using cache): %v", r.Name, rerr))
			}
		}
		if serr := a.state.Save(); serr != nil {
			return fail("state: " + serr.Error())
		}
	} else {
		a.paths.Log("ADOPT-SELF", "no network — adopting from the cached registry")
	}

	// Merge the cached manifests and find the kpm entry (config order wins, matching
	// install/sync). None → a clean error: no network if that's why the cache is
	// empty, otherwise the configured registries genuinely offer no kpm package.
	mans, _ := a.cachedManifests(cfg)
	entries, _ := registry.Merge(mans)
	entry, ok := entries[selfID]
	if !ok {
		if !netUp {
			return fail("no network — check Wi-Fi and retry")
		}
		return fail("no configured registry offers a kpm package — refresh or add the kpm registry")
	}

	// Refuse an under-specified kpm def before writing it, so a registry typo never
	// enrols a silently-unconfigured self-update (mirrors install's C1 guard).
	if miss := missingDefFields(entry.Def); len(miss) > 0 {
		return fail(fmt.Sprintf("registry %q kpm def is missing required field(s): %s — fix the registry def",
			entry.Registry, strings.Join(miss, ", ")))
	}

	// Adopt: source/forge → state, kpm.toml stays sourceless, existing state pin
	// preserved (pass the current pin, persisted only when non-empty).
	if err := a.adoptSelfFromEntry(entry, a.state.Get(selfID).Pin); err != nil {
		return fail(err.Error())
	}
	if err := a.state.Save(); err != nil {
		return fail("state: " + err.Error())
	}
	a.paths.Log("ADOPT-SELF", fmt.Sprintf("%s  from %s (%s)", selfID, entry.Registry, entry.Def.Source))
	fmt.Printf("enabled self-update for kpm from registry %s\n", entry.Registry)
	// adopt-self only records kpm's adoption identity — it stages nothing and needs
	// no reboot (JSON-OUTPUT.md §2.3), so both flags are false.
	if jsonMode {
		emitMutation([]string{selfID}, nil, false, false, "")
	}
	return exitOK
}

// netUp reports whether host is reachable, through the injectable netWait seam
// (nil in normal use → device.WaitForNetwork over the real client). Tests inject a
// fast stub so adopt-self's offline paths don't wait out the ~30s network budget.
func (a *App) netUp(host string) bool {
	if a.netWait != nil {
		return a.netWait(host)
	}
	return device.WaitForNetwork(a.client.HTTP(), host)
}
