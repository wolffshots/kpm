package main

// json.go implements the machine-readable `--json` output mode shared by the
// UI-relevant commands (JSON-OUTPUT.md). Every command's human/progress output
// still streams to stdout exactly as before; when --json is set the final line
// of stdout is the literal marker BEGIN_JSON immediately followed by one
// compact, single-line JSON object (JSON-OUTPUT.md §1). The consumer (the
// NickelHardcover-derived hook parser) treats everything before the marker as
// log text and everything after it as JSON.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"kpm/internal/config"
	"kpm/internal/registry"
	"kpm/internal/uninstall"
	"kpm/internal/version"
)

// takeJSON splits a --json flag out of the pre-parsed flag list, returning the
// remaining flags and whether --json was present (JSON-OUTPUT.md §1). Commands
// call it after splitArgs so their existing flag validation still rejects any
// other stray flag, and commands NOT listed in §1 never call it (so --json is
// the usual unknown-flag error there).
func takeJSON(flags []string) (rest []string, jsonMode bool) {
	for _, f := range flags {
		if f == "--json" || f == "-json" {
			jsonMode = true
			continue
		}
		rest = append(rest, f)
	}
	return rest, jsonMode
}

// jsonLine writes the machine payload as the final stdout line: BEGIN_JSON then
// one compact JSON object (JSON-OUTPUT.md §1). HTML escaping is disabled so
// source/homepage URLs stay literal; a payload we built ourselves always
// marshals, but on the impossible error we still emit a structured object.
func jsonLine(v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Printf("BEGIN_JSON{\"error\":%q}\n", err.Error())
		return
	}
	fmt.Print("BEGIN_JSON")
	fmt.Println(strings.TrimRight(buf.String(), "\n"))
}

// jsonError emits the minimal structured-failure payload BEGIN_JSON{"error":…}
// for a command that failed before it had richer structure (JSON-OUTPUT.md §1).
func jsonError(msg string) { jsonLine(map[string]any{"error": msg}) }

// ptr returns &s, or nil when s is empty so an absent value marshals to JSON
// null (JSON-OUTPUT.md §1: unknown/absent values are null).
func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---- shared sub-objects -------------------------------------------------

type jsonStagedSummary struct {
	Count int      `json:"count"`
	IDs   []string `json:"ids"`
}

type jsonFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// stagedSummary is the global {"count","ids"} of packages carrying a pending
// staged change (JSON-OUTPUT.md §2.1).
func (a *App) stagedSummary() jsonStagedSummary {
	ids := []string{}
	for id, ps := range a.state.Packages {
		if ps.StagedVersion != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return jsonStagedSummary{Count: len(ids), IDs: ids}
}

// methodUninstallable reports whether an uninstall of this package would
// actually plan — the "an uninstall method exists" test of JSON-OUTPUT.md §2.1.
// It asks uninstall.Compute itself (pure, no filesystem access) rather than
// mirroring its rules, so policy refusals (e.g. a marker_file outside the
// deletable allowlist) are reflected instead of over-reported.
func methodUninstallable(u config.Uninstall, manifest []string) bool {
	_, err := uninstall.Compute(manifest, u, false)
	return err == nil
}

// ---- search (§2.1) ------------------------------------------------------

type jsonSearchPkg struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Description   *string `json:"description"`
	Homepage      *string `json:"homepage"`
	Source        string  `json:"source"`
	Registry      *string `json:"registry"`
	Installed     *string `json:"installed"`
	Pinned        *string `json:"pinned"`
	Staged        bool    `json:"staged"`
	Uninstallable bool    `json:"uninstallable"`
	MinKpm        *string `json:"min_kpm"`
	MinKpmOK      bool    `json:"min_kpm_ok"`
	HasConfig     bool    `json:"has_config"` // local snapshot declares >=1 config (CONFIG.md §4)
}

type jsonRegistryFreshness struct {
	Name      string  `json:"name"`
	Refreshed *string `json:"refreshed"`
}

type jsonSearchPayload struct {
	Packages   []jsonSearchPkg         `json:"packages"`
	Staged     jsonStagedSummary       `json:"staged"`
	Registries []jsonRegistryFreshness `json:"registries"`
}

// registryFreshness renders each configured registry's name + last-refreshed
// timestamp (RFC3339, null if never), in config order (JSON-OUTPUT.md §2.1).
func (a *App) registryFreshness(cfg *registry.Config) []jsonRegistryFreshness {
	out := make([]jsonRegistryFreshness, 0, len(cfg.Registries))
	for _, r := range cfg.Registries {
		fetched := ""
		if a.state.Registries != nil {
			if rs := a.state.Registries[r.Name]; rs != nil {
				fetched = rs.LastFetched
			}
		}
		out = append(out, jsonRegistryFreshness{Name: r.Name, Refreshed: ptr(fetched)})
	}
	return out
}

