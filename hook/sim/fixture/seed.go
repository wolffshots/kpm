// Command seed populates a kpm sandbox (KPM_ROOT + KPM_SYSROOT) so the desktop
// simulator (hook/sim) can drive real `kpm search`/`kpm uninstall` against it,
// entirely offline. It seeds packages in mixed states:
//
//	koreader     ORPHANED             (registry def declaring conflicts=[nickelclock]
//	                                    + a packages.d def but NO state entry: exactly
//	                                    what a failed two-step install leaves behind —
//	                                    registered, not installed, and blocked from
//	                                    re-installing. The --exercise-conflicts
//	                                    "Drop registration" target, INTEGRITY.md §4.3)
//	nickelclock  installed v0.4.0     (packages.d def + settings.ini on disk —
//	                                    the ini config-editing target; its registry def
//	                                    is update_check = "rolling" AND it is half of
//	                                    the already-installed conflict pair, so it is
//	                                    also the badge-priority fixture: conflict must
//	                                    outrank "rebuilt ↑")
//	kobocloud    installed latest     (registry def with update_check = "rolling" and
//	                                    no conflicts — the clean §6.3 rebuilt-badge
//	                                    fixture, modelled on the real KoboCloud)
//	ghostmod     installed v1.0.0     (STATE ONLY — no registry entry and no
//	                                    packages.d def, so it never appears in search
//	                                    while still blocking nickelmenu: the
//	                                    `kpm remove`d-but-not-uninstalled case, §4.3)
//	nickelmenu   not installed        (registry def declaring conflicts=[ghostmod])
//	plato        installed 0.9.45     (the APP-TREE fixture: [install] method =
//	                                    "app-tree", extracted into .adds/plato and
//	                                    live without a reboot, plus the NickelMenu
//	                                    drop-in it declares. Its manifest names the
//	                                    extracted files, so uninstall is a real
//	                                    manifest delete; a Settings.toml the manifest
//	                                    does NOT name sits alongside them as the
//	                                    user-data-survives invariant of APP-TREE.md
//	                                    §9.1. Drives --exercise-apptree)
//	nickelnote   installed v1.2.0     (packages.d def + three text templates;
//	                                    pin.template absent — the create path; its
//	                                    registry def carries an OLD tested_fw vs the
//	                                    seeded .kobo/version — search reports
//	                                    fw_untested=true, the firmware-badge fixture;
//	                                    it also declares conflicts=[nickelclock], the
//	                                    already-installed pair of INTEGRITY.md §4.5)
//	samplemod    installed v1.0.0     (packages.d def + manifest + a real file
//	                                    under KPM_SYSROOT — the uninstall target;
//	                                    also registry-managed with a STALE local
//	                                    def missing the registry's [[configs]] —
//	                                    the sync exercise target; and carries a
//	                                    seeded MissingFiles — the "files missing"
//	                                    badge fixture)
//	kf-core-fonts installed v2026.08.05 (the FONT fixture: [install] method =
//	                                    "app-tree" with dest = /mnt/onboard/fonts, the
//	                                    content directory search derives kind = "font"
//	                                    from (D1). Its manifest names two .ttf files;
//	                                    a third, UserFont-Regular.ttf, sits beside them
//	                                    and is NOT in the manifest — the per-file
//	                                    ownership rule of D4, which an uninstall must
//	                                    leave untouched. Drives --exercise-tabs and the
//	                                    font half of --exercise-uninstall)
//	kpm          installed 0.7.0      (SOURCELESS packages.d/kpm.toml + empty state
//	                                    source = un-adopted self row; registry offers
//	                                    a kpm def with a real source — search reports
//	                                    is_self=true, self_configured=false, driving
//	                                    the "self-update off" badge + the Enable
//	                                    self-update button — the --exercise-enroll target)
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
//
// Everything the conflicts/rolling UI reads (INTEGRITY.md §6.1) IS seeded here and
// computed by the real kpm, so --exercise-conflicts asserts against `kpm search
// --json` output rather than against a payload the simulator wrote itself. Only
// `check`'s latest/update pair is still synthesized by the exercise, because it is
// the one thing that genuinely requires the network.
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
# B (INTEGRITY.md §4): koreader is NOT installed and nickelclock IS, so
# conflicts_active is non-empty and "kpm install koreader" hard-refuses — the
# blocked-install fixture. Declared on one side only: the relation is symmetric
# (§4.2), which is what makes nickelclock report the pair without declaring it.
conflicts = ["nickelclock"]

