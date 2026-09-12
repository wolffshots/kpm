package main

// apptree.go is the cmd-layer half of app-tree installs (APP-TREE.md §6/§8/§9):
// extract the tree, prune what the previous version installed and this one does
// not, register a NickelMenu launcher, and record the result — all WITHOUT
// touching the staging slot, because an app-tree package is installed the moment
// this returns and never waits for a reboot.

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"kpm/internal/apptree"
	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/modconfig"
	"kpm/internal/registry"
	"kpm/internal/state"
	"kpm/internal/uninstall"
)

// resolvedApp is `resolved`'s app-tree sibling: a package with a downloaded zip
// and a fully-validated extraction plan. The plan is built during resolve, so a
// hostile or broken archive fails the package before anything is written or
// staged — exactly where a bad tgz fails today.
type resolvedApp struct {
	pkg    *config.Package
	tag    string
	cache  string // downloaded+verified zip path
	sha256 string // the digest fetchAsset computed for THAT file, re-checked before Apply
	plan   *apptree.Plan
	// fingerprint mirrors resolved.fingerprint: recorded for every package, made
	// inert for a non-rolling def by the gates in rebuildDetected.
	fingerprint string
}

// installAppTrees extracts, prunes, registers and records each app-tree target,
// one package at a time with a save after each, so a failure on package 3 keeps
// packages 1-2 recorded. It returns the ids that changed and the per-package
// failures for the JSON payload.
//
// It runs BEFORE the tgz merge (§6.4) for two reasons: a merge failure must not
// strand an app-tree package that was going to succeed, and the free-space check
// must run before ~40 MiB of merged tgz lands on the same partition.
//
// partial reports that at least one package installed but could not be fully
// tidied up — a stale file the prune could not remove. The new files ARE
// installed, so the version is still recorded and the package still counts as
// changed; only the exit code softens to exitPartial (§9.2).
func (a *App) installAppTrees(targets []resolvedApp, forceLauncher bool) (changed []string, failures []jsonFailure, partial bool) {
	for _, r := range targets {
		incomplete, err := a.installAppTree(r, forceLauncher)
		if err != nil {
			failures = append(failures, jsonFailure{ID: r.pkg.ID, Error: err.Error()})
			fmt.Fprintf(os.Stderr, "kpm update: %s: %v\n", r.pkg.ID, err)
			a.paths.Log("ERROR", fmt.Sprintf("%s  %v", r.pkg.ID, err))
			continue
		}
		partial = partial || incomplete
		changed = append(changed, r.pkg.ID)
	}
	return changed, failures, partial
}

