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
	if err := fs.Parse(flags); err != nil {
		return exitError
	}
	if len(pos) > 0 {
		fmt.Fprintln(os.Stderr, "usage: kpm check [--notify]")
		return exitError
	}
	n := notifier{on: *notify}

	pkgs, err := a.loadPackages()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm check:", err)
		return exitError
	}
	if len(pkgs) == 0 {
		fmt.Println("no packages registered")
		return exitOK
	}

	n.toast("kpm: checking for updates…")

	// Wait for connectivity against the first configured package's forge host.
	if host := firstConfiguredHost(pkgs); host != "" {
		if !device.WaitForNetwork(a.client.HTTP(), host) {
			msg := "kpm: no network — check Wi-Fi and retry"
			a.paths.WriteStatus(msg)
			a.paths.Log("CHECK", "aborted: no network")
			n.toast(msg)
			fmt.Fprintln(os.Stderr, msg)
			return exitError
		}
	}

	now := time.Now().UTC().Format(state.TimeFormat)
	failed := 0
	available := 0
	for _, p := range pkgs {
		// Unconfigured (e.g. self-update with empty source): skip silently (F7).
		if !p.Configured() {
			continue
		}
		tag, err := a.resolveTag(p)
		ps := a.state.Get(p.ID)
		ps.LastChecked = now
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "kpm check: %s: %v\n", p.ID, err)
			a.paths.Log("CHECK", fmt.Sprintf("%s  error: %v", p.ID, err))
			continue
		}
		// Only record latest_seen for unpinned packages so a pin can't poison
		// the meaning of "latest" (F1).
		if a.effectivePin(p) == "" {
			ps.LatestSeen = tag
		}
		if _, avail := updateTarget(p, ps, a.effectivePin(p)); avail {
			available++
			a.paths.Log("CHECK", fmt.Sprintf("%s  %s -> %s available",
				p.ID, dash(ps.InstalledVersion), tag))
		}
	}
	a.state.LastCheck = now
	if err := a.state.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "kpm check: state:", err)
		return exitError
	}

	status := a.buildStatus(pkgs)
	if failed > 0 {
		status += fmt.Sprintf("%d check(s) failed — see log.\n", failed)
	}
	a.paths.WriteStatus(status)
	fmt.Print(status)

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
	pin := a.effectivePin(p)
	if pin != "" {
		rel, err := f.ReleaseByTag(ctx, p.Host(), p.Owner(), p.Repo(), pin)
		if err != nil {
			return "", err
		}
		return rel.Tag, nil
	}
	rel, err := f.LatestRelease(ctx, p.Host(), p.Owner(), p.Repo())
	if err != nil {
		return "", err
	}
	return rel.Tag, nil
}
