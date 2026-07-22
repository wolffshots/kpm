// Command seed populates a kpm sandbox (KPM_ROOT + KPM_SYSROOT) so the desktop
// simulator (hook/sim) can drive real `kpm search`/`kpm uninstall` against it,
// entirely offline. It seeds packages in mixed states:
//
//   koreader     not installed        (registry def, rich description)
//   nickelclock  installed v0.4.0     (packages.d def + settings.ini on disk —
//                                       the ini config-editing target)
//   nickelmenu   not installed        (registry def)
//   nickelnote   installed v1.2.0     (packages.d def + three text templates;
//                                       pin.template absent — the create path)
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

[packages.nickelnote]
name = "NickelNote"
source = "github.com/kbanh/NickelNote"
forge = "github"
asset = "KoboRoot.tgz"
description = "Show a custom note and stylesheet on the sleep and PIN screens."
homepage = "https://github.com/kbanh/NickelNote"
`

// clockSettings is a realistic NickelClock settings.ini with comments and three
// sections, written to the sandbox so a surgical `config set` visibly rewrites
// only the one target line (comments/blank lines/other keys byte-preserved).
const clockSettings = `; NickelClock settings — edited on-device by kpm config.
; Comment lines (starting with ; or #) must survive edits untouched.

[General]
Margin = 10

[Clock]
Enabled = true
Placement = Footer
Position = Right

[Battery]
Enabled = false
ShowPercentage = true
`

// NickelNote's short text templates (Qt rich text + a QSS stylesheet). pin.template
// is deliberately NOT written, so it exercises the "not created / create on first
// save" path in both the CLI and the ConfigDialog file picker.
const noteContent = `<p>Good night.</p>
<p>Sweet dreams from your Kobo.</p>
`

const noteStyle = `p { font-size: 30px; color: #202020; }
body { margin: 40px; }
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

// writeDev writes content at a device-absolute path (e.g. /mnt/onboard/...) into
// the KPM_SYSROOT sandbox — the same host mapping `kpm config` reads/writes.
func writeDev(sysroot, devPath, content string) {
	host := filepath.Join(sysroot, filepath.FromSlash(devPath))
	must(os.MkdirAll(filepath.Dir(host), 0o755))
	must(os.WriteFile(host, []byte(content), 0o644))
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

	// nickelclock: a packages.d def carrying its ini config declaration (so
	// has_config is true and `kpm config` can edit it), plus the marker-remove
	// uninstall recipe from the registry, and the real settings.ini on disk. This
	// is the single-file / ini ConfigDialog target (--exercise-config nickelclock).
	must(config.Save(p.PackageFile("nickelclock"), &config.Package{
		Name:   "NickelClock",
		Source: "github.com/shermp/NickelClock",
		Forge:  config.ForgeGitHub,
		Asset:  "NickelClock-*.zip",
		MinKpm: "0.4.0",
		Uninstall: config.Uninstall{
			Method:     config.MethodMarkerRemove,
			MarkerFile: "/mnt/onboard/.adds/nickelclock/uninstall",
		},
		Configs: []config.ModConfig{{
			Name:        "Settings",
			Path:        "/mnt/onboard/.adds/nickelclock/settings.ini",
			Format:      config.FormatINI,
			Reload:      config.ReloadReboot,
			Description: "Clock and battery display options.",
		}},
	}))
	writeDev(sysroot, "/mnt/onboard/.adds/nickelclock/settings.ini", clockSettings)

	// nickelnote: an installed package declaring three text templates (create=true).
	// content/style are written; pin.template is left absent to exercise the
	// not-created + create-on-first-save path. This is the multi-file / text
	// ConfigDialog target (the file-picker page + text editor).
	must(config.Save(p.PackageFile("nickelnote"), &config.Package{
		Name:   "NickelNote",
		Source: "github.com/kbanh/NickelNote",
		Forge:  config.ForgeGitHub,
		Asset:  "KoboRoot.tgz",
		Configs: []config.ModConfig{
			{Name: "Note content", Path: "/mnt/onboard/.adds/nickelnote/content.template", Format: config.FormatText, Reload: config.ReloadAuto, Create: true, Description: "Rich text shown on the sleep screen. Needs Style too."},
			{Name: "Style", Path: "/mnt/onboard/.adds/nickelnote/style.template", Format: config.FormatText, Reload: config.ReloadAuto, Create: true, Description: "Stylesheet for the sleep-screen note."},
			{Name: "PIN screen message", Path: "/mnt/onboard/.adds/nickelnote/pin.template", Format: config.FormatText, Reload: config.ReloadAuto, Create: true, Description: "Message shown on the lock-PIN screen."},
		},
	}))
	writeDev(sysroot, "/mnt/onboard/.adds/nickelnote/content.template", noteContent)
	writeDev(sysroot, "/mnt/onboard/.adds/nickelnote/style.template", noteStyle)

	// State: installed versions, samplemod's manifest, and the registry's
	// last-refreshed timestamp (recent, so the browse view isn't all "stale").
	st, err := state.Load(p.StateFile())
	must(err)
	st.Get("nickelclock").InstalledVersion = "v0.4.0"
	st.Get("nickelnote").InstalledVersion = "v1.2.0"
	sm := st.Get("samplemod")
	sm.InstalledVersion = "v1.0.0"
	sm.Manifest = []string{manifestPath}
	st.Registry("main").LastFetched = time.Now().UTC().Format(time.RFC3339)
	must(st.Save())

	fmt.Println("seed: sandbox ready")
	fmt.Println("  KPM_ROOT    =", os.Getenv("KPM_ROOT"))
	fmt.Println("  KPM_SYSROOT =", sysroot)
	fmt.Println("  packages: koreader(not-installed) nickelclock(v0.4.0) nickelmenu(not-installed) nickelnote(v1.2.0) samplemod(v1.0.0)")
	fmt.Println("  uninstall target: samplemod ->", hostFile)
	fmt.Println("  config targets: nickelclock(settings.ini, ini) nickelnote(3 templates, text)")
}
