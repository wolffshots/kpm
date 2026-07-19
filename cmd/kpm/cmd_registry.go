package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/forge"
	"kpm/internal/registry"
	"kpm/internal/state"
)

// cmdRegistry dispatches the registry subcommand group (§4). add/remove/refresh
// mutate (lock held); list is read-only.
func (a *App) cmdRegistry(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kpm registry add|remove|list|refresh")
		return exitError
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return a.cmdRegistryAdd(rest)
	case "remove":
		return a.cmdRegistryRemove(rest)
	case "list":
		return a.cmdRegistryList(rest)
	case "refresh":
		return a.cmdRegistryRefresh(rest)
	default:
		fmt.Fprintf(os.Stderr, "kpm registry: unknown subcommand %q\n", sub)
		return exitError
	}
}

func (a *App) cmdRegistryAdd(args []string) int {
	fs := flag.NewFlagSet("registry add", flag.ContinueOnError)
	name := fs.String("name", "", "registry nickname (default derived from repo)")
	ref := fs.String("ref", registry.DefaultRef, "branch or tag")
	path := fs.String("path", registry.DefaultPath, "manifest path in the repo")
	forgeFlag := fs.String("forge", "", "forge type: github|forgejo (overrides detection)")
	flags, pos := splitArgs(args, map[string]bool{"name": true, "ref": true, "path": true, "forge": true})
	if err := fs.Parse(flags); err != nil {
		return exitError
	}
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "usage: kpm registry add <url> [--name n] [--ref branch] [--path p] [--forge f]")
		return exitError
	}

	// A registry URL is host/owner/repo; a pasted release-page URL would have
	// its tag silently discarded, so reject it and point at --ref (C4).
	if strings.Contains(pos[0], "/releases") {
		fmt.Fprintln(os.Stderr, "kpm registry add: registry URLs must not include /releases — use --ref <branch-or-tag>")
		return exitError
	}

	// Reuse addurl host/owner/repo parsing (no /releases forms are meaningful).
	spec, err := config.ParseAddURL(pos[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm registry add:", err)
		return exitError
	}

	// Resolve forge once at add time: explicit flag > github.com > probe (§9.3).
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
			fmt.Fprintf(os.Stderr, "kpm registry add: could not detect forge for %q; pass --forge github|forgejo\n", spec.Host)
			return exitError
		}
	}
	if forgeKind != config.ForgeGitHub && forgeKind != config.ForgeForgejo {
		fmt.Fprintf(os.Stderr, "kpm registry add: invalid forge %q\n", forgeKind)
		return exitError
	}
	if forgeKind == config.ForgeGitHub && spec.Host != "github.com" {
		fmt.Fprintf(os.Stderr, "kpm registry add: github forge only supports github.com, not %q; use --forge forgejo\n", spec.Host)
		return exitError
	}

	regName := spec.ID
	if *name != "" {
		regName = *name
	}
	if !config.ValidID(regName) {
		fmt.Fprintf(os.Stderr, "kpm registry add: invalid registry name %q (need [a-z0-9-]+)\n", regName)
		return exitError
	}

	cfg, err := a.loadRegistryConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm registry add:", err)
		return exitError
	}
	if _, exists := cfg.Find(regName); exists {
		fmt.Fprintf(os.Stderr, "kpm registry add: registry %q already configured\n", regName)
		return exitError
	}

	cfg.Registries = append(cfg.Registries, registry.Registry{
		Name:  regName,
		URL:   spec.Source(),
		Ref:   *ref,
		Path:  *path,
		Forge: forgeKind,
	})
	if err := a.saveRegistryConfig(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "kpm registry add:", err)
		return exitError
	}
	a.paths.Log("REGADD", fmt.Sprintf("%s  %s (%s, ref %s)", regName, spec.Source(), forgeKind, *ref))
	fmt.Printf("added registry %s -> %s [%s], ref %s, path %s\n", regName, spec.Source(), forgeKind, *ref, *path)
	fmt.Printf("run \"kpm registry refresh %s\" to fetch its package list\n", regName)
	return exitOK
}