// searchJSON builds and emits the browse payload (JSON-OUTPUT.md §2.1): the
// UNION of all registry entries and every locally-registered package, so the UI
// sees everything kpm manages — including installed-but-unregistered packages
// (in packages.d/state but no registry def), which carry registry:null,
// description:null. Read-only: registry cache + state only, no network, no lock.
func (a *App) searchJSON(cfg *registry.Config, entries map[string]*registry.Entry, term string) int {
	locals := map[string]*config.Package{}
	if pkgs, err := a.loadPackages(); err == nil {
		for _, p := range pkgs {
			locals[p.ID] = p
		}
	}

	idset := map[string]bool{}
	for id := range entries {
		idset[id] = true
	}
	for id := range locals {
		idset[id] = true
	}
	ids := make([]string, 0, len(idset))
	for id := range idset {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	tl := strings.ToLower(term)
	pkgs := make([]jsonSearchPkg, 0, len(ids))
	for _, id := range ids {
		e := entries[id]
		local := locals[id]
		ps := a.state.Get(id)

		var name, desc, home, source, regName, minKpm string
		if e != nil {
			name = e.Def.Name
			desc = e.Def.Description
			home = e.Def.Homepage
			source = e.Def.Source
			regName = e.Registry
			minKpm = e.Def.MinKpm
		}
		if local != nil {
			if name == "" {
				name = local.Name
			}
			if source == "" {
				source = a.effectiveSource(local)
			}
			if regName == "" {
				regName = local.Registry
			}
			if minKpm == "" {
				minKpm = local.MinKpm
			}
		}
		if name == "" {
			name = id
		}

		// term filters on id or name, exactly like the human search path.
		if term != "" && !strings.Contains(strings.ToLower(id), tl) && !strings.Contains(strings.ToLower(name), tl) {
			continue
		}

		pin := ""
		if local != nil {
			pin = a.effectivePin(local)
		} else if id == selfID {
			pin = a.state.Get(selfID).Pin
		}

		// Uninstallable is driven by the LOCAL def only: cmdUninstall requires
		// loadPackage(id) to succeed, so a package that has only a registry def
		// (no local packages.d entry — e.g. after "kpm remove" left state behind)
		// is NOT uninstallable no matter what recipe the registry advertises, and
		// kpm refuses to uninstall itself (M2).
		uninstallable := false
		if local != nil && id != selfID {
			uninstallable = methodUninstallable(local.Uninstall, ps.Manifest)
		}

		// has_config is driven by the LOCAL snapshot only (like uninstallable): the
		// UI's Settings button opens the offline packages.d declarations, so a
		// registry-only package advertising configs is not yet editable (CONFIG.md §4).
		hasConfig := local != nil && len(local.Configs) > 0

		pkgs = append(pkgs, jsonSearchPkg{
			ID:            id,
			Name:          name,
			Description:   ptr(desc),
			Homepage:      ptr(home),
			Source:        source,
			Registry:      ptr(regName),
			Installed:     ptr(ps.InstalledVersion),
			Pinned:        ptr(pin),
			Staged:        ps.StagedVersion != "",
			Uninstallable: uninstallable,
			MinKpm:        ptr(minKpm),
			MinKpmOK:      registry.MinKpmSatisfied(version.Version, minKpm),
			HasConfig:     hasConfig,
		})
	}

	jsonLine(jsonSearchPayload{
		Packages:   pkgs,
		Staged:     a.stagedSummary(),
		Registries: a.registryFreshness(cfg),
	})
	return exitOK
}

// ---- config list / show (CONFIG.md §3.3) --------------------------------
//
// These payloads are the forward contract the Nickel ConfigDialog (Phase 2)
// will be built against — pinned here in json_test.go/uicontract_test.go so the
// hook has a stable shape to render before the .cc code exists.

type jsonConfigFile struct {
	Name        string  `json:"name"`
	Path        string  `json:"path"`
	Format      string  `json:"format"`
	Reload      string  `json:"reload"`
	Exists      bool    `json:"exists"`
	CanCreate   bool    `json:"can_create"`
	Editable    bool    `json:"editable"`
	HasTemplate bool    `json:"has_template"` // a seed template is declared (`config init` — CONFIG.md §3.x)
	Description *string `json:"description"`
}

type jsonConfigListPayload struct {
	ID      string           `json:"id"`
	Configs []jsonConfigFile `json:"configs"`
}

type jsonConfigShowFile struct {
	Name        string `json:"name"`
	Format      string `json:"format"`
	Reload      string `json:"reload"`
	Exists      bool   `json:"exists"`
	HasTemplate bool   `json:"has_template"` // a seed template is declared (`config init` — CONFIG.md §3.x)
}

// jsonConfigEntry is one row of a show payload. key is null for text lines
// (they are addressed by line, not key); section is "" for text and for global
// ini keys. For a sensitive entry the real value IS included — the UI needs it to
// edit and masks display itself; --json is the only surface a token appears on
// (CONFIG.md §3.3).
type jsonConfigEntry struct {
	Section   string  `json:"section"`
	Key       *string `json:"key"`
	Line      int     `json:"line"`
	Value     string  `json:"value"`
	Sensitive bool    `json:"sensitive"`
}

type jsonConfigShowPayload struct {
	ID        string             `json:"id"`
	File      jsonConfigShowFile `json:"file"`
	Entries   []jsonConfigEntry  `json:"entries"`
	Truncated bool               `json:"truncated"`
}

// ---- check (§2.2) -------------------------------------------------------

type jsonCheckPkg struct {
	ID        string  `json:"id"`
	Installed *string `json:"installed"`
	Latest    *string `json:"latest"`
	Update    bool    `json:"update"`
	Pinned    *string `json:"pinned"`
	Error     *string `json:"error"`
}

type jsonCheckPayload struct {
	Packages []jsonCheckPkg `json:"packages"`
	Checked  string         `json:"checked"`
}

// ---- mutations: install / update / uninstall (§2.3) ---------------------

type jsonMutation struct {
	Changed        []string      `json:"changed"`
	Failed         []jsonFailure `json:"failed"`
	Staged         bool          `json:"staged"`
	RebootRequired bool          `json:"reboot_required"`
	Error          string        `json:"error,omitempty"`
}

// emitMutation writes the shared install/update/uninstall payload (§2.3),
// normalizing nil slices to [] so they never marshal as null.
func emitMutation(changed []string, failed []jsonFailure, staged, reboot bool, errMsg string) {
	if changed == nil {
		changed = []string{}
	}
	if failed == nil {
		failed = []jsonFailure{}
	}
	jsonLine(jsonMutation{Changed: changed, Failed: failed, Staged: staged, RebootRequired: reboot, Error: errMsg})
}

type jsonUnstage struct {
	Unstaged bool     `json:"unstaged"`
	IDs      []string `json:"ids"`
}

// ---- registry refresh / list (§2.4) -------------------------------------

type jsonRefreshed struct {
	Name     string `json:"name"`
	Packages int    `json:"packages"`
}

type jsonRegFailure struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

type jsonRegRefreshPayload struct {
	Refreshed []jsonRefreshed  `json:"refreshed"`
	Failed    []jsonRegFailure `json:"failed"`
}

type jsonRegListEntry struct {
	Name      string  `json:"name"`
	URL       string  `json:"url"`
	Ref       string  `json:"ref"`
	Path      string  `json:"path"`
	Forge     string  `json:"forge"`
	Refreshed *string `json:"refreshed"`
}

type jsonRegListPayload struct {
	Registries []jsonRegListEntry `json:"registries"`
}

// ---- list / status / version (§2.4) -------------------------------------

type jsonListPkg struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Installed *string `json:"installed"`
	Pinned    *string `json:"pinned"`
	Source    string  `json:"source"`
	Registry  *string `json:"registry"`
}