// installAppTree is one package's install. The ORDER is load-bearing and is
// argued in APP-TREE.md §5.3: extract everything first (a crash leaves a mixture
// of old and new WHOLE files), prune the stale set second (so a crash can never
// leave files deleted but not replaced), record state last (so a crash before it
// makes the next `kpm update` re-run the whole thing against the still-correct
// old manifest, converging exactly).
func (a *App) installAppTree(r resolvedApp, forceLauncher bool) (partial bool, err error) {
	ps := a.state.Get(r.pkg.ID)
	old := ps.Manifest

	// Report entries the def's `root` excluded, so a silent drop is impossible to
	// miss in kpm.log (§5.1 rule 5).
	if n := len(r.plan.Ignored); n > 0 {
		a.paths.Log("WARN", fmt.Sprintf("%s  %d zip entr(ies) outside root %q not extracted: %s",
			r.pkg.ID, n, r.pkg.Install.Root, firstFew(r.plan.Ignored, 5)))
	}

	// The plan was computed against these bytes at DOWNLOAD time; Apply re-opens
	// the file and reads content by zip index, which can be minutes later — an
	// `update --all` resolves and downloads every package before the first
	// extraction, and the cache sits at a predictable path on a partition the user
	// mounts over USB. The paths are safe either way (they come from the plan),
	// but the CONTENT is not: a substituted koreader.sh is root code execution
	// through the NickelMenu launcher. Re-hash immediately before the read.
	if err := verifyCachedAsset(r.cache, r.sha256); err != nil {
		return false, err
	}

	// Per-file ownership inside a shared content dir (D4). It runs HERE and not at
	// resolve time: `update --all` resolves every package before it installs any,
	// so two font packages shipping the same file would both pass a resolve-time
	// check and the second would still overwrite the first.
	if err := a.contentCollision(r.pkg, r.plan, old); err != nil {
		return false, err
	}

	res, err := apptree.Apply(a.paths, r.plan, r.cache)
	if err != nil {
		// Extraction failed partway, so real files are on disk. Record what was
		// written before returning: with nothing recorded, `kpm list` reported the
		// package as not installed AND `kpm uninstall` refused it for having no
		// manifest — the tree was neither nameable nor removable. The union with
		// the previous manifest is the stale-but-correct state this package's doc
		// argues for; installed_version is deliberately NOT advanced, so the next
		// update still re-runs the whole extraction and converges (§5.3).
		a.recordPartial(r.pkg.ID, r.plan.Dest, ps, old, res)
		return false, err
	}
	members := res.Manifest

	// The launcher is written after the tree (so a failed extraction never leaves
	// a menu entry pointing at nothing) and recorded in the manifest, so
	// `kpm uninstall` removes it.
	if nm, err := a.writeLauncher(r.pkg, forceLauncher); err != nil {
		a.paths.Log("WARN", fmt.Sprintf("%s  launcher: %v", r.pkg.ID, err))
	} else if nm != "" {
		members = append(members, strings.TrimPrefix(nm, "/"))
	}

	// Stale-file prune: exactly the members the previous install placed that this
	// one does not. kpm deletes only files it previously installed; everything
	// else inside dest is user data by definition and is never touched (§9.1).
	if stale := missingFrom(old, members); len(stale) > 0 {
		// A stale-file prune IS an uninstall of the removed subset, so it reuses
		// internal/uninstall rather than reimplementing deletion: path policy on
		// every candidate, the symlinked-parent guard, ".." rejection,
		// already-absent tolerance and deepest-first rmdir all come for free.
		plan, perr := uninstall.Compute(stale, config.Uninstall{Method: config.MethodManifest}, false)
		if perr != nil {
			a.paths.Log("WARN", fmt.Sprintf("%s  stale-file prune skipped: %v", r.pkg.ID, perr))
		} else {
			pres := uninstall.Execute(a.paths, plan)
			for _, f := range pres.Failed {
				a.paths.Log("WARN", fmt.Sprintf("%s  could not remove stale %s: %v", r.pkg.ID, f.Path, f.Err))
				partial = true
			}
			for _, s := range pres.Skipped {
				a.paths.Log("WARN", fmt.Sprintf("%s  stale %s skipped: %s", r.pkg.ID, s.Path, s.Reason))
				partial = true
			}
			if n := len(pres.Deleted) + len(pres.Removed); n > 0 {
				a.paths.Log("PRUNE", fmt.Sprintf("%s  removed %d stale path(s)", r.pkg.ID, n))
			}
		}
	}

	// Record LAST. An app-tree package never sets StagedVersion, which is what
	// makes it invisible to reconcile with no change to internal/state at all.
	now := time.Now().UTC().Format(state.TimeFormat)
	prev := ps.InstalledVersion
	ps.InstalledVersion = r.tag
	ps.InstalledAt = now
	ps.Manifest = members
	ps.InstalledFingerprint = r.fingerprint
	// Post-install verification, the same check reconcile runs after a staged
	// install: for a freshly-extracted tree MissingFiles should always be empty,
	// and if it is not, something deleted files between the write and the check —
	// which is exactly what the hook's badge is for (§9.4).
	ps.VerifiedAt = now
	ps.MissingFiles = verifyManifest(a.paths, members)
	if err := a.state.Save(); err != nil {
		return partial, fmt.Errorf("state: %w", err)
	}
	a.paths.Log("INSTALLED", fmt.Sprintf("%s  %s -> %s", r.pkg.ID, dash(prev), r.tag))
	if len(ps.MissingFiles) > 0 {
		a.paths.Log("WARN", fmt.Sprintf("files missing after install: %s  missing: %s",
			r.pkg.ID, strings.Join(ps.MissingFiles, ", ")))
	}
	if partial {
		a.paths.Log("WARN", fmt.Sprintf("%s  installed, but some stale files could not be removed", r.pkg.ID))
	}
	os.Remove(r.cache)
	return partial, nil
}

