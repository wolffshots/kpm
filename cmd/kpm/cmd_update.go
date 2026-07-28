package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kpm/internal/apptree"
	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/forge"
	"kpm/internal/registry"
	"kpm/internal/state"
	"kpm/internal/tgz"
	"kpm/internal/version"
)

// checkFreshness is the window within which a prior check is trusted (§7.1).
const checkFreshness = 5 * time.Minute

// resolved is a package that has a concrete target release to stage.
type resolved struct {
	pkg      *config.Package
	tag      string
	asset    forge.Asset
	cache    string   // downloaded+verified tgz path
	manifest []string // captured member manifest
	// fingerprint is the release fingerprint this staging describes; it becomes
	// staged_fingerprint and is promoted to installed_fingerprint after the
	// reboot install (INTEGRITY.md §3.6). It is recorded for EVERY package,
	// rolling or not — the `rolling` gates in rebuildDetected are what make it
	// inert for a non-rolling def, not an empty value.
	fingerprint string
}

func (a *App) cmdUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	all := fs.Bool("all", false, "update all registered packages")
	reboot := fs.Bool("reboot", false, "reboot after staging (installs on boot)")
	notify := fs.Bool("notify", false, "emit NickelDBus toasts")
	// --force-launcher is on `update` only, never on `install`: it exists to
	// re-assert a NickelMenu drop-in the user has since customised, which is a
	// deliberate act, not something a first install should ever need (§8.2).
	forceLauncher := fs.Bool("force-launcher", false, "rewrite an app-tree package's NickelMenu entry even if it exists")
	flags, pos := splitArgs(args, nil)
	flags, jsonMode := takeJSON(flags)
	if err := fs.Parse(flags); err != nil {
		return exitError
	}
	n := notifier{on: *notify}

	pkgs, err := a.selectPackages(pos, *all)
	if err != nil {
		if jsonMode {
			jsonError(err.Error())
		}
		fmt.Fprintln(os.Stderr, "kpm update:", err)
		return exitError
	}
	if len(pkgs) == 0 {
		// Human line before the emit: BEGIN_JSON must be the final stdout
		// line on every branch (JSON-OUTPUT.md §1).
		fmt.Println("no packages selected")
		if jsonMode {
			emitMutation(nil, nil, false, false, "")
		}
		return exitOK
	}

	// M5: a def that failed to PARSE is skipped with err == nil, so it is
	// invisible to the conflict graph — its stem is a valid id, so it may well be
	// a registered package, and a conflict it declares (or that names it) cannot
	// be seen. The guard would then answer "clear" from a view it knows is
	// partial. Refuse instead, before the cache sweep and the network wait: it is
	// cheap, offline and self-describing.
	//
	// A file whose STEM is not a valid package id is a different thing and is
	// deliberately not covered: it was never registered, so it can declare
	// nothing and cannot weaken the guard. Refusing on a stray `NickelClock.toml`
	// or `README.toml` would protect against nothing while blocking every update.
	if len(a.skipped.Unparseable) > 0 {
		msg := unreadableRefusal(a.skipped.Unparseable)
		if jsonMode {
			jsonError(msg)
		}
		fmt.Fprintln(os.Stderr, "kpm update:", msg)
		a.paths.Log("ERROR", msg)
		return exitError
	}

	// failures accumulates per-package resolve/download errors for the §2.3
	// mutation payload; changed is filled from the staged targets at the end.
	// skips accumulates the deliberate, non-error no-ops (§4.3 conflict
	// refusals under --all), which are reported but never affect the exit code.
	var failures []jsonFailure
	var skips []jsonSkip
	// explicit is true when the user NAMED the packages: a conflict refusal is
	// then a direct request that cannot be satisfied and must fail loudly. Under
	// --all it is a skip instead — see the loop below (§4.3).
	explicit := !*all

	// Cache hygiene: drop stale .part files and week-old cached tgzs (F3).
	a.cleanCache()
	// A merged .kpm-part left in the boot slot dir by a power loss mid-merge is
	// never live (commitStaged renames it into place); sweep it so it can't hold
	// tens of MB and ENOSPC the next merge on a near-full onboard partition (#7).
	os.Remove(a.paths.StagedTgz() + ".kpm-part")

	n.toast("kpm: updating…")

	// Wait for connectivity before resolving (like check), but only if any
	// selected package is actually configured to resolve (D3/F7).
	if host := a.firstConfiguredHost(pkgs); host != "" {
		if !device.WaitForNetwork(a.client.HTTP(), host) {
			msg := "kpm: no network — check Wi-Fi and retry"
			a.paths.WriteStatus(msg)
			a.paths.Log("UPDATE", "aborted: no network")
			n.toast(msg)
			if jsonMode {
				jsonError("no network — check Wi-Fi and retry")
			}
			fmt.Fprintln(os.Stderr, msg)
			return exitError
		}
	}

	// B: the conflict graph, built once for the run — the reverse direction needs
	// packages that were not selected (§4.2), and it must be the SAME graph
	// `search --json` reports on or the UI and the CLI disagree (§4.3).
	conflicts, cerr := a.updateConflictGraph()
	if cerr != nil {
		if jsonMode {
			jsonError(cerr.Error())
		}
		fmt.Fprintln(os.Stderr, "kpm update:", cerr)
		a.paths.Log("ERROR", cerr.Error())
		return exitError
	}

	// 1+2. Resolve targets and download+verify each asset. App-tree packages go
	// into their own slice: they never enter the merge, never set a staged
	// version, and are installed outright further down (APP-TREE.md §6.1).
	var targets []resolved
	var appTargets []resolvedApp
	failed := 0
	// staging is the C1 in-run pending set: ids already resolved for staging in
	// THIS run. ps.StagedVersion is only written after the whole loop, so without
	// it neither half of a conflicting pair sees the other and both land in one
	// merged KoboRoot.tgz — the normal new-device "Update all" sequence, which
	// defeated feature B entirely.
	staging := map[string]bool{}
	for _, p := range pkgs {
		r, app, skip, blocked, err := a.resolveAndDownload(p, conflicts, staging)
		if err != nil {
			failed++
			failures = append(failures, jsonFailure{ID: p.ID, Error: err.Error()})
			fmt.Fprintf(os.Stderr, "kpm update: %s: %v\n", p.ID, err)
			a.paths.Log("ERROR", fmt.Sprintf("%s  %v", p.ID, err))
			continue
		}
		if blocked != "" {
			// A conflict refusal is PERMANENT: no retry can clear it, which makes
			// it categorically different from the transient failures failed[] and a
			// non-zero exit exist for. Under --all it is a skip-with-note, so one
			// registered-but-not-installed def can't make every "Update all" tap
			// end in an error dialog forever. When the user NAMED the package it
			// stays a hard failure — silence there would be wrong (§4.3).
			if explicit {
				failed++
				failures = append(failures, jsonFailure{ID: p.ID, Error: blocked})
				fmt.Fprintf(os.Stderr, "kpm update: %s: %s\n", p.ID, blocked)
				a.paths.Log("ERROR", fmt.Sprintf("%s  %s", p.ID, blocked))
				continue
			}
			skips = append(skips, jsonSkip{ID: p.ID, Reason: blocked})
			fmt.Printf("%s: skipped — %s\n", p.ID, blocked)
			a.paths.Log("WARN", fmt.Sprintf("%s  skipped — %s", p.ID, blocked))
			continue
		}
		if skip {
			continue
		}
		if app != nil {
			appTargets = append(appTargets, *app)
			staging[app.pkg.ID] = true
			continue
		}
		targets = append(targets, r)
		staging[r.pkg.ID] = true
	}

	// App-tree installs run FIRST, before the merge: a tgz merge failure must not
	// strand a package that was going to succeed, and the extractor's free-space
	// check must run before ~40 MiB of merged tgz lands on the same partition
	// (§6.4). Each package is recorded on success, one save at a time.
	var appChanged []string
	appPartial := false
	if len(appTargets) > 0 {
		var appFailures []jsonFailure
		appChanged, appFailures, appPartial = a.installAppTrees(appTargets, *forceLauncher)
		failed += len(appFailures)
		failures = append(failures, appFailures...)
		for _, w := range a.launcherWarnings(appTreePkgs(appTargets)) {
			fmt.Println(w)
			a.paths.Log("WARN", w)
		}
	}

	if len(targets) == 0 {
		// An app-tree-only run is COMPLETE here: nothing is staged, nothing needs
		// a reboot, and the packages are already installed.
		if len(appChanged) > 0 {
			status := a.buildStatus(pkgs)
			a.paths.WriteStatus(status + appTreeStatusNote(a.launcherWarnings(appTreePkgs(appTargets))))
			fmt.Print(status)
			n.toast(fmt.Sprintf("kpm: %d package(s) installed", len(appChanged)))
			if !jsonMode {
				fmt.Printf("%d package(s) installed\n", len(appChanged))
			}
			if jsonMode {
				errMsg := ""
				if failed > 0 {
					errMsg = "some packages failed"
				}
				// staged/reboot_required describe THE RUN, and this run staged
				// nothing — so the hook prompts for no reboot (§11.2).
				emitMutationWithSkips(sortedUnion(appChanged, nil), failures, skips, false, false, errMsg)
			}
			// A stale file the prune could not remove leaves the package INSTALLED
			// but not fully tidy: the new files are there, so it is partial, not a
			// failure (§9.2).
			if failed > 0 || appPartial {
				return exitPartial
			}
			return exitOK
		}

		if err := a.state.Save(); err != nil {
			if jsonMode {
				jsonError("state: " + err.Error())
			}
			fmt.Fprintln(os.Stderr, "kpm update: state:", err)
			a.paths.Log("ERROR", "state: "+err.Error())
			return exitError
		}
		status := a.buildStatus(pkgs)
		a.paths.WriteStatus(status)
		fmt.Print(status)
		// Honor --reboot when nothing new resolved but a kpm-staged tgz is
		// still pending: reboot to install it (F2).
		if *reboot && a.stagedTgzIsOurs() {
			a.paths.Log("REBOOT", "installing pending staging")
			n.toast("kpm: rebooting to install…")
			// Emit the payload BEFORE rebooting so --json always ends on the
			// marker, even if the reboot call itself fails (JSON-OUTPUT §1).
			if jsonMode {
				emitMutationWithSkips(nil, failures, skips, false, false, "")
			}
			if err := device.Reboot(); err != nil {
				fmt.Fprintln(os.Stderr, "kpm update: reboot failed:", err)
				return exitError
			}
			return exitOK
		}
		if failed > 0 {
			// Everything selected failed and nothing was staged (F2).
			if jsonMode {
				emitMutationWithSkips(nil, failures, skips, false, false, "update failed")
			}
			n.toast("kpm: update failed — see status")
			return exitError
		}
		// Human line before the emit: BEGIN_JSON must be the final stdout
		// line on every branch (JSON-OUTPUT.md §1).
		fmt.Println("nothing to stage")
		if jsonMode {
			// Nothing to stage: already up to date / already staged / skipped
			// by a conflict refusal under --all (§2.3, §4.3).
			emitMutationWithSkips(nil, failures, skips, false, false, "")
		}
		return exitOK
	}

	// 3. Merge into a not-yet-live part file (hashed before it goes live).
	if err := a.guardExistingTgz(); err != nil {
		if jsonMode {
			jsonError(err.Error())
		}
		fmt.Fprintln(os.Stderr, "kpm update:", err)
		a.paths.WriteStatus("kpm: refusing to stage — " + err.Error())
		return exitError
	}
	part, sum, size, dups, err := a.mergeStaged(targets)
	if err != nil {
		if jsonMode {
			jsonError("stage: " + err.Error())
		}
		fmt.Fprintln(os.Stderr, "kpm update: stage:", err)
		a.paths.Log("ERROR", "stage: "+err.Error())
		return exitError
	}
	for _, d := range dups {
		a.paths.Log("WARN", "duplicate path across packages (last wins): "+d)
	}

	// 4. Record the staged intent BEFORE the tgz goes live: StagedCommitted
	// stays false until the move succeeds, so a crash between here and the live
	// move never leaves installed files unrecorded or falsely promotes an
	// uninstalled version — reconcile rolls an uncommitted staging back (B6).
	//
	// Snapshot the identity + per-target staged intent we're about to overwrite,
	// so a failed commit can restore it. On a re-stage, a previously committed
	// tgz is still live in the boot slot until the rename lands; without this,
	// overwriting its identity/committed flag and then failing to go live would
	// leave state describing a tgz that isn't there — reconcile would roll the
	// still-live staging back (orphaning its files, untracked) and the guard
	// would refuse the slot (M3).
	type stagedSnap struct {
		id, version, at, fingerprint string
		manifest                     []string
	}
	prevSHA, prevSize, prevCommitted := a.state.StagedSHA256, a.state.StagedSize, a.state.StagedCommitted
	var snaps []stagedSnap
	now := time.Now().UTC().Format(state.TimeFormat)
	for _, r := range targets {
		ps := a.state.Get(r.pkg.ID)
		snaps = append(snaps, stagedSnap{r.pkg.ID, ps.StagedVersion, ps.StagedAt, ps.StagedFingerprint, ps.StagedManifest})
		ps.StagedVersion = r.tag
		ps.StagedAt = now
		// The fingerprint the staged payload describes, promoted to
		// installed_fingerprint by reconcile (INTEGRITY.md §3.5/§3.6).
		ps.StagedFingerprint = r.fingerprint
		// Record the pending manifest separately; Manifest (installed) is only
		// updated by reconcile after the reboot install actually happens (B2).
		ps.StagedManifest = r.manifest
	}
	a.state.StagedSHA256 = sum
	a.state.StagedSize = size
	a.state.StagedCommitted = false
	if err := a.state.Save(); err != nil {
		os.Remove(part)
		if jsonMode {
			jsonError("state: " + err.Error())
		}
		fmt.Fprintln(os.Stderr, "kpm update: state:", err)
		a.paths.Log("ERROR", "state: "+err.Error())
		return exitError
	}

	// 5. Commit: move the tgz into the live boot slot, then mark committed.
	if err := a.commitStaged(part); err != nil {
		// The new tgz never went live. Restore the pre-stage identity and the
		// affected packages' prior staged intent so state matches whatever is
		// actually in the boot slot — a still-live previous staging, or nothing
		// (M3). Best-effort: on a restore-save failure the reconcile/guard
		// recovery still applies on the next run.
		a.state.StagedSHA256, a.state.StagedSize, a.state.StagedCommitted = prevSHA, prevSize, prevCommitted
		for _, s := range snaps {
			ps := a.state.Get(s.id)
			ps.StagedVersion, ps.StagedAt, ps.StagedManifest = s.version, s.at, s.manifest
			ps.StagedFingerprint = s.fingerprint
		}
		_ = a.state.Save()
		if jsonMode {
			jsonError("stage: " + err.Error())
		}
		fmt.Fprintln(os.Stderr, "kpm update: stage:", err)
		a.paths.Log("ERROR", "stage: "+err.Error())
		a.paths.WriteStatus("kpm: staging failed — reboot to install any pending update, or 'kpm unstage'")
		return exitError
	}
	for _, r := range targets {
		ps := a.state.Get(r.pkg.ID)
		a.paths.Log("STAGE", fmt.Sprintf("%s  %s -> %s",
			r.pkg.ID, dash(ps.InstalledVersion), r.tag))
		os.Remove(r.cache) // clear merged cache file
	}
	status := a.buildStatus(pkgs)
	a.paths.WriteStatus(status + appTreeStatusNote(a.launcherWarnings(appTreePkgs(appTargets))))
	fmt.Print(status)
	n.toast(fmt.Sprintf("kpm: %d package(s) staged", len(targets)))

	// §2.3: at least one package staged; every staged update installs on reboot.
	// Emitted before the reboot decision so the payload is on stdout even when
	// --reboot would otherwise not return.
	if jsonMode {
		changed := make([]string, 0, len(targets))
		for _, r := range targets {
			changed = append(changed, r.pkg.ID)
		}
		errMsg := ""
		if failed > 0 {
			errMsg = "some packages failed"
		}
		// A mixed run's `changed` is the UNION, sorted; `staged`/`reboot_required`
		// describe THE RUN, and this run did stage something, so both are true.
		// The hook prompts once — correctly, for the tgz package that needs it;
		// the app-tree package in the same array is already installed (§6.4).
		emitMutationWithSkips(sortedUnion(changed, appChanged), failures, skips, true, true, errMsg)
	}

	// 5. Reboot. At least one package staged, so a reboot is worthwhile even if
	// some others failed (F2). An app-tree-only run never gets here: it returned
	// above, so `update koreader --reboot` — and the NickelMenu "Update all" entry,
	// which always passes --reboot — completes without rebooting the device for no
	// reason (§6.1, §6.5).
	if *reboot {
		a.paths.Log("REBOOT", fmt.Sprintf("staging %d package(s)", len(targets)))
		n.toast("kpm: rebooting to install…")
		if err := device.Reboot(); err != nil {
			fmt.Fprintln(os.Stderr, "kpm update: reboot failed:", err)
			if failed > 0 {
				return exitPartial
			}
			return exitError
		}
		return exitOK
	}

	if !jsonMode {
		// In JSON mode this line would trail the §2.3 payload emitted above;
		// BEGIN_JSON must stay the final stdout line (JSON-OUTPUT.md §1). The
		// staged summary already went out via fmt.Print(status) pre-marker.
		if len(appChanged) > 0 {
			fmt.Printf("%d package(s) installed, %d staged — reboot to install\n", len(appChanged), len(targets))
		} else {
			fmt.Printf("%d package(s) staged — reboot to install\n", len(targets))
		}
	}
	if failed > 0 || appPartial {
		return exitPartial // mixed: some staged, some failed (F2)
	}
	return exitOK
}

