package main

// carryover.go keeps the mods across the upgrade from firmware 4.x to 6.x
// (QT6-FIRMWARE6.md section 3h).
//
// The upgrade writes a new rootfs, so every Nickel plugin is gone after it. The
// first 6.x boot installs .kobo/Kobo.tgz, but the 4.x rcS installs and deletes
// that file on the upgrade boot itself, before the upgrade runs. The 4.x rcK
// runs after the upgrade stage 1, with the user partition mounted. So:
//
//  1. On line 4, kpm keeps a bundle: one Kobo.tgz with the 6.x build of each
//     installed tgz package that has one, kpm itself last.
//  2. kpm adds a block to the 4.x rcK. During the upgrade only, the block
//     copies the bundle to .kobo/Kobo.tgz.
//  3. On line 6, kpm records the carried packages as installed with their 6.x
//     files (adoptCarryover), before the firmware-change check forgets them.

import (
	"context"
	"errors"
	"fmt"
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
)

// rcKPath is the 4.x shutdown script. Init runs it on each reboot, also on
// the reboot at the end of upgrade stage 1.
const rcKPath = "/etc/init.d/rcK"

const (
	rcKBegin = "# kpm carry-over: begin"
	rcKEnd   = "# kpm carry-over: end"
)

// rcKBlock copies the bundle to .kobo/Kobo.tgz only during the upgrade to 6.x:
// the upgrade writes the updateQt5ToQt6 marker and update.tar, and 4.x never
// uses update.tar otherwise. If stage 1 fails, the upgrade deletes update.tar,
// so the block does nothing and the device boots 4.x as before. It never
// stops rcK, and it never touches update.tar: without that file the 6.x
// recovery erases the user partition.
var rcKBlock = rcKBegin + ` (kpm manages this block, see the kpm README)
( K=/mnt/onboard/.kobo; D=/mnt/onboard/.adds/kpm/carryover
  if [ -e "$K/updateQt5ToQt6" ] && [ -f "$K/update.tar" ] && [ ! -e "$K/Kobo.tgz" ] && [ -f "$D/Kobo.tgz" ]; then
    cp "$D/Kobo.tgz" "$K/Kobo.tgz.kpm-part" && mv "$K/Kobo.tgz.kpm-part" "$K/Kobo.tgz" && sync && echo copied > "$D/rcK.done"
  fi ) >/dev/null 2>&1 || true
` + rcKEnd + "\n"

// carryoverCommands are the commands after which kpm brings the bundle up to
// date on line 4. online says if the command may use the network for it.
var carryoverCommands = map[string]bool{"check": true, "update": true, "install": true, "uninstall": false}

// carryoverAfter runs after a command of carryoverCommands. Best effort: a
// failure is a log line, never a change of the command result.
func (a *App) carryoverAfter(cmd string) {
	online, ok := carryoverCommands[cmd]
	if !ok || !a.locked || a.paths.Line() != device.Line4 {
		return
	}
	if err := a.refreshCarryover(online); err != nil {
		a.paths.Log("WARN", "carry-over: "+err.Error())
	}
}

// cmdCarryover is `kpm carryover`: it brings the bundle up to date now and
// prints what it holds.
func (a *App) cmdCarryover(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: kpm carryover")
		return exitError
	}
	if a.paths.Line() != device.Line4 {
		fmt.Println("carry-over: only on firmware 4.x")
		return exitOK
	}
	if err := a.refreshCarryover(true); err != nil {
		fmt.Fprintln(os.Stderr, "kpm carryover:", err)
		a.paths.Log("WARN", "carry-over: "+err.Error())
		return exitError
	}
	co := a.state.Carryover
	if co == nil {
		fmt.Println("carry-over: no bundle")
		return exitOK
	}
	fmt.Printf("carry-over bundle for firmware 6.x: %s (%d bytes)\n", a.paths.CarryoverBundle(), co.Size)
	for _, id := range sortedKeys(co.Packages) {
		fmt.Printf("  %s %s\n", id, co.Packages[id].Version)
	}
	if len(co.LeftOut) > 0 {
		fmt.Println("left out (these packages do not come back on firmware 6.x):")
		for _, id := range sortedKeys(co.LeftOut) {
			fmt.Printf("  %s: %s\n", id, co.LeftOut[id])
		}
	}
	if ok, err := a.rcKHasBlock(); err == nil && ok {
		fmt.Println("rcK block: present")
	} else {
		fmt.Println("rcK block: missing")
	}
	return exitOK
}