// contentCollision refuses the first write that would touch a file this package
// does not own, and returns nil when every write is safe or the dest is not a
// content dir (D4).
//
// InstallDest refuses a shared root because an exact uninstall is impossible once
// packages interleave in one directory. A content dir is the exception, and THIS
// is what buys the exception back: kpm never writes a file it does not own, so
// the prune and the uninstall — which already delete only manifest members —
// cannot reach a sibling package's font or the user's own.
//
// Nothing is written when this fires. The package fails on its own; the rest of
// an `update --all` proceeds.
func (a *App) contentCollision(p *config.Package, plan *apptree.Plan, old []string) error {
	if _, ok := uninstall.ContentDirFor(p.Install); !ok {
		return nil
	}
	owner := map[string]string{}
	// Range the map directly. state.Get INSERTS an entry for an unknown id, which
	// would register every package named here as locally known.
	for other, ps := range a.state.Packages {
		if other == p.ID || ps == nil {
			continue
		}
		for _, m := range ps.Manifest {
			owner[foldMember(m)] = other
		}
	}
	mine := map[string]bool{}
	for _, m := range old {
		mine[foldMember(m)] = true
	}
	for _, e := range plan.Entries {
		k := foldMember(strings.TrimPrefix(e.DevAbs, "/"))
		if o, ok := owner[k]; ok {
			// Owner-first is the safer order: it holds even when this package
			// installed the file too, so kpm never silently takes a file from
			// another package. That makes the message the only way out, so it has
			// to name one.
			return fmt.Errorf("%s is installed by %s. Uninstall %s to release the file, then retry", e.DevAbs, o, o)
		}
		// A file this package placed last time is its own to replace. That is also
		// what makes a crashed install resumable: recordPartial recorded every path
		// the failed extraction wrote, so the retry finds them in `old`.
		if mine[k] {
			continue
		}
		if _, err := os.Lstat(a.paths.HostPath(e.DevAbs)); err == nil {
			return fmt.Errorf("%s already exists and kpm did not install it. "+
				"Move it off the device, then retry", e.DevAbs)
		}
	}
	return nil
}

// rebootToApply reports whether a change to p takes effect only after a reboot:
// every staged tgz, and every app-tree install into a content dir (D6). A font is
// written immediately but Nickel keeps rendering the old one until it restarts,
// and a missing prompt is a SILENT failure — the user sees the old font and
// concludes kpm did nothing.
func rebootToApply(p *config.Package) bool {
	if !p.IsAppTree() {
		return true
	}
	_, ok := uninstall.ContentDirFor(p.Install)
	return ok
}

// contentRebootNeeded reports whether any package this run actually installed
// needs a reboot before it is used. A package that failed changed nothing, so it
// is excluded.
func contentRebootNeeded(targets []resolvedApp, changed []string) bool {
	done := make(map[string]bool, len(changed))
	for _, id := range changed {
		done[id] = true
	}
	for _, r := range targets {
		if done[r.pkg.ID] && rebootToApply(r.pkg) {
			return true
		}
	}
	return false
}

// verifyCachedAsset re-hashes a cached download and compares it with the digest
// recorded when it was fetched. An empty want means nothing was recorded (no
// caller does that today) and the check is skipped rather than becoming a
// spurious failure.
func verifyCachedAsset(path, want string) error {
	if want == "" {
		return nil
	}
	sum, _, err := sha256AndSize(path)
	if err != nil {
		return fmt.Errorf("re-verify %s: %w", path, err)
	}
	if !strings.EqualFold(sum, want) {
		os.Remove(path)
		return fmt.Errorf("cached asset %s changed after it was verified (sha256 %s, expected %s) — refusing to install", path, sum, want)
	}
	return nil
}

