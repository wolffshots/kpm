package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/forge"
	"kpm/internal/state"
)

func (a *App) cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	notify := fs.Bool("notify", false, "emit NickelDBus toasts")
	flags, pos := splitArgs(args, nil)
	flags, jsonMode := takeJSON(flags)
	if err := fs.Parse(flags); err != nil {
		return exitError
	}
	if len(pos) > 0 {
		fmt.Fprintln(os.Stderr, "usage: kpm check [--notify] [--json]")
		return exitError
	}
	n := notifier{on: *notify}

	pkgs, err := a.loadPackages()
	if err != nil {
		if jsonMode {
			jsonError(err.Error())
		}
		fmt.Fprintln(os.Stderr, "kpm check:", err)
		return exitError
	}
	if len(pkgs) == 0 {
		if jsonMode {
			jsonLine(jsonCheckPayload{Packages: []jsonCheckPkg{}, Checked: time.Now().UTC().Format(state.TimeFormat)})
			return exitOK
		}
		fmt.Println("no packages registered")
		return exitOK
	}

	n.toast("kpm: checking for updates…")

	// Wait for connectivity against the first configured package's forge host.
	if host := a.firstConfiguredHost(pkgs); host != "" {
		if !device.WaitForNetwork(a.client.HTTP(), host) {
			msg := "kpm: no network — check Wi-Fi and retry"
			a.paths.WriteStatus(msg)
			a.paths.Log("CHECK", "aborted: no network")
			n.toast(msg)
			if jsonMode {
				jsonError("no network — check Wi-Fi and retry")
			}
			fmt.Fprintln(os.Stderr, msg)
			return exitError
		}
	}

	now := time.Now().UTC().Format(state.TimeFormat)
	failed := 0
	okCount := 0
	available := 0
	checkPkgs := []jsonCheckPkg{} // §2.2: per-package latest, in pkgs order
	// Unconfigured (e.g. self-update with empty source): skip silently (F7).
	todo := make([]*config.Package, 0, len(pkgs))
	for _, p := range pkgs {
		if a.configured(p) {
			todo = append(todo, p)
		}
	}
	// The release lookups — the only network work here — run concurrently and
	// bounded; everything below stays exactly as it was, in pkgs order.
	fetched := a.prefetchReleases(todo)
	for i, p := range todo {
		rel, err := fetched[i].rel, fetched[i].err
		ps := a.state.Get(p.ID)
		pin := a.effectivePin(p)
		if err != nil {
			failed++
			checkPkgs = append(checkPkgs, jsonCheckPkg{
				ID: p.ID, Installed: ptr(ps.InstalledVersion), Latest: nil,
				Update: false, Pinned: ptr(pin), Error: ptr(err.Error()),
			})
			fmt.Fprintf(os.Stderr, "kpm check: %s: %v\n", p.ID, err)
			a.paths.Log("CHECK", fmt.Sprintf("%s  error: %v", p.ID, err))
			continue
		}
		okCount++
		tag := rel.Tag
		// Record the per-package check time only on SUCCESS: update's freshness
		// window keys on it to decide whether latest_seen may be reused, so a
		// failed check must not mark the package freshly checked (M2).
		ps.LastChecked = now
		// Only record latest_seen for unpinned packages so a pin can't poison
		// the meaning of "latest" (F1).
		if pin == "" {
			ps.LatestSeen = tag
		}
		// Rolling detection (INTEGRITY.md §3.6). Asset matching is best-effort:
		// an unresolvable glob passes nil to Fingerprint rather than failing the
		// check, so check's failure semantics (and its exit-2 partial accounting)
		// are exactly as they were.
		var matched *forge.Asset
		if asset, aerr := rel.MatchAsset(p.Asset); aerr == nil {
			matched = &asset
		}
		fp := rel.Fingerprint(matched)
		if a.warnRollingDegraded(p, fp) {
			fmt.Printf("%s: rolling update detection unavailable from this forge — falling back to tag comparison\n", p.ID)
		}
		a.adoptFingerprintBaseline(p, ps, fp)
		// LatestFingerprint follows LatestSeen exactly, pin rule included.
		if pin == "" {
			ps.LatestFingerprint = fp
		}
		_, avail := updateTarget(p, ps, pin)
		if avail {
			available++
			a.paths.Log("CHECK", fmt.Sprintf("%s  %s -> %s available",
				p.ID, dash(ps.InstalledVersion), tag))
		}
		checkPkgs = append(checkPkgs, jsonCheckPkg{
			ID: p.ID, Installed: ptr(ps.InstalledVersion), Latest: ptr(tag),
			Update: avail, Pinned: ptr(pin), Error: nil,
		})
	}
	a.state.LastCheck = now
	if err := a.state.Save(); err != nil {
		if jsonMode {
			jsonError("state: " + err.Error())
		}
		fmt.Fprintln(os.Stderr, "kpm check: state:", err)
		return exitError
	}

	status := a.buildStatus(pkgs)
	if failed > 0 {
		status += fmt.Sprintf("%d check(s) failed — see log.\n", failed)
	}
	a.paths.WriteStatus(status)
	fmt.Print(status)

	if jsonMode {
		jsonLine(jsonCheckPayload{Packages: checkPkgs, Checked: now})
	}

	// Toast reflects failures too (F8).
	switch {
	case failed > 0 && available > 0:
		n.toast(fmt.Sprintf("kpm: %d update(s) available, %d check failed", available, failed))
	case failed > 0:
		n.toast("kpm: check failed (see log)")
	case available > 0:
		n.toast(fmt.Sprintf("kpm: %d update(s) available", available))
	default:
		n.toast("kpm: everything up to date")
	}

	if failed > 0 {
		// Exit 2 is "partial" — only when at least one check also succeeded.
		// If every check failed, that is a plain error (exit 1), matching the
		// documented rule for update.
		if okCount == 0 {
			return exitError
		}
		return exitPartial
	}
	return exitOK
}