// carryoverWant returns, for each package that goes into the bundle, the
// package and the key of its 4.x install. A package goes in when it is
// installed, uses the tgz method (app-tree and file packages are on the user
// partition and stay), and has a 6.x build.
func (a *App) carryoverWant() (map[string]*config.Package, map[string]string, error) {
	pkgs, err := a.loadPackages()
	if err != nil {
		return nil, nil, err
	}
	defs := map[string]*config.Package{}
	keys := map[string]string{}
	for _, p := range pkgs {
		ps := a.state.Packages[p.ID]
		if ps == nil || ps.InstalledVersion == "" || a.carryoverSkip(p, nil) != "" {
			continue
		}
		src, tag, _ := a.fw6Tag(p)
		defs[p.ID] = p
		keys[p.ID] = strings.Join([]string{ps.InstalledVersion, src, p.ForLine(device.Line6).Asset, tag}, "|")
	}
	return defs, keys, nil
}

// The reasons of carryoverSkip that are not a loss: the package stays, or
// firmware 6.x replaces it.
const (
	skipLive    = "installs on the user partition, which the upgrade keeps"
	skipBuiltin = "built into firmware 6.x"
)

// carryoverSkip returns why an installed package does not go into the bundle,
// or "" when it goes in. entry is the registry entry of the package, or nil:
// with it, a local def without the [fw6] table that the registry def has gets
// a reason that names the fix.
func (a *App) carryoverSkip(p *config.Package, entry *registry.Entry) string {
	src, _ := a.fw6SourcePin(p)
	switch {
	case p.InstallsLive():
		return skipLive
	case p.BuiltinFw6 != "":
		return skipBuiltin
	case p.Fw6 == nil && entry != nil && entry.Def.Fw6 != nil:
		return "the local def has no [fw6] table: sync updates it"
	case p.Fw6 == nil:
		return config.Unavailable6
	case !config.SourceConfigured(src):
		return "no source for the 6.x build"
	}
	return ""
}

// carryoverLeftOut returns, for each installed package that the bundle leaves
// out and that firmware 6.x does not keep or replace, why it is left out. The
// upgrade removes these packages. A missing registry cache only loses the
// sync hint.
func (a *App) carryoverLeftOut() map[string]string {
	pkgs, err := a.loadPackages()
	if err != nil {
		return nil
	}
	entries := map[string]*registry.Entry{}
	if cfg, err := a.loadRegistryConfig(); err == nil {
		mans, _ := a.cachedManifests(cfg)
		entries, _ = registry.Merge(mans)
	}
	out := map[string]string{}
	for _, p := range pkgs {
		ps := a.state.Packages[p.ID]
		if ps == nil || ps.InstalledVersion == "" {
			continue
		}
		switch reason := a.carryoverSkip(p, entries[p.ID]); reason {
		case "", skipLive, skipBuiltin:
		default:
			out[p.ID] = reason
		}
	}
	return out
}

// noteLeftOut logs a warning for each package of now that was not left out
// for the same reason in before.
func (a *App) noteLeftOut(before, now map[string]string) {
	for _, id := range sortedKeys(now) {
		if r, ok := before[id]; ok && r == now[id] {
			continue
		}
		a.paths.Log("WARN", fmt.Sprintf("carry-over: %s is not in the bundle and does not come back on firmware 6.x: %s", id, now[id]))
	}
}

