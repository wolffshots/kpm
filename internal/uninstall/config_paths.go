package uninstall

// config_paths.go exposes the path-safety engine (cleanDeviceAbs, classify,
// symlinkedParent) to the config editor (internal/modconfig, cmd/kpm) as thin
// exported wrappers. Declared config paths get the identical treatment an
// uninstall marker_file gets (CONFIG.md §5 / UNINSTALL.md §4): they must clean
// without ".." traversal AND classify vAllowed, so a malicious or fat-fingered
// registry can never point the editor at /etc/shadow or a firmware tree.

import (
	"fmt"

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