// recordPartial writes the manifest of a FAILED extraction: everything the
// previous install recorded, plus every path this one actually created. Both
// halves matter — the old members are still on disk (a failure leaves a mixture
// of old and new whole files) and the new ones would otherwise be untracked.
//
// Nothing else about the package's state is touched: no version, no timestamp,
// no verification stamp. This says "these files exist", which is true, and
// claims nothing about the install having succeeded, which it did not. A save
// failure here is swallowed to a log line: the caller is already returning the
// extraction error, and replacing it with a state error would hide the cause.
// dest is excluded for the same reason Plan.Manifest() excludes it: a member
// equal to dest would swallow the package's own recursive purge_paths entry.
func (a *App) recordPartial(id, dest string, ps *state.PackageState, old []string, res apptree.Result) {
	if len(res.Written) == 0 && len(res.Dirs) == 0 {
		return
	}
	have := map[string]bool{}
	members := make([]string, 0, len(old)+len(res.Dirs)+len(res.Written))
	add := func(m string) {
		if m = strings.TrimPrefix(m, "/"); m != "" && !have[foldMember(m)] {
			have[foldMember(m)] = true
			members = append(members, m)
		}
	}
	for _, m := range old {
		add(m)
	}
	for _, d := range res.Dirs {
		if d != dest {
			add(d)
		}
	}
	for _, w := range res.Written {
		add(w)
	}
	ps.Manifest = members
	if err := a.state.Save(); err != nil {
		a.paths.Log("WARN", fmt.Sprintf("%s  could not record the partial manifest: %v", id, err))
		return
	}
	a.paths.Log("WARN", fmt.Sprintf("%s  install failed partway; recorded %d path(s) so they can be listed and removed", id, len(members)))
}

// missingFrom returns the members of old that are absent from want, preserving
// old's order. Comparison folds case for onboard paths, because the FAT32
// partition does: a member that only changed case has NOT gone away, and deleting
// it would delete the file the new version just wrote.
func missingFrom(old, want []string) []string {
	have := make(map[string]bool, len(want))
	for _, m := range want {
		have[foldMember(m)] = true
	}
	var out []string
	for _, m := range old {
		if !have[foldMember(m)] {
			out = append(out, m)
		}
	}
	return out
}

// foldMember normalizes a tar-style member for comparison, folding case for the
// ones that live on the case-insensitive onboard partition.
//
// The fold rule is the policy engine's, not a second copy of it. It folds ASCII
// and nothing else, which is what Linux vfat does through its NLS table -- and on
// a utf8 mount non-ASCII is not folded at all.
//
// Both fold directions carry a risk here, so the tie is broken by matching the
// policy layer rather than by arguing the risks. Under-folding can miss an owner
// in the owner map; over-folding can make a folded collision look like this
// package's OWN previous file in the mine map, skipping the on-disk check and
// permitting exactly the overwrite that check exists to prevent. Two layers
// normalizing one path differently is what produced the aliasing bug this file
// already guards against; one rule for the whole codebase closes that class.
func foldMember(m string) string {
	// Tar-style ("mnt/onboard/x") is the recorded convention, but a hand-edited
	// state.json or a future writer can spell a member "/mnt/onboard/x". Keyed
	// as-is, those two never matched: the ownership check missed the real owner and
	// told the user to delete a file another package installs, and missingFrom
	// pruned a file the new version had just written.
	m = strings.TrimPrefix(m, "/")
	return strings.TrimPrefix(uninstall.FoldPath("/"+m), "/")
}

// firstFew renders at most n items, with an ellipsis when there are more.
func firstFew(ss []string, n int) string {
	if len(ss) <= n {
		return strings.Join(ss, ", ")
	}
	return strings.Join(ss[:n], ", ") + ", …"
}

// ---- launcher registration (§8.2) ---------------------------------------

