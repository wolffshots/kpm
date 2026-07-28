package uninstall

// config_paths_test.go pins the app-tree write policy (APP-TREE.md §5.1). The
// question every case asks is the one LauncherPath already answers for a
// DECLARED nm_file: can a package reach kpm's own NickelMenu drop-in? The
// extractor never consults LauncherPath — it goes through InstallDest and
// InstallPath — so both of those have to answer it too.

import "testing"

// TestInstallPathRefusesKpmNmDropIn: InstallPath is the extractor's per-entry
// gate, so a zip entry that resolves to kpm's own drop-in must be refused there
// and not only where a def DECLARES an nm_file. Case is folded because
// /mnt/onboard is FAT32: nm/KPM and nm/kpm are the same file on the device.
func TestInstallPathRefusesKpmNmDropIn(t *testing.T) {
	for _, raw := range []string{
		"/mnt/onboard/.adds/nm/kpm",
		"/mnt/onboard/.adds/nm/KPM",
		"/mnt/onboard/.adds/NM/Kpm",
		"mnt/onboard/.adds/nm/kpm", // tar-style
	} {
		if _, err := InstallPath(raw); err == nil {
			t.Errorf("InstallPath(%q) must refuse kpm's own NickelMenu drop-in", raw)
		}
	}
	// Every other drop-in in the shared directory stays writable.
	if _, err := InstallPath("/mnt/onboard/.adds/nm/koreader"); err != nil {
		t.Errorf("InstallPath(nm/koreader) = %v, want nil", err)
	}
}

// TestInstallDestRefusesNmDir: the shared NickelMenu directory is exactly the
// "interleaved files, no exact uninstall" case InstallDest already refuses for
// .adds and /usr/local — and, worse, a tree extracted onto it can replace every
// other package's menu entry.
func TestInstallDestRefusesNmDir(t *testing.T) {
	for _, raw := range []string{
		"/mnt/onboard/.adds/nm",
		"/mnt/onboard/.adds/nm/",
		"/mnt/onboard/.adds/NM",
	} {
		if _, err := InstallDest(raw); err == nil {
			t.Errorf("InstallDest(%q) must refuse the shared NickelMenu directory", raw)
		}
	}
	// A package-specific directory inside .adds is still a valid dest.
	if _, err := InstallDest("/mnt/onboard/.adds/koreader"); err != nil {
		t.Errorf("InstallDest(.adds/koreader) = %v, want nil", err)
	}
}
