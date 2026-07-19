package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"kpm/internal/config"
	"kpm/internal/forge"
	"kpm/internal/state"
)

func (a *App) cmdAdd(args []string) int {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	asset := fs.String("asset", "", "release asset name or glob (default KoboRoot.tgz)")
	forgeFlag := fs.String("forge", "", "forge type: github|forgejo (overrides detection)")
	name := fs.String("name", "", "package id (default derived from repo)")
	installed := fs.String("installed", "", "seed installed_version for a pre-kpm install")
	flags, pos := splitArgs(args, map[string]bool{"asset": true, "forge": true, "name": true, "installed": true})
	if err := fs.Parse(flags); err != nil {
		return exitError
	}
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "usage: kpm add <url> [--asset g] [--forge f] [--name id] [--installed v]")
		return exitError
	}

	spec, err := config.ParseAddURL(pos[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm add:", err)
		return exitError
	}

	// Resolve forge: explicit flag > github.com detection > probe.
	forgeKind := *forgeFlag
	if forgeKind == "" {
		forgeKind = spec.Forge
	}
	if forgeKind == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		ok := forge.Probe(ctx, a.client, spec.Host)
		cancel()
		if ok {
			forgeKind = config.ForgeForgejo
		} else {
			fmt.Fprintf(os.Stderr, "kpm add: could not detect forge for %q; pass --forge github|forgejo\n", spec.Host)
			return exitError
		}
	}
	if forgeKind != config.ForgeGitHub && forgeKind != config.ForgeForgejo {
		fmt.Fprintf(os.Stderr, "kpm add: invalid forge %q\n", forgeKind)
		return exitError
	}
	// GitHub Enterprise is out of scope: a github source must be github.com (D1).
	if forgeKind == config.ForgeGitHub && spec.Host != "github.com" {
		fmt.Fprintf(os.Stderr, "kpm add: github forge only supports github.com, not %q; use --forge forgejo for self-hosted\n", spec.Host)
		return exitError
	}

	id := spec.ID
	if *name != "" {
		id = *name
	}
	if !config.ValidID(id) {
		fmt.Fprintf(os.Stderr, "kpm add: invalid package id %q (need [a-z0-9-]+)\n", id)
		return exitError
	}

	path := a.paths.PackageFile(id)
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(os.Stderr, "kpm add: package %q already registered\n", id)
		return exitError
	}

	assetName := *asset
	if assetName == "" {
		assetName = config.DefaultAsset
	}

	pkg := &config.Package{
		Name:   spec.Repo,
		Source: spec.Source(),
		Forge:  forgeKind,
		Asset:  assetName,
		Pin:    spec.Pin,
	}
	if err := config.Save(path, pkg); err != nil {
		fmt.Fprintln(os.Stderr, "kpm add:", err)
		return exitError
	}

	if *installed != "" {
		ps := a.state.Get(id)
		ps.InstalledVersion = *installed
		ps.InstalledAt = time.Now().UTC().Format(state.TimeFormat)
		if err := a.state.Save(); err != nil {
			fmt.Fprintln(os.Stderr, "kpm add: state:", err)
			return exitError
		}
	}

	a.paths.Log("ADD", fmt.Sprintf("%s  %s (%s)", id, pkg.Source, forgeKind))
	fmt.Printf("registered %s -> %s [%s], asset %q\n", id, pkg.Source, forgeKind, assetName)
	if spec.Pin != "" {
		fmt.Printf("pinned to %s\n", spec.Pin)
	}
	return exitOK
}
