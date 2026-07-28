package main

// conflicts.go implements feature B (INTEGRITY.md §4): a per-def `conflicts`
// list, the install/update-time hard refusal it drives, and the reporting
// surfaces for a pair that is already installed.
//
// The relation is treated as SYMMETRIC. kpm checks both directions — the
// target's own list, and any other package whose list names the target — so a
// curator declares it once, on whichever side is natural, and cannot create a
// half-declared pair (§4.2).
//
// There is no persisted "blocked" flag anywhere: every answer here is a pure
// function of the live packages.d snapshot plus state, which is why uninstalling
// the blocker clears the block with no bookkeeping (§4.4).

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"kpm/internal/config"
	"kpm/internal/registry"
)

// normalizeConflicts returns a def's conflict ids sorted, deduped, and with the
// package's own id removed. Used by every consumer, so the JSON payload and the
// refusal check can never disagree. Non-ValidID entries are dropped at use, so a
// malformed hand-edited local list can never make an id-shaped string reach a
// comparison (§9.2). The result is never nil, so a JSON array never marshals
// null.
func normalizeConflicts(ids []string, self string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == self || seen[id] || !config.ValidID(id) {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// installedOrStaged reports whether id currently has files on the device, or
// will after the next reboot. Staged counts because those files land at the next
// boot (§4.3). It reads the state map directly rather than through State.Get,
// which would create an entry for an unknown id — a write, from paths that
// include read-only commands (B1).
//
// pending carries the ids a single `update` run has already resolved for staging
// but has not yet written to state: the staging phase runs after the WHOLE
// resolve loop, so without it neither half of a conflicting pair sees the other
// and one run stages both into the same KoboRoot.tgz (C1). It is nil everywhere
// except that loop.
func (a *App) installedOrStaged(id string, pending map[string]bool) bool {
	if pending[id] {
		return true
	}
	ps := a.state.Packages[id]
	return ps != nil && (ps.InstalledVersion != "" || ps.StagedVersion != "")
}

// conflictGraph builds id -> declared conflicts from the given defs.
func conflictGraph(defs map[string][]string) map[string][]string {
	out := make(map[string][]string, len(defs))
	for id, ids := range defs {
		out[id] = normalizeConflicts(ids, id)
	}
	return out
}

// conflictGraphFor builds the conflict graph over the registry entries plus the
// local packages.d snapshot: the registry entry's declaration wins wherever one
// exists (even when it declares nothing), the local def is the fallback.
//
// This is the ONE construction, called by searchJSON, installBlockers and
// update. Two implementations of the precedence rule is exactly what let the
// reporting surface and the enforcing surface disagree — in both directions: a
// registry that ADDS a conflict against a stale local def had `search` grey out
// an Install that `install` performed, and a registry that REMOVES one had
// `search` report clear while `install` hard-refused, leaving the UI with no
// route out. Sharing the function makes disagreement unrepresentable regardless
// of which precedence rule is right.
//
// A nil entries map yields the local-only graph — what `status --json` needs
// (§6.2, it loads no registry caches) and what localConflictGraph passes.
func conflictGraphFor(entries map[string]*registry.Entry, locals []*config.Package) map[string][]string {
	defs := make(map[string][]string, len(entries)+len(locals))
	for _, p := range locals {
		defs[p.ID] = p.Conflicts
	}
	for id, e := range entries {
		if e != nil && e.Def != nil {
			defs[id] = e.Def.Conflicts
		}
	}
	return conflictGraph(defs)
}

// localConflictGraph is conflictGraphFor with no registry side — the local-only
// view `status --json` and the already-installed pair report use.
func localConflictGraph(pkgs []*config.Package) map[string][]string {
	return conflictGraphFor(nil, pkgs)
}

// activeConflicts returns the ids that conflict with target in EITHER direction
// and are currently installed or staged, sorted. Always non-nil (empty slice
// when there are none) so the JSON payload never marshals null. pending is the
// in-run staging set (see installedOrStaged); nil outside `update`'s loop.
func (a *App) activeConflicts(target string, graph map[string][]string, pending map[string]bool) []string {
	out := []string{}
	seen := map[string]bool{}
	// Direction 1: the target declares them.
	for _, j := range graph[target] {
		if !seen[j] && a.installedOrStaged(j, pending) {
			seen[j] = true
			out = append(out, j)
		}
	}
	// Direction 2: they declare the target.
	for j, list := range graph {
		if j == target || seen[j] || !a.installedOrStaged(j, pending) {
			continue
		}
		for _, c := range list {
			if c == target {
				seen[j] = true
				out = append(out, j)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// conflictRefusal renders the refusal sentence for a non-empty, sorted blocker
// list. install prefixes it with `"<id>" `, update's per-package text is this
// sentence alone (the caller already prefixes `kpm update: <id>: `) — so both
// carry the substring "conflicts with installed package", which is what the hook
// matches on (§4.6). It reads packages.d to pick each blocker's remedy, so it is
// a method; conflictRefusalText holds the pure formatting.
func (a *App) conflictRefusal(blockers []string) string {
	orphan := map[string]bool{}
	for _, b := range blockers {
		if _, err := os.Stat(a.paths.PackageFile(b)); err != nil {
			orphan[b] = true
		}
	}
	return conflictRefusalText(blockers, orphan)
}

// conflictRefusalText formats the refusal. orphan names the blockers that have
// state but no packages.d def: `kpm remove` deletes the def and leaves state
// (§4.3), and cmdUninstall requires a local def — so for those the plain
// "kpm uninstall <id>" remedy dead-ends with `package "<id>" not registered`,
// and the user follows a printed command straight into an error. They get the
// re-register-then-uninstall recipe instead, plus one clause saying why.
func conflictRefusalText(blockers []string, orphan map[string]bool) string {
	noun, remedy := "package", "uninstall it first"
	if len(blockers) > 1 {
		noun, remedy = "packages", "uninstall them first"
	}
	quoted := make([]string, len(blockers))
	cmds := make([]string, len(blockers))
	var orphans []string
	for i, b := range blockers {
		quoted[i] = strconv.Quote(b)
		if orphan[b] {
			orphans = append(orphans, strconv.Quote(b))
			cmds[i] = fmt.Sprintf("kpm install %s --yes; kpm uninstall %s", b, b)
		} else {
			cmds[i] = "kpm uninstall " + b
		}
	}
	s := fmt.Sprintf("conflicts with installed %s %s — %s: %s",
		noun, strings.Join(quoted, ", "), remedy, strings.Join(cmds, "; "))
	if len(orphans) > 0 {
		was := "was"
		if len(orphans) > 1 {
			was = "were"
		}
		s += fmt.Sprintf(" (%s %s unregistered by \"kpm remove\" but the files remain, so it must be re-registered before it can be uninstalled — use \"kpm add <url>\" if no registry offers it)",
			strings.Join(orphans, ", "), was)
	}
	return s
}

// unreadableRefusal renders the refusal for a conflict-gated operation whose
// packages.d snapshot is incomplete. An unparseable per-file TOML lands in
// a.skipped.Unparseable and is skipped with err == nil, so the conflict graph
// silently loses whatever that def declared (and whatever declares it) — the
// guard then reports "clear" from a view it knows is partial. Refusing is a new
// failure mode, but it is loud, self-describing and one `kpm remove` from
// resolution, unlike a .so deleted behind kpm's back (M5).
//
// Callers pass Unparseable only, never SkippedDefs.All(): a file whose stem is
// not a valid id was never registered and cannot weaken the guard.
func unreadableRefusal(files []string) string {
	return fmt.Sprintf("%d package definition(s) unreadable (%s) — cannot check conflicts against a partial view of packages.d; fix or \"kpm remove\" them and retry",
		len(files), strings.Join(files, ", "))
}

// blockingConflicts returns the ids that must block an install/update of target,
// given the conflict graph in force. It is empty when the target is ITSELF
// already installed or staged: at that point the collision has already happened,
// and refusing would strand both packages while undoing nothing — such a pair is
// reported, never blocked (§1.2 item 3, §4.5). pending is the in-run staging set
// (see installedOrStaged); nil outside `update`'s loop.
func (a *App) blockingConflicts(target string, graph map[string][]string, pending map[string]bool) []string {
	if a.installedOrStaged(target, pending) {
		return nil
	}
	return a.activeConflicts(target, graph, pending)
}

// conflictPairs returns the distinct unordered pairs among pkgs (plus anything
// they name) that conflict and are BOTH currently installed or staged, sorted —
// the already-installed pairs `kpm status` reports rather than blocks (§4.5).
func (a *App) conflictPairs(pkgs []*config.Package) [][2]string {
	graph := localConflictGraph(pkgs)
	seen := map[string]bool{}
	var out [][2]string
	for id, list := range graph {
		if !a.installedOrStaged(id, nil) {
			continue
		}
		for _, j := range list {
			if !a.installedOrStaged(j, nil) {
				continue
			}
			lo, hi := id, j
			if lo > hi {
				lo, hi = hi, lo
			}
			key := lo + "\x00" + hi
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, [2]string{lo, hi})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}