// fw6Tag returns the source of the 6.x build and the release tag to take it
// from. exact is true for a pin: then only that tag will do. Without a pin,
// the tag is the installed 4.x version, so the bundle carries the same
// version to 6.x. That works only when the 6.x build comes from the base
// source: another repository has other tags. With tag "", the bundle takes
// the latest release.
func (a *App) fw6Tag(p *config.Package) (src, tag string, exact bool) {
	src, pin := a.fw6SourcePin(p)
	if pin != "" {
		return src, pin, true
	}
	if p.Fw6 != nil && p.Fw6.Source != "" && p.Fw6.Source != a.effectiveSource(p) {
		return src, "", false
	}
	return src, a.state.Get(p.ID).InstalledVersion, false
}

// fw6SourcePin returns the source and the pin of the 6.x build. A pin and
// kpm's own source in state apply only when the [fw6] table keeps the base
// source: a pin of another repository names a tag that does not exist there.
func (a *App) fw6SourcePin(p *config.Package) (string, string) {
	if p.Fw6 != nil && p.Fw6.Source != "" {
		return p.Fw6.Source, ""
	}
	return a.effectiveSource(p), a.effectivePin(p)
}

// carryoverPlan is the work of one bundle refresh: the packages that go in
// (kpm last), their keys, the 6.x builds kept from the last bundle, and the
// ones that need a download. Each download is a progress step, and the
// bundle build is one more.
type carryoverPlan struct {
	defs    map[string]*config.Package
	keys    map[string]string
	ids     []string
	carried map[string]state.CarriedPackage
	fetch   []string
}

// planCarryover returns the work that refreshCarryover does now, or nil when
// the bundle is up to date. It reads only local files. without leaves one
// package out, as if it were not installed: uninstall plans the refresh that
// follows the removal before it removes anything. The errors are the ones of
// refreshCarryover.
func (a *App) planCarryover(online bool, without string) (*carryoverPlan, error) {
	defs, keys, err := a.carryoverWant()
	if err != nil {
		return nil, err
	}
	if without != "" {
		delete(defs, without)
		delete(keys, without)
	}
	co := a.state.Carryover
	if co != nil && sameKeys(co.From, keys) {
		if sum, size, err := sha256AndSize(a.paths.CarryoverBundle()); err == nil && sum == co.SHA256 && size == co.Size {
			return nil, nil
		}
	}
	if _, ok := defs[selfID]; !ok {
		return nil, fmt.Errorf("no bundle: kpm itself has no 6.x build to carry (enrol self-update with 'kpm adopt-self', or run 'kpm sync kpm' for its [fw6] table)")
	}
	dir := a.paths.CarryoverDir()
	ids := sortedKeys(defs)
	sort.SliceStable(ids, func(i, j int) bool { return ids[j] == selfID && ids[i] != selfID })

	// Keep the 6.x build of each package whose key did not change. The others
	// need a download.
	plan := &carryoverPlan{defs: defs, keys: keys, ids: ids, carried: map[string]state.CarriedPackage{}}
	for _, id := range ids {
		if co != nil && co.From[id] == keys[id] {
			if cp, ok := co.Packages[id]; ok {
				if sum, _, err := sha256AndSize(filepath.Join(dir, id+".tgz")); err == nil && sum == cp.SHA256 {
					plan.carried[id] = cp
					continue
				}
			}
		}
		if !online {
			return nil, fmt.Errorf("%s changed and its 6.x build needs a download; the next check or update builds the bundle", id)
		}
		plan.fetch = append(plan.fetch, id)
	}
	return plan, nil
}

// carryoverSteps returns the progress steps of the bundle refresh that
// carryoverAfter(cmd) runs, so that cmd can count them in its plan before its
// first step. without is as in planCarryover. 0 when progress is off, when
// cmd runs no refresh, or when the refresh has nothing to do or fails before
// its first step.
func (a *App) carryoverSteps(cmd, without string) int {
	online, ok := carryoverCommands[cmd]
	if a.prog == nil || !ok || !a.locked || a.paths.Line() != device.Line4 {
		return 0
	}
	plan, err := a.planCarryover(online, without)
	if err != nil || plan == nil {
		return 0
	}
	return len(plan.fetch) + 1
}