[packages.nickelclock]
name = "NickelClock"
source = "github.com/shermp/NickelClock"
forge = "github"
asset = "NickelClock-*.zip"
# update_check REQUIRES min_kpm or ParseManifest drops the entry (§3.8), so this
# is 0.10.0 rather than the 0.4.0 the def used to carry. nickelclock is rolling AND
# conflicted, which is the combination PackageRow's badge priority has to resolve:
# "conflict" outranks "rebuilt ↑" (packagerow.cc).
min_kpm = "0.10.0"
update_check = "rolling"
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
# The blocker named here is seeded into state.json ONLY (no registry entry, no
# packages.d def), so it blocks while being invisible to search — the
# removed-but-not-uninstalled case of §4.3. The UI must fall back to the raw id.
conflicts = ["ghostmod"]

# The rolling fixture with NOTHING else on it: kobocloud republishes under the
# permanently constant tag "latest", so check reports update=true with latest
# EQUAL to installed and the badge must read "latest rebuilt ↑" rather than
# "latest → latest ↑" (§6.3). Modelled on the real KoboCloud, the package feature A
# exists for. update_check requires min_kpm (§3.8).
[packages.kobocloud]
name = "KoboCloud"
source = "github.com/fsantini/KoboCloud"
forge = "github"
asset = "KoboRoot.tgz"
min_kpm = "0.10.0"
update_check = "rolling"
description = "Sync books from cloud storage. Republishes under a rolling tag."
homepage = "https://github.com/fsantini/KoboCloud"

[packages.nickelnote]
name = "NickelNote"
source = "github.com/kbanh/NickelNote"
forge = "github"
asset = "KoboRoot.tgz"
description = "Show a custom note and stylesheet on the sleep and PIN screens."
homepage = "https://github.com/kbanh/NickelNote"
# Both halves of this pair are installed, so it is REPORTED and never blocked
# (§4.5): every action stays available on both detail pages. nickelclock declares
# nothing — the reverse direction is what surfaces it there.
conflicts = ["nickelclock"]
# tested_fw is advisory registry-only metadata (D): the newest firmware this
# release was confirmed working on. Seeded OLDER than the sandbox's .kobo/version
# firmware (below) so search reports fw_untested=true and the detail page shows
# the "Untested on your firmware" warning — the firmware-badge fixture.
tested_fw = "4.38.23429"

# plato: the app-tree fixture (APP-TREE.md §2). Its release zip is a raw directory
# tree, so kpm extracts it into .adds/plato itself and it is live with NO reboot —
# the whole point of install_method in the UI contract (§11.1). Modelled on the real
# Plato: a flat zip (no "root" prefix), a location-independent plato.sh, and a
# NickelMenu drop-in kpm writes so the app is launchable after install (§8.2).
#
# min_kpm is deliberately NOT set here even though §12.3 makes it mandatory on a
# real registry entry: the sim's kpm is built as version "dev", which compares BELOW
# any min_kpm, so search would report min_kpm_ok=false and the detail page would
# render the "Requires kpm >= …" branch instead of the Install/Update button this
# fixture exists to exercise.
[packages.plato]
name = "Plato"
source = "github.com/baskerville/plato"
forge = "github"
asset = "plato-*.zip"
description = "A document reader for e-ink devices, installed as a directory tree — no reboot needed."
homepage = "https://github.com/baskerville/plato"

  [packages.plato.install]
  method = "app-tree"
  dest = "/mnt/onboard/.adds/plato"

    [packages.plato.install.launcher]
    nm_file = "/mnt/onboard/.adds/nm/plato"
    nm_entry = "menu_item:main:Plato:cmd_spawn:quiet:exec /mnt/onboard/.adds/plato/plato.sh"

# kf-core-fonts: the font fixture (the fonts plan §3.10). Fonts reuse the app-tree
# method unchanged — the ebook-fonts Kobo zips are flat archives of .ttf files — so
# the ONLY thing that makes this a font is where it installs, which is exactly what
# D1 derives the kind from. A kpm that predates the content-dir policy drops this def
# here with a warning on stderr (the plan's §1) and the packages.d def seeded below
# still carries the row, which is also what a real device sees before it updates.
#
# min_kpm is deliberately unset for the same reason as plato: the sim's kpm is built
# as version "dev", which compares BELOW any min_kpm, so search would report
# min_kpm_ok=false and the detail page would render "Requires kpm >= …" instead of
# the action button. A real registry entry sets min_kpm = "0.12.0" (§3.11).
[packages.kf-core-fonts]
name = "KF Core fonts"
source = "github.com/nicoverbruggen/ebook-fonts"
forge = "github"
asset = "kobo-core-fonts.zip"
description = "Reading fonts tuned for Kobo's kepub renderer. Installs into the fonts folder. Reboot after installing or updating."
homepage = "https://github.com/nicoverbruggen/ebook-fonts"

  [packages.kf-core-fonts.install]
  method = "app-tree"
  dest = "/mnt/onboard/fonts"

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

