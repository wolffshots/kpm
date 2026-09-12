# NickelKPM desktop simulator (Tier-3 off-device UI testing)

Runs the **real** NickelKPM dialog sources (`hook/src/browsedialog.cc`,
`detaildialog.cc`, `configdialog.cc`, `kpmprocess.cc`, `widgets/*.cc`) on a PC
against a host-built `kpm` binary, so the on-device UI flows can be exercised
without the build-copy-reboot device loop.

This is possible because the dialog code never includes a Nickel header: every
Nickel touchpoint goes through function pointers declared in `hook/src/nkpm.h`.
On device `hook/src/nkpm.cc` fills those pointers via `dlsym` against libnickel;
here `sim/nickelstub.{h,cc}` fills them with plain-Qt widgets instead. The device
sources are compiled **unmodified**.

## Layout

| File | Role |
|------|------|
| `NickelHook.h` | Stub of the device mod-loader header — provides only `nh_log` (stderr). Wins via include order (`-I sim`, angle-include). |
| `nickelstub.{h,cc}` | Desktop implementations of every `nkpm.h` pointer + `construct_*`/`rebootDevice`. Fixed-size Kobo-portrait window, N3Dialog/Confirmation/error dialogs, always-connected Wi-Fi, no-op keyboard. |
| `main.cc` | `QApplication`, installs the stubs, opens `BrowseDialog`. Flags below. |
| `Makefile` | Standalone Qt5 build (WSL Ubuntu). Not `-Werror`; deprecation warnings suppressed for the sim only. |
| `fixture/seed.go` | Seeds an offline `KPM_ROOT`/`KPM_SYSROOT` sandbox with packages in mixed states. Imports kpm's own `internal/*`. |
| `run.sh` | Builds everything, seeds the sandbox, launches the sim. |

## Prerequisites

- **WSL Ubuntu** with Qt5: `qtbase5-dev qtbase5-dev-tools` (pulls `g++`, `make`,
  `moc`, and the `offscreen` QPA plugin). Install as root:
  `wsl -d Ubuntu -u root -- bash -lc 'apt-get update && apt-get install -y qtbase5-dev qtbase5-dev-tools'`
- **Go** to cross-build the linux `kpm` + seed binaries — either `go` inside WSL,
  or Windows `go.exe` (run.sh drives it through `cmd.exe` so `GOOS=linux` crosses
  the interop boundary).

## Build

```sh
cd hook/sim
make                       # -> ./nkpm-sim   (Qt5, offscreen-capable)
```

`run.sh` also (re)builds `build/kpm` (linux, `go build ./cmd/kpm`) and
`build/seed`, then seeds the sandbox — you normally just run it.

## Run

Interactive window (WSLg shows a real window on Win11):

```sh
./run.sh                        # default 758x1024 Kobo portrait
./run.sh --size 600x800         # override size
```

Offscreen screenshots (automated validation — needs `QT_QPA_PLATFORM=offscreen`,
which run.sh sets automatically for these modes):

```sh
./run.sh --screenshot out       # browse/detail + detail-fw-untested + config-list/config-edit/config-files/config-init + detail-enroll png's
```

`browse.png` shows samplemod's top-priority **files missing** badge (A) and the
un-adopted kpm row's **self-update off** badge; `detail-fw-untested.png` shows
nickelnote's **Untested on your firmware** warning line (D); and
`detail-enroll.png` shows the kpm detail page's self-update advisory line plus the
**Enable self-update** button — all driven by the seeded fixture below.

Offscreen end-to-end uninstall (drives DetailDialog -> confirm -> `kpm uninstall`):

