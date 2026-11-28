package main

// requires.go implements package dependencies (docs/dependencies.md): a per-def
// `requires` list of package ids that must be on the device for the package to
// work.
//
// The relation is ONE WAY, unlike `conflicts`. If A requires B, then:
//   - `kpm install A` also registers B from the registry, in the same confirm
//     pause, when B has no def yet;
//   - `kpm update A` adds B to the same run when B is not installed yet, and
//     handles B before A;
//   - `kpm uninstall B` refuses while A is installed or staged.
//
// Like conflicts, nothing is persisted: every answer is a pure function of the
// registry caches, the packages.d snapshot and state.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"kpm/internal/config"
	"kpm/internal/registry"
)

// depGraph is the requires graph in force for one command.
type depGraph struct {
	// req maps a package id to its normalized requires list.
	req map[string][]string
	// builtin marks the ids whose def says the firmware 6.x has the feature
	// built in. On firmware 6.x such a requirement is met without an install.
	builtin map[string]bool
	// line is the firmware line of the device.
	line int
}

// normalizeRequires returns a def's required ids sorted, deduped, with the
// package's own id and invalid ids removed. The result is never nil, so a JSON
// array never marshals null.
func normalizeRequires(ids []string, self string) []string {
	return normalizeConflicts(ids, self)
}

// requiresGraphFor builds the requires graph over the registry entries plus the
// local packages.d snapshot. The precedence rule is the same as for conflicts:
// the registry entry wins wherever one exists, the local def is the fallback.
func requiresGraphFor(entries map[string]*registry.Entry, locals []*config.Package, line int) depGraph {
	g := depGraph{req: map[string][]string{}, builtin: map[string]bool{}, line: line}
	for _, p := range locals {
		g.req[p.ID] = normalizeRequires(p.Requires, p.ID)
		g.builtin[p.ID] = p.BuiltinFw6 != ""
	}
	for id, e := range entries {
		if e != nil && e.Def != nil {
			g.req[id] = normalizeRequires(e.Def.Requires, id)
			g.builtin[id] = e.Def.BuiltinFw6 != ""
		}
	}
	return g
}

// requirementMet reports whether the requirement r is satisfied on this device:
// kpm itself is always present, an installed or staged package counts (staged
// files land at the next boot, together with the package that needs them), and
// on firmware 6.x a package that is built into the firmware counts. pending is
// the in-run staging set of `update`, nil elsewhere.
func (a *App) requirementMet(r string, g depGraph, pending map[string]bool) bool {
	if r == selfID || a.installedOrStaged(r, pending) {
		return true
	}
	return g.line == config.Line6 && g.builtin[r]
}

// unmetRequirements returns the direct requirements of id that are not met,
// sorted. Always non-nil.
func (a *App) unmetRequirements(id string, g depGraph, pending map[string]bool) []string {
	out := []string{}
	for _, r := range g.req[id] {
		if !a.requirementMet(r, g, pending) {
			out = append(out, r)
		}
	}
	return out
}

