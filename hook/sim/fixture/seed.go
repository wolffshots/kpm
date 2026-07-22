// Command seed populates a kpm sandbox (KPM_ROOT + KPM_SYSROOT) so the desktop
// simulator (hook/sim) can drive real `kpm search`/`kpm uninstall` against it,
// entirely offline. It seeds packages in mixed states:
//
//	koreader     not installed        (registry def, rich description)
//	nickelclock  installed v0.4.0     (packages.d def + settings.ini on disk —
//	                                    the ini config-editing target)
//	nickelmenu   not installed        (registry def)
//	nickelnote   installed v1.2.0     (packages.d def + three text templates;
//	                                    pin.template absent — the create path; its
//	                                    registry def carries an OLD tested_fw vs the
//	                                    seeded .kobo/version — search reports
//	                                    fw_untested=true, the firmware-badge fixture)
//	samplemod    installed v1.0.0     (packages.d def + manifest + a real file
//	                                    under KPM_SYSROOT — the uninstall target;
//	                                    also registry-managed with a STALE local
//	                                    def missing the registry's [[configs]] —
//	                                    the sync exercise target; and carries a
//	                                    seeded MissingFiles — the "files missing"
//	                                    badge fixture)
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
	"kpm/internal/registry"
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
# tested_fw is advisory registry-only metadata (D): the newest firmware this
# release was confirmed working on. Seeded OLDER than the sandbox's .kobo/version
# firmware (below) so search reports fw_untested=true and the detail page shows
# the "Untested on your firmware" warning — the firmware-badge fixture.
tested_fw = "4.38.23429"

[packages.samplemod]
name = "Sample Mod"
source = "codeberg.org/o/samplemod"
forge = "forgejo"
asset = "KoboRoot.tgz"
description = "A demo mod that recently gained an editable config — the sync target."

  [[packages.samplemod.configs]]
  name = "Settings"
  path = "/mnt/onboard/.adds/samplemod/settings.ini"
  format = "ini"
  reload = "reboot"
  description = "Sample mod options."
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

// Seed templates for the three NickelNote config files, matching the registry
// shape (PII scrubbed). content/style are also written to disk (so they exist);
// pin.template is left absent so its template drives the "Create from example"
// path in the ConfigDialog and the `kpm config init` exercise (main.cc).
const noteContentTemplate = `<span>Your Name</span><br />
<span style="font-size: 32px">If found, Please return at</span><br />
<span style="font-size: 32px;">+1 555 000 0000</span>
`

const noteStyleTemplate = `#infoWidget {
  color: black;
  background-color: rgba(255,255,255,170);
  min-height: 290px;
  max-height: 290px;

  margin-top: 200px;
  margin-left: -30px;
}

#infoWidget[powerOffView=true]{
  background-color: rgba(0,0,0,170);
}

QLabel{
	min-width: 400px;
	min-height: 300px;
}
`

