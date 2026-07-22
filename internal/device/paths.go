// Package device isolates all on-device filesystem paths and side effects
// (connectivity wait, toasts, reboot, log/status writers) behind an
// overridable root so host-side tests never touch a real Kobo.
package device

import (
	"os"
	"path/filepath"
	"strings"

	"kpm/internal/config"
)

// Root is the user-visible storage mount. On a Kobo this is /mnt/onboard.
// Tests (and dev runs on Windows) override it via the KPM_ROOT env var.
func Root() string {
	if r := os.Getenv("KPM_ROOT"); r != "" {
		return r
	}
	return "/mnt/onboard"
}

// SysRoot is the rootfs mount. On a Kobo this is "/". Uninstall resolves
// absolute device paths (/usr/local/..., /mnt/onboard/...) against it; tests
// override it via KPM_SYSROOT so deletions land inside a sandbox. Set
// KPM_ROOT to <KPM_SYSROOT>/mnt/onboard to keep the two consistent.
func SysRoot() string {
	if r := os.Getenv("KPM_SYSROOT"); r != "" {
		return r
	}
	return "/"
}

// HostPath maps a cleaned absolute device path (e.g. "/usr/local/x") to the
// host path under SysRoot(). On a real device SysRoot() is "/" so it is the
// identity. The argument must already be an absolute, cleaned device path.
func (p Paths) HostPath(deviceAbs string) string {
	rel := strings.TrimPrefix(deviceAbs, "/")
	return filepath.Join(SysRoot(), filepath.FromSlash(rel))
}

// Paths holds every path kpm reads or writes, all derived from Root().
type Paths struct {
	Root string
}

// New resolves paths against the current Root().
func New() Paths { return Paths{Root: Root()} }

// AddsKpm is /mnt/onboard/.adds/kpm — kpm's home.
func (p Paths) AddsKpm() string { return filepath.Join(p.Root, ".adds", "kpm") }

// Bin is the installed kpm binary path.
func (p Paths) Bin() string { return filepath.Join(p.AddsKpm(), "bin", "kpm") }

// PackagesDir holds one TOML per registered package.
func (p Paths) PackagesDir() string { return filepath.Join(p.AddsKpm(), "packages.d") }

// PackageFile is packages.d/<id>.toml.
func (p Paths) PackageFile(id string) string {
	return filepath.Join(p.PackagesDir(), id+".toml")
}

// StateFile is state.json.
func (p Paths) StateFile() string { return filepath.Join(p.AddsKpm(), "state.json") }

// LogFile is the append-only human-readable history.
func (p Paths) LogFile() string { return filepath.Join(p.AddsKpm(), "kpm.log") }

// StatusFile is the last-result summary shown by the NickelMenu status dialog.
func (p Paths) StatusFile() string { return filepath.Join(p.AddsKpm(), "status.txt") }

// CacheDir holds downloaded assets awaiting staging.
func (p Paths) CacheDir() string { return filepath.Join(p.AddsKpm(), "cache") }

// ConfigFile is the global config.toml holding [[registries]] entries
// (REGISTRY.md §9.5).
func (p Paths) ConfigFile() string { return filepath.Join(p.AddsKpm(), "config.toml") }

// RegistryCache is the cached registry.toml for a named registry
// (cache/registry-<name>.toml). It shares CacheDir but uses a distinct prefix
// so cleanCache (which only touches *.part/*.tgz) never deletes it (§7.5).
//
// The name MUST be ValidID-shaped; a name with "/" or ".." could escape CacheDir
// (a hand-edited config.toml cache-escape). loadRegistryConfig already drops
// invalid names before any call here, so this panic is unreachable defense in
// depth (C2).
func (p Paths) RegistryCache(name string) string {
	if !config.ValidID(name) {
		panic("kpm: invalid registry cache name " + name)
	}
	return filepath.Join(p.CacheDir(), "registry-"+name+".toml")
}

// LockFile is the single-instance lock (B1).
func (p Paths) LockFile() string { return filepath.Join(p.AddsKpm(), "lock") }

// NmConfig is the NickelMenu drop-in.
func (p Paths) NmConfig() string { return filepath.Join(p.Root, ".adds", "nm", "kpm") }

// StagedTgz is the boot-time install slot rcS consumes.
func (p Paths) StagedTgz() string { return filepath.Join(p.Root, ".kobo", "KoboRoot.tgz") }

// VersionFile is the firmware descriptor Nickel writes at .kobo/version: one
// comma-separated line whose third field is the firmware version (confirmed
// against calibre + KOReader readers). kpm reads it advisorily for the
// tested_fw compatibility note (D).
func (p Paths) VersionFile() string { return filepath.Join(p.Root, ".kobo", "version") }

// Firmware returns the device firmware version parsed from VersionFile() and
// whether it was read. The file is one comma-separated line; the firmware is the
// 3rd field (index 2), trimmed. Any failure — missing file, short/empty line,
// blank field — yields ("", false). It never errors: firmware detection is
// advisory and its absence must silence the note, never break a command (D).
func (p Paths) Firmware() (string, bool) {
	b, err := os.ReadFile(p.VersionFile())
	if err != nil {
		return "", false
	}
	line := string(b)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	fields := strings.Split(line, ",")
	if len(fields) < 3 {
		return "", false
	}
	fw := strings.TrimSpace(fields[2])
	if fw == "" {
		return "", false
	}
	return fw, true
}

// EnsureDirs creates the directories kpm writes into.
func (p Paths) EnsureDirs() error {
	for _, d := range []string{p.PackagesDir(), p.CacheDir(), filepath.Dir(p.Bin())} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
