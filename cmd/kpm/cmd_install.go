package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"kpm/internal/config"
	"kpm/internal/registry"
	"kpm/internal/state"
	"kpm/internal/version"
)

// cmdInstall copies a package def from a cached registry into packages.d,
// provenance-stamped, after a confirm pause (§9.6). It reads caches only — never
// the network. Afterwards the package behaves exactly like a hand-added one, so
// "check"/"update" install the actual software.
func (a *App) cmdInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	pin := fs.String("pin", "", "pin the installed def to an exact tag")
	installed := fs.String("installed", "", "seed installed_version for a pre-kpm install")
	yes := fs.Bool("yes", false, "write the def (required; install pauses to show it first)")
	adopt := fs.Bool("adopt", false, "take over an existing local def, preserving its pin and state")
	flags, pos := splitArgs(args, map[string]bool{"pin": true, "installed": true})
	flags, jsonMode := takeJSON(flags)
	if err := fs.Parse(flags); err != nil {
		return exitError
	}
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "usage: kpm install <id> [--pin <tag>] [--installed <ver>] [--yes] [--adopt] [--json]")
		return exitError
	}
	id := pos[0]
	// failJSON emits a best-effort structured error for the UI (JSON-OUTPUT.md
	// §1/§2.3); the exit code stays authoritative.
	failJSON := func(msg string) {
		if jsonMode {
			emitMutation(nil, []jsonFailure{{ID: id, Error: msg}}, false, false, msg)
		}
	}
	if !config.ValidID(id) {
		fmt.Fprintf(os.Stderr, "kpm install: invalid package id %q\n", id)
		return exitError
	}

	// Resolve the def from cached registries (earliest in config order wins, §9.6).
	cfg, err := a.loadRegistryConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm install:", err)
		return exitError
	}
	mans, missing := a.cachedManifests(cfg)
	entries, _ := registry.Merge(mans)
	entry, ok := entries[id]
	if !ok {
		if len(missing) > 0 {
			failJSON(fmt.Sprintf("%q not found; some registries have no cache", id))
			fmt.Fprintf(os.Stderr, "kpm install: %q not found; some registries have no cache (%v) — run: kpm registry refresh\n", id, missing)
		} else {
			failJSON(fmt.Sprintf("%q not found in any registry", id))
			fmt.Fprintf(os.Stderr, "kpm install: %q not found in any registry\n", id)
		}
		return exitError
	}

	// Reject an under-specified registry def before writing it, so a registry
	// typo never produces a silently "unconfigured" package (C1).
	if missing := missingDefFields(entry.Def); len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "kpm install: %q from registry %q is missing required field(s): %s — fix the registry def\n",
			id, entry.Registry, strings.Join(missing, ", "))
		return exitError
	}

	// min_kpm gate: refuse if the running kpm is too old (§9.8). A dev build
	// skips the gate with a printed note instead of refusing everything (C9).
	if !registry.MinKpmSatisfied(version.Version, entry.Def.MinKpm) {
		if version.Version == "dev" {
			fmt.Println("(dev build: skipping min_kpm check)")
		} else {
			fmt.Fprintf(os.Stderr, "kpm install: %q requires kpm >= %s (running %s)\n", id, entry.Def.MinKpm, version.Version)
			return exitError
		}
	}

	// Guard an existing local def (§9.6).
	existing, existsErr := a.loadPackage(id)
	if existsErr != nil {
		// A present-but-unparseable def must not be silently overwritten: a
		// malformed TOML looks "absent" to loadPackage but still holds the
		// user's config. Refuse unless --adopt is explicit.
		if _, statErr := os.Stat(a.paths.PackageFile(id)); statErr == nil && !*adopt {
			fmt.Fprintf(os.Stderr, "kpm install: %q already exists but its def is unreadable — fix it, \"kpm remove %s\", or use --adopt to replace it\n", id, id)
			return exitError
		}
	}
	if existsErr == nil {
		switch {
		case existing.Registry != "" && !*adopt:
			// Idempotent re-install: if the local def is exactly this registry
			// entry (no local drift), report success so the UI's install->update
			// chain proceeds instead of dead-ending. Without this, a failed update
			// right after a successful install leaves "Install" as the only UI
			// action, and a second tap would error here forever (M1). A drifted
			// def still routes the user to sync.
			if h, herr := registry.HashDef(entry.Def); herr == nil && h == a.state.Get(id).SyncedDefSHA256 {
				fmt.Printf("%s is already installed from registry %s\n", id, existing.Registry)
				if jsonMode {
					emitMutation([]string{id}, nil, false, false, "")
				}
				return exitOK
			}
			failJSON(fmt.Sprintf("%q is already installed from registry %q", id, existing.Registry))
			fmt.Fprintf(os.Stderr, "kpm install: %q is already installed from registry %q — use \"kpm sync %s\" to update its def\n", id, existing.Registry, id)
			return exitError
		case existing.Registry == "" && !*adopt:
			failJSON(fmt.Sprintf("%q already exists as a hand-added package", id))
			fmt.Fprintf(os.Stderr, "kpm install: %q already exists as a hand-added package — \"kpm remove %s\" first, or use --adopt to take it over\n", id, id)
			return exitError
		}
	}

	// Disclose a provenance change: adopting an already-registry-managed id
	// whose winning registry differs from the recorded one (C5).
	provenanceChange := ""
	if *adopt && existsErr == nil && existing.Registry != "" && existing.Registry != entry.Registry {
		provenanceChange = existing.Registry + " -> " + entry.Registry
	}

	// Decide the pin: --pin overrides; otherwise adopt preserves the local pin.
	pinVal := *pin
	if *pin == "" && *adopt && existsErr == nil {
		pinVal = existing.Pin
	}

	pkg := entry.Def.ToPackage(id, entry.Registry, pinVal)

	// Confirm pause: print the def and exit 3 unless --yes (§9.6).
	printInstallDef(os.Stdout, id, entry.Registry, pkg, provenanceChange)
	if !*yes {
		// Human line before the emit: BEGIN_JSON must be the final stdout
		// line on every branch (JSON-OUTPUT.md §1).
		fmt.Println("\nRe-run with --yes to write this def.")
		failJSON("re-run with --yes to write this def")
		return exitConfirm
	}

	// kpm's own source/forge/pin live in state.json (so a self-update that
	// overwrites kpm.toml can't drop them, §10); persistDef moves them there and
	// blanks them in the def before it is written (SELF-SOURCE §5).
	logSource := pkg.Source // record the real source for the INSTALL log, pre-blank
	ps := a.state.Get(id)
	a.persistDef(id, pkg, ps)
	if err := config.SaveReplace(a.paths.PackageFile(id), pkg); err != nil {
		fmt.Fprintln(os.Stderr, "kpm install:", err)
		return exitError
	}

	// Record the synced def hash for later drift/up-to-date checks (§9.7).
	hash, err := registry.HashDef(entry.Def)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm install:", err)
		return exitError
	}
	ps.SyncedDefSHA256 = hash
	if id == selfID && pinVal != "" {
		ps.Pin = pinVal // kpm's pin belongs in state.json (§10), read by effectivePin
	}
	if *installed != "" {
		ps.InstalledVersion = *installed
		ps.InstalledAt = time.Now().UTC().Format(state.TimeFormat)
	}
	if err := a.state.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "kpm install: state:", err)
		return exitError
	}

	verb := "INSTALL"
	a.paths.Log(verb, fmt.Sprintf("%s  from %s (%s)", id, entry.Registry, logSource))
	if *adopt {
		fmt.Printf("adopted %s from registry %s (pin and state preserved)\n", id, entry.Registry)
		if provenanceChange != "" {
			fmt.Printf("provenance: %s\n", provenanceChange)
		}
	} else {
		fmt.Printf("installed def for %s from registry %s\n", id, entry.Registry)
	}
	fmt.Printf("run \"kpm check\" then \"kpm update %s\" to fetch and stage the software\n", id)
	// install only writes the def (it does not fetch/stage the software), so
	// nothing is staged and no reboot is required (JSON-OUTPUT.md §2.3).
	if jsonMode {
		emitMutation([]string{id}, nil, false, false, "")
	}
	return exitOK
}