# kpm's own registry def: it carries a real source/forge/asset so the merged
# manifest offers a kpm package (the enrol source, per kpm-self-enrol-plan §6).
# The sandbox ships a SOURCELESS packages.d/kpm.toml with an empty state source,
# so search reports is_self=true, self_configured=false (the un-adopted self row).
# adopt-self reads THIS def to wire up the source — the --exercise-enroll target.
[packages.kpm]
name = "kpm"
source = "github.com/wolffshots/kpm"
forge = "github"
asset = "KoboRoot.tgz"
description = "The Kobo package manager itself — enable self-update to get new kpm releases here."
homepage = "https://github.com/wolffshots/kpm"
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

// platoInstallTable is plato's [install] block, appended verbatim to the
// packages.d def config.Save writes.
//
// Written as RAW TOML on purpose. The Go side of 0.11.0 (config.Install and the
// install_method JSON field) is being built in parallel with this fixture, and
// seed.go is compiled by the Go lane's own `go test ./...`; referencing a struct
// whose fields are still moving would break that build every time it moved. TOML
// text costs this file nothing and is what kpm actually parses, so the local
// snapshot is the same shape a real `kpm install plato` would leave behind — one
// that older kpm binaries simply ignore (plain toml.Unmarshal, no Undecoded check,
// APP-TREE.md §12.3).
const platoInstallTable = `
[install]
method = "app-tree"
dest = "/mnt/onboard/.adds/plato"

  [install.launcher]
  nm_file = "/mnt/onboard/.adds/nm/plato"
  nm_entry = "menu_item:main:Plato:cmd_spawn:quiet:exec /mnt/onboard/.adds/plato/plato.sh"
`

// The extracted plato tree: a launcher script, one shipped stylesheet, and the
// NickelMenu drop-in kpm writes (§8.2). All three are in the recorded manifest, so
// `kpm uninstall plato` is a real manifest delete against the sandbox.
const platoLauncherScript = `#!/bin/sh
DIR="$(dirname "$0")"
exec "$DIR/plato"
`

const platoShippedCSS = `@page { margin: 0; }
body { text-align: justify; }
`

const platoNmEntry = "menu_item:main:Plato:cmd_spawn:quiet:exec /mnt/onboard/.adds/plato/plato.sh\n"

// fontsInstallTable is kf-core-fonts' [install] block, appended verbatim to the
// packages.d def for the same reason plato's is (see platoInstallTable): seed.go is
// compiled by the Go lane's own `go test`, and raw TOML is what kpm parses anyway.
const fontsInstallTable = `
[install]
method = "app-tree"
dest = "/mnt/onboard/fonts"
`

// The fonts kpm extracted into the shared content directory. Content, not shape, is
// what matters here — nothing in the sim parses a TTF — so these are marker files
// whose only job is to exist before an uninstall and be gone after it.
const fontPayload = "KF Core font (fixture) — not a real TTF.\n"

// userFontPayload is a font the USER copied onto the device by hand. It is NOT in
// kf-core-fonts' manifest, so uninstall must leave it alone (D4): /mnt/onboard/fonts
// is shared by every font package and by the user, and "kpm deletes only what it
// installed" is the whole reason a shared directory is safe to install into.
const userFontPayload = "A font the user copied in by hand. kpm never touches it.\n"