// requirementClosure returns every package that id requires, directly or
// through another requirement, in install order: a requirement always comes
// before the packages that need it. id itself is not in the result. A cycle is
// an error that names the loop, because no install order exists for it.
func requirementClosure(id string, g depGraph) ([]string, error) {
	var out []string
	done := map[string]bool{}
	onPath := map[string]bool{}
	var path []string
	var visit func(n string) error
	visit = func(n string) error {
		if done[n] {
			return nil
		}
		if onPath[n] {
			i := 0
			for path[i] != n {
				i++
			}
			loop := append(append([]string{}, path[i:]...), n)
			return fmt.Errorf("requires makes a loop: %s", strings.Join(loop, " -> "))
		}
		onPath[n] = true
		path = append(path, n)
		for _, r := range g.req[n] {
			if err := visit(r); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		onPath[n] = false
		done[n] = true
		if n != id {
			out = append(out, n)
		}
		return nil
	}
	if err := visit(id); err != nil {
		return nil, err
	}
	return out, nil
}

// dependents returns the installed or staged packages that require target,
// sorted. Always non-nil.
func (a *App) dependents(target string, g depGraph) []string {
	out := []string{}
	for id, list := range g.req {
		if id == target || !a.installedOrStaged(id, nil) {
			continue
		}
		for _, r := range list {
			if r == target {
				out = append(out, id)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// orderByRequires returns pkgs in an order where each package comes after the
// selected packages it requires. Packages with no relation keep their order. A
// loop keeps the original order for its members: the loop cannot be installed
// in any order, and install already refuses to register one.
func orderByRequires(pkgs []*config.Package, g depGraph) []*config.Package {
	byID := make(map[string]*config.Package, len(pkgs))
	for _, p := range pkgs {
		byID[p.ID] = p
	}
	out := make([]*config.Package, 0, len(pkgs))
	state := map[string]int{} // 1 = visiting, 2 = done
	var visit func(p *config.Package)
	visit = func(p *config.Package) {
		if state[p.ID] != 0 {
			return
		}
		state[p.ID] = 1
		for _, r := range g.req[p.ID] {
			if q := byID[r]; q != nil {
				visit(q)
			}
		}
		state[p.ID] = 2
		out = append(out, p)
	}
	for _, p := range pkgs {
		visit(p)
	}
	return out
}

// addRequirements returns pkgs plus each registered package that a selected
// package requires, directly or not, and that is not met yet. A requirement
// with no def in all is left out: the update of the package that needs it then
// blocks with a message that says how to install it.
func (a *App) addRequirements(pkgs, all []*config.Package, g depGraph) []*config.Package {
	byID := make(map[string]*config.Package, len(all))
	for _, p := range all {
		byID[p.ID] = p
	}
	have := make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		have[p.ID] = true
	}
	out := append([]*config.Package{}, pkgs...)
	for _, p := range pkgs {
		closure, err := requirementClosure(p.ID, g)
		if err != nil {
			continue
		}
		for _, r := range closure {
			q := byID[r]
			if q == nil || have[r] || a.requirementMet(r, g, nil) {
				continue
			}
			have[r] = true
			out = append(out, q)
		}
	}
	return out
}

// requiresRefusal renders the block sentence for a non-empty, sorted list of
// unmet requirements. registered names the ids that have a packages.d def: for
// those the remedy is an update, for the rest an install.
func requiresRefusal(unmet []string, registered map[string]bool) string {
	noun := "package"
	if len(unmet) > 1 {
		noun = "packages"
	}
	quoted := make([]string, len(unmet))
	cmds := make([]string, len(unmet))
	for i, r := range unmet {
		quoted[i] = strconv.Quote(r)
		if registered[r] {
			cmds[i] = "kpm update " + r
		} else {
			cmds[i] = fmt.Sprintf("kpm install %s --yes; kpm update %s", r, r)
		}
	}
	return fmt.Sprintf("requires %s %s, which is not installed — install it first: %s",
		noun, strings.Join(quoted, ", "), strings.Join(cmds, "; "))
}

// requiredByRefusal renders the uninstall refusal for a non-empty, sorted list
// of dependents. The hook matches on the substring "is required by installed".
func requiredByRefusal(id string, deps []string) string {
	noun, remedy := "package", "uninstall it first"
	if len(deps) > 1 {
		noun, remedy = "packages", "uninstall them first"
	}
	quoted := make([]string, len(deps))
	cmds := make([]string, len(deps))
	for i, d := range deps {
		quoted[i] = strconv.Quote(d)
		cmds[i] = "kpm uninstall " + d
	}
	return fmt.Sprintf("%q is required by installed %s %s — %s: %s",
		id, noun, strings.Join(quoted, ", "), remedy, strings.Join(cmds, "; "))
}

// runEntries loads the packages.d snapshot and the merged registry entries for a
// command that checks conflicts or requires. A registry problem is logged and
// leaves entries nil: the graphs then use packages.d only.
func (a *App) runEntries() ([]*config.Package, map[string]*registry.Entry, error) {
	locals, err := a.loadPackages()
	if err != nil {
		return nil, nil, err
	}
	var entries map[string]*registry.Entry
	cfg, cerr := a.loadRegistryConfig()
	if cerr != nil {
		a.paths.Log("WARN", "registry config unreadable ("+cerr.Error()+") — checking conflicts and requires against packages.d only")
		return locals, nil, nil
	}
	mans, missing := a.cachedManifests(cfg)
	if len(missing) > 0 {
		a.paths.Log("WARN", fmt.Sprintf(
			"no cached data for registry %s — checking conflicts and requires against packages.d only for ids it offers; run: kpm registry refresh",
			strings.Join(missing, ", ")))
	}
	entries, _ = registry.Merge(mans)
	return locals, entries, nil
}