func (a *App) cmdRegistryRemove(args []string) int {
	flags, pos := splitArgs(args, nil)
	if len(flags) > 0 || len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "usage: kpm registry remove <name>")
		return exitError
	}
	name := pos[0]
	cfg, err := a.loadRegistryConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm registry remove:", err)
		return exitError
	}
	if _, exists := cfg.Find(name); !exists {
		fmt.Fprintf(os.Stderr, "kpm registry remove: registry %q not configured\n", name)
		return exitError
	}
	kept := cfg.Registries[:0]
	for _, r := range cfg.Registries {
		if r.Name != name {
			kept = append(kept, r)
		}
	}
	cfg.Registries = kept
	if err := a.saveRegistryConfig(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "kpm registry remove:", err)
		return exitError
	}
	// Forget its cache file and state entry; installed packages are unaffected (§9.11).
	os.Remove(a.paths.RegistryCache(name))
	delete(a.state.Registries, name)
	if err := a.state.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "kpm registry remove: state:", err)
		return exitError
	}
	a.paths.Log("REGREMOVE", name)
	fmt.Printf("removed registry %s (installed packages unaffected)\n", name)
	return exitOK
}

func (a *App) cmdRegistryList(args []string) int {
	if flags, pos := splitArgs(args, nil); len(flags) > 0 || len(pos) > 0 {
		fmt.Fprintln(os.Stderr, "usage: kpm registry list")
		return exitError
	}
	cfg, err := a.loadRegistryConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm registry list:", err)
		return exitError
	}
	if len(cfg.Registries) == 0 {
		fmt.Println("no registries configured — add one with \"kpm registry add <url>\"")
		return exitOK
	}
	now := time.Now()
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tURL\tREF\tREFRESHED\tPACKAGES")
	for _, r := range cfg.Registries {
		fetched := ""
		if a.state.Registries != nil {
			if rs := a.state.Registries[r.Name]; rs != nil {
				fetched = rs.LastFetched
			}
		}
		note := registry.Staleness(fetched, now)
		count := "-"
		if m, err := a.cachedManifest(r); err == nil {
			count = fmt.Sprintf("%d", len(m.Packages))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.URL, r.Ref, note, count)
	}
	w.Flush()
	return exitOK
}

func (a *App) cmdRegistryRefresh(args []string) int {
	flags, pos := splitArgs(args, nil)
	if len(flags) > 0 {
		// The name is a positional; a stray --name (or any flag) is a usage error.
		fmt.Fprintln(os.Stderr, "usage: kpm registry refresh [<name>] (pass the name as a positional, not a flag)")
		return exitError
	}
	cfg, err := a.loadRegistryConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm registry refresh:", err)
		return exitError
	}

	// Select targets: all registries, or the named one.
	var targets []registry.Registry
	switch len(pos) {
	case 0:
		targets = cfg.Registries
	case 1:
		r, ok := cfg.Find(pos[0])
		if !ok {
			fmt.Fprintf(os.Stderr, "kpm registry refresh: registry %q not configured\n", pos[0])
			return exitError
		}
		targets = []registry.Registry{r}
	default:
		fmt.Fprintln(os.Stderr, "usage: kpm registry refresh [<name>]")
		return exitError
	}
	if len(targets) == 0 {
		fmt.Println("no registries configured — add one with \"kpm registry add <url>\"")
		return exitOK
	}

	// Network wait (the sole network path for registries, §9.2), gated like check.
	if host := firstRegistryHost(targets); host != "" {
		if !device.WaitForNetwork(a.client.HTTP(), host) {
			msg := "kpm: no network — check Wi-Fi and retry"
			a.paths.Log("REFRESH", "aborted: no network")
			fmt.Fprintln(os.Stderr, msg)
			return exitError
		}
	}

	failed := 0
	for _, r := range targets {
		if err := a.refreshOne(r); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "kpm registry refresh: %s: %v\n", r.Name, err)
			a.paths.Log("REFRESH", fmt.Sprintf("%s  error: %v", r.Name, err))
		}
	}
	if err := a.state.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "kpm registry refresh: state:", err)
		return exitError
	}

	// Conflict detection across all cached registries: WARN once per shadowed id
	// (§9.6). Uses config order so the winner matches search/install.
	mans, _ := a.cachedManifests(cfg)
	_, conflicts := registry.Merge(mans)
	a.reportConflicts(conflicts)

	if failed > 0 {
		return exitPartial
	}
	return exitOK
}