// refreshCarryover brings the bundle up to date on line 4 and makes sure the
// rcK block is there. A package whose key did not change keeps its 6.x build
// from the last bundle, so only a changed package uses the network. With
// online false, a changed package is an error and the old bundle stays. kpm
// must be in the bundle: without its hook nothing can start kpm on 6.x.
func (a *App) refreshCarryover(online bool) error {
	if err := a.ensureRcKBlock(); err != nil {
		a.paths.Log("WARN", "carry-over: rcK block: "+err.Error())
	}
	leftOut := a.carryoverLeftOut()
	var before map[string]string
	if a.state.Carryover != nil {
		before = a.state.Carryover.LeftOut
	}
	built, err := a.buildCarryover(online, leftOut)
	if built {
		a.noteLeftOut(before, leftOut)
		return err
	}
	// No new bundle: it is up to date, or the build failed. The warning
	// matters most when there is no bundle, so record a change of the
	// left-out set on the old record, or warn each time without one.
	if co := a.state.Carryover; co == nil {
		a.noteLeftOut(nil, leftOut)
	} else if !sameKeys(co.LeftOut, leftOut) {
		a.noteLeftOut(before, leftOut)
		co.LeftOut = leftOut
		if serr := a.state.Save(); err == nil {
			err = serr
		}
	}
	return err
}

// buildCarryover is the build of refreshCarryover. built is true when it
// wrote a new record, with leftOut in it.
func (a *App) buildCarryover(online bool, leftOut map[string]string) (built bool, err error) {
	plan, err := a.planCarryover(online, "")
	if err != nil || plan == nil {
		return false, err
	}
	keys, ids, carried := plan.keys, plan.ids, plan.carried
	dir := a.paths.CarryoverDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	// The command counted these steps in its plan (carryoverSteps): they are
	// the last ones.
	a.prog.Tail(len(plan.fetch) + 1)
	for _, id := range plan.fetch {
		a.prog.Step("Getting " + id + " for firmware 6")
		cp, err := a.fetchFw6(plan.defs[id], filepath.Join(dir, id+".tgz"))
		if err != nil {
			return false, fmt.Errorf("%s: %w", id, err)
		}
		carried[id] = cp
	}
	var sources []string
	for _, id := range ids {
		sources = append(sources, filepath.Join(dir, id+".tgz"))
	}
	a.prog.Step("Building the firmware 6 bundle")

	part := a.paths.CarryoverBundle() + ".kpm-part"
	dups, err := tgz.Merge(sources, part)
	if err != nil {
		os.Remove(part)
		return false, err
	}
	for _, d := range dups {
		a.paths.Log("WARN", "carry-over: more than one package writes "+d)
	}
	sum, size, err := sha256AndSize(part)
	if err != nil {
		os.Remove(part)
		return false, err
	}
	if err := os.Rename(part, a.paths.CarryoverBundle()); err != nil {
		os.Remove(part)
		return false, err
	}
	device.FsyncDir(dir)
	a.removeStaleCarried(carried)

	a.state.Carryover = &state.Carryover{
		SHA256:   sum,
		Size:     size,
		BuiltAt:  time.Now().UTC().Format(state.TimeFormat),
		From:     keys,
		Packages: carried,
		LeftOut:  leftOut,
	}
	if err := a.state.Save(); err != nil {
		return true, err
	}
	var list []string
	for _, id := range ids {
		list = append(list, id+" "+carried[id].Version)
	}
	a.paths.Log("INFO", "carry-over bundle for firmware 6.x: "+strings.Join(list, ", "))
	return true, nil
}

