# Package definitions and registries

This page is the reference for package definitions (defs): the files in `packages.d/`, the `registry.toml` of a registry, the install methods and the `[uninstall]` table. The [README](../README.md) covers daily use.

## Contents

- [Add a package by URL](#add-a-package-by-url)
- [The package def](#the-package-def)
- [Registries](#registries)
- [Fields that change install and update](#fields-that-change-install-and-update)
- [Firmware 6.x fields](#firmware-6x-fields)
- [Install methods](#install-methods)
- [Uninstall](#uninstall)

## Add a package by URL

`kpm add <url>` writes `packages.d/<id>.toml`. The URL is a repository URL. It can end in `/releases`, `/releases/latest` or `/releases/tag/<tag>`. A `/tag/<tag>` URL pins that tag.

```
kpm add https://codeberg.org/StrayRose/NickelHardcover
kpm add https://github.com/owner/repo --asset "KoboRoot*.tgz"
kpm add https://codeberg.org/o/r/releases/tag/v0.5.0     # pinned
```

- kpm detects the forge. `github.com` is GitHub. For any other host, kpm asks `https://<host>/api/v1/version` (Forgejo or Gitea). If detection fails, give `--forge github|forgejo`.
- The default asset is `KoboRoot.tgz`. `--asset <glob>` changes it. On firmware 6.x, `kpm add` writes the asset into an `[fw6]` table, and the default asset there is `Kobo.tgz`.
- If you installed the package before kpm, give its version, so that kpm does not install it again: `kpm add <url> --installed v0.5.0`.

## The package def

```toml
name = "NickelHardcover"
source = "codeberg.org/StrayRose/NickelHardcover"
forge = "forgejo"
asset = "KoboRoot.tgz"
pin = ""                # empty: track the latest release. Else an exact tag.

# [uninstall]           # optional, see "Uninstall"
# purge_paths = ["/mnt/onboard/.adds/NickelHardcover/**"]
```

- A pin freezes the package. kpm does not fetch a rolling package again while a pin is set.
- kpm keeps its own pin, source and registry name in `state.json`, not in `kpm.toml`. Each kpm release ships `packages.d/kpm.toml`, so a self-update writes that file again.
- `kpm pin` and `kpm unpin` write the TOML again. They keep unknown fields but not comments.
- A def with a bad `[uninstall]` table fails only when `uninstall` runs. It never breaks `add`, `check` or `update`.
- kpm skips a file with malformed TOML, or with a file name that is not a valid id, and logs a warning. The other packages stay visible.
- A file with malformed TOML blocks `kpm install` and `kpm update`, with a message that names the file. kpm cannot see the conflicts of that package. Fix the file or `kpm remove` it.

## Registries

A registry is a git repository with one file, `registry.toml`. It holds defs only, never binaries. The software still comes from the release page of each package. kpm reads `registry.toml` over HTTPS as a raw file, with no git client.

The public registry is [wolffshots/kobo-registry](https://github.com/wolffshots/kobo-registry). Browse it at [wolffshots.github.io/kobo-registry](https://wolffshots.github.io/kobo-registry/).

### Trust

To trust a registry is the same decision as to run `kpm add` on each of its entries. A def chooses the source repository and the uninstall recipe, and `run_before` and `run_after` run as root. kpm does not sign defs or assets. TLS protects the fetch, not the intent of the maintainer.

A `checksums` entry (see below) makes this gap smaller. kpm checks the hash against the downloaded bytes and stops on a mismatch. This protects against a changed CDN file or an upstream upload that replaced an asset. It is a statement of the curator, not a signature. An older kpm ignores the field.

### registry.toml

```toml
schema_version = 1

[packages.nickelmenu]
name   = "NickelMenu"
source = "github.com/pgaskin/NickelMenu"
forge  = "github"
asset  = "KoboRoot.tgz"
min_kpm = "0.3.0"                 # optional: an older kpm skips the def with a note

  [packages.nickelmenu.uninstall]
  method       = "marker"
  marker_file  = "/mnt/onboard/.adds/nm/uninstall"
  needs_reboot = true
```

- A package id matches `[a-z0-9-]+`.
- The fields are the fields of a local def, plus `min_kpm`, without `pin`. A pin is a local decision.
- `schema_version` must be `1`. A newer schema tells you to update kpm.
- `description`, `homepage` and `tested_fw` are information only. If the device runs a firmware newer than `tested_fw` (major.minor), the `kpm install` review prints a note. The note never blocks the install.

### How kpm uses a registry

- Only `kpm registry refresh` uses the network. `kpm registry add` also asks the forge once, unless you give `--forge`. `search`, `install` and `sync` read the cache on the device (`cache/registry-<name>.toml`), so they work offline. `search` and `registry list` show the age of the cache. Refresh uses ETags, so an unchanged registry costs one 304 response.
- `kpm install <id>` prints the def and exits 3. Add `--yes` to write `packages.d/<id>.toml`. The def gets `registry = "<name>"`.
- `kpm sync` copies the registry def again, for each field except your local `pin`, and prints a diff for each package. `check` and `update` never do this. If you edited a registry def by hand, `sync` skips it. `--overwrite` replaces it. Delete the `registry =` line to detach the def. If a package leaves its registry, `sync` warns and keeps your copy.
- If two registries offer the same id, the first in `config.toml` wins. kpm logs a `WARN` once for each refresh.
- `kpm install <id> --adopt` takes over a def that you added by hand, or the kpm placeholder. It keeps your pin and the installed state.
- `config.toml` holds the `[[registries]]` entries (`name`, `url`, `ref`, `path`, `forge`). The CLI keeps unknown keys and `github_token`, but not comments.

## Fields that change install and update

These three fields (kpm 0.10.0) change what install and update do. `kpm sync` copies a curator change of them into the local def. kpm drops a def with a malformed value from the registry and logs a `WARN` with the id and the reason at each refresh and cache read. `kpm log` then shows why a package is gone.

| Field | What it does | `min_kpm = "0.10.0"` required |
|---|---|---|
| `update_check = "rolling"` | Finds a changed release when the tag does not move. kpm compares `published_at` and the asset size. | Yes |
| `conflicts = ["<id>", ...]` | Refuses an install next to a named package. | Yes |
| `requires = ["<id>", ...]` | Installs the named packages first, and keeps them while this package is installed. See [dependencies.md](dependencies.md). | Yes, `0.17.0` |
| `checksums` (tag to sha256) | Checks the downloaded asset against a hash from the curator. | No |

```toml
# KoboCloud publishes each build under the tag "latest".
[packages.kobocloud]
name         = "KoboCloud"
source       = "github.com/fsantini/KoboCloud"
forge        = "github"
asset        = "KoboRoot.tgz"
update_check = "rolling"
min_kpm      = "0.10.0"

# kobo-tweaks removes NickelClock when it installs.
[packages.kobo-tweaks]
name      = "Kobo Tweaks"
source    = "github.com/.../kobo-tweaks"
forge     = "github"
asset     = "KoboRoot.tgz"
conflicts = ["nickelclock"]
min_kpm   = "0.10.0"

# The key is the exact upstream tag. One leading "v" does not matter.
[packages.nickelclock.checksums]
"v0.4.0" = "3b1f8c...(64 lowercase hex characters)"
```

Notes for curators:

- Declare `conflicts` on one side only. kpm checks both directions.
- Declare `requires` on the package that needs the other one. Set `min_kpm = "0.17.0"` on that package: an older kpm ignores `requires` and installs the package alone. A def that requires itself, or requires an id that it also lists in `conflicts`, is a parse error.
- Get a hash with `curl -sL <browser_download_url> | sha256sum`. Each download also logs a `CHECKSUM` line with the hash in `kpm log`. Hash the asset as it is published: for a zip asset, the zip, not the tgz in it. You can paste a GitHub `sha256:` digest. kpm removes the prefix and white space.
- Never put `checksums` on a `rolling` def. A rolling asset changes with no registry edit. kpm drops such a registry def as a parse error. A local def with both falls back to no check, with a `WARN`.
- `update_check = "rolling"` without `min_kpm` is a parse error. An older kpm ignores the key and shows the package as up to date forever.
- Releases with no checksum install with TLS only, as before.

## Firmware 6.x fields

- `[fw6]` gives the asset for firmware 6.x, for example `asset = "Kobo.tgz"`. A tgz package installs on 6.x only with an `[fw6]` table. Without it, `install` and `update` refuse it with "no build for firmware 6.x", and `update --all` skips it with a note.
- A 6.x `Kobo.tgz` of a package can write only under `imageformats/`. kpm refuses a `KoboRoot.tgz` as a 6.x asset.
- App-tree and file packages install on both firmware lines without an `[fw6]` table.
- `builtin_fw6` marks a package that firmware 6.x has built in, for example NickelClock (6.x has a reader clock). The package is never available on 6.x. kpm shows "built into firmware 6.x" and the text of the def. On 6.x, kpm uninstalls such a package with purge and deletes its def.

## Install methods

| | `tgz` (default) | `app-tree` | `file` |
|---|---|---|---|
| Used for | Nickel mods | KOReader, Plato, fonts | Dictionaries |
| When it takes effect | After a reboot | At once (fonts: after a reboot) | At once |
| `kpm update` reports | `staged — reboot to install` | `installed` | `installed` |
| `kpm uninstall` | Removes the files at the next reboot, or uses the marker of the mod | Removes the files now | Removes the file now |
| `install_method` in `--json` | `"tgz"` | `"app-tree"` | `"file"` |

### tgz

The firmware extracts `.kobo/KoboRoot.tgz` (4.x) or `.kobo/Kobo.tgz` over the root file system at boot, then deletes it. kpm downloads the asset of each pending package, merges them into one archive and stages it. kpm merges its own files last, so no package can replace the new kpm binary. If you run `update` again before the reboot, kpm merges the staged packages again, so none are lost.

kpm never replaces a staged archive that it did not stage. It checks the content hash. `kpm unstage` removes the archive only if kpm staged it.

kpm checks each archive before it stages it. It refuses absolute paths, `..` paths, links whose target is outside the archive, device and FIFO entries, and setuid or setgid files. No package except kpm can write the kpm install tree.

### app-tree

KOReader and Plato ship a plain folder tree in a release zip. kpm extracts it into `/mnt/onboard/.adds/<name>/` itself, with no reboot.

```toml
[packages.koreader.install]
method = "app-tree"
root   = "koreader"                       # the prefix inside the zip
dest   = "/mnt/onboard/.adds/koreader"    # where its contents go

  [packages.koreader.install.launcher]    # optional NickelMenu entry
  nm_file  = "/mnt/onboard/.adds/nm/koreader"
  nm_entry = "menu_item:main:KOReader:cmd_spawn:quiet:exec /mnt/onboard/.adds/koreader/koreader.sh"
```

- `dest` must be in the kpm allowlist (`.adds` and `.kobo` on the user partition, `/usr/local` and `/opt` on the rootfs). It must be a folder of the package, never a shared root such as `.adds`. `/mnt/onboard/fonts` is the one exception, with the per-file rule of fonts.
- kpm checks each zip entry before it writes a byte. It refuses absolute paths, `..`, backslashes, control characters, symlinks, device nodes and setuid entries. It checks each destination again just before the write. Any fault stops the whole install.
- kpm deletes only files that it installed. It records a manifest at each install. Settings, statistics, plugins and other user data in the folder stay. `kpm uninstall koreader` removes the app and keeps your data. `--purge` removes the whole folder.
- kpm writes the NickelMenu entry of the def, and never replaces an entry that exists. `kpm update <id> --force-launcher` writes it again. Without NickelMenu, kpm writes the file anyway and tells you to install NickelMenu.
- KFMon already points at `koreader.sh` and `plato.sh`. kpm cannot place the KFMon icons (`/mnt/onboard/koreader.png`, `/mnt/onboard/icons/plato.png`), because they are outside the allowlist. Copy them from the release zip over USB. `kpm doctor` tells you if they are missing.
- KOReader publishes a separate `koreader-kobov5-*.zip` for firmware 5.x. The registry entry tracks the 4.x build, so on 5.x the install review shows the "untested" note.

### Fonts

A font package is an app-tree package with `dest = "/mnt/onboard/fonts"`. The zip must be flat: Nickel reads only the files directly in `fonts`.

```toml
[packages.kf-core-fonts.install]
method = "app-tree"
dest   = "/mnt/onboard/fonts"
```

- Nickel uses a new or replaced font only after a reboot. The UI shows the **Reboot now** prompt for a font install, update or uninstall.
- kpm never replaces a font that it did not install. Move a font that you copied by hand off the device before kpm installs a package with the same file name.
- An uninstall removes only the files in the manifest of the package. Your own fonts and the `fonts` folder stay.
- Two limits: a tgz package can ship a file into `fonts`, and Nickel unpacks it at boot with no ownership check. A def can also name one file in `fonts` in `extra_paths`, and kpm deletes it at uninstall.
- kpm 0.11.0 and earlier reject font defs with "not within a writable location". This is cosmetic and stops after kpm 0.12.0.

### Dictionaries

A dictionary package uses the `file` method. kpm puts the release asset, unchanged, into `/mnt/onboard/.kobo/custom-dict`. Nickel (4.24 and later) lists each `dicthtml-*.zip` there as a custom dictionary.

```toml
[packages.dict-example.install]
method = "file"
dest   = "/mnt/onboard/.kobo/custom-dict/dicthtml-example.zip"
```

- `dest` names the file. It must be directly in `custom-dict` with a `dicthtml-*.zip` name.
- No reboot: Nickel lists `custom-dict` again at each lookup. This comes from a read of libnickel 4.46. No device test confirms it yet. If a lookup shows old entries, select another dictionary and back, or reboot.
- kpm moves the download into place with a rename, so an update needs no extra free space.
- The ownership rules of fonts apply.
- kpm 0.11.0 and earlier drop a `file` def. Give it `min_kpm = "0.12.0"`.

## Uninstall

`kpm remove <id>` only deletes the def. The files stay. `kpm uninstall <id>` deletes the files. It prints a plan and exits 3 without `--yes`.

```
kpm uninstall nickelhardcover              # prints the plan, exits 3
kpm uninstall nickelhardcover --dry-run    # prints the plan, changes nothing, exits 0
kpm uninstall nickelhardcover --yes        # applies it
kpm uninstall nickelhardcover --yes --purge   # also removes user data (purge_paths)
```

After an uninstall, kpm clears the package from `state.json` and deletes its def. `--keep-registration` keeps the def. `--reboot` reboots after it.

### The [uninstall] table

All fields are optional.

```toml
[uninstall]
method       = "manifest"   # "manifest" (default) | "marker" | "marker-remove"
extra_paths  = []           # more files to delete always
purge_paths  = []           # user data and config, deleted only with --purge
keep_paths   = []           # files to keep (taken out of the delete set)
allow_paths  = []           # extend the allowlist (never the denylist)
marker_file  = ""           # marker: file to create. marker-remove: file to delete.
needs_reboot = false        # default true for marker and marker-remove
run_before   = ""           # /bin/sh -c before removal. Nonzero stops it (unless --force).
run_after    = ""           # /bin/sh -c after removal. Nonzero is logged.
```

Paths are absolute device paths. A path in `extra_paths`, `purge_paths` or `keep_paths` can end in `/**`: the folder and all files in it. `run_before` and `run_after` run as root, as the install of the package already did.

### Methods

- **manifest** (default): deletes the files that kpm recorded when it staged the package, plus `extra_paths`, minus `keep_paths`. A package installed before kpm has no manifest. Set `extra_paths`, or update the package through kpm once, else uninstall refuses.
- **marker**: for a package with its own removal, such as NickelMenu. kpm creates the file that the package watches for, and the package removes itself at the next boot. `kpm uninstall nickelmenu --yes --reboot`.
- **marker-remove**: for NickelHook mods such as NickelClock, NickelDBus and NickelTypeFix. The package ships a trigger file, and it removes itself at the next boot when the file is gone. kpm deletes the file. If the file is already gone, the uninstall succeeds and does nothing. A folder at the marker path is an error.

`extra_paths`, `purge_paths` and `keep_paths` also apply to both marker methods.

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

### Path safety

kpm checks each path to delete, from the manifest, `extra_paths` or `purge_paths`:

- The denylist always wins, also over `allow_paths`: `/bin`, `/sbin`, `/lib`, `/drivers`, `/dev`, `/proc`, `/sys`, `/root`, `/var`. All of `/etc` except `/etc/udev/rules.d` and `/etc/dbus-1`. All of `/usr/local/Kobo` except `/usr/local/Kobo/imageformats`. All of `/mnt/onboard` except `.adds`, `.kobo` and `fonts` (so your books are safe, and a purge of the whole `fonts` folder is refused). Also the kpm `bin`, `state.json` and `kpm.log`.
- The allowlist: `/mnt/onboard/.adds`, `/mnt/onboard/.kobo`, `/usr/local`, `/usr/bin`, `/usr/lib`, `/opt`, `/etc/udev/rules.d`, `/etc/dbus-1`. `allow_paths` adds to it.
- kpm skips any other path with a `WARN` and continues.
- kpm never follows a symlink. It removes the link. It removes a folder only when the folder is empty.
- `--force` skips a failed `run_before`. It never skips the path rules.