// updateConflictGraph builds the graph `update` enforces against: the shared
// registry-first-with-local-fallback construction (conflictGraphFor), so this
// command and `search --json` can never disagree about whether a package is
// blocked (§4.3).
//
// The registry side comes from the CACHE only — never a network call. `update`
// must work offline (it is the command that runs when Wi-Fi has just come up for
// a download), so a missing or unparseable cache degrades to the local
// packages.d graph with a WARN rather than failing.
//
// The unreadable-def refusal (M5) is NOT here: cmdUpdate makes it before the
// network wait, off a.unreadable, so it costs nothing when it fires.
func (a *App) updateConflictGraph() (map[string][]string, error) {
	locals, err := a.loadPackages()
	if err != nil {
		return nil, fmt.Errorf("cannot check conflicts: %v", err)
	}
	var entries map[string]*registry.Entry
	cfg, cerr := a.loadRegistryConfig()
	if cerr != nil {
		a.paths.Log("WARN", "registry config unreadable ("+cerr.Error()+") — checking conflicts against packages.d only")
	} else {
		mans, missing := a.cachedManifests(cfg)
		if len(missing) > 0 {
			a.paths.Log("WARN", fmt.Sprintf(
				"no cached data for registry %s — checking conflicts against packages.d only for ids it offers; run: kpm registry refresh",
				strings.Join(missing, ", ")))
		}
		entries, _ = registry.Merge(mans)
	}
	return conflictGraphFor(entries, locals), nil
}