```sh
./run.sh --exercise-uninstall samplemod
# then: state.json no longer lists samplemod, its packages.d def and its file
#       under $KPM_SYSROOT are gone.

./run.sh --exercise-uninstall kf-core-fonts
# the offline end-to-end proof of per-file ownership in a shared content directory
# (fonts plan D4), plus the font detail page's copy. Asserts: nothing on the page
# promises "(no reboot)"; the confirmation says the files go NOW, not on reboot; both
# manifest fonts are deleted while UserFont-Regular.ttf (the user's, not in the
# manifest) and the fonts directory itself survive; and the post-action prompt —
# raised from kpm's own uninstall payload — is the content sentence rather than
# "Changes staged" (exit 0 = PASS; 5 = a "(no reboot)" promise, 7 = wrong removal
# wording, 8 = a manifest font survived, 9 = the directory went, 10 = the user's font
# was deleted, 11 = the wrong post-action prompt).
```

Offscreen end-to-end config edit (drives DetailDialog -> Settings -> ConfigDialog
-> edit a row -> Save -> `kpm config set`):

```sh
./run.sh --exercise-config nickelclock
# opens the ini editor, flips [Clock] Enabled, then byte-compares settings.ini
# before/after and asserts the edit was SURGICAL — exactly one line changed
# (exit 0 = PASS; a non-surgical rewrite or a missing button fails non-zero).
```

Offscreen end-to-end config init (drives DetailDialog -> Settings -> the
multi-file picker -> Open the un-created PIN template -> **Create from example** ->
`kpm config init`):

```sh
./run.sh --exercise-init
# taps "Create from example" on nickelnote's not-created pin.template, asserts the
# seeded file equals the normalized template bytes EXACTLY, then edits one line and
# Save and asserts the follow-up edit was surgical (exit 0 = PASS).
```

Offscreen end-to-end sync (drives the browse footer's **Sync** button ->
`kpm sync`):

```sh
./run.sh --exercise-sync
# taps Sync, then asserts (a) samplemod's packages.d def gained the [[configs]]
# table the registry cache declares but its local def predated, and (b) a fresh
# `kpm search --json` reports has_config=true for samplemod (exit 0 = PASS; a
# missing button or an un-synced def fails non-zero).
```

Offscreen end-to-end self-enrol (drives DetailDialog -> **Enable self-update** ->
`kpm adopt-self`):

```sh
./run.sh --exercise-enroll
# opens the un-adopted kpm detail, taps "Enable self-update", rejects the post-enrol
# "Check now?" prompt (Not now — a check needs the network), then asserts (a) state.json
# gained packages.kpm.source and (b) a fresh `kpm search --json` reports
# self_configured=true for kpm (exit 0 = PASS; a missing button or an un-adopted state
# fails non-zero). Works offline: adopt-self's best-effort refresh no-ops / soft-fails on
# the warm pre-seeded registry cache, then adopts from the cache.
```

Offscreen conflicts UI (INTEGRITY.md §4/§6.1), driven by **real `kpm search
--json` output** — the sandbox declares the conflicts and the rolling defs
(`fixture/seed.go`) and the shipped kpm computes `conflicts` /
`conflicts_active` / `rolling` from them, so a divergence between what the Go core
emits and what the hook consumes fails here rather than on a device:

```sh
./run.sh --exercise-conflicts out
# Asserts, in order:
#   §6.1  four "conflict" badges straight off `kpm search --json` — the blocked
#         package, BOTH halves of the already-installed pair (the relation is
#         symmetric, §4.2, so nickelclock reports a conflict it never declared),
#         and the unresolvable-blocker case below
#   §6.3  feeding a CHECK-shaped payload (latest/update only — check is the one
#         thing that still needs the network) through BrowseDialog::onCheckDone
#         badges kobocloud "latest rebuilt ↑", which can only happen if the real
#         mergeCheck preserved the search payload's `rolling`
#   §6.1  nickelclock is rolling AND rebuilt AND conflicted: its badge stays
#         "conflict", pinning the documented priority (packagerow.cc)
#   §4.3  a blocker that is NOT in the search list (a `kpm remove`d but never
#         uninstalled package still blocks) falls back to its raw id, so the
#         button reads "Manage ghostmod" rather than "Manage "
#   §4.3  routing to that unreachable blocker reports the miss AND keeps the page
#         that explained the conflict, with its button live again
#   §4.3  the blocked package's detail page shows the advisory, has NO Install
#         button (kpm hard-refuses it and --yes does not override), and offers a
#         "Manage NickelClock" route — the blocker's DISPLAY name, not its id
#   §4.4  following that route (tapped twice, as on an unresponsive e-ink screen)
#         lands ONCE on the blocker's own page, which reports the pair and offers
#         the Uninstall that actually clears the block
# Writes browse-conflict.png, detail-conflict.png and detail-conflict-pair.png into
# <dir>.
```