// missingDefFields returns the required def fields (source, forge, asset) that
// are empty, so install can refuse an under-specified registry entry and search
// can flag it (C1).
func missingDefFields(d *registry.PackageDef) []string {
	var missing []string
	if strings.TrimSpace(d.Source) == "" {
		missing = append(missing, "source")
	}
	if strings.TrimSpace(d.Forge) == "" {
		missing = append(missing, "forge")
	}
	if strings.TrimSpace(d.Asset) == "" {
		missing = append(missing, "asset")
	}
	return missing
}

// printInstallDef renders the def install is about to write, so the user can
// review the source, asset, min_kpm and full uninstall recipe (incl. hooks that
// run as root) before it is committed (§6/§9.6). A non-empty provenanceChange is
// disclosed as "provenance: <old> -> <new>" for an adopt that switches registry
// (C5).
func printInstallDef(w io.Writer, id, regName string, p *config.Package, provenanceChange string) {
	fmt.Fprintf(w, "install %s from registry %s:\n", id, regName)
	if provenanceChange != "" {
		fmt.Fprintf(w, "  provenance: %s\n", provenanceChange)
	}
	fmt.Fprintf(w, "  name     %s\n", p.Name)
	fmt.Fprintf(w, "  source   %s\n", p.Source)
	fmt.Fprintf(w, "  forge    %s\n", p.Forge)
	fmt.Fprintf(w, "  asset    %s\n", p.Asset)
	if p.MinKpm != "" {
		fmt.Fprintf(w, "  min_kpm  %s\n", p.MinKpm)
	}
	if p.Pin != "" {
		fmt.Fprintf(w, "  pin      %s\n", p.Pin)
	}
	u := p.Uninstall
	if u.EffectiveMethod() != config.MethodManifest || u.MarkerFile != "" ||
		len(u.ExtraPaths) > 0 || len(u.PurgePaths) > 0 || len(u.KeepPaths) > 0 ||
		len(u.AllowPaths) > 0 || u.RunBefore != "" || u.RunAfter != "" {
		fmt.Fprintf(w, "  uninstall method %s\n", u.EffectiveMethod())
		if u.MarkerFile != "" {
			fmt.Fprintf(w, "    marker_file %s\n", u.MarkerFile)
		}
		printPathList(w, "extra_paths", u.ExtraPaths)
		printPathList(w, "purge_paths", u.PurgePaths)
		printPathList(w, "keep_paths", u.KeepPaths)
		printPathList(w, "allow_paths", u.AllowPaths)
		if u.RunBefore != "" {
			fmt.Fprintf(w, "    run_before %s\n", u.RunBefore)
		}
		if u.RunAfter != "" {
			fmt.Fprintf(w, "    run_after %s\n", u.RunAfter)
		}
	}
}

func printPathList(w io.Writer, name string, paths []string) {
	for _, p := range paths {
		fmt.Fprintf(w, "    %s %s\n", name, p)
	}
}