// firstConfiguredHost returns the forge host of the first configured package,
// or "" if none is configured (nothing to resolve, so no network wait). It
// tests the effective source so an adopted kpm (source in state) counts as
// configured (SELF-SOURCE §1).
func (a *App) firstConfiguredHost(pkgs []*config.Package) string {
	for _, p := range pkgs {
		if a.configured(p) {
			return config.SourceHost(a.effectiveSource(p))
		}
	}
	return ""
}

// selectPackages returns the packages named in args, or all of them with --all.
func (a *App) selectPackages(ids []string, all bool) ([]*config.Package, error) {
	allPkgs, err := a.loadPackages()
	if err != nil {
		return nil, err
	}
	if all {
		if len(ids) > 0 {
			return nil, fmt.Errorf("cannot combine --all with explicit ids")
		}
		return allPkgs, nil
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("specify package id(s) or --all")
	}
	byID := map[string]*config.Package{}
	for _, p := range allPkgs {
		byID[p.ID] = p
	}
	var out []*config.Package
	for _, id := range ids {
		p, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("package %q not registered", id)
		}
		out = append(out, p)
	}
	return out, nil
}

// resolveAndDownload resolves a package's target release and downloads+verifies
// its asset. skip=true means the package needs no action (unconfigured, too old
// a kpm, already up to date, or already staged). blocked is non-empty when the
// package was left alone because something on the device collides with it: the
// refusal sentence, for the caller to grade as a skip or a failure (§4.3).
// conflicts is the run's conflict graph; a nil graph simply blocks nothing.
// staging is the C1 in-run pending set.
//
// Exactly one of the first two results is populated: `app` is non-nil for an
// app-tree package (APP-TREE.md §6.1), in which case the zip has been downloaded
// and apptree.Compute has already validated every entry it contains — so a
// hostile or broken archive fails the package HERE, before anything is written.
func (a *App) resolveAndDownload(p *config.Package, conflicts map[string][]string, staging map[string]bool) (resolved, *resolvedApp, bool, string, error) {
	// Unconfigured (e.g. self-update with empty source): skip silently (F7).
	if !a.configured(p) {
		return resolved{}, nil, true, "", nil
	}
	// Honor the def's min_kpm: an old kpm skips a package that needs a newer one
	// (with a note) rather than staging it blindly (§9.8). install/sync gate too,
	// but a hand-edited def can still carry a min_kpm the running kpm can't meet.
	if !registry.MinKpmSatisfied(version.Version, p.MinKpm) && version.Version != "dev" {
		fmt.Printf("%s: requires kpm >= %s (running %s) — skipped; update kpm first\n", p.ID, p.MinKpm, version.Version)
		a.paths.Log("WARN", fmt.Sprintf("%s  requires kpm >= %s — skipped", p.ID, p.MinKpm))
		return resolved{}, nil, true, "", nil
	}
	// B: refuse to fetch software that would collide with something already on
	// the device (INTEGRITY.md §4.3). Required here as well as at install because
	// a def can be hand-`add`ed, or can predate the field. Cheap and offline, so
	// it fails fast — before the network resolve.
	if blockers := a.blockingConflicts(p.ID, conflicts, staging); len(blockers) > 0 {
		return resolved{}, nil, true, a.conflictRefusal(blockers), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	rel, err := a.resolveRelease(ctx, p)
	cancel()
	if err != nil {
		return resolved{}, nil, false, "", err
	}
	// A 2xx that decodes to an empty tag (a misconfigured proxy, or a non-forge
	// endpoint on a hand-edited source) must not read as "up to date" via
	// tagsEqual("", "") for a never-installed package — fail loudly (§11).
	if rel.Tag == "" {
		return resolved{}, nil, false, "", fmt.Errorf("forge returned a release with an empty tag")
	}

	ps := a.state.Get(p.ID)
	pin := a.effectivePin(p)
	// Only record latest_seen for unpinned packages, so a pin can't poison the
	// meaning of "latest" (F1).
	if pin == "" {
		ps.LatestSeen = rel.Tag
	}
	ps.LastChecked = time.Now().UTC().Format(state.TimeFormat)

	// Resolve the asset BEFORE the up-to-date checks: the rolling fingerprint
	// needs it, and the download needs it anyway, so the call is hoisted rather
	// than duplicated (INTEGRITY.md §3.6).
	//
	// The failure classes are NOT the same and must not be treated alike. A
	// malformed glob (path.ErrBadPattern) is a property of the DEF — it can never
	// match anything in any release — so it stays loud and early. "no asset
	// matching" / "N assets match" are properties of the CURRENT RELEASE: a
	// rolling repo republishing by recreating its release has a window where the
	// release exists with no assets yet, and treating that as a def error made
	// `check` say an update was available while `update --all` exited 1 saying it
	// failed — on every publish. So those compute the fingerprint from a nil
	// asset (exactly as cmd_check.go does), fall through the up-to-date and
	// already-staged short-circuits, and hard-fail only where a download is
	// actually about to happen.
	asset, assetErr := rel.MatchAsset(p.Asset)
	if assetErr != nil && errors.Is(assetErr, path.ErrBadPattern) {
		return resolved{}, nil, false, "", assetErr
	}
	var matched *forge.Asset
	if assetErr == nil {
		matched = &asset
	}

	fp := rel.Fingerprint(matched)
	a.warnRollingDegraded(p, fp)
	if a.adoptFingerprintBaseline(p, ps, fp) {
		// The adoption makes this run a no-op, which is right (§3.6.1) but
		// mystifying for a user who deliberately typed `kpm update <id>`. Say so
		// rather than reporting "nothing to stage" with no explanation (m1).
		fmt.Printf("%s: recording this release as the rolling baseline — nothing to update now; a later rebuild will be detected\n", p.ID)
	}
	if pin == "" {
		ps.LatestFingerprint = fp
	}
	rebuilt := rebuildDetected(p, pin, ps.InstalledFingerprint, fp)

	if tagsEqual(ps.InstalledVersion, rel.Tag) && !rebuilt {
		return resolved{}, nil, true, "", nil // up to date
	}
	// A pin freezes the staged payload too: without the pin term a pinned
	// package whose release was rebuilt would fall through and re-download
	// (rebuildDetected covers the installed comparison above).
	if ps.StagedVersion != "" && tagsEqual(ps.StagedVersion, rel.Tag) &&
		(pin != "" || !forge.FingerprintChanged(ps.StagedFingerprint, fp)) {
		return resolved{}, nil, true, "", nil // already staged, awaiting reboot
	}

	// Past every short-circuit: this package IS about to be downloaded, so an
	// unresolved asset is now fatal (see the MatchAsset comment above).
	if assetErr != nil {
		return resolved{}, nil, false, "", assetErr
	}

	// C: the checksum the registry attests for THIS release, "" when the tag is
	// not listed (unverified — the normal state, INTEGRITY.md §5.3).
	wantSHA, ok := p.ChecksumFor(rel.Tag)
	declared := p.ChecksumDeclaredFor(rel.Tag)
	switch {
	case ok:
	case p.EffectiveUpdateCheck() == config.UpdateCheckRolling && len(p.Checksums) > 0:
		// rolling + checksums is a parse-time error for a REGISTRY def, but
		// packages.d is user-editable and a hand-edited def can carry both. The
		// tag never moves while the asset does, so the recorded hash is correct
		// exactly once and then hard-fails every download forever with wording
		// that reads like a security incident. Degrade to unverified (§9.2).
		a.paths.Log("WARN", fmt.Sprintf(
			"%s  update_check = %q cannot be combined with checksums (a rolling tag's asset changes without a def edit) — ignoring the checksums table and downloading unverified",
			p.ID, config.UpdateCheckRolling))
	case declared:
		a.paths.Log("WARN", fmt.Sprintf(
			"%s  checksums entry for tag %s is not a 64-character hex sha256 — downloading unverified", p.ID, rel.Tag))
	}

	// APP-TREE: the archive is a directory tree, not a tarball for rcS. Fetch it
	// as a .zip (calling it a .tgz would be a lie the IsZip sniffer disagrees
	// with) and PLAN it here, so a malformed or hostile archive fails the package
	// exactly where a bad tgz fails today — before anything is written or staged.
	if p.IsAppTree() {
		cachePath, sum, err := a.fetchAsset(p.ID, rel.Tag, ".zip", wantSHA, declared, len(p.Checksums) > 0, asset)
		if err != nil {
			return resolved{}, nil, false, "", err
		}
		plan, perr := apptree.Compute(cachePath, p.Install.Root, p.Install.Dest)
		if perr != nil {
			os.Remove(cachePath)
			return resolved{}, nil, false, "", fmt.Errorf("refusing %s: %w", p.ID, perr)
		}
		return resolved{}, &resolvedApp{pkg: p, tag: rel.Tag, cache: cachePath, sha256: sum, plan: plan, fingerprint: fp}, false, "", nil
	}

	cachePath, manifest, err := a.download(p.ID, rel.Tag, wantSHA, declared, len(p.Checksums) > 0, asset)
	if err != nil {
		return resolved{}, nil, false, "", err
	}
	// No package other than kpm itself may write kpm's reserved paths (its
	// install tree or NickelMenu drop-in). This makes the "kpm merged last"
	// invariant robust regardless of merge order: a package's tgz can never
	// carry a member that would clobber the kpm binary/state/registrations
	// (§7.3, A1).
	if p.ID != selfID {
		if hit := firstReservedPath(manifest); hit != "" {
			os.Remove(cachePath)
			return resolved{}, nil, false, "", fmt.Errorf("refusing %s: its archive writes to kpm's reserved path %q", p.ID, hit)
		}
	}
	return resolved{pkg: p, tag: rel.Tag, asset: asset, cache: cachePath, manifest: manifest, fingerprint: fp}, nil, false, "", nil
}

// firstReservedPath returns the first manifest member that lands in a path only
// kpm's own package may write, or "" if none. Comparison is case-insensitive
// because the onboard partition (where these live) is FAT32.
func firstReservedPath(manifest []string) string {
	for _, m := range manifest {
		lm := strings.ToLower(m)
		if lm == "mnt/onboard/.adds/kpm" || strings.HasPrefix(lm, "mnt/onboard/.adds/kpm/") ||
			lm == "mnt/onboard/.adds/nm/kpm" {
			return m
		}
	}
	return ""
}

// resolveRelease returns the target Release, honoring pins and the 5-minute
// check-freshness window for latest tracking (§7.1).
func (a *App) resolveRelease(ctx context.Context, p *config.Package) (forge.Release, error) {
	f, err := a.forgeFor(p)
	if err != nil {
		return forge.Release{}, err
	}
	// Split the effective source so an adopted kpm resolves from state (§1/§3).
	src := a.effectiveSource(p)
	host, owner, repo := config.SourceHost(src), config.SourceOwner(src), config.SourceRepo(src)
	pin := a.effectivePin(p)
	if pin != "" {
		return f.ReleaseByTag(ctx, host, owner, repo, pin)
	}
	ps := a.state.Get(p.ID)
	// Trust a cached latest tag only if THIS package was itself checked within
	// the freshness window. Keying on the global last_check let a package that
	// failed (or was pinned during) the last check reuse a stale latest_seen and
	// stage an old release (M2). ps.LastChecked is set only on a successful
	// resolve (here and in cmd_check).
	if ps.LatestSeen != "" && fresh(ps.LastChecked) {
		return f.ReleaseByTag(ctx, host, owner, repo, ps.LatestSeen)
	}
	return f.LatestRelease(ctx, host, owner, repo)
}

func fresh(lastCheck string) bool {
	if lastCheck == "" {
		return false
	}
	t, err := time.Parse(state.TimeFormat, lastCheck)
	if err != nil {
		return false
	}
	d := time.Since(t)
	if d < 0 {
		return false // future timestamp (clock skew): treat as stale (F1)
	}
	return d < checkFreshness
}

// download streams the asset to cache/<id>-<tag>.tgz (via .part), verifies the
// registry-declared sha256 if there is one, unwraps a zip-wrapped tgz if the
// asset is one (ZIP-ASSETS §1), then verifies gzip integrity + walks the tar,
// returning the captured manifest.
//
// wantSHA is the checksum the registry attests for this exact release, or "" —
// which is not an error but the normal unverified case (INTEGRITY.md §5.3).
// declaredForTag (an entry exists for THIS tag) and hasChecksums (the def
// carries a table at all) only distinguish the unverified situations in the log
// line; see verifiedSuffix.
func (a *App) download(id, tag, wantSHA string, declaredForTag, hasChecksums bool, asset forge.Asset) (string, []string, error) {
	final, _, err := a.fetchAsset(id, tag, ".tgz", wantSHA, declaredForTag, hasChecksums, asset)
	if err != nil {
		return "", nil, err
	}
	// The asset may be a zip wrapping the real KoboRoot.tgz (e.g. NickelClock's
	// NickelClock-<tag>.zip). Detection is by content, not filename: unwrap the
	// single inner tgz over `final` so the cache path and everything downstream
	// (Verify, merge, staged-hash guard B4, uninstall manifests) are identical to
	// the non-zip flow (ZIP-ASSETS §1). The zip is deleted after successful
	// extraction and on every error path; its temporary .zip.part name keeps a
	// crash leftover inside cleanCache's .part sweep (F3).
	if tgz.IsZip(final) {
		zipTmp := final + ".zip.part"
		if err := os.Rename(final, zipTmp); err != nil {
			os.Remove(final)
			return "", nil, err
		}
		uerr := tgz.Unwrap(zipTmp, final)
		os.Remove(zipTmp)
		if uerr != nil {
			return "", nil, fmt.Errorf("unwrap: %w", uerr)
		}
	}
	res, err := tgz.Verify(final)
	if err != nil {
		os.Remove(final)
		return "", nil, fmt.Errorf("verify: %w", err)
	}
	for _, w := range res.Warnings {
		a.paths.Log("WARN", fmt.Sprintf("%s  unusual path: %s", id, w))
	}
	return final, res.Manifest, nil
}

// fetchAsset streams the asset to cache/<id>-<tag><ext> (via .part), verifies the
// registry-declared sha256 if there is one, and returns the cache path AND the
// digest of the bytes that landed there. It is the half of download() that is
// identical for both install shapes: an app-tree package needs the download and
// the checksum but none of the tgz unwrap/verify, and it lands as .zip rather
// than .tgz because that is what it is.
//
// The digest is returned even when the registry attests nothing: it is not an
// attestation of WHAT was downloaded but a record of what this process wrote, so
// a later reader of the cache file can tell it is still the same bytes.
func (a *App) fetchAsset(id, tag, ext, wantSHA string, declaredForTag, hasChecksums bool, asset forge.Asset) (string, string, error) {
	final := a.paths.CacheDir() + "/" + id + "-" + safeTag(tag) + ext
	part := final + ".part"
	f, err := os.Create(part)
	if err != nil {
		return "", "", err
	}
	ctx := context.Background()
	_, derr := a.client.Download(ctx, asset.DownloadURL, f)
	if derr == nil {
		derr = f.Sync() // durable before the rename to the final name (B3)
	}
	cerr := f.Close()
	if derr != nil {
		os.Remove(part)
		return "", "", fmt.Errorf("download: %w", derr)
	}
	if cerr != nil {
		os.Remove(part)
		return "", "", cerr
	}
	// Checksum the asset EXACTLY as the forge publishes it — before the zip
	// unwrap (which rewrites the file) and before the rename, so a mismatch never
	// produces a file at the final cache name at all (INTEGRITY.md §5.4). There
	// is no retry: Download's own retry loop already succeeded, so the bytes are
	// what the server serves and re-fetching would produce the same result.
	sum, _, err := sha256AndSize(part)
	if err != nil {
		os.Remove(part)
		return "", "", err
	}
	if wantSHA != "" && !strings.EqualFold(sum, wantSHA) {
		os.Remove(part)
		a.paths.Log("CHECKSUM", fmt.Sprintf("%s  %s  MISMATCH want=%s got=%s", id, tag, wantSHA, sum))
		return "", "", fmt.Errorf(
			"checksum mismatch for asset %q of %s %s: registry declares sha256 %s, downloaded %s — refusing to install",
			asset.Name, id, tag, wantSHA, sum)
	}
	// Emitted on every successful download, verified or not: a curator with the
	// package installed reads the hash straight out of `kpm log` (§5.5).
	a.paths.Log("CHECKSUM", fmt.Sprintf("%s  %s  sha256=%s%s", id, tag, sum, verifiedSuffix(wantSHA, declaredForTag, hasChecksums)))
	if err := os.Rename(part, final); err != nil {
		os.Remove(part) // don't leak the full-size .part on a rename failure (#9)
		return "", "", err
	}
	return final, sum, nil
}

// verifiedSuffix annotates the CHECKSUM log line with what the hash proves.
// The unverified cases are deliberately distinguished, because each is a
// different, otherwise-invisible fact:
//
//   - declaredForTag: an attestation exists for THIS exact release but was
//     rejected at use (malformed, or ignored because the def is rolling). Keying
//     the middle case on `len(Checksums) > 0` alone logged "none for this tag"
//     for a tag that had one — the opposite of what happened.
//   - hasChecksums without declaredForTag: the curator IS attesting for this
//     package and has fallen behind a release.
//
// It changes only the log text; the degradation behaviour of INTEGRITY.md §5.3
// is unchanged and nothing reaches --json.
func verifiedSuffix(wantSHA string, declaredForTag, hasChecksums bool) string {
	switch {
	case wantSHA != "":
		return " (verified)"
	case declaredForTag:
		return " (unverified — the checksum declared for this tag is unusable)"
	case hasChecksums:
		return " (unverified — registry has checksums but none for this tag)"
	default:
		return " (unverified — no registry checksum)"
	}
}

// guardExistingTgz refuses to overwrite a staged tgz that kpm did not create.
// A present tgz is "ours" only if its content hash matches what state recorded
// at stage time (B4) — a foreign or manually-installed tgz (different hash) is
// never clobbered (§7.3).
func (a *App) guardExistingTgz() error {
	if _, err := os.Stat(a.paths.StagedTgz()); err != nil {
		return nil // no existing tgz
	}
	if a.stagedTgzIsOurs() {
		return nil // ours (re-stage per A1)
	}
	return fmt.Errorf("%s exists but was not staged by kpm; remove it or reboot to install it first", a.paths.StagedTgz())
}

// stagedTgzIsOurs reports whether the on-disk staged KoboRoot.tgz is the exact
// archive kpm last staged, proven by content hash + size (B4).
func (a *App) stagedTgzIsOurs() bool {
	fi, err := os.Stat(a.paths.StagedTgz())
	if err != nil || a.state.StagedSHA256 == "" {
		return false
	}
	sum, size, err := sha256AndSize(a.paths.StagedTgz())
	if err != nil {
		return false
	}
	return size == a.state.StagedSize && fi.Size() == size && sum == a.state.StagedSHA256
}

// mergeStaged merges all target tgzs into a NOT-yet-live .kpm-part file and
// returns its path plus content hash/size. If a kpm-staged tgz already exists it
// is re-merged FIRST so previously staged packages keep their payload; the run's
// new targets follow (alphabetical by id, kpm last so it cannot be clobbered —
// §7.3, A1). Dup warnings that involve the re-merged source are suppressed
// (expected re-stage collisions). The part is hashed BEFORE it goes live so a
// hashing failure can never leave a live tgz with a stale/empty recorded hash
// (which would wedge the guard). commitStaged moves it live afterwards (B6).
func (a *App) mergeStaged(targets []resolved) (part, sum string, size int64, dups []string, err error) {
	ordered := append([]resolved(nil), targets...)
	sort.Slice(ordered, func(i, j int) bool {
		if (ordered[i].pkg.ID == selfID) != (ordered[j].pkg.ID == selfID) {
			return ordered[j].pkg.ID == selfID // kpm sinks to the end
		}
		return ordered[i].pkg.ID < ordered[j].pkg.ID
	})

	var sources []string
	suppress := map[string]bool{}
	if a.stagedTgzIsOurs() {
		res, verr := tgz.Verify(a.paths.StagedTgz())
		if verr != nil {
			// Our previously-staged tgz is corrupt. Silently dropping it would
			// re-merge without its packages while state still marks them staged,
			// so after reboot they'd promote to installed with a payload that
			// never shipped. Abort instead so the user can unstage and retry.
			return "", "", 0, nil, fmt.Errorf("existing staged tgz failed verification (%v); run 'kpm unstage' and retry", verr)
		}
		for _, n := range res.Manifest {
			suppress[n] = true
		}
		sources = append(sources, a.paths.StagedTgz())
	}
	for _, r := range ordered {
		sources = append(sources, r.cache)
	}

	part = a.paths.StagedTgz() + ".kpm-part"
	dups, err = tgz.Merge(sources, part)
	if err != nil {
		os.Remove(part)
		return "", "", 0, nil, err
	}
	sum, size, err = sha256AndSize(part)
	if err != nil {
		os.Remove(part)
		return "", "", 0, nil, err
	}

	var filtered []string
	for _, d := range dups {
		if suppress[d] {
			continue // re-stage collision against the re-merged source (A1)
		}
		filtered = append(filtered, d)
	}
	return part, sum, size, filtered, nil
}

// commitStaged moves the merged .kpm-part into the live boot slot, makes the
// directory entry durable, and marks the staging committed. It is called only
// after the per-package staged fields (and the tgz hash/size) have already been
// saved with StagedCommitted=false, so a crash before this leaves no installed
// files unrecorded, and a crash during it is reconciled safely (B6).
func (a *App) commitStaged(part string) error {
	if err := os.Rename(part, a.paths.StagedTgz()); err != nil {
		os.Remove(part)
		return err
	}
	device.FsyncDir(filepath.Dir(a.paths.StagedTgz()))
	a.state.StagedCommitted = true
	return a.state.Save()
}

// sha256AndSize returns the hex SHA-256 and byte size of the file at path.
func sha256AndSize(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// cleanCache removes stale cache artifacts at the start of an update run: all
// .part files (interrupted downloads) and any .tgz/.zip older than 7 days (F3).
// It only touches *.part / *.tgz / *.zip to stay registry-compatible
// (REGISTRY.md §7.5) — the registry caches use a registry-<name>.toml prefix.
// .zip joined the sweep in 0.11.0: an app-tree asset is cached as
// <id>-<tag>.zip, and a 42 MiB KOReader zip left behind forever would be the
// largest thing in .adds/kpm (APP-TREE.md §6.1).
func (a *App) cleanCache() {
	entries, err := os.ReadDir(a.paths.CacheDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		full := filepath.Join(a.paths.CacheDir(), name)
		switch {
		case strings.HasSuffix(name, ".part"):
			os.Remove(full)
		case strings.HasSuffix(name, ".tgz"), strings.HasSuffix(name, ".zip"):
			if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
				os.Remove(full)
			}
		}
	}
}

// cmdUnstage cancels a pending staging: it removes the staged KoboRoot.tgz IF
// its hash proves kpm staged it, then clears every staged_* field (B4). A
// foreign tgz is refused (never removed).
func (a *App) cmdUnstage(args []string) int {
	flags, pos := splitArgs(args, nil)
	flags, jsonMode := takeJSON(flags)
	if len(flags) > 0 || len(pos) > 0 {
		fmt.Fprintln(os.Stderr, "usage: kpm unstage [--json]")
		return exitError
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err != nil {
		if jsonMode {
			jsonLine(jsonUnstage{Unstaged: false, IDs: []string{}})
			return exitOK
		}
		fmt.Println("no staged update to cancel")
		return exitOK
	}
	if !a.stagedTgzIsOurs() {
		if jsonMode {
			jsonError(fmt.Sprintf("%s was not staged by kpm; refusing to remove it", a.paths.StagedTgz()))
		}
		fmt.Fprintf(os.Stderr, "kpm unstage: %s was not staged by kpm; refusing to remove it\n", a.paths.StagedTgz())
		return exitError
	}
	if err := os.Remove(a.paths.StagedTgz()); err != nil {
		if jsonMode {
			jsonError(err.Error())
		}
		fmt.Fprintln(os.Stderr, "kpm unstage:", err)
		return exitError
	}
	unstaged := []string{}
	for id, ps := range a.state.Packages {
		if ps.StagedVersion == "" {
			continue
		}
		a.paths.Log("UNSTAGE", fmt.Sprintf("%s  %s", id, ps.StagedVersion))
		ps.StagedVersion = ""
		ps.StagedAt = ""
		ps.StagedManifest = nil
		ps.StagedFingerprint = ""
		unstaged = append(unstaged, id)
	}
	sort.Strings(unstaged)
	a.state.StagedSHA256 = ""
	a.state.StagedSize = 0
	a.state.StagedCommitted = false
	if err := a.state.Save(); err != nil {
		if jsonMode {
			jsonError("state: " + err.Error())
		}
		fmt.Fprintln(os.Stderr, "kpm unstage: state:", err)
		return exitError
	}
	a.paths.WriteStatus("kpm: staging cancelled")
	if jsonMode {
		jsonLine(jsonUnstage{Unstaged: true, IDs: unstaged})
		return exitOK
	}
	fmt.Println("staging cancelled")
	return exitOK
}

// safeTag makes a tag safe for a cache filename.
func safeTag(tag string) string {
	return strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(tag)
}