// resolveCheckRelease returns the whole release a package currently resolves
// to: the pinned tag (confirmed to exist) or the latest release. check needs
// more than the tag now — the rolling fingerprint is built from the release's
// published_at and its matched asset's size (INTEGRITY.md §3.6).
//
// It is the two halves below run back to back, which is what every non-check
// caller wants. check itself runs them apart: the App half must stay on the main
// goroutine (see checkTarget), only the network half fans out.
func (a *App) resolveCheckRelease(p *config.Package) (forge.Release, error) {
	return a.checkTargetFor(p).resolve()
}

// checkTarget is everything a release lookup needs that lives on App, captured
// so the lookup itself touches nothing shared and mutable.
//
// This split is not decoration. effectiveForge/effectiveSource/effectivePin all
// go through a.state.Get for kpm's own id, and State.Get CREATES the map entry
// when it is absent — a write to a map the whole run shares. Resolving packages
// concurrently with that inside a worker is a data race, so it is hoisted here
// instead.
type checkTarget struct {
	f   forge.Forge
	src string
	pin string
	err error // an unknown forge identifier, surfaced at the package's turn
}

// checkTargetFor captures one package's lookup inputs. MAIN GOROUTINE ONLY.
func (a *App) checkTargetFor(p *config.Package) checkTarget {
	f, err := a.forgeFor(p)
	if err != nil {
		return checkTarget{err: err}
	}
	// Split the effective source so an adopted kpm resolves from state (§1/§3).
	return checkTarget{f: f, src: a.effectiveSource(p), pin: a.effectivePin(p)}
}

// resolve performs the lookup. It reads only its own fields and the shared
// *forge.Client (whose http.Client is concurrency-safe and whose cache dir is
// fixed before the first request), so it is safe to run concurrently. The
// per-package 60s budget lives here, one per package, exactly as before.
func (t checkTarget) resolve() (forge.Release, error) {
	if t.err != nil {
		return forge.Release{}, t.err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var rel forge.Release
	var err error
	if t.pin != "" {
		rel, err = t.f.ReleaseByTag(ctx, config.SourceHost(t.src), config.SourceOwner(t.src), config.SourceRepo(t.src), t.pin)
	} else {
		rel, err = t.f.LatestRelease(ctx, config.SourceHost(t.src), config.SourceOwner(t.src), config.SourceRepo(t.src))
	}
	if err != nil {
		return forge.Release{}, err
	}
	if rel.Tag == "" {
		return forge.Release{}, fmt.Errorf("forge returned a release with an empty tag")
	}
	return rel, nil
}

// checkResult is one package's lookup outcome, held until the ordered apply
// pass so state, counters, stderr and the log stay single-threaded.
type checkResult struct {
	rel forge.Release
	err error
}

// checkConcurrency bounds how many release lookups check has in flight. Four,
// not one per package: the device CPU is weak and the TLS handshake is the
// expensive part of each lookup, and GitHub applies secondary limits to
// aggressive concurrency. It is the knob if hardware says otherwise.
const checkConcurrency = 4

// prefetchReleases resolves every package's release concurrently, bounded by
// checkConcurrency, and returns the outcomes INDEXED BY POSITION so the caller
// applies them in pkgs order (JSON-OUTPUT.md §2.2).
//
// Only the network call fans out. Every App read is done here on this goroutine,
// and no worker writes state, logs, or prints — those all stay in the ordered
// apply pass, or state.json races and log lines interleave nondeterministically.
func (a *App) prefetchReleases(pkgs []*config.Package) []checkResult {
	targets := make([]checkTarget, len(pkgs))
	for i, p := range pkgs {
		targets[i] = a.checkTargetFor(p)
	}
	out := make([]checkResult, len(pkgs))
	sem := make(chan struct{}, checkConcurrency)
	var wg sync.WaitGroup
	for i := range pkgs {
		sem <- struct{}{} // bounds live goroutines, not just live requests
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i].rel, out[i].err = targets[i].resolve()
		}(i)
	}
	wg.Wait()
	return out
}
