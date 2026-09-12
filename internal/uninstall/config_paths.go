package uninstall

// config_paths.go exposes the path-safety engine (cleanDeviceAbs, classify,
// symlinkedParent) to the config editor (internal/modconfig, cmd/kpm) as thin
// exported wrappers. Declared config paths get the identical treatment an
// uninstall marker_file gets (CONFIG.md §5 / UNINSTALL.md §4): they must clean
// without ".." traversal AND classify vAllowed, so a malicious or fat-fingered
// registry can never point the editor at /etc/shadow or a firmware tree.

import (
	"fmt"
	"strings"

	"kpm/internal/config"
	"kpm/internal/device"
)

// CleanDeviceAbs normalizes a raw (tar-style or absolute) device path into a
// cleaned absolute device path, rejecting ".." traversal, WITHOUT applying the
// deletable-allowlist policy. Post-install manifest verification (A) uses it to
// map each manifest member to a host path for an existence check: members land
// anywhere the tarball placed them (firmware-adjacent imageformats, /opt, …), so
// classify would wrongly reject legitimate members — only the traversal guard
// applies here.
func CleanDeviceAbs(raw string) (string, error) { return cleanDeviceAbs(raw) }

// ConfigPath validates a declared config path against the uninstall path policy
// and returns the cleaned absolute device path. It is called both when a def is
// parsed/synced and immediately before every read/write (defense in depth — the
// local packages.d snapshot is user-editable).
func ConfigPath(raw string) (string, error) {
	abs, err := cleanDeviceAbs(raw)
	if err != nil {
		return "", err
	}
	if classify(abs, nil) != vAllowed {
		return "", fmt.Errorf("config path %q is not within a writable location "+
			"(.adds/.kobo on the user partition; /usr/local, /opt on rootfs)", raw)
	}
	if underContentDir(abs) {
		return "", fmt.Errorf("config path %q is inside a content directory, which holds only content", raw)
	}
	return abs, nil
}

// ValidateConfigDecls checks a package's config declarations end to end: format,
// required fields, and name uniqueness (config.ValidateConfigs) plus the path
// policy for every entry. ParseManifest drops a registry def whose configs fail
// this, exactly as it drops an invalid package id (CONFIG.md §2).
func ValidateConfigDecls(cfgs []config.ModConfig) error {
	if err := config.ValidateConfigs(cfgs); err != nil {
		return err
	}
	for _, c := range cfgs {
		if _, err := ConfigPath(c.Path); err != nil {
			return err
		}
	}
	return nil
}

// SymlinkedConfigParent reports the deepest intermediate directory that is a
// symlink on the way to dev, if any (C7). A config write through a symlinked
// parent would resolve outside the allowlisted tree, so the editor refuses it —
// the same guard Execute applies to deletions. No per-package allowlist extension.
func SymlinkedConfigParent(p device.Paths, dev string) (string, bool) {
	return symlinkedParent(p, dev, nil)
}

// ---- app-tree installs (APP-TREE.md §2.4 / §5) --------------------------
//
// The extractor writes bulk package payload as root, so every path it touches
// goes through THIS engine — the same classify() that guards deletions — rather
// than through a second set of rules that could drift out of step with it.

// nmDir is the shared NickelMenu drop-in directory, and nmKpmFile is kpm's own
// drop-in inside it (device.Paths.NmConfig, expressed device-absolute so the
// policy engine can compare it without importing device's Root()). No package
// but kpm may declare either as a launcher target.
var (
	nmDir     = "/mnt/onboard/.adds/nm"
	nmKpmFile = "/mnt/onboard/.adds/nm/kpm"
)

// FoldPath returns the comparison key for a cleaned absolute device path: ASCII
// case-folded at or under the FAT32 onboard partition (C2), unchanged on the
// case-sensitive rootfs. Any layer that keys a map by device path must fold
// through THIS function, so every layer agrees with the policy engine about which
// spellings are one file on the device.
func FoldPath(abs string) string { return foldPath(abs) }

// PathUnder reports whether a cleaned absolute device path is at or below prefix,
// honoring the FAT32 case rule (C2). It is the containment predicate the app-tree
// extractor re-asserts after composing each destination path (§5.1 rule 7).
func PathUnder(abs, prefix string) bool { return under(abs, prefix) }

