package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
}

func (a *App) cmdUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	all := fs.Bool("all", false, "update all registered packages")
	reboot := fs.Bool("reboot", false, "reboot after staging (installs on boot)")
	notify := fs.Bool("notify", false, "emit NickelDBus toasts")
	flags, pos := splitArgs(args, nil)
	if err := fs.Parse(flags); err != nil {
		return exitError
	}
	n := notifier{on: *notify}

	pkgs, err := a.selectPackages(pos, *all)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm update:", err)
		return exitError
	}
	if len(pkgs) == 0 {
		fmt.Println("no packages selected")
		return exitOK
	}

	// Cache hygiene: drop stale .part files and week-old cached tgzs (F3).
	a.cleanCache()

	n.toast("kpm: updating…")

	// Wait for connectivity before resolving (like check), but only if any
	// selected package is actually configured to resolve (D3/F7).
	if host := firstConfiguredHost(pkgs); host != "" {
		if !device.WaitForNetwork(a.client.HTTP(), host) {
			msg := "kpm: no network — check Wi-Fi and retry"
			a.paths.WriteStatus(msg)
			a.paths.Log("UPDATE", "aborted: no network")
			n.toast(msg)
			fmt.Fprintln(os.Stderr, msg)
			return exitError
		}
	}

	// 1+2. Resolve targets and download+verify each asset.
	var targets []resolved
	failed := 0
	for _, p := range pkgs {
		r, skip, err := a.resolveAndDownload(p)
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "kpm update: %s: %v\n", p.ID, err)
			a.paths.Log("ERROR", fmt.Sprintf("%s  %v", p.ID, err))
			continue
		}
		if skip {
			continue
		}
		targets = append(targets, r)
	}

	if len(targets) == 0 {
		if err := a.state.Save(); err != nil {
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
			if err := device.Reboot(); err != nil {
				fmt.Fprintln(os.Stderr, "kpm update: reboot failed:", err)
				return exitError
			}
			return exitOK
		}
		if failed > 0 {
			// Everything selected failed and nothing was staged (F2).
			n.toast("kpm: update failed — see status")
			return exitError
		}
		fmt.Println("nothing to stage")
		return exitOK
	}

	// 3. Merge into a not-yet-live part file (hashed before it goes live).
	if err := a.guardExistingTgz(); err != nil {
		fmt.Fprintln(os.Stderr, "kpm update:", err)
		a.paths.WriteStatus("kpm: refusing to stage — " + err.Error())
		return exitError
	}
	part, sum, size, dups, err := a.mergeStaged(targets)
	if err != nil {
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
	now := time.Now().UTC().Format(state.TimeFormat)
	for _, r := range targets {
		ps := a.state.Get(r.pkg.ID)
		ps.StagedVersion = r.tag
		ps.StagedAt = now
		// Record the pending manifest separately; Manifest (installed) is only
		// updated by reconcile after the reboot install actually happens (B2).
		ps.StagedManifest = r.manifest
	}
	a.state.StagedSHA256 = sum
	a.state.StagedSize = size
	a.state.StagedCommitted = false
	if err := a.state.Save(); err != nil {
		os.Remove(part)
		fmt.Fprintln(os.Stderr, "kpm update: state:", err)
		a.paths.Log("ERROR", "state: "+err.Error())
		return exitError
	}

	// 5. Commit: move the tgz into the live boot slot, then mark committed.
	if err := a.commitStaged(part); err != nil {
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
	a.paths.WriteStatus(status)
	fmt.Print(status)
	n.toast(fmt.Sprintf("kpm: %d package(s) staged", len(targets)))

	// 5. Reboot. At least one package staged, so a reboot is worthwhile even if
	// some others failed (F2).
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

	fmt.Printf("%d package(s) staged — reboot to install\n", len(targets))
	if failed > 0 {
		return exitPartial // mixed: some staged, some failed (F2)
	}
	return exitOK
}

// firstConfiguredHost returns the forge host of the first configured package,
// or "" if none is configured (nothing to resolve, so no network wait).
func firstConfiguredHost(pkgs []*config.Package) string {
	for _, p := range pkgs {
		if p.Configured() {
			return p.Host()
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
// its asset. skip=true means the package is already up to date or already
// staged and needs no action.
func (a *App) resolveAndDownload(p *config.Package) (resolved, bool, error) {
	// Unconfigured (e.g. self-update with empty source): skip silently (F7).
	if !p.Configured() {
		return resolved{}, true, nil
	}
	// Honor the def's min_kpm: an old kpm skips a package that needs a newer one
	// (with a note) rather than staging it blindly (§9.8). install/sync gate too,
	// but a hand-edited def can still carry a min_kpm the running kpm can't meet.
	if !registry.MinKpmSatisfied(version.Version, p.MinKpm) && version.Version != "dev" {
		fmt.Printf("%s: requires kpm >= %s (running %s) — skipped; update kpm first\n", p.ID, p.MinKpm, version.Version)
		a.paths.Log("WARN", fmt.Sprintf("%s  requires kpm >= %s — skipped", p.ID, p.MinKpm))
		return resolved{}, true, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	rel, err := a.resolveRelease(ctx, p)
	cancel()
	if err != nil {
		return resolved{}, false, err
	}

	ps := a.state.Get(p.ID)
	// Only record latest_seen for unpinned packages, so a pin can't poison the
	// meaning of "latest" (F1).
	if a.effectivePin(p) == "" {
		ps.LatestSeen = rel.Tag
	}
	ps.LastChecked = time.Now().UTC().Format(state.TimeFormat)

	if tagsEqual(ps.InstalledVersion, rel.Tag) {
		return resolved{}, true, nil // up to date
	}
	if tagsEqual(ps.StagedVersion, rel.Tag) && ps.StagedVersion != "" {
		return resolved{}, true, nil // already staged, awaiting reboot
	}

	asset, err := rel.MatchAsset(p.Asset)
	if err != nil {
		return resolved{}, false, err
	}

	cachePath, manifest, err := a.download(p.ID, rel.Tag, asset)
	if err != nil {
		return resolved{}, false, err
	}
	// No package other than kpm itself may write kpm's reserved paths (its
	// install tree or NickelMenu drop-in). This makes the "kpm merged last"
	// invariant robust regardless of merge order: a package's tgz can never
	// carry a member that would clobber the kpm binary/state/registrations
	// (§7.3, A1).
	if p.ID != selfID {
		if hit := firstReservedPath(manifest); hit != "" {
			os.Remove(cachePath)
			return resolved{}, false, fmt.Errorf("refusing %s: its archive writes to kpm's reserved path %q", p.ID, hit)
		}
	}
	return resolved{pkg: p, tag: rel.Tag, asset: asset, cache: cachePath, manifest: manifest}, false, nil
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
	pin := a.effectivePin(p)
	if pin != "" {
		return f.ReleaseByTag(ctx, p.Host(), p.Owner(), p.Repo(), pin)
	}
	ps := a.state.Get(p.ID)
	if ps.LatestSeen != "" && fresh(a.state.LastCheck) {
		return f.ReleaseByTag(ctx, p.Host(), p.Owner(), p.Repo(), ps.LatestSeen)
	}
	return f.LatestRelease(ctx, p.Host(), p.Owner(), p.Repo())
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

// download streams the asset to cache/<id>-<tag>.tgz (via .part), then verifies
// gzip integrity + walks the tar, returning the captured manifest.
func (a *App) download(id, tag string, asset forge.Asset) (string, []string, error) {
	final := a.paths.CacheDir() + "/" + id + "-" + safeTag(tag) + ".tgz"
	part := final + ".part"
	f, err := os.Create(part)
	if err != nil {
		return "", nil, err
	}
	ctx := context.Background()
	_, derr := a.client.Download(ctx, asset.DownloadURL, f)
	if derr == nil {
		derr = f.Sync() // durable before the rename to the final name (B3)
	}
	cerr := f.Close()
	if derr != nil {
		os.Remove(part)
		return "", nil, fmt.Errorf("download: %w", derr)
	}
	if cerr != nil {
		os.Remove(part)
		return "", nil, cerr
	}
	if err := os.Rename(part, final); err != nil {
		return "", nil, err
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
// .part files (interrupted downloads) and any .tgz older than 7 days (F3).
// It only touches *.part / *.tgz to stay registry-compatible (REGISTRY.md §7.5).
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
		case strings.HasSuffix(name, ".tgz"):
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
	_, pos := splitArgs(args, nil)
	if len(pos) > 0 {
		fmt.Fprintln(os.Stderr, "usage: kpm unstage")
		return exitError
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err != nil {
		fmt.Println("no staged update to cancel")
		return exitOK
	}
	if !a.stagedTgzIsOurs() {
		fmt.Fprintf(os.Stderr, "kpm unstage: %s was not staged by kpm; refusing to remove it\n", a.paths.StagedTgz())
		return exitError
	}
	if err := os.Remove(a.paths.StagedTgz()); err != nil {
		fmt.Fprintln(os.Stderr, "kpm unstage:", err)
		return exitError
	}
	for id, ps := range a.state.Packages {
		if ps.StagedVersion == "" {
			continue
		}
		a.paths.Log("UNSTAGE", fmt.Sprintf("%s  %s", id, ps.StagedVersion))
		ps.StagedVersion = ""
		ps.StagedAt = ""
		ps.StagedManifest = nil
	}
	a.state.StagedSHA256 = ""
	a.state.StagedSize = 0
	a.state.StagedCommitted = false
	if err := a.state.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "kpm unstage: state:", err)
		return exitError
	}
	a.paths.WriteStatus("kpm: staging cancelled")
	fmt.Println("staging cancelled")
	return exitOK
}

// safeTag makes a tag safe for a cache filename.
func safeTag(tag string) string {
	return strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(tag)
}