// writeLauncher writes the package's declared NickelMenu drop-in and returns its
// device path (or "" when the def declares none). It is deliberately NOT an error
// for NickelMenu to be absent: the drop-in is inert without it and becomes live
// the moment NickelMenu is installed.
//
// An EXISTING file is never overwritten unless force is set. A user may have
// customised their entry — a different menu location, a chained Wi-Fi action —
// and silently replacing that on every update would be its own bug.
func (a *App) writeLauncher(p *config.Package, force bool) (string, error) {
	if p.Install == nil || p.Install.Launcher == nil {
		return "", nil
	}
	l := p.Install.Launcher
	// Re-validate immediately before writing (defense in depth: packages.d is
	// user-editable), through the same policy engine ParseManifest used.
	if err := uninstall.ValidateInstallDecl(p.Install); err != nil {
		return "", err
	}
	abs, err := uninstall.LauncherPath(l.NmFile)
	if err != nil {
		return "", err
	}
	if err := offDeviceWriteGuard(); err != nil {
		return "", err
	}
	host := a.paths.HostPath(abs)
	if _, err := os.Stat(host); err == nil && !force {
		a.paths.Log("INFO", fmt.Sprintf("%s  nm entry already present, left unchanged", p.ID))
		fmt.Printf("%s: nm entry already present, left unchanged\n", p.ID)
		return abs, nil
	}
	// Line endings are normalised to LF with exactly one trailing newline: a CRLF
	// NickelMenu config is a known way to get an entry silently ignored. This is
	// the same normaliser `kpm config init` seeds template content through.
	body, err := modconfig.SeedContent(config.ModConfig{Template: l.NmEntry})
	if err != nil {
		return "", err
	}
	// Never write through a symlinked parent (C7) — the same guard config set and
	// uninstall apply.
	if bad, ok := uninstall.SymlinkedConfigParent(a.paths, abs); ok {
		return "", fmt.Errorf("refusing to write %s: symlinked parent %s", abs, bad)
	}
	if err := device.WriteFileAtomic(host, body); err != nil {
		return "", err
	}
	a.paths.Log("LAUNCHER", fmt.Sprintf("%s  %s", p.ID, abs))
	return abs, nil
}

// offDeviceWriteGuard is the KPM_SYSROOT guard cmd_config.go's write paths apply:
// writes resolve against KPM_SYSROOT, so a KPM_ROOT sandbox without KPM_SYSROOT
// would land on the real rootfs. On a real device both are unset (G5).
func offDeviceWriteGuard() error {
	if os.Getenv("KPM_SYSROOT") != "" {
		return nil
	}
	if os.Getenv("KPM_ROOT") != "" {
		return fmt.Errorf("KPM_ROOT is set but KPM_SYSROOT is not — refusing to write against the real rootfs; set KPM_SYSROOT")
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("set KPM_SYSROOT when running off-device")
	}
	return nil
}

// nickelMenuPresent reports whether NickelMenu is on the device, by either
// signal: its actual payload on the rootfs, or a kpm-tracked install (§8.2).
func (a *App) nickelMenuPresent() bool {
	if _, err := os.Stat(a.paths.HostPath(device.NickelMenuSO)); err == nil {
		return true
	}
	return a.state.Get("nickelmenu").InstalledVersion != ""
}

// launcherWarnings returns the advisory lines for the app-tree packages in this
// run: NickelMenu missing (so the package is installed but not launchable yet)
// and, on firmware >= 4.17, the ExcludeSyncFolders library-clutter note.
//
// Both are WARNINGS, never failures. NickelMenu is a genuine dependency that
// 0.12.0 will express with `requires`; until then the honest interim degrades to
// "installed but not launchable yet", not to a broken device.
func (a *App) launcherWarnings(pkgs []*config.Package) []string {
	var out []string
	nm := a.nickelMenuPresent()
	deviceFw, _ := a.paths.Firmware()
	noteFw := ""
	for _, p := range pkgs {
		if !p.IsAppTree() {
			continue
		}
		if !nm && p.Install != nil && p.Install.Launcher != nil {
			out = append(out, fmt.Sprintf(
				"%s: NickelMenu not detected — install it (\"kpm install nickelmenu\") to get a launch entry", p.ID))
		}
		if noteFw == "" && excludeSyncVerdict(a.paths) != syncExcludeOK &&
			registry.FirmwareAtLeast(deviceFw, 4, 17) {
			noteFw = p.ID
		}
	}
	if noteFw != "" {
		out = append(out, fmt.Sprintf(
			"%s: firmware %s indexes hidden folders — see \"kpm doctor\" to stop this app appearing in your library",
			noteFw, deviceFw))
	}
	return out
}

// ---- ExcludeSyncFolders: detect and tell, never write (§10) --------------

// The verdicts for the ExcludeSyncFolders key in Kobo eReader.conf.
const (
	syncExcludeOK      = "ok"      // present and covers nested hidden directories
	syncExcludeWeak    = "weak"    // present but does not cover them
	syncExcludeAbsent  = "absent"  // key or [FeatureSettings] not present
	syncExcludeUnknown = "unknown" // conf unreadable — silence is the safe default
)