// InstallDest validates a declared app-tree destination against the uninstall
// path policy and returns the cleaned absolute device path. It additionally
// refuses a shared allowlist root: extracting a tree onto /mnt/onboard/.adds or
// /usr/local would interleave one package's files with every other package's and
// make an exact uninstall impossible (APP-TREE.md §2.2/§6).
//
// The NickelMenu drop-in directory is refused for the same reason and one worse:
// it is shared by every package that ships a menu entry, so a tree extracted onto
// it can replace any of them — kpm's own included — and would carry them off in
// the extracting package's manifest at the next uninstall.
func InstallDest(raw string) (string, error) {
	abs, err := cleanDeviceAbs(raw)
	if err != nil {
		return "", err
	}
	if classify(abs, nil) != vAllowed {
		return "", fmt.Errorf("install dest %q is not within a writable location "+
			"(.adds/.kobo on the user partition; /usr/local, /opt on rootfs)", raw)
	}
	// A content dir is the one shared root that IS a valid dest. The refusal above
	// exists because interleaved files make an exact uninstall impossible; inside a
	// content dir the cmd layer restores that property per file instead — kpm
	// refuses to write a file another package owns or the user put there (D4), and
	// both the prune and the uninstall delete only manifest members. Checked before
	// the shared-root refusal, which isAllowRoot would otherwise trigger.
	if isContentDir(abs) {
		return abs, nil
	}
	// A dest UNDER a content dir is refused. classify allows the whole subtree, but
	// every content guard keys on the directory ITSELF: a dest of
	// /mnt/onboard/fonts/MyFonts would install with no flat rule, no per-file
	// ownership, no reboot prompt and kind "mod", and would overwrite and later
	// delete whatever the user keeps there. 0.11.0 refused it outright, and so does
	// this. Nickel ignores the subdirectory anyway (3.2).
	if underContentDir(abs) {
		return "", fmt.Errorf("install dest %q is inside the content directory %s, which is read flat; "+
			"use %s itself", raw, contentDirOf(abs), contentDirOf(abs))
	}
	if isAllowRoot(abs, nil) || pathEqual(abs, nmDir) {
		return "", fmt.Errorf("install dest %q is a shared root; use a package-specific subdirectory", raw)
	}
	return abs, nil
}

// ContentDirFor returns the content-dir row an app-tree install writes into, and
// whether there is one. A nil block, a tgz def, or any other dest reports false.
// It is the single predicate the cmd layer branches on for per-file ownership,
// the reboot rule and the `kind` field — one relation, one function.
func ContentDirFor(i *config.Install) (ContentDir, bool) {
	if i == nil || i.Method != config.MethodAppTree {
		return ContentDir{}, false
	}
	abs, err := cleanDeviceAbs(i.Dest)
	if err != nil {
		return ContentDir{}, false
	}
	for _, c := range contentDirs {
		if pathEqual(abs, c.Dir) {
			return c, true
		}
	}
	return ContentDir{}, false
}

// contentDirOf returns the content dir abs lives in, or "" when it is in none.
func contentDirOf(abs string) string {
	for _, c := range contentDirs {
		if under(abs, c.Dir) {
			return c.Dir
		}
	}
	return ""
}

// ManifestHasContent reports whether a recorded manifest places a file INSIDE a
// content dir. A tgz package may ship a font without declaring any [install]
// block, so the def alone cannot answer "does removing this change what Nickel
// renders" -- the manifest can, and that is what decides the reboot prompt (D6).
// The directory itself does not count: no package owns it.
func ManifestHasContent(manifest []string) bool {
	for _, m := range manifest {
		abs, err := cleanDeviceAbs(m)
		if err != nil {
			continue
		}
		if underContentDir(abs) && !isContentDir(abs) {
			return true
		}
	}
	return false
}

// ValidateUninstallDecl rejects an [uninstall] recipe that names a content dir
// ITSELF as something to delete or to mark. It is a PARSE-time check, unlike the
// rest of the recipe, which Compute validates at use: a recursive delete of a
// content dir is refused by Compute, and that refusal fails the WHOLE uninstall,
// so a def carrying one leaves the package unremovable. purge_paths = "<dest>/**"
// is the shape every app-tree def uses, so a curator who copies the koreader
// entry writes exactly this. Failing at parse costs the curator one clear message
// instead of costing a user their only route to removing the package.
//
// A single FILE inside a content dir stays legal in all these lists (3.1): that is
// the same exposure every file under .adds has.
func ValidateUninstallDecl(u config.Uninstall) error {
	for _, field := range []struct {
		name string
		raws []string
	}{
		{"extra_paths", u.ExtraPaths},
		{"purge_paths", u.PurgePaths},
	} {
		for _, raw := range field.raws {
			base, _ := splitRecursive(raw)
			abs, err := cleanDeviceAbs(base)
			if err != nil {
				continue // shape errors stay Compute's to report
			}
			if isContentDir(abs) {
				return fmt.Errorf("%s entry %q names the shared content directory %s; "+
					"list the files this package installs, not the directory", field.name, raw, abs)
			}
		}
	}
	if raw := strings.TrimSpace(u.MarkerFile); raw != "" {
		if abs, err := cleanDeviceAbs(raw); err == nil && underContentDir(abs) {
			return fmt.Errorf("marker_file %q is inside a content directory, which holds only content", raw)
		}
	}
	return nil
}