const notePinTemplate = `<p style="font-size: 32px;">
This tablet is protected and belongs to <br/>
<b>Your Name</b>
<br/>
<br/>
If found, please return to <br/>
US: +1 555 000 0000<br/>
CA: +1 555 000 0000<br/>
</p>
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
	// KPM_SYSROOT, exactly like the uicontract uninstall test. It is ALSO
	// registry-managed (Registry="main") with a STALE local def: the registry
	// cache above has since added an [[configs]] declaration this local def lacks,
	// so `kpm sync` re-copies the def and has_config flips true — the sync
	// exercise target.
	sampleLocal := &config.Package{
		Name:      "Sample Mod",
		Source:    "codeberg.org/o/samplemod",
		Forge:     config.ForgeForgejo,
		Asset:     "KoboRoot.tgz",
		Registry:  "main",
		Uninstall: config.Uninstall{}, // default "manifest" method
	}
	must(config.Save(p.PackageFile("samplemod"), sampleLocal))
	// Baseline the sync at the OLD (config-less) def hash so sync sees a clean
	// upstream change (decision-tree case 3) and applies, rather than treating the
	// mismatch as local drift.
	oldSampleHash, err := registry.HashDef(registry.DefFromPackage(sampleLocal))
	must(err)
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
			{Name: "Note content", Path: "/mnt/onboard/.adds/nickelnote/content.template", Format: config.FormatText, Reload: config.ReloadAuto, Create: true, Description: "Rich text shown on the sleep screen. Needs Style too.", Template: noteContentTemplate},
			{Name: "Style", Path: "/mnt/onboard/.adds/nickelnote/style.template", Format: config.FormatText, Reload: config.ReloadAuto, Create: true, Description: "Stylesheet for the sleep-screen note.", Template: noteStyleTemplate},
			{Name: "PIN screen message", Path: "/mnt/onboard/.adds/nickelnote/pin.template", Format: config.FormatText, Reload: config.ReloadAuto, Create: true, Description: "Message shown on the lock-PIN screen.", Template: notePinTemplate},
		},
	}))
	writeDev(sysroot, "/mnt/onboard/.adds/nickelnote/content.template", noteContent)
	writeDev(sysroot, "/mnt/onboard/.adds/nickelnote/style.template", noteStyle)

	// Device firmware descriptor (D): .kobo/version is one comma-separated line
	// whose 3rd field is the firmware. Seeded NEWER (4.45) than nickelnote's
	// tested_fw (4.38) so device.Firmware() reads it and search computes
	// fw_untested=true for nickelnote. Written at p.VersionFile() — the exact path
	// device.Firmware() reads (KPM_ROOT/.kobo/version).
	must(os.MkdirAll(filepath.Dir(p.VersionFile()), 0o755))
	must(os.WriteFile(p.VersionFile(), []byte("N418765432100,3.0.35,4.45.23697,kobo7,0000000000\n"), 0o644))

	// State: installed versions, samplemod's manifest, and the registry's
	// last-refreshed timestamp (recent, so the browse view isn't all "stale").
	st, err := state.Load(p.StateFile())
	must(err)
	st.Get("nickelclock").InstalledVersion = "v0.4.0"
	st.Get("nickelnote").InstalledVersion = "v1.2.0"
	sm := st.Get("samplemod")
	sm.InstalledVersion = "v1.0.0"
	sm.Manifest = []string{manifestPath}
	sm.SyncedDefSHA256 = oldSampleHash
	// MissingFiles (A): seeded DIRECTLY — on-device kpm computes this at the
	// staged->installed flip, but the sim never boots rcS, so seed the field the
	// reconcile would have written. A non-empty list drives PackageRow's
	// top-priority "files missing" badge and the detail page's "Missing files:"
	// line. These members are absent from KPM_SYSROOT (only manifestPath exists),
	// mirroring an rcS failure that promoted without landing every file.
	sm.MissingFiles = []string{
		"usr/local/Kobo/imageformats/libsamplemod.so",
		"usr/local/samplemod/data.bin",
	}
	st.Registry("main").LastFetched = time.Now().UTC().Format(time.RFC3339)
	must(st.Save())

	fmt.Println("seed: sandbox ready")
	fmt.Println("  KPM_ROOT    =", os.Getenv("KPM_ROOT"))
	fmt.Println("  KPM_SYSROOT =", sysroot)
	fmt.Println("  packages: koreader(not-installed) nickelclock(v0.4.0) nickelmenu(not-installed) nickelnote(v1.2.0) samplemod(v1.0.0)")
	fmt.Println("  uninstall target: samplemod ->", hostFile)
	fmt.Println("  config targets: nickelclock(settings.ini, ini) nickelnote(3 templates, text)")
	fmt.Println("  sync target: samplemod (local def missing the registry's [[configs]])")
	fmt.Println("  files-missing badge: samplemod (seeded MissingFiles)")
	fmt.Println("  firmware badge: nickelnote (tested_fw 4.38 < device 4.45)")
}
