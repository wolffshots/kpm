package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"kpm/internal/config"
	"kpm/internal/device"
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
	for _, p := range pkgs {
		// Unconfigured (e.g. self-update with empty source): skip silently (F7).
		if !a.configured(p) {
			continue
		}
		tag, err := a.resolveTag(p)
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
		// Record the per-package check time only on SUCCESS: update's freshness
		// window keys on it to decide whether latest_seen may be reused, so a
		// failed check must not mark the package freshly checked (M2).
		ps.LastChecked = now
		// Only record latest_seen for unpinned packages so a pin can't poison
		// the meaning of "latest" (F1).
		if pin == "" {
			ps.LatestSeen = tag
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

// resolveTag returns the release tag a package currently resolves to: the
// pinned tag (confirmed to exist) or the latest release.
func (a *App) resolveTag(p *config.Package) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f, err := a.forgeFor(p)
	if err != nil {
		return "", err
	}
	// Split the effective source so an adopted kpm resolves from state (§1/§3).
	src := a.effectiveSource(p)
	pin := a.effectivePin(p)
	if pin != "" {
		rel, err := f.ReleaseByTag(ctx, config.SourceHost(src), config.SourceOwner(src), config.SourceRepo(src), pin)
		if err != nil {
			return "", err
		}
		if rel.Tag == "" {
			return "", fmt.Errorf("forge returned a release with an empty tag")
		}
		return rel.Tag, nil
	}
	rel, err := f.LatestRelease(ctx, config.SourceHost(src), config.SourceOwner(src), config.SourceRepo(src))
	if err != nil {
		return "", err
	}
	if rel.Tag == "" {
		return "", fmt.Errorf("forge returned a release with an empty tag")
	}
	return rel.Tag, nil
}