// nestedHiddenAlternative is the part of the recommended value that actually
// covers a hidden directory NESTED under a visible one — i.e. .adds/koreader.
// A value without it excludes only top-level hidden folders.
const nestedHiddenAlternative = `[^/]*/)+`

// excludeSyncVerdict classifies the device's ExcludeSyncFolders setting. It is
// READ-ONLY, and it must stay that way: Nickel owns this file and rewrites it,
// kpm's UI runs inside a live Nickel, and the documented recovery for a botched
// Kobo eReader.conf is a factory reset (§10.2). Detect and tell; never write.
func excludeSyncVerdict(p device.Paths) string {
	raw, err := os.ReadFile(p.NickelConf())
	if err != nil {
		return syncExcludeUnknown
	}
	decl := config.ModConfig{Name: "Kobo eReader.conf", Path: "/", Format: config.FormatINI}
	entries, err := modconfig.List(raw, decl)
	if err != nil {
		return syncExcludeUnknown
	}
	for _, e := range entries {
		if !strings.EqualFold(e.Section, "FeatureSettings") || !strings.EqualFold(e.Key, "ExcludeSyncFolders") {
			continue
		}
		if strings.Contains(e.Value, nestedHiddenAlternative) {
			return syncExcludeOK
		}
		return syncExcludeWeak
	}
	return syncExcludeAbsent
}

// excludeSyncAdvice is the full doctor block for a package whose files Nickel
// would otherwise index into the library. It tells the user exactly what to add
// AND why kpm does not add it — without that last sentence the advice reads like
// a missing feature.
func excludeSyncAdvice(id, deviceFw string) string {
	return fmt.Sprintf(`%s: firmware %s indexes hidden folders, so this app's files may
          appear in your library. To stop that, edit .kobo/Kobo/Kobo eReader.conf
          over USB (with the Kobo idle, not from this menu) and add:

            [FeatureSettings]
            ExcludeSyncFolders=(\\.(?!kobo|adobe).+|([^.][^/]*/)+\\..+)

          kpm does not make this edit: Nickel rewrites this file and would
          revert it.`, id, deviceFw)
}

// ---- KFMon (§8.1) --------------------------------------------------------

// kfmonIcons maps an app-tree package id to the launcher icon KFMon's shipped
// config watches for it. kpm cannot place these: both live at the root of the
// user partition, which is outside the writable allowlist (.adds/.kobo only), so
// a KFMon user copies them over USB themselves. NickelMenu users need neither.
var kfmonIcons = map[string]string{
	"koreader": "/mnt/onboard/koreader.png",
	"plato":    "/mnt/onboard/icons/plato.png",
}

// kfmonAdvice returns the advisory line for an app-tree package when KFMon is
// installed but the icon it watches is missing, or "" when there is nothing to
// say. Never an error, never a non-zero exit — advisory only, exactly like the
// firmware-untested note.
func kfmonAdvice(p device.Paths, id string) string {
	icon, known := kfmonIcons[id]
	if !known {
		return ""
	}
	if _, err := os.Stat(p.HostPath(device.KfmonBin)); err != nil {
		return "" // no KFMon: nothing to say
	}
	if _, err := os.Stat(p.HostPath(icon)); err == nil {
		return ""
	}
	return fmt.Sprintf(`%s: KFMon is installed but %s is missing.
          Copy it from the release zip to the root of your Kobo over USB.
          (kpm cannot write the partition root — only .adds and .kobo.)`, id, icon)
}

// appTreePkgs lifts the package defs out of a run's app-tree targets.
func appTreePkgs(targets []resolvedApp) []*config.Package {
	out := make([]*config.Package, 0, len(targets))
	for _, r := range targets {
		out = append(out, r.pkg)
	}
	return out
}

// appTreeStatusNote renders the app-tree advisories as trailing status-file lines
// (§8.2/§10.3). status.txt is what a user without NickelDBus reads after an
// update, so a warning that only reached kpm.log would be invisible to them.
func appTreeStatusNote(warnings []string) string {
	if len(warnings) == 0 {
		return ""
	}
	return strings.Join(warnings, "\n") + "\n"
}

// sortedUnion merges two id lists into one sorted, deduplicated slice — the
// `changed` array of a mixed run's JSON payload (§6.4).
func sortedUnion(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