// reportConflicts logs and prints one WARN per package id offered by more than
// one registry, naming the winner (earliest in config order) — §9.6.
func (a *App) reportConflicts(conflicts []registry.Conflict) {
	for _, c := range conflicts {
		a.paths.Log("WARN", fmt.Sprintf("package %q offered by %v; using %q, shadowing the rest",
			c.ID, append([]string{c.Winner}, c.Shadowed...), c.Winner))
		fmt.Printf("warning: %q is offered by multiple registries; using %q\n", c.ID, c.Winner)
	}
}

// refreshOne fetches a single registry's manifest and stores it. A bad fetch
// (network/HTTP/parse/schema) never replaces a good cache (§9.3). For Forgejo it
// tries the branch raw-URL then the tag raw-URL, falling through only on a 404
// so tag-pinned registries refresh (B2).
func (a *App) refreshOne(r registry.Registry) error {
	urls, err := registry.RawURLs(r.Forge, r.URL, r.Ref, r.Path)
	if err != nil {
		return err
	}
	// C8: read the recorded etag WITHOUT creating a state entry (a failed
	// refresh must not leave an empty {} behind). B1: only send If-None-Match
	// when the cache file actually exists — a 304 with no cache would leave us
	// with neither a body nor a cache to fall back on.
	etag := ""
	if _, statErr := os.Stat(a.paths.RegistryCache(r.Name)); statErr == nil {
		if rs := a.state.Registries[r.Name]; rs != nil {
			etag = rs.Etag
		}
	}
	var notFound error
	for _, url := range urls {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		res, ferr := a.client.FetchRaw(ctx, url, etag)
		cancel()
		if ferr == nil {
			return a.storeRefresh(r, res)
		}
		if ferr == forge.ErrNotFound {
			notFound = fmt.Errorf("manifest not found at %s/%s (checked %s)", r.URL, r.Path, r.Ref)
			continue // try the next candidate (branch -> tag)
		}
		return ferr // any non-404 error aborts
	}
	return notFound
}

// storeRefresh validates a fetched result and updates the cache + state. It is
// the network-free half of refresh so the cache-safety rules are host-testable:
// the fetched TOML is parsed and schema-gated BEFORE the cache file is replaced,
// so a bad or unsupported manifest keeps the previous good cache (§9.3). A 304
// keeps the cache and only bumps last_fetched.
func (a *App) storeRefresh(r registry.Registry, res forge.RawResult) error {
	rs := a.state.Registry(r.Name)
	now := time.Now().UTC().Format(state.TimeFormat)
	if res.NotModified {
		rs.LastFetched = now // cache still current (§9.3)
		a.paths.Log("REFRESH", fmt.Sprintf("%s  not modified", r.Name))
		return nil
	}
	m, err := registry.ParseManifest(res.Body)
	if err != nil {
		return err // old cache untouched
	}
	if err := device.WriteFileAtomic(a.paths.RegistryCache(r.Name), res.Body); err != nil {
		return err
	}
	rs.LastFetched = now
	rs.Etag = res.Etag
	a.paths.Log("REFRESH", fmt.Sprintf("%s  %d package(s)", r.Name, len(m.Packages)))
	return nil
}

// firstRegistryHost returns the forge host to poll for connectivity: for github
// registries the raw-file host, otherwise the instance host.
func firstRegistryHost(regs []registry.Registry) string {
	for _, r := range regs {
		if u, err := registry.RawURL(r.Forge, r.URL, r.Ref, r.Path); err == nil {
			if parsed, perr := url.Parse(u); perr == nil {
				return parsed.Host
			}
		}
	}
	return ""
}