// IsContentDir reports whether a cleaned absolute device path IS a content dir.
// The app-tree extractor asks it about its own dest to decide whether the archive
// must be flat.
func IsContentDir(abs string) bool { return isContentDir(abs) }

// InstallPath validates ONE path the extractor is about to write and returns the
// cleaned absolute device path. There is deliberately no allow_paths extension: a
// registry may widen where kpm DELETES (it declares its own files) but never
// where kpm WRITES, because the write set comes from an archive kpm did not
// author (APP-TREE.md §5.1 rule 8).
//
// kpm's own NickelMenu drop-in is denied HERE and not only in LauncherPath:
// LauncherPath validates a DECLARED nm_file, which is not the path an archive
// entry travels. classify already denies the rest of kpm's tree (kpmDir); the
// drop-in lives outside it, in the shared nm directory, so it needs saying
// separately. pathEqual folds case because /mnt/onboard is FAT32, where nm/KPM
// and nm/kpm are one file.
func InstallPath(raw string) (string, error) {
	abs, err := cleanDeviceAbs(raw)
	if err != nil {
		return "", err
	}
	if classify(abs, nil) != vAllowed {
		return "", fmt.Errorf("%q is not a writable location", abs)
	}
	if pathEqual(abs, nmKpmFile) {
		return "", fmt.Errorf("%q is kpm's own NickelMenu drop-in", abs)
	}
	return abs, nil
}

// SymlinkedInstallParent reports the deepest intermediate directory that is a
// symlink on the way to dev, if any. The extractor calls it before creating each
// directory AND again immediately before each file write: a check that passed
// once can be invalidated by anything running concurrently as root, which is the
// second layer of the 0.6.2 discipline (APP-TREE.md §5.2).
func SymlinkedInstallParent(p device.Paths, dev string) (string, bool) {
	return symlinkedParent(p, dev, nil)
}

// LauncherPath validates a declared NickelMenu drop-in path: it must clean, must
// live under the shared /mnt/onboard/.adds/nm directory, and must never be kpm's
// own drop-in (which carries kpm's menu entries — a package that could overwrite
// it could replace every kpm action on the device).
func LauncherPath(raw string) (string, error) {
	abs, err := cleanDeviceAbs(raw)
	if err != nil {
		return "", err
	}
	if !under(abs, nmDir) || pathEqual(abs, nmDir) {
		return "", fmt.Errorf("install launcher nm_file %q must be under %s", raw, nmDir)
	}
	if pathEqual(abs, nmKpmFile) {
		return "", fmt.Errorf("install launcher nm_file must not be kpm's own NickelMenu drop-in")
	}
	// Belt and braces: /mnt/onboard/.adds/nm is inside the allowlist, so this can
	// only fire if the policy changes underneath us.
	if classify(abs, nil) != vAllowed {
		return "", fmt.Errorf("install launcher nm_file %q is not within a writable location "+
			"(.adds/.kobo on the user partition; /usr/local, /opt on rootfs)", raw)
	}
	return abs, nil
}

// ValidateInstallDecl checks a package's [install] block end to end: method, root
// shape, dest policy, and launcher constraints. ParseManifest drops a registry def
// whose install block fails this, exactly as it drops one whose [[configs]] fail
// (APP-TREE.md §2.4) — a malformed block must never reach the extractor. A nil
// block (every def written before 0.11.0) is valid and means the tgz shape.
func ValidateInstallDecl(i *config.Install) error {
	if i == nil {
		return nil
	}
	if err := i.Validate(); err != nil {
		return err
	}
	// The dest policy applies whenever a dest is declared, not only for app-tree:
	// a nonsensical dest on a tgz def is a curator error either way, and refusing
	// it now keeps the field from becoming live later without re-validation.
	if strings.TrimSpace(i.Dest) != "" {
		if _, err := InstallDest(i.Dest); err != nil {
			return err
		}
	}
	if i.Launcher != nil {
		if _, err := LauncherPath(i.Launcher.NmFile); err != nil {
			return err
		}
	}
	return nil
}