type jsonListPayload struct {
	Packages []jsonListPkg `json:"packages"`
}

type jsonStatusPkg struct {
	ID        string  `json:"id"`
	Installed *string `json:"installed"`
	Staged    *string `json:"staged"`
	Latest    *string `json:"latest"`
	Pinned    *string `json:"pinned"`
	State     string  `json:"state"` // unconfigured|staged|update-available|up-to-date
}

type jsonStatusPayload struct {
	Version  string            `json:"version"`
	Checked  *string           `json:"checked"`
	Packages []jsonStatusPkg   `json:"packages"`
	Staged   jsonStagedSummary `json:"staged"`
}

type jsonVersion struct {
	Version string  `json:"version"`
	Commit  *string `json:"commit"`
}

// statusJSON mirrors the human status output as flat structured fields
// (JSON-OUTPUT.md §2.4): kpm version, last-check time, per-package one-liners,
// and the global staged summary. Read-only (loads packages + state, no writes).
func (a *App) statusJSON() int {
	pkgs, err := a.loadPackages()
	if err != nil {
		jsonError(err.Error())
		return exitError
	}
	out := make([]jsonStatusPkg, 0, len(pkgs))
	for _, p := range pkgs {
		ps := a.state.Get(p.ID)
		target, avail := updateTarget(p, ps, a.effectivePin(p))
		st := "up-to-date"
		var latest *string
		switch {
		case !a.configured(p):
			st = "unconfigured"
		case ps.StagedVersion != "":
			st = "staged"
		case avail:
			st = "update-available"
			latest = ptr(target)
		}
		if latest == nil && a.configured(p) {
			latest = ptr(ps.LatestSeen)
		}
		out = append(out, jsonStatusPkg{
			ID:        p.ID,
			Installed: ptr(ps.InstalledVersion),
			Staged:    ptr(ps.StagedVersion),
			Latest:    latest,
			Pinned:    ptr(a.effectivePin(p)),
			State:     st,
		})
	}
	jsonLine(jsonStatusPayload{
		Version:  version.Version,
		Checked:  ptr(a.state.LastCheck),
		Packages: out,
		Staged:   a.stagedSummary(),
	})
	return exitOK
}