// platoUserSettings is NOT in plato's manifest. KOReader and Plato both keep user
// data inside their own install tree (APP-TREE.md §9.1), so the rule that makes
// updates safe is "kpm deletes only what it installed" — and this file is the
// fixture's standing proof: it must survive an update and a plain uninstall alike.
const platoUserSettings = `# Written by Plato, never by kpm.
[home]
summary-size = 2
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

// appendRaw appends literal text to a file config.Save has already written. Used
// for the one table this fixture states as TOML rather than as a Go struct — see
// platoInstallTable.
func appendRaw(path, extra string) {
	b, err := os.ReadFile(path)
	must(err)
	must(os.WriteFile(path, append(b, extra...), 0o644))
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

	// koreader: an ORPHANED def — registered in packages.d with NO state entry.
	// That is precisely what the hook's two-step Install leaves behind when the
	// second command fails (`install` writes the def, `update` fetches; a network
	// drop or a checksum mismatch ends it there — INTEGRITY.md §4.3). Because
	// nickelclock is installed and koreader conflicts with it, koreader can no
	// longer be installed either, so the detail page has no Install and no
	// Uninstall: without a way to drop the registration it is a dead end, and every
	// `kpm update --all` skips it forever. The --exercise-conflicts target for the
	// "Drop registration" button (`kpm remove`).
	must(config.Save(p.PackageFile("koreader"), &config.Package{
		Name:      "KOReader",
		Source:    "github.com/koreader/koreader",
		Forge:     config.ForgeGitHub,
		Asset:     "koreader-kobo-*.zip",
		Registry:  "main",
		Conflicts: []string{"nickelclock"},
	}))

	// kobocloud: installed, rolling, and conflict-free — the clean §6.3 fixture.
	// Its local def mirrors the registry's update_check so the local snapshot is
	// coherent (search reads `rolling` off the registry entry either way).
	must(config.Save(p.PackageFile("kobocloud"), &config.Package{
		Name:        "KoboCloud",
		Source:      "github.com/fsantini/KoboCloud",
		Forge:       config.ForgeGitHub,
		Asset:       "KoboRoot.tgz",
		Registry:    "main",
		MinKpm:      "0.10.0",
		UpdateCheck: config.UpdateCheckRolling,
	}))

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

	// plato: the app-tree fixture (APP-TREE.md §11). Installed, registry-managed, and
	// carrying the [install] table that makes `kpm search --json` report
	// install_method = "app-tree" for it — the field the whole hook-side feature
	// keys off. The struct write covers every field config.Package models; the
	// [install] table is appended as TOML (see platoInstallTable).
	must(config.Save(p.PackageFile("plato"), &config.Package{
		Name:     "Plato",
		Source:   "github.com/baskerville/plato",
		Forge:    config.ForgeGitHub,
		Asset:    "plato-*.zip",
		Registry: "main",
	}))
	appendRaw(p.PackageFile("plato"), platoInstallTable)
	// The tree kpm extracted, plus the drop-in it wrote. These are the manifest
	// members, so an uninstall really deletes them out of the sandbox.
	writeDev(sysroot, "/mnt/onboard/.adds/plato/plato.sh", platoLauncherScript)
	writeDev(sysroot, "/mnt/onboard/.adds/plato/css/epub.css", platoShippedCSS)
	writeDev(sysroot, "/mnt/onboard/.adds/nm/plato", platoNmEntry)
	// ...and one file kpm never installed, sitting in the same directory.
	writeDev(sysroot, "/mnt/onboard/.adds/plato/Settings.toml", platoUserSettings)

	// kf-core-fonts: the font fixture (§3.10). Installed, registry-managed, and
	// carrying the [install] table that puts it in /mnt/onboard/fonts — the content
	// directory `kind = "font"` is derived from (D1), which is what splits it out of
	// the Mods tab and into Fonts.
	must(config.Save(p.PackageFile("kf-core-fonts"), &config.Package{
		Name:     "KF Core fonts",
		Source:   "github.com/nicoverbruggen/ebook-fonts",
		Forge:    config.ForgeGitHub,
		Asset:    "kobo-core-fonts.zip",
		Registry: "main",
	}))
	appendRaw(p.PackageFile("kf-core-fonts"), fontsInstallTable)
	// The two fonts kpm extracted (both in the manifest below), and one the user
	// copied in by hand, which is not. All three live in the same shared directory.
	writeDev(sysroot, "/mnt/onboard/fonts/KFCore-Regular.ttf", fontPayload)
	writeDev(sysroot, "/mnt/onboard/fonts/KFCore-Bold.ttf", fontPayload)
	writeDev(sysroot, "/mnt/onboard/fonts/UserFont-Regular.ttf", userFontPayload)

	// No dictionary fixture: nothing emits kind = "dictionary" in 0.12.0 (D8), and a
	// `method = "file"` def would not parse. The Dictionaries tab is asserted EMPTY,
	// with its work-in-progress note.

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

	// kpm's own un-adopted self row (kpm-self-enrol-plan §6): ship the SOURCELESS
	// packages.d/kpm.toml exactly as a real KoboRoot.tgz does (name/asset only —
	// source/forge/pin live in state.json once adopted, build/package.go's selfToml).
	// With an EMPTY state source (set below) search reports is_self=true,
	// self_configured=false — the "self-update off" badge + the Enable button target.
	// The sim's kpm binary is built as version "dev", so seedSelf/migrateSelfSource
	// never auto-adopt it (a dev binary never migrates source or reconciles version).
	must(config.Save(p.PackageFile("kpm"), &config.Package{
		Name:  "kpm",
		Asset: "KoboRoot.tgz",
	}))

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
	// kobocloud's tag never moves — that is what update_check = "rolling" is for.
	st.Get("kobocloud").InstalledVersion = "latest"
	// ghostmod exists ONLY here: no registry entry, no packages.d def. It is
	// therefore absent from `kpm search --json` while installedOrStaged() still
	// reports it, which is exactly how a `kpm remove`d-but-not-uninstalled package
	// keeps blocking (§4.3) — and the case the UI must not render as "Manage " with
	// nothing after it.
	st.Get("ghostmod").InstalledVersion = "v1.0.0"
	// kpm installed but NOT adopted: an installed version with an EMPTY Source is
	// the un-adopted self row (self_configured=false). adopt-self fills Source from
	// the registry def — the --exercise-enroll assertion (state gains kpm.source).
	st.Get("kpm").InstalledVersion = "0.7.0"
	// plato: installed with the manifest an app-tree extraction records (tar-style,
	// no leading slash — APP-TREE.md §5.2 step 6, the same convention tgz.Verify
	// produces and internal/uninstall consumes). Settings.toml is deliberately
	// absent from it: that is the whole preservation rule (§9.1).
	pl := st.Get("plato")
	pl.InstalledVersion = "0.9.45"
	pl.Manifest = []string{
		"mnt/onboard/.adds/plato/plato.sh",
		"mnt/onboard/.adds/plato/css/epub.css",
		"mnt/onboard/.adds/nm/plato",
	}
	// kf-core-fonts: installed, with a manifest naming ONLY the two fonts kpm
	// extracted (tar-style, no leading slash). UserFont-Regular.ttf is deliberately
	// absent from it — that absence is the entire uninstall proof (D4), and the
	// content directory itself is never a manifest member, so no package owns it.
	kf := st.Get("kf-core-fonts")
	kf.InstalledVersion = "v2026.08.05"
	kf.Manifest = []string{
		"mnt/onboard/fonts/KFCore-Regular.ttf",
		"mnt/onboard/fonts/KFCore-Bold.ttf",
	}
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
	fmt.Println("  packages: koreader(registered, NOT installed) kobocloud(latest, rolling) nickelclock(v0.4.0, rolling) nickelmenu(not-installed) nickelnote(v1.2.0) plato(0.9.45, app-tree) samplemod(v1.0.0) kf-core-fonts(v2026.08.05, font) kpm(0.7.0, un-adopted) ghostmod(v1.0.0, state-only)")
	fmt.Println("  conflicts: koreader->nickelclock (blocked install) · nickelnote<->nickelclock (installed pair) · nickelmenu->ghostmod (invisible blocker)")
	fmt.Println("  orphaned def: koreader (packages.d def, no state entry — the 'Drop registration' target)")
	fmt.Println("  rolling: kobocloud (clean 'rebuilt' badge) · nickelclock (rolling + conflict — badge priority)")
	fmt.Println("  uninstall target: samplemod ->", hostFile)
	fmt.Println("  config targets: nickelclock(settings.ini, ini) nickelnote(3 templates, text)")
	fmt.Println("  sync target: samplemod (local def missing the registry's [[configs]])")
	fmt.Println("  files-missing badge: samplemod (seeded MissingFiles)")
	fmt.Println("  firmware badge: nickelnote (tested_fw 4.38 < device 4.45)")
	fmt.Println("  self-enrol target: kpm (un-adopted: sourceless kpm.toml + empty state source; registry offers kpm)")
	fmt.Println("  app-tree target: plato ([install] method=app-tree, dest=/mnt/onboard/.adds/plato, nm drop-in; Settings.toml is user data, not in the manifest)")
	fmt.Println("  font target: kf-core-fonts (dest=/mnt/onboard/fonts => kind=font; UserFont-Regular.ttf is the user's, not in the manifest)")
	fmt.Println("  dictionaries: none — nothing emits kind=dictionary in 0.12.0 (D8), so that tab shows its work-in-progress note")
}
