// Command seed populates a kpm sandbox (KPM_ROOT + KPM_SYSROOT) so the desktop
// simulator (hook/sim) can drive real `kpm search`/`kpm uninstall` against it,
// entirely offline. It seeds packages in mixed states:
//
//   koreader     not installed        (registry def, rich description)
//   nickelclock  installed v0.4.0     (registry def, marker-remove uninstall)
//   nickelmenu   not installed        (registry def)
//   samplemod    installed v1.0.0     (packages.d def + manifest + a real file
//                                       under KPM_SYSROOT — the uninstall target)
//
// It imports kpm's own internal packages (it lives inside module kpm) to write
// state exactly the way the real commands and the UI-contract tests do, so the
// seeded sandbox is byte-compatible with what `kpm search --json` reads.
//
// The network-dependent states (update-available badges from `kpm check`, and
// install/update staging) are intentionally NOT seeded: the stock kpm binary is
// https-only and trusts an embedded CA bundle (internal/forge/http.go), so a
// local fixture forge cannot be trusted without patching the binary. See the sim
// README for which of the seven UI flows this supports.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/state"
)

// registryCache is the cached registry.toml (schema_version 1) the sim's
// `kpm search` reads for the registered packages and their descriptions.
const registryCache = `schema_version = 1

[packages.koreader]
name = "KOReader"
source = "github.com/koreader/koreader"
forge = "github"
asset = "koreader-kobo-*.zip"
description = "A document viewer for PDF, DjVu, EPUB, FB2 and more, with a rich feature set for e-ink readers."
homepage = "https://github.com/koreader/koreader"

[packages.nickelclock]
name = "NickelClock"
source = "github.com/shermp/NickelClock"
forge = "github"
asset = "NickelClock-*.zip"
min_kpm = "0.4.0"
description = "Show the time in the reading header."
homepage = "https://github.com/shermp/NickelClock"

  [packages.nickelclock.uninstall]
  method = "marker-remove"
  marker_file = "/mnt/onboard/.adds/nickelclock/uninstall"

[packages.nickelmenu]
name = "NickelMenu"
source = "github.com/pgaskin/NickelMenu"
forge = "github"
asset = "KoboRoot.tgz"
description = "A launcher that adds custom menu items to the Kobo home screen."
homepage = "https://github.com/pgaskin/NickelMenu"
`

const registryConfig = `[[registries]]
name = "main"
url = "codeberg.org/o/kpm-registry"
ref = "main"
path = "registry.toml"
forge = "forgejo"
`

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed: fatal:", err)
		os.Exit(1)
	}
}

func main() {
	sysroot := os.Getenv("KPM_SYSROOT")
	if sysroot == "" || os.Getenv("KPM_ROOT") == "" {
		fmt.Fprintln(os.Stderr, "seed: KPM_ROOT and KPM_SYSROOT must be set")
		os.Exit(2)
	}

	p := device.New()
	must(p.EnsureDirs())
	must(os.MkdirAll(filepath.Dir(p.StagedTgz()), 0o755))

	// Registry config + cache (the registered, browsable packages).
	must(os.WriteFile(p.ConfigFile(), []byte(registryConfig), 0o644))
	must(os.WriteFile(p.RegistryCache("main"), []byte(registryCache), 0o644))

	// samplemod: an installed, manifest-uninstallable package (the uninstall
	// target). Written as a packages.d def + state manifest + a real file under
	// KPM_SYSROOT, exactly like the uicontract uninstall test.
	must(config.Save(p.PackageFile("samplemod"), &config.Package{
		Name:      "Sample Mod",
		Source:    "codeberg.org/o/samplemod",
		Forge:     config.ForgeForgejo,
		Asset:     "KoboRoot.tgz",
		Uninstall: config.Uninstall{}, // default "manifest" method
	}))
	manifestPath := "usr/local/samplemod/app"
	hostFile := filepath.Join(sysroot, filepath.FromSlash(manifestPath))
	must(os.MkdirAll(filepath.Dir(hostFile), 0o755))
	must(os.WriteFile(hostFile, []byte("sample payload\n"), 0o644))

	// State: installed versions, samplemod's manifest, and the registry's
	// last-refreshed timestamp (recent, so the browse view isn't all "stale").
	st, err := state.Load(p.StateFile())
	must(err)
	st.Get("nickelclock").InstalledVersion = "v0.4.0"
	sm := st.Get("samplemod")
	sm.InstalledVersion = "v1.0.0"
	sm.Manifest = []string{manifestPath}
	st.Registry("main").LastFetched = time.Now().UTC().Format(time.RFC3339)
	must(st.Save())

	fmt.Println("seed: sandbox ready")
	fmt.Println("  KPM_ROOT    =", os.Getenv("KPM_ROOT"))
	fmt.Println("  KPM_SYSROOT =", sysroot)
	fmt.Println("  packages: koreader(not-installed) nickelclock(v0.4.0) nickelmenu(not-installed) samplemod(v1.0.0)")
	fmt.Println("  uninstall target: samplemod ->", hostFile)
}
