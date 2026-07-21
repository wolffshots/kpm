# kpm — Kobo Package Manager

A single static Go binary that manages third-party software on Kobo e-readers
(firmware 4.x). Packages are distributed as `KoboRoot.tgz` release assets on git
forges (GitHub, Codeberg, or any Gitea/Forgejo instance). kpm registers release
sources, checks for updates, stages installs into the firmware's single boot-time
install slot, pins versions, keeps a human-readable log, and updates itself the
same way. It is driven from NickelMenu on-device and works as a normal CLI over
telnet/SSH.

> Firmware 4.x only. No signature verification of assets — installing a package
> trusts its author, exactly like a manual `KoboRoot.tgz` install. Assets are
> fetched over TLS (HTTPS only; a redirect that downgrades to plain HTTP is
> refused) with an embedded Mozilla CA bundle. Archives are walked before
> staging: absolute/`..` paths, symlink/hardlink entries whose target escapes the
> archive, device/FIFO entries, and setuid/setgid files are rejected, and no
> package but kpm itself may write kpm's own install tree.

## How it works

Kobo's boot script extracts `/mnt/onboard/.kobo/KoboRoot.tgz` over the root
filesystem once per reboot, then deletes it. kpm never installs files itself: it
downloads each package's `KoboRoot.tgz`, **merges all pending packages into one**
staged archive, and reboots. On the next run it notices the staged archive is
gone and promotes those packages from *staged* to *installed*.

Everything kpm owns lives under `/mnt/onboard/.adds/kpm/` so you can inspect or
repair it over USB:

```
/mnt/onboard/.adds/kpm/
  bin/kpm              the static ARM binary
  packages.d/          one TOML file per registered package (kpm.toml = itself)
  state.json           installed/staged versions, manifests, last check
  kpm.log              append-only history
  status.txt           last summary (shown by the NickelMenu Status dialog)
  cache/               package downloads (*.tgz/*.part, swept selectively) and
                       registry caches (registry-<name>.toml, never swept)
/mnt/onboard/.adds/nm/kpm   NickelMenu drop-in
```

## Install