// fetchFw6 downloads the 6.x build of a package, checks it as a Kobo.tgz, and
// moves it to dst.
func (a *App) fetchFw6(p *config.Package, dst string) (state.CarriedPackage, error) {
	lp := p.ForLine(device.Line6)
	rel, asset, err := a.fw6Release(p, &lp)
	if err != nil {
		return state.CarriedPackage{}, err
	}
	wantSHA, _ := lp.ChecksumFor(rel.Tag)
	cache, manifest, err := a.downloadFor(device.Line6, p.ID, rel.Tag, wantSHA, lp.ChecksumDeclaredFor(rel.Tag), len(lp.Checksums) > 0, asset)
	if err != nil {
		return state.CarriedPackage{}, err
	}
	if err := os.Rename(cache, dst); err != nil {
		os.Remove(cache)
		return state.CarriedPackage{}, err
	}
	sum, _, err := sha256AndSize(dst)
	if err != nil {
		return state.CarriedPackage{}, err
	}
	return state.CarriedPackage{Version: rel.Tag, Fingerprint: rel.Fingerprint(&asset), SHA256: sum, Manifest: manifest}, nil
}

// fw6Release finds the release and the asset of the 6.x build (fw6Tag). When
// the release of the installed version is not there, or has no 6.x build, it
// takes the latest release and logs a warning. A network failure is an error:
// the next refresh tries again.
func (a *App) fw6Release(p *config.Package, lp *config.Package) (forge.Release, forge.Asset, error) {
	src, tag, exact := a.fw6Tag(p)
	f, err := a.forgeFor(lp)
	if err != nil {
		return forge.Release{}, forge.Asset{}, err
	}
	host, owner, repo := config.SourceHost(src), config.SourceOwner(src), config.SourceRepo(src)
	get := func(tag string) (forge.Release, forge.Asset, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var rel forge.Release
		var err error
		if tag != "" {
			rel, err = f.ReleaseByTag(ctx, host, owner, repo, tag)
		} else {
			rel, err = f.LatestRelease(ctx, host, owner, repo)
		}
		if err != nil {
			return forge.Release{}, forge.Asset{}, err
		}
		if rel.Tag == "" {
			return forge.Release{}, forge.Asset{}, fmt.Errorf("forge returned a release with an empty tag")
		}
		asset, err := rel.MatchAsset(lp.Asset)
		return rel, asset, err
	}
	if exact || tag == "" {
		return get(tag)
	}
	// The installed version, as recorded or with the other "v" form: kpm
	// records its own version as 0.16.0, and its release tag is v0.16.0.
	for _, t := range tagForms(tag) {
		rel, asset, err := get(t)
		if err == nil {
			return rel, asset, nil
		}
		if errors.Is(err, forge.ErrNotFound) {
			continue
		}
		if rel.Tag != "" {
			break // the release is there, but it has no 6.x build
		}
		return forge.Release{}, forge.Asset{}, err
	}
	rel, asset, err := get("")
	if err != nil {
		return forge.Release{}, forge.Asset{}, err
	}
	a.paths.Log("WARN", fmt.Sprintf("carry-over: %s %s has no 6.x build, so the bundle carries %s", p.ID, tag, rel.Tag))
	return rel, asset, nil
}

// tagForms returns the tag, and then the same tag with the "v" prefix added
// or removed.
func tagForms(tag string) []string {
	if strings.HasPrefix(tag, "v") {
		return []string{tag, strings.TrimPrefix(tag, "v")}
	}
	return []string{tag, "v" + tag}
}

// removeStaleCarried deletes the 6.x build of each package that left the bundle.
func (a *App) removeStaleCarried(keep map[string]state.CarriedPackage) {
	entries, err := os.ReadDir(a.paths.CarryoverDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		id := strings.TrimSuffix(name, ".tgz")
		if e.IsDir() || name == "Kobo.tgz" || id == name {
			continue
		}
		if _, ok := keep[id]; !ok {
			os.Remove(filepath.Join(a.paths.CarryoverDir(), name))
		}
	}
}