Offscreen failed-install trap — the skip notice and the orphaned def
(INTEGRITY.md §4.3/§6.4):

```sh
./run.sh --exercise-orphan out
# The hook's Install is two commands (`install` writes the def, `update` fetches),
# so a failed fetch leaves a package REGISTERED BUT NOT INSTALLED. If its
# conflicting counterpart is then installed, that package can be neither installed
# nor uninstalled, and every `kpm update --all` skips it for good. Asserts:
#   §6.4  an update-all payload carrying `skipped` renders one line per package in
#         the list, with kpm's own refusal sentence — not an error dialog, and not
#         silence (the command succeeded)
#   §4.3  the orphan's detail page offers "Drop registration", and tapping it
#         (through its confirmation) actually removes packages.d/koreader.toml —
#         a real `kpm remove` against the sandbox, the one hook command with no
#         --json mode
#   §4.3  a blocked package that was NEVER registered is offered no such button
#         (the gate is a real `kpm list --json` probe, not a guess)
# Writes browse-skipped.png and detail-orphan.png into <dir>.
```

Offscreen staged-blocker copy and route (INTEGRITY.md §4.3):

```sh
./run.sh --exercise-staged
# conflicts_active counts a blocker that is merely STAGED, so the advisory must not
# call it installed and the page the route lands on must not be a label with no
# buttons. The sandbox cannot stage anything offline (staging needs a download), so
# this is the ONE exercise that edits the real payload: it flips the blocker to
# staged and feeds it back through BrowseDialog::onSearch. Asserts the advisory says
# "staged to install at the next reboot" (never "installed package"), and that the
# blocker's staged page offers "Reboot now" — the action that actually resolves it.
```

Offscreen app-tree copy (APP-TREE.md §11.3) — the second install shape:

```sh
./run.sh --exercise-apptree out
# 0.11.0 adds a package shape that is extracted into .adds/<name>/ and is live with
# NO reboot. The hook's entire share of that is copy, so this reads strings off real
# widgets after a real `kpm search --json`. Asserts, in order:
#   §6.4   a MIXED "Update all" (plato extracted + nickelclock staged) is prompted
#          with what is ALREADY installed as well as what still needs the reboot —
#          never the bare "Changes staged", which would read as if nothing landed
#   §11.3  the app-tree detail page's action reads "Update (no reboot)"
#   §11.3  its Uninstall confirmation says the files go NOW, and never "on reboot"
#   §11.3  a tgz package (samplemod) keeps every 0.10.0 string: a plain "Update"
#          button and the "removed on reboot" confirmation
#   §11.3  tapping the app-tree Update shows "Installing…", not the generic "Working…"
# Writes detail-apptree.png and reboot-mixed.png (the prompt itself) into <dir>.
#
# The Go half of §11.1 (`install_method` on search --json) is built in a parallel
# lane. The exercise uses the field when the binary emits it and INJECTS it when it
# does not, printing which happened — so a green run never hides that the payload
# was synthesized. Everything past onSearch is device code either way.
```

Offscreen browse tabs (fonts plan §3.8) — Mods, Fonts and Dictionaries:

```sh
./run.sh --exercise-tabs out
# 0.12.0 splits the browse list by the `kind` field of `kpm search --json`, which kpm
# derives from where a package installs (D1). A tab switch runs no kpm command: it is
# the search-commit path (render(false)). This reads strings and widgets off the real
# view after a real `kpm search --json`. Asserts, in order:
#   (1) the view opens on Mods, on ONE page (no visible page label — the guard that
#       makes "kf-core-fonts is absent" mean filtered out, not paginated away), the
#       mods are listed, the font is not, and page-1 chrome shows all three of the
#       staged banner, its Reboot now button and the stale hint
#   (2) Fonts lists exactly kf-core-fonts and takes the active-tab style
#   (3) the switch keeps every page-1 chrome item, and a one-page tab shows no label
#   (4) Dictionaries lists nothing, shows "Dictionaries are a work in progress. A
#       later kpm release adds them.", and STILL carries the banner, the Reboot now
#       button and the stale hint — an empty tab must not take the only reboot
#       affordance in the browse view off screen with the rows
#   (5) with "kf" in the search box, Mods shows "No packages match your search." with
#       the chrome intact, and Fonts still shows kf-core-fonts: the text is global,
#       the filter is per tab
#   (6) a synthetic font-only `update --all` payload fed through onActionDone is
#       prompted with "Font changes take effect after a reboot. Reboot now?", never
#       "Changes staged" and never "installed and ready" (a font is install_method
#       "app-tree", so the live test alone would say the wrong one)
#   (7) that run also skipped a package, and the skip note is readable from the empty
#       Dictionaries tab
#   (8) a mixed run (font + app-tree mod + tgz) reports "1 package installed and
#       ready" and "2 more need a reboot" — the font counts with the tgz
# Writes browse-fonts.png and browse-dicts.png into <dir>.
#
# `kind` is never injected: it is the contract under test, so a kpm whose search emits
# no `kind` fails at step 1, and the run prints up front whether the field is present.
# Two things ARE injected, because an offline sandbox cannot produce them and they
# drive the page-1 chrome the empty-tab assertions are about: a staged count, and a
# registry last refreshed in 2020. There is no dictionary fixture — nothing emits
# kind = "dictionary" in 0.12.0 (D8).
#
# NOT asserted here, deliberately: that a switch destroys the previous tab's rows.
# The in-place repaint path is unreachable on a switch — render(false) never offers
# it, and repaintRowsInPlace independently declines on a row-count or id mismatch,
# which a tab switch always is — so the assertion could not fail and would only read
# as coverage. The guarantee lives in those two checks in browsedialog.cc, and step
# (2) already fails if a stale row is left listed.
```

Offscreen badge assertions (verifies the two Wave-3 reliability badges render):

```sh
./run.sh --exercise-badges
# asserts the browse list shows samplemod's "files missing" badge (A, seeded
# MissingFiles) and nickelnote's detail page shows "Untested on your firmware"
# (D, an older registry tested_fw than the seeded .kobo/version). Read-only, so
# the seeded MissingFiles never self-clears (exit 0 = PASS, missing label = fail).
```

Offscreen in-place row repaint (flash-reduction **win 4**: after an action, repaint
only the rows whose badge changed instead of rebuilding the page):

```sh
./run.sh --exercise-rowrepaint
# uninstalls samplemod through the detail page, then asserts (a) samplemod's badge
# became "not installed" (it starts at the seeded "files missing") and (b) EVERY
# PackageRow widget captured before the action is still alive — i.e. the acted-on
# row repainted in place instead of the page being rebuilt. The QPointer check is
# what makes this a real gate: with win 4 disabled it reports
# "6 of 6 rows were destroyed" and exits 8.
#
# The sim cannot show e-ink refreshes, so this proves the state-correctness half of
# win 4 (no stale row after a mutation), NOT that the flashing actually dropped —
# only the device can show that.
```

Offscreen end-to-end sleep/wake chrome guard (drives the status bar + nav-bar
hide/restore and the `ChromeGuard` that re-hides them after a wake re-show):

```sh
./run.sh --exercise-wake
# opens browse then detail, asserts the fake status bar + MainNavView are hidden
# (Dialog::hideChrome), simulates Nickel's wake re-show of both bars, asserts the
# ChromeGuard re-hid them, then closes the UI and asserts both are restored
# exactly once (exit 0 = PASS). The fake bars are zero-height, so this is visually
# inert and screenshots are unaffected.
```

