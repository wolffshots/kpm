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
./run.sh --screenshot out       # browse/detail + config-list/config-edit/config-files png's
```

Offscreen end-to-end uninstall (drives DetailDialog -> confirm -> `kpm uninstall`):

```sh
./run.sh --exercise-uninstall samplemod
# then: state.json no longer lists samplemod, its packages.d def and its file
#       under $KPM_SYSROOT are gone.
```

Offscreen end-to-end config edit (drives DetailDialog -> Settings -> ConfigDialog
-> edit a row -> Save -> `kpm config set`):

```sh
./run.sh --exercise-config nickelclock
# opens the ini editor, flips [Clock] Enabled, then byte-compares settings.ini
# before/after and asserts the edit was SURGICAL — exactly one line changed
# (exit 0 = PASS; a non-surgical rewrite or a missing button fails non-zero).
```

The sandbox lives at `/tmp/kpm-sim-sandbox` (override with `SANDBOX=...`; keep an
existing one with `RESEED=0`). `KPM_SYSROOT` points into it, so sim-driven
uninstalls never touch the real filesystem.

## Seeded fixture (offline)

`kpm search --json` returns four packages in mixed states:

| id | state | detail action |
|----|-------|---------------|
| koreader | not installed | Install |
| nickelclock | installed v0.4.0 | Uninstall (marker-remove) + Settings (ini config — the config-edit target) |
| nickelmenu | not installed | Install |
| nickelnote | installed v1.2.0 | Settings (three text templates; `pin.template` absent → the create path) |
| samplemod | installed v1.0.0 | Uninstall (manifest delete — the uninstall exercise target) |

## Which of the ten UI actions work end-to-end

The ten commands the hook issues (`hook/src/kpmprocess.cc`):

| action | works in sim | notes |
|--------|:---:|-------|
| **search** (browse) | ✅ | offline, read-only; drives the whole browse/detail view |
| **uninstall** | ✅ | offline mutation; confined to `KPM_SYSROOT`; validated end-to-end |
| **config list / show** | ✅ | offline reads; drive the ConfigDialog file picker + entries |
| **config set** | ✅ | offline mutation; confined to `KPM_SYSROOT`; `--exercise-config` byte-verifies the surgical write |
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
"update available" badges (those come only from a live `kpm check`).

## Fidelity gaps (out of scope by design)

- **Nickel styling / e-ink look**: the sim uses plain large black-on-white Qt,
  not Kobo's real widget theme or e-ink rendering. Layout/behavior is faithful;
  pixels are not.
- **Menu injection**: the "Package manager" More-menu entry
  (`nkpm.cc _nkpm_menu_hook`) is device-only; the sim opens `BrowseDialog`
  directly.
- **Wi-Fi gate**: always reports connected, so the connect spinner / failure
  dialog paths aren't exercised.
- **Keyboard**: the desktop's real keyboard types into the search box; Return
  commits (drives `commitRequested()`). The on-device virtual keyboard is a no-op.
- Build artifacts (`build/`, `nkpm-sim`, `shots/`) and the `/tmp` sandbox are
  untracked.

## Device-source note

The sim requires exactly **one** guarded edit to a device source,
`hook/src/files.h`: a `#ifdef NKPM_SIM` block that resolves the kpm binary from
`$NKPM_KPM`. Include-path shadowing cannot work here because `src/*.cc` include
`"files.h"` as a quote-include that always resolves to `src/files.h` first. The
block is inert on device (`hook/Makefile` never defines `NKPM_SIM`).