kpm is installed the same way it installs everything else: a `KoboRoot.tgz`
goes into the firmware's boot-time install slot and the next reboot installs
it. Grab the archive from the
[latest release](https://github.com/wolffshots/kpm/releases/latest) or build it
yourself:

```
go run ./build          # produces dist/kpm and dist/KoboRoot.tgz
```

Get it into the slot either way:

- **Over USB** — copy the archive into the hidden install slot, then eject;
  the Kobo reboots and installs it on boot:

  ```
  cp dist/KoboRoot.tgz /media/<you>/KOBOeReader/.kobo/KoboRoot.tgz
  ```

- **Over SSH** — if the device already has SSH access:

  ```
  scp dist/KoboRoot.tgz root@<kobo>:/mnt/onboard/.kobo/KoboRoot.tgz
  ssh root@<kobo> reboot
  ```

The binary installs to `/mnt/onboard/.adds/kpm/bin/kpm`, which is not on the
device's `PATH` — over SSH/telnet run it by full path or add it to your shell:

```
export PATH="$PATH:/mnt/onboard/.adds/kpm/bin"
```

[NickelMenu](https://github.com/pgaskin/NickelMenu) is **optional but
recommended**: with it kpm gets on-device menu entries (see "NickelMenu
usage"); without it kpm is a normal CLI over SSH/telnet. NickelDBus (`qndb`)
is also optional — if present, kpm shows toasts; if not, use the **Status**
menu entry. What to do next depends on which side you start from:

### Already running NickelMenu

kpm's menu entries appear after the install reboot. Register the mods you
already have so kpm *tracks* them instead of thinking they need a reinstall —
seed each one with the version currently on the device:

```
kpm registry add https://github.com/wolffshots/kobo-registry
kpm registry refresh
kpm install nickelmenu --installed v0.6.0 --yes    # the version you have
```

A hand-added `kpm add https://github.com/pgaskin/NickelMenu --installed v0.6.0`
works too, but the registry def also carries the curated uninstall recipe.

### No NickelMenu yet (SSH-first install)

kpm works headless, so use it to install NickelMenu:

```
kpm registry add https://github.com/wolffshots/kobo-registry
kpm registry refresh
kpm install nickelmenu --yes
kpm update nickelmenu --reboot
```

The reboot installs NickelMenu, and kpm's menu entries appear with it.

### Self-update

kpm ships `packages.d/kpm.toml` with an **empty `source`**, so self-update is
"not configured": `check`/`update` skip kpm silently and `list`/`status` show it
as `self-update not configured` — never an error. Adopt the registry's kpm def
to turn it on (after the `registry add`/`refresh` above):

```
kpm install kpm --adopt --yes
```

and kpm maintains itself like any other package.

Since 0.5.0 the adoption (`source`/`forge`) is stored in `state.json`, not in
`kpm.toml` — the same place kpm's pin lives, and for the same reason. Every kpm
release ships `packages.d/kpm.toml`, so a self-update overwrites that file; when
the adoption lived there, each self-update silently un-adopted kpm and it went
back to `self-update not configured`. Storing it in `state.json`, which updates
never overwrite, makes the adoption durable.

Upgrading from **0.4.1 or earlier**: if your kpm is still adopted (`status` does
not say `self-update not configured`), 0.5.0 migrates the source into
`state.json` automatically on its first mutating command. If a previous
self-update already wiped it, re-adopt **once** under 0.5.0 and it sticks for
good:

```
kpm registry refresh && kpm install kpm --adopt --yes
```

kpm's own recorded version self-heals from the running binary, so a
USB-sideloaded kpm corrects its `installed_version` on the next mutating command.

## Registering packages

`kpm add <url>` writes a `packages.d/<id>.toml`. It accepts a repo URL, with an
optional trailing `/releases`, `/releases/latest`, or `/releases/tag/<tag>` (a
`/tag/<tag>` URL pins that tag):

```
kpm add https://codeberg.org/StrayRose/NickelHardcover
kpm add https://github.com/owner/repo --asset "KoboRoot*.tgz"
kpm add https://codeberg.org/o/r/releases/tag/v0.5.0     # pinned
```

- Forge is auto-detected: `github.com` → GitHub; any other host is probed at
  `https://<host>/api/v1/version` (Forgejo/Gitea). If detection fails, pass
  `--forge github|forgejo`.
- Default asset is `KoboRoot.tgz`; override with `--asset <glob>`.
- For a package you installed **before** kpm, seed its version so kpm won't think
  a reinstall is needed: `kpm add <url> --installed v0.5.0`.

A package TOML looks like:

```toml
name = "NickelHardcover"
source = "codeberg.org/StrayRose/NickelHardcover"
forge = "forgejo"
asset = "KoboRoot.tgz"
pin = ""                # empty = track latest; else an exact tag

# [uninstall]           # optional — customizes "kpm uninstall" (see below)
# purge_paths = ["/mnt/onboard/.adds/NickelHardcover/**"]
```

(kpm's own pin is stored in `state.json`, not `kpm.toml`, so it survives a
self-update that overwrites the TOML.)

## Registries

Instead of adding each package by URL, you can trust a **registry**: a git repo
that ships package definitions (the same TOML schema as `packages.d`, plus
curated `[uninstall]` recipes). kpm fetches its `registry.toml` over raw-file
HTTPS — no git client, no clone. A registry holds *definitions only*, never
binaries; the actual software still comes from each package's own release page.

A public registry of common Kobo mods lives at
[wolffshots/kobo-registry](https://github.com/wolffshots/kobo-registry) —
**browse its packages at
[wolffshots.github.io/kobo-registry](https://wolffshots.github.io/kobo-registry/)**.

Trusting a registry is the same trust decision as running `kpm add` on each of
its entries yourself: a def chooses the source repo and the uninstall recipe
(including `run_before`/`run_after` hooks, which run as root). There is **no def
signing** in this version, just as there is no asset signing — TLS with the
embedded CA bundle protects the fetch, not the maintainer's intent.

**A registry repo** has one file at its root, `registry.toml`:

```toml
schema_version = 1

[packages.nickelmenu]
name   = "NickelMenu"
source = "github.com/pgaskin/NickelMenu"
forge  = "github"
asset  = "KoboRoot.tgz"
min_kpm = "0.3.0"                 # optional; older kpm skips it with a note

  [packages.nickelmenu.uninstall]
  method       = "marker"
  marker_file  = "/mnt/onboard/.adds/nm/uninstall"
  needs_reboot = true
```

Package ids follow the `[a-z0-9-]+` rule. Fields match the local schema except
there is **no `pin`** (pins are a local decision, never distributed) and the
optional `min_kpm`. `schema_version` must be `1`; a newer schema tells you to
update kpm.

**Using registries** (CLI/telnet — there is no NickelMenu entry for these):

```
kpm registry add https://github.com/owner/kobo-registry --name main
kpm registry refresh                # the only registry command that hits the network
                                    #   (registry add also runs a one-time forge probe unless --forge is given)
kpm search                          # what's available (marks installed / updatable / min_kpm)
kpm search nickel                   # filter by id or name
kpm install nickelmenu              # prints the def and exits 3 (review first)
kpm install nickelmenu --yes        # writes packages.d/nickelmenu.toml, stamped registry = "main"
kpm check && kpm update nickelmenu  # then it behaves exactly like a hand-added package
```

Key behaviors:

- **Network only in `registry refresh`** (plus an optional one-time
  forge-detection probe during `registry add`, skipped with `--forge`).
  `search`/`install`/`sync` read the on-device cache
  (`cache/registry-<name>.toml`) exclusively, so they work offline and are
  predictable. `search`/`registry list` show a cache-age note
  (`cached 3d ago`) instead of silently refetching. Refresh uses ETags, so an
  unchanged registry is a cheap 304.
- **Provenance & `sync`.** An installed def is stamped `registry = "<name>"`.
  When a registry publishes a fixed uninstall recipe or changed asset glob,
  `kpm sync` re-copies every field *except* your local `pin` and reports a
  per-package diff. `sync` is deliberately manual — `check`/`update` never
  change install/uninstall behavior underneath you. If you hand-edit a
  registry-managed def, `sync` detects the drift and skips it (use
  `--overwrite` to replace, or delete the `registry =` line to detach). If a
  package disappears from its registry, `sync` warns but leaves your working
  copy intact.
- **Conflicts.** If several registries offer the same id, the earliest in
  config order wins; the rest are shadowed (a `WARN` is logged once per
  refresh).
- **`--adopt`.** `kpm install <id> --adopt` takes over an existing hand-added
  def (or kpm's own placeholder), writing the registry def while preserving your
  local pin and installed state — how kpm's own def gets a real source once a
  registry carries it.

Registries are configured in `/mnt/onboard/.adds/kpm/config.toml`
(`[[registries]]` entries), managed by the CLI and hand-editable; unknown keys
(and a future `github_token`) are preserved across edits, though comments are
not.

## NickelMenu usage

The shipped drop-in (`/mnt/onboard/.adds/nm/kpm`) adds three entries to the main
menu:

- **Check for updates** — brings Wi-Fi up, runs `kpm check --notify` in the
  background. With NickelDBus you get a toast; otherwise tap **Status**.
- **Update all** — brings Wi-Fi up, runs `kpm update --all --reboot --notify`.
  This stages every pending package and reboots to install them.
- **Status** — shows `status.txt` in a dialog (offline, instant).

Because NickelMenu's `cmd_spawn` reports success on *spawn*, not on completion,
long-running work can't report back through the menu — kpm signals completion via
`status.txt` (always) and NickelDBus toasts (if `qndb` exists).

## CLI reference

```
kpm add <url> [--asset <glob>] [--forge github|forgejo] [--name <id>] [--installed <ver>]
                            register a package from a forge release URL
kpm remove <id>             unregister only (deletes the TOML; files stay — see uninstall)
kpm uninstall <id> [--purge] [--dry-run] [--yes] [--force] [--keep-registration] [--reboot] [--notify]
                            delete a package's installed files (see "Uninstalling")
kpm list                    offline table: id, installed, staged, latest, pin
kpm check [--notify]        query forges for all packages; update state + status.txt
kpm update [<id>...] [--all] [--reboot] [--notify]
                            download, verify, merge and stage updates; reboot to install
kpm unstage                 cancel a pending staging (remove the kpm-staged tgz, clear staged state)
kpm pin <id> <tag>          pin to an exact tag (downgrade allowed)
kpm unpin <id>              track latest again
kpm registry add <url> [--name <n>] [--ref <branch>] [--path <p>] [--forge github|forgejo]
                            trust a registry of package definitions (see "Registries")
kpm registry remove <name>  forget a registry and its cache (installed packages unaffected)
kpm registry list           name, url, ref, cache age, package count
kpm registry refresh [<name>]  refetch registry.toml (all by default; the only registry
                            network call besides registry add's one-time forge probe)
kpm search [<term>]         list/filter packages across cached registries
kpm install <id> [--pin <tag>] [--installed <ver>] [--yes] [--adopt]
                            copy a package def from a registry into packages.d
kpm sync [<id>...] [--overwrite]  re-copy registry defs for registry-managed packages
kpm log [-n N]              print the last N log lines (default 12)
kpm status                  print status.txt + any pending staging; fast/offline
kpm version                 print the compiled-in version
```

Exit codes: `0` ok / nothing to do, `1` error, `2` partial (some packages failed
but others still staged), `3` confirmation required (`uninstall`/`install`
without `--yes`).
If every selected `update` package fails and nothing stages, the exit is `1`.

`update` merges all pending packages into one staged `KoboRoot.tgz` (kpm's own
entries are merged last so nothing can clobber the new binary). Re-running
`update` before a reboot re-merges the already-staged packages so none are lost.
A download or verification failure skips only that package. kpm refuses to
overwrite a `KoboRoot.tgz` it didn't stage (verified by content hash), so a
manual install is never clobbered — and `kpm unstage` cancels a pending staging
(it removes the tgz only if kpm staged it, then clears the staged state).

Only one mutating kpm command runs at a time: it takes a lock at
`.adds/kpm/lock`, and a second mutating command fails with "another kpm instance
is running" (a lock older than 10 minutes is assumed stale and broken).
Read-only commands (`list`/`status`/`log`/`version`/`search`/`registry list`)
don't take the lock and never write state.

`KPM_ROOT` overrides the `/mnt/onboard` root (used by tests and for dev runs off
the device). `KPM_SYSROOT` overrides the rootfs `/` so `uninstall` deletions land
inside a sandbox during tests.

## Uninstalling packages

`kpm remove <id>` only unregisters a package (deletes its TOML); the installed
files stay. To delete the files, use `kpm uninstall <id>`.

Uninstall is destructive and kpm runs non-interactively, so it never prompts:
it prints a **plan** and, without `--yes`, exits `3` so you can review first.

```
kpm uninstall nickelhardcover              # prints the plan, exits 3
kpm uninstall nickelhardcover --dry-run    # prints the plan, changes nothing, exits 0
kpm uninstall nickelhardcover --yes        # applies it
kpm uninstall nickelhardcover --yes --purge   # also removes user data (purge_paths)
```

On success kpm clears the package from `state.json` and deletes its
`packages.d/<id>.toml` (keep it with `--keep-registration`). `--reboot` reboots
afterwards; some packages need a reboot to finish (see the marker method).

**Path safety.** Every deletion candidate — whether from the recorded manifest,
`extra_paths`, or `purge_paths` — is checked against a policy:

- A **hard denylist** is always refused, even via `allow_paths`: `/bin`, `/sbin`,
  `/lib`, `/drivers`, `/dev`, `/proc`, `/sys`, `/root`, `/var`; all of `/etc`
  **except** `/etc/udev/rules.d` and `/etc/dbus-1` (so `/etc/passwd`, `/etc/shadow`,
  `/etc/inittab`, `/etc/init.d`, `/etc/fstab`, … can never be deleted); all of
  `/usr/local/Kobo` **except** `/usr/local/Kobo/imageformats` (where NickelHook
  mods like `libnm.so` live); everything at/under `/mnt/onboard` **except**
  `/mnt/onboard/.adds` and `/mnt/onboard/.kobo` (so the book library is protected);
  plus kpm's own `bin`, `state.json`, and `kpm.log`.
- An **allowlist** is deletable: `/mnt/onboard/.adds`, `/mnt/onboard/.kobo`,
  `/usr/local`, `/usr/bin`, `/usr/lib`, `/opt`, `/etc/udev/rules.d`, `/etc/dbus-1`.
  Because the denylist is checked first, `allow_paths` can extend the allowlist to
  new locations (e.g. `/srv/...`) but never re-enable any denied path above.
- Per-package `allow_paths` extends the allowlist but can never override the
  denylist.
- Anything else is **skipped with a WARN** and the rest of the removal continues.

Symlinks are never followed (the link itself is removed); shared directories
survive because kpm only `rmdir`s directories that end up empty. `--force` only
bypasses a failing `run_before`, never the path policy.

### The `[uninstall]` table

Every field is optional. A bad `[uninstall]` block only errors when `uninstall`
runs — it never breaks `add`/`check`/`update`. And a package file that is
entirely unreadable (malformed TOML, or a filename that isn't a valid id) is
skipped with a warning in the log rather than making every package invisible;
that one package simply doesn't exist until you fix the file.

(`kpm pin`/`unpin` rewrite the package's TOML, preserving unknown fields but
**not comments** — a TOML-library limitation.)

```toml
[uninstall]
method       = "manifest"   # "manifest" (default) | "marker" | "marker-remove"
extra_paths  = []           # extra software artifacts to always delete
purge_paths  = []           # user data/config; deleted ONLY with --purge
keep_paths   = []           # protect these (subtracted from the deletion set)
allow_paths  = []           # extend the deletable-path allowlist (never the denylist)
marker_file  = ""           # marker: file to create; marker-remove: file to delete (required for both)
needs_reboot = false        # defaults to true for marker/marker-remove, false otherwise
run_before   = ""           # /bin/sh -c before removal; nonzero aborts (unless --force)
run_after    = ""           # /bin/sh -c after removal; nonzero is logged, not fatal
```

Configured paths are absolute device paths. Entries in `extra_paths`,
`purge_paths`, and `keep_paths` may end in `/**` to mean "this directory,
recursively". `run_before`/`run_after` run as root, exactly like the package's
own install already did.

**Manifest method** (the default) deletes exactly what the package installed —
the manifest kpm captured when it staged the package — plus `extra_paths`, minus
`keep_paths`. Example, NickelHardcover, also wiping its data with `--purge`:

```toml
# packages.d/nickelhardcover.toml
name = "NickelHardcover"
source = "codeberg.org/StrayRose/NickelHardcover"
forge = "forgejo"
asset = "KoboRoot.tgz"

[uninstall]
purge_paths = ["/mnt/onboard/.adds/NickelHardcover/**"]
```

```
kpm uninstall nickelhardcover --yes --purge   # removes files AND the data dir
```

If a package predates kpm it has no manifest; set `extra_paths` (or update it
through kpm once) or uninstall refuses.

**Marker method** is for packages with their own removal mechanism, like
NickelMenu: instead of deleting files out from under a running Nickel, kpm
creates the file NickelMenu watches for and reboots. NickelMenu removes itself
on the next boot:

```toml
# packages.d/nickelmenu.toml
name = "NickelMenu"
source = "github.com/pgaskin/NickelMenu"
forge = "github"
asset = "KoboRoot.tgz"

[uninstall]
method       = "marker"
marker_file  = "/mnt/onboard/.adds/nm/uninstall"
needs_reboot = true
```

```
kpm uninstall nickelmenu --yes --reboot   # writes the marker, reboots to finish
```

`extra_paths`/`purge_paths`/`keep_paths` still apply for the marker method (e.g.
purge NickelMenu's config dir alongside the marker with `--purge`).

**Marker-remove method** is the inverse convention used by NickelHook mods like
NickelClock, NickelDBus, and NickelTypeFix: the package *ships* a trigger file
whose *absence* makes it remove itself on the next boot. kpm deletes that file
and reboots — deleting the mod's files directly would rip a loaded `.so` out
from under Nickel instead of following the package's supported path:

```toml
# packages.d/nickelclock.toml
name = "NickelClock"
source = "github.com/shermp/NickelClock"
forge = "github"
asset = "NickelClock-*.zip"

[uninstall]
method      = "marker-remove"
marker_file = "/mnt/onboard/.adds/nickelclock/uninstall"
purge_paths = ["/mnt/onboard/.adds/nickelclock/**"]
```

```
kpm uninstall nickelclock --yes --reboot   # deletes the trigger, reboots to finish
```

If the trigger file is already absent (the package is already uninstalling, or
was removed by hand), the uninstall succeeds as a no-op — the reboot note still
applies. A *directory* at the marker path is an error. The other fields compose
exactly as for `marker` (e.g. `--purge` removes the config dir alongside the
trigger delete).

There is no NickelMenu entry for uninstall — it stays CLI-only (telnet/SSH) on
purpose: a one-tap destructive action is a footgun.

### Removing kpm itself

`kpm uninstall kpm` is refused. Remove kpm by hand over USB: delete
`/mnt/onboard/.adds/kpm` (its binary, state, log, and registrations) and
`/mnt/onboard/.adds/nm/kpm` (its NickelMenu drop-in), then reboot.

## First install & smoke test (telnet)

After the bootstrap reboot, connect over telnet/SSH (as root) and confirm:

```
# kpm version
0.3.0

# kpm add https://codeberg.org/StrayRose/NickelHardcover
registered nickelhardcover -> codeberg.org/StrayRose/NickelHardcover [forgejo], asset "KoboRoot.tgz"

# kpm check
kpm 0.3.0 — checked 2026-07-19 10:30
nickelhardcover  - -> v0.5.1  UPDATE AVAILABLE
kpm              0.3.0        self-update not configured
1 update available. Use "Update all" to install (reboots).

# kpm update --all           # stage without rebooting
1 package(s) staged — reboot to install

# kpm log
2026-07-19 10:30:00  CHECK      nickelhardcover  - -> v0.5.1 available
2026-07-19 10:31:02  STAGE      nickelhardcover  - -> v0.5.1

# reboot                     # firmware installs the staged tgz on boot
# kpm check                  # after reboot, the next mutating command promotes
                             #   staged -> installed (status is read-only and shows
                             #   the result; it does not itself promote)
```

Inspect `/mnt/onboard/.adds/kpm/kpm.log` at any time for the full history.

## Troubleshooting

- **A staged update you want to cancel.** Run `kpm unstage` (before rebooting).
  It removes the staged `KoboRoot.tgz` only if kpm staged it and clears the
  staged state. Deleting `.kobo/KoboRoot.tgz` by hand instead desyncs kpm (it
  will still promote the packages it thought it staged), so prefer `unstage`.
- **Corrupt `state.json`.** If the state file ever becomes unreadable, kpm
  renames it to `state.json.corrupt-<timestamp>`, logs a `WARN`, and starts from
  empty state rather than failing every command. (If it can neither rename nor
  copy the corrupt file aside, it errors instead of overwriting the only copy.)
  Re-seed installed versions with `kpm add <url> --installed <ver>`; kpm's own
  version re-seeds automatically. If an update was staged but not yet installed
  when the corruption happened, its `.kobo/KoboRoot.tgz` becomes "foreign"
  afterward — reboot to install it, or delete that file by hand.
- **The log.** `kpm.log` rotates to `kpm.log.1` once it passes 256 KiB (a single
  older file is kept). `kpm log` only reads the current file; open `kpm.log.1`
  over USB for older history.

## Build & release

- `go vet ./...` and `go test ./...` — unit tests (httptest forge fixtures,
  tar-merge, URL parsing, state reconcile) run on any host.
- `go run ./build` cross-compiles the `linux/arm/GOARM=7` static binary
  (`CGO_ENABLED=0`) and assembles `dist/KoboRoot.tgz` entirely in Go — no
  `tar`/WSL needed on Windows — then self-checks the archive's exact member list
  and modes. Override the embedded version with `KPM_VERSION=<v> go run ./build`.
- Release artifacts are copied to `releases/<version>/KoboRoot.tgz`.

## Non-goals (v1)

No dependency resolution, no repo indexes, no firmware 5.x, no asset signature
verification, no GUI beyond NickelMenu.