The sandbox lives at `/tmp/kpm-sim-sandbox` (override with `SANDBOX=...`; keep an
existing one with `RESEED=0`). `KPM_SYSROOT` points into it, so sim-driven
uninstalls never touch the real filesystem.

## Seeded fixture (offline)

`kpm search --json` returns nine packages in mixed states (plus `ghostmod`, which
is seeded into `state.json` only and therefore never appears). Eight are mods and
show on the Mods tab. `kf-core-fonts` is the one font and shows on the Fonts tab.
The Dictionaries tab is always empty, because no fixture is a dictionary:

| id | state | detail action |
|----|-------|---------------|
| koreader | **registered but NOT installed** (packages.d def, no state entry) and blocked by nickelclock → the orphaned-def fixture | Manage NickelClock + **Drop registration** |
| kobocloud | installed `latest`, `update_check = "rolling"`, no conflicts | the clean §6.3 "rebuilt ↑" badge fixture |
| nickelclock | installed v0.4.0, `update_check = "rolling"`, half of the installed conflict pair | Uninstall (marker-remove) + Settings (ini config — the config-edit target); badge priority: conflict outranks "rebuilt ↑" |
| nickelmenu | not installed, blocked by `ghostmod` (installed, state-only ⇒ invisible to search) | Manage ghostmod (raw-id fallback) |
| nickelnote | installed v1.2.0 | Settings (three text templates; `pin.template` absent → the create path); registry def carries an old `tested_fw` (4.38) vs the seeded `.kobo/version` (4.45) → **firmware badge** ("Untested on your firmware") |
| plato | installed 0.9.45, **app-tree** | the only package whose def carries an `[install]` table (`method = "app-tree"`, `dest = /mnt/onboard/.adds/plato`, plus a NickelMenu drop-in) → `install_method = "app-tree"`, driving the `(no reboot)` action suffix, the `Installing…` busy label and the immediate-removal uninstall copy — the `--exercise-apptree` target. Its manifest names the extracted files and the drop-in; `Settings.toml` sits in the same directory and is deliberately **absent** from it (APP-TREE.md §9.1's preservation invariant) |
| samplemod | installed v1.0.0 | Uninstall (manifest delete — the uninstall target); registry-managed with a stale def missing the registry's `[[configs]]` — the **Sync** exercise target; seeded `MissingFiles` → **"files missing"** badge; also the tgz **control** for `--exercise-apptree` |
| kf-core-fonts | installed v2026.08.05, **font** | the font fixture: its def carries `[install]` with `method = "app-tree"` and `dest = /mnt/onboard/fonts`, so `kind = "font"`. The Fonts tab lists it, it takes the plain "Update" label (never "(no reboot)"), and a run that changes only this package gets the content reboot sentence (`--exercise-tabs`). Its manifest names two `.ttf` files. `UserFont-Regular.ttf` sits next to them and is **not** in the manifest, so `--exercise-uninstall kf-core-fonts` must keep it (D4). The registry def has no `min_kpm`, for the same reason as plato. A kpm older than the content-dir policy drops the registry def with a warning, and the packages.d def still carries the row |
| kpm | installed 0.7.0 | un-adopted self row: sourceless `packages.d/kpm.toml` + empty state source (the registry offers a kpm def with a real source) → `is_self=true`, `self_configured=false` → **"self-update off"** badge + **Enable self-update** button — the `--exercise-enroll` target |

## Which of the fifteen UI actions work end-to-end

The fifteen commands the hook issues (`hook/src/kpmprocess.cc`):

| action | works in sim | notes |
|--------|:---:|-------|
| **search** (browse) | ✅ | offline, read-only; drives the whole browse/detail view |
| **list** | ✅ | offline read-only probe: the only command that reports which ids are *registered*, used to detect an orphaned def (`--exercise-orphan`) |
| **remove** | ✅ | offline mutation; the one command with **no `--json` mode**, so `KpmProcess` runs it with `expectsPayload=false` and treats exit 0 with no `BEGIN_JSON` as success; `--exercise-orphan` verifies the def file is gone |
| **uninstall** | ✅ | offline mutation; confined to `KPM_SYSROOT`; validated end-to-end |
| **adopt-self** | ✅ | offline mutation (best-effort refresh no-ops / soft-fails on the warm cache, then adopts from it); `--exercise-enroll` verifies kpm gains `self_configured=true` |
| **sync** | ✅ | offline mutation; reads the registry cache + rewrites packages.d; `--exercise-sync` verifies samplemod gains its config |
| **config list / show** | ✅ | offline reads; drive the ConfigDialog file picker + entries |
| **config set** | ✅ | offline mutation; confined to `KPM_SYSROOT`; `--exercise-config` byte-verifies the surgical write |
| **config init** | ✅ | offline mutation; seeds a missing file from its template; `--exercise-init` asserts byte-exact seed + a surgical follow-up edit |
| install | ⚠️ | `kpm install --yes` registers the def offline (works), but DetailDialog chains `kpm update`, which needs the network — the fetch fails and the error dialog shows |
| check | ❌ | needs network |
| registry refresh | ❌ | needs network |
| update / update --all | ❌ | needs network (fetches release assets) |

**Why the network flows don't run locally.** The stock `kpm` binary is
**https-only and trusts an embedded CA bundle** (`internal/forge/http.go` refuses
non-https URLs and builds its transport from `internal/forge/cacert.pem`). A local
fixture forge cannot present a certificate that bundle trusts, and the embedded
pool ignores `SSL_CERT_FILE`, so the update/check/refresh fetches can't be
satisfied without **patching the binary** — out of scope for a sim that runs the
real release build. kpm's own `cmd/kpm/uicontract_test.go` exercises those flows
by injecting a test `*http.Client` into the in-process `App`, which the compiled
CLI has no hook for. So the sim seeds state **offline** (via `fixture/seed.go`,
importing `internal/*`) rather than through a fixture server.

The browse list therefore shows installed / not-installed states but **not**
"update available" badges (those come only from a live `kpm check`). The conflict
and rolling *declarations* are a different matter: those are registry/state facts,
they are seeded, and the real kpm computes `conflicts_active` / `rolling` from
them — so only `check`'s latest/update pair is ever synthesized, and only by
`--exercise-conflicts`, in `check`'s own shape through the real `mergeCheck`.

## Fidelity gaps (out of scope by design)

- **Nickel styling / e-ink look**: the sim uses plain large black-on-white Qt,
  not Kobo's real widget theme or e-ink rendering. Layout/behavior is faithful;
  pixels are not. The one Nickel style property the sim does back is
  `primaryButton` (dark fill, light text, in `nickelstub.cc`). The active browse
  tab depends on it, so without it the tab restyle would not show offscreen. The
  sim runs Qt 5.15, so whether the unpolish/polish restyle works on Kobo's Qt 5.2.1
  is device test T8.
- **Menu injection**: the "Package manager" More-menu entry
  (`nkpm.cc _nkpm_menu_hook`) is device-only; the sim opens `BrowseDialog`
  directly.
- **Wi-Fi gate**: always reports connected, so the connect spinner / failure
  dialog paths aren't exercised.
- **Keyboard**: the desktop's real keyboard types into the search box; Return
  commits (drives `commitRequested()`). The on-device virtual keyboard is a no-op.
- Build artifacts (`build/`, `nkpm-sim`, `out/`) and the `/tmp` sandbox are
  untracked.

## Device-source note

The sim requires exactly **one** guarded edit to a device source,
`hook/src/files.h`: a `#ifdef NKPM_SIM` block that resolves the kpm binary from
`$NKPM_KPM`. Include-path shadowing cannot work here because `src/*.cc` include
`"files.h"` as a quote-include that always resolves to `src/files.h` first. The
block is inert on device (`hook/Makefile` never defines `NKPM_SIM`).
