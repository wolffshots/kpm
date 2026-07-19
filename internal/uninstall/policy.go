// Package uninstall computes and executes a package removal plan: which files
// and directories to delete (from the recorded manifest plus per-package
// [uninstall] extras), subject to a path-safety policy, all through the device
// layer's overridable root so the logic is unit-testable on any host.
package uninstall

import (
	"fmt"
	"path"
	"runtime"
	"strings"
)

// verdict is a path-policy classification.
type verdict int

const (
	vAllowed    verdict = iota // deletable
	vDenied                    // hard denylist — refused even via allow_paths
	vNotAllowed                // outside the allowlist — skipped with a WARN
)

// denyPrefixes are roots whose contents can brick boot/Nickel. Anything at or
// under these is refused, even if listed in allow_paths (§4).
var denyPrefixes = []string{
	"/bin", "/sbin", "/lib", "/drivers", "/etc/init.d",
}

// kpmSelfDenied are kpm's own paths that must never be deleted (§4). These are
// device-absolute (Root is /mnt/onboard on device).
var (
	kpmBinDir     = "/mnt/onboard/.adds/kpm/bin"
	kpmStateFile  = "/mnt/onboard/.adds/kpm/state.json"
	kpmLogFile    = "/mnt/onboard/.adds/kpm/kpm.log"
	koboDir       = "/usr/local/Kobo"
	koboException = "/usr/local/Kobo/imageformats"
)

// allowPrefixes are the built-in deletable roots (§4).
var allowPrefixes = []string{
	"/mnt/onboard/.adds",
	"/mnt/onboard/.kobo",
	"/usr/local",
	"/usr/bin",
	"/usr/lib",
	"/opt",
	"/etc/udev/rules.d",
	"/etc/dbus-1",
}

// onboardPrefix is the FAT32 user partition. Path comparisons at or under it
// are case-insensitive because FAT32 is (C2); rootfs (ext4) comparisons stay
// case-sensitive.
const onboardPrefix = "/mnt/onboard"

// onOnboard reports whether p is at or under /mnt/onboard (case-insensitively).
func onOnboard(p string) bool {
	lp := strings.ToLower(p)
	return lp == onboardPrefix || strings.HasPrefix(lp, onboardPrefix+"/")
}

// pathEqual compares two device paths, case-insensitively when either is on the
// FAT32 onboard partition, case-sensitively otherwise (C2).
func pathEqual(a, b string) bool {
	if onOnboard(a) || onOnboard(b) {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// under (aliased pathUnder) reports whether abs is at or below prefix (exact
// match or prefix/ ...), applying the onboard case rule (C2).
func under(abs, prefix string) bool {
	if pathEqual(abs, prefix) {
		return true
	}
	sep := prefix + "/"
	if onOnboard(abs) || onOnboard(prefix) {
		return len(abs) >= len(sep) && strings.EqualFold(abs[:len(sep)], sep)
	}
	return strings.HasPrefix(abs, sep)
}

// components counts the non-empty path segments of a cleaned absolute path.
func components(abs string) int {
	t := strings.Trim(abs, "/")
	if t == "" {
		return 0
	}
	return len(strings.Split(t, "/"))
}

// classify applies the §4 path policy to a cleaned absolute device path.
// allowExtra extends the built-in allowlist but cannot override the denylist.
func classify(abs string, allowExtra []string) verdict {
	// Denylist first — beats allow_paths.
	if abs == "/" || components(abs) < 2 {
		return vDenied
	}
	for _, d := range denyPrefixes {
		if under(abs, d) {
			return vDenied
		}
	}
	if pathEqual(abs, "/etc/inittab") {
		return vDenied
	}
	// /usr/local/Kobo is denied EXCEPT the imageformats subtree.
	if under(abs, koboDir) && !under(abs, koboException) {
		return vDenied
	}
	if under(abs, kpmBinDir) || pathEqual(abs, kpmStateFile) || pathEqual(abs, kpmLogFile) {
		return vDenied
	}

	// Allowlist (built-in + per-package extension).
	for _, a := range allowPrefixes {
		if under(abs, a) {
			return vAllowed
		}
	}
	for _, a := range allowExtra {
		if under(abs, a) {
			return vAllowed
		}
	}
	return vNotAllowed
}

// cleanDeviceAbs normalizes a raw path into a cleaned absolute device path
// ("/a/b/c"). Manifest entries are tar-style (no leading slash); configured
// paths already start with "/". ".." traversal is rejected. A bare "**"
// component (not the "/**" recursive suffix, which callers strip first) is
// rejected (C6). Backslash is only treated as a separator on Windows hosts; on
// the Linux target it is a literal filename byte (C5).
func cleanDeviceAbs(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty path")
	}
	s := raw
	if runtime.GOOS == "windows" {
		s = strings.ReplaceAll(s, "\\", "/")
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return "", fmt.Errorf("path traversal in %q", raw)
		}
		if seg == "**" {
			return "", fmt.Errorf("use /** for recursive deletion, not a bare ** component in %q", raw)
		}
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s // tar-style manifest entry
	}
	clean := path.Clean(s)
	if clean == "/" || clean == "." {
		return "", fmt.Errorf("refusing root path %q", raw)
	}
	return clean, nil
}

// splitRecursive strips a trailing "/**" or "/**/" (recursive-directory marker)
// and reports whether it was present (C6).
func splitRecursive(raw string) (base string, recursive bool) {
	if strings.HasSuffix(raw, "/**/") {
		return strings.TrimSuffix(raw, "/**/"), true
	}
	if strings.HasSuffix(raw, "/**") {
		return strings.TrimSuffix(raw, "/**"), true
	}
	return raw, false
}