// ensureRcKBlock adds the carry-over block to the 4.x rcK, or replaces an old
// version of it. The block goes right after the "#!" line, before rcK
// unmounts the user partition. A missing rcK (not a 4.x rootfs) is not an
// error. A 4.x firmware update can replace rcK, so each refresh checks it.
func (a *App) ensureRcKBlock() error {
	path := a.paths.HostPath(rcKPath)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(b)
	if strings.Contains(text, rcKBlock) {
		return nil
	}
	text = stripRcKBlock(text)
	if strings.HasPrefix(text, "#!") {
		nl := strings.IndexByte(text, '\n')
		if nl < 0 {
			text += "\n"
			nl = len(text) - 1
		}
		text = text[:nl+1] + rcKBlock + text[nl+1:]
	} else {
		text = rcKBlock + text
	}
	if err := writeKeepMode(path, []byte(text), info.Mode().Perm()); err != nil {
		return err
	}
	a.paths.Log("INFO", "carry-over: added the block to "+rcKPath)
	return nil
}

// rcKHasBlock reports whether the rcK has the current block.
func (a *App) rcKHasBlock() (bool, error) {
	b, err := os.ReadFile(a.paths.HostPath(rcKPath))
	if err != nil {
		return false, err
	}
	return strings.Contains(string(b), rcKBlock), nil
}

// stripRcKBlock removes each carry-over block (any version) from the text.
func stripRcKBlock(text string) string {
	for {
		i := strings.Index(text, rcKBegin)
		if i < 0 {
			return text
		}
		j := strings.Index(text[i:], rcKEnd)
		if j < 0 {
			return text
		}
		end := i + j + len(rcKEnd)
		if end < len(text) && text[end] == '\n' {
			end++
		}
		text = text[:i] + text[end:]
	}
}

// writeKeepMode writes a file through a temporary file and a rename, with the
// mode set before the rename. Init runs rcK directly, so it must never lose
// its execute bits, not even after a crash between two steps.
func writeKeepMode(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".kpm-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	device.FsyncDir(dir)
	return nil
}

// adoptCarryover runs on line 6. When the first 6.x boot installed the bundle,
// it records each carried package as installed with its 6.x files, and then
// drops the record and the bundle folder. It waits while the bundle is still
// in .kobo (the firmware did not install it yet). A package whose 6.x files
// are not all there is left to the firmware-change check. It returns true when
// the state changed.
func (a *App) adoptCarryover() bool {
	co := a.state.Carryover
	if co == nil || a.paths.Line() != device.Line6 {
		return false
	}
	pending := filepath.Join(a.paths.Root, ".kobo", "Kobo.tgz")
	if sum, _, err := sha256AndSize(pending); err == nil && sum == co.SHA256 {
		return false
	}
	now := time.Now().UTC().Format(state.TimeFormat)
	for _, id := range sortedKeys(co.Packages) {
		cp := co.Packages[id]
		// A package uninstalled since the bundle was built has no state entry.
		// An entry with no installed version is one that the firmware-change
		// check forgot while the bundle still waited in .kobo: adopt it too.
		ps := a.state.Packages[id]
		if ps == nil {
			continue
		}
		if missing := verifyManifest(a.paths, cp.Manifest); len(missing) > 0 {
			a.paths.Log("WARN", fmt.Sprintf("%s  not carried over to firmware 6.x: %d of its files are missing", id, len(missing)))
			continue
		}
		ps.InstalledVersion = cp.Version
		ps.InstalledAt = now
		ps.InstalledFingerprint = cp.Fingerprint
		ps.Manifest = append([]string(nil), cp.Manifest...)
		ps.MissingFiles = nil
		ps.VerifiedAt = now
		a.paths.Log("INFO", fmt.Sprintf("%s  carried over to firmware 6.x: %s", id, cp.Version))
	}
	a.state.Carryover = nil
	os.RemoveAll(a.paths.CarryoverDir())
	return true
}

// sameKeys reports whether two id->key maps are equal.
func sameKeys(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// sortedKeys returns the keys of a map in order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
