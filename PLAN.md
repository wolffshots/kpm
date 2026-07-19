# kpm — Kobo Package Manager

## Design Document / Implementation Plan

A single static Go binary that manages third-party software on Kobo e-readers
(firmware 4.x). Packages are distributed as `KoboRoot.tgz` release assets on git
forges (GitHub, Codeberg, or any Gitea/Forgejo instance). kpm registers release
sources, checks for updates, stages installs, pins versions, tracks everything in
a human-readable log on the user-visible storage, and updates itself through the
same mechanism. It is driven from NickelMenu entries on-device and is also a
normal CLI over telnet/SSH.

---

## 1. Background & constraints

- **The install mechanism is fixed by the firmware.** On every boot, Kobo's init
  script (`/etc/init.d/rcS`) looks for `/mnt/onboard/.kobo/KoboRoot.tgz`. If
  present, it extracts it over the root filesystem (`tar -x` at `/`) and deletes
  it. This is the ONLY sanctioned install path and there is exactly **one slot
  per reboot**. kpm never extracts packages itself — it stages a tgz and reboots.
- **Community packaging convention:** a package's tgz contains paths like
  `./usr/local/<Name>/...` and `./mnt/onboard/.adds/<name>/...`. Extracting at
  `/` lands files both on the rootfs and on the user-visible FAT32 partition.
- **Everything runs as root.** Nickel, NickelMenu, and anything they spawn run
  as root. No privilege handling needed.
- **`/mnt/onboard` is FAT32** (no exec bits, no symlinks — but mounted so
  binaries ARE executable; KOReader runs from there). kpm's binary and all its
  data live under `/mnt/onboard/.adds/kpm/` so users can inspect/repair over USB.
- **Stock shell tooling cannot be trusted for HTTPS** (busybox wget frequently
  lacks TLS). This is the core reason for Go: `net/http` + an **embedded CA
  bundle** (`go:embed` a Mozilla `cacert.pem`; fall back to the system pool if it
  exists). Zero runtime dependencies.
- **CPU:** all supported devices are ARMv7 (Cortex-A8/A9/A7, hard-float works via
  softfp-compatible GOARM=7). Build: `CGO_ENABLED=0 GOOS=linux GOARCH=arm
  GOARM=7`. Static pure-Go binary, no libc dependency, immune to the device's
  old glibc.
- **NickelMenu facts that shape the UX:**
  - Config drop-ins: any file in `/mnt/onboard/.adds/nm/` (`#` comments).
  - `menu_item:<location>:<label>:<action>:<arg>`; locations `main`, `reader`, …
  - `cmd_spawn` runs `/bin/sh -c` in the **background**; `chain_success` fires
    on successful *spawn*, not process exit → long-running work cannot report
    back through NickelMenu. kpm must do its own completion signaling.
  - `cmd_output:<timeout_ms>:<cmd>` shows stdout in a dialog but timeout is
    hard-capped at 10,000 ms → only fast, offline commands may use it.
  - `nickel_wifi:autoconnect` brings Wi-Fi up; kpm still waits/retries for
    actual connectivity itself.
  - Optional toasts: if **NickelDBus** is installed (`qndb` binary present), kpm
    shells out to `qndb -m mwcToast <ms> "<msg>"` for user feedback. If absent,
    feedback happens via the status dialog menu item (see §6). Never a hard dep.

## 2. On-device layout

```
/mnt/onboard/.adds/kpm/
  bin/kpm                  # the static ARM binary
  packages.d/              # one TOML file per registered package
    kpm.toml               # kpm registers itself (self-update, §9)
    nickelhardcover.toml
  state.json               # machine state: installed/staged versions, manifests, last check
  kpm.log                  # append-only human-readable history (the user-facing log)
  status.txt               # last result summary, shown by the NickelMenu status dialog
  cache/                   # package downloads (*.tgz/*.part, swept selectively) plus
                           #   registry caches (registry-<name>.toml, never swept)
/mnt/onboard/.adds/nm/kpm  # NickelMenu drop-in config (shipped by kpm's own tgz)
```

kpm's own `KoboRoot.tgz` ships exactly: `./mnt/onboard/.adds/kpm/bin/kpm`,
`./mnt/onboard/.adds/kpm/packages.d/kpm.toml` (only written if absent — see
§10 packaging note), and `./mnt/onboard/.adds/nm/kpm`.

## 3. Package definition (`packages.d/*.toml`)

TOML because humans will read and occasionally hand-edit these. Use
`github.com/BurntSushi/toml` (pure Go, tiny). One file per package; filename =
package id (`[a-z0-9-]+`).

```toml
# packages.d/nickelhardcover.toml
name   = "NickelHardcover"                        # display name
source = "codeberg.org/StrayRose/NickelHardcover" # host/owner/repo
forge  = "forgejo"                                # "github" | "forgejo" (gitea-compatible)
asset  = "KoboRoot.tgz"                           # release asset name; glob allowed (e.g. "KoboRoot*.tgz")
pin    = ""                                       # empty = track latest; else exact tag, e.g. "v0.5.0"

# --- stretch goal (§11), parsed but unused in v1: ---
# [uninstall]
# extra_paths = ["/mnt/onboard/.adds/nickelhardcover"]  # config/data dirs beyond the file manifest
```

`kpm add <url>` generates these files. URL parsing rules:
- Accept `https://github.com/owner/repo`, `https://codeberg.org/owner/repo`,
  with optional trailing `/releases`, `/releases/latest`, `/releases/tag/<tag>`
  (a `/tag/<tag>` URL sets `pin` to that tag).
- Forge detection: host `github.com` → `github`; anything else → probe
  `https://<host>/api/v1/version` (Forgejo/Gitea answer it) → `forgejo`; if the
  probe fails, error out and tell the user to pass `--forge` explicitly.
- Default `asset = "KoboRoot.tgz"`; `--asset <glob>` overrides.

## 4. State (`state.json`) and log (`kpm.log`)

`state.json` — written atomically (temp file + rename), stdlib JSON:

```json
{
  "packages": {
    "nickelhardcover": {
      "installed_version": "v0.5.0",
      "installed_at": "2026-07-19T10:00:00Z",
      "staged_version": "v0.5.1",
      "staged_at": "2026-07-19T10:31:02Z",
      "latest_seen": "v0.5.1",
      "last_checked": "2026-07-19T10:30:00Z",
      "manifest": ["usr/local/NickelHardcover/...", "mnt/onboard/.adds/..."]
    }
  },
  "last_check": "2026-07-19T10:30:00Z"
}
```

**Staged → installed promotion:** kpm cannot observe the reboot-time install
directly. Instead, on **every invocation** kpm first runs a reconcile step: if
any package has `staged_version` set AND `/mnt/onboard/.kobo/KoboRoot.tgz` no
longer exists (rcS deletes it after extracting), promote `staged_version` →
`installed_version`, record the manifest captured at stage time, clear staged
fields, and log `INSTALLED`. If the tgz still exists, staging is pending (no
reboot happened yet) — leave state alone.

`kpm.log` — the log file the user asked for; append-only, one line per event:

```
2026-07-19 10:30:00  CHECK      nickelhardcover  v0.5.0 -> v0.5.1 available
2026-07-19 10:31:02  STAGE      nickelhardcover  v0.5.0 -> v0.5.1
2026-07-19 10:31:02  REBOOT     staging 1 package(s)
2026-07-19 10:34:11  INSTALLED  nickelhardcover  v0.5.1
2026-07-20 09:00:00  PIN        nickelhardcover  v0.5.1
```

Bootstrapping versions for pre-kpm installs: `kpm add --installed <ver>` seeds
`installed_version` (there is no reliable way to read a version off the device).
Otherwise the first `update` treats the package as new.

## 5. Forge clients

One small interface, two implementations:

```go
type Release struct {
    Tag      string
    Assets   []Asset // Name, Size, DownloadURL
}
type Forge interface {
    LatestRelease(ctx, host, owner, repo string) (Release, error)
    ReleaseByTag(ctx, host, owner, repo, tag string) (Release, error)
}
```

- **Forgejo/Gitea** (covers Codeberg): `GET https://<host>/api/v1/repos/{o}/{r}/releases/latest`
  and `.../releases/tags/{tag}`. Asset download via `browser_download_url`
  (pattern: `https://<host>/{o}/{r}/releases/download/<tag>/<asset>` — verified
  against Codeberg).
- **GitHub:** `GET https://api.github.com/repos/{o}/{r}/releases/latest` and
  `.../releases/tags/{tag}`, headers `Accept: application/vnd.github+json`,
  `User-Agent: kpm/<version>`. Unauthenticated (60 req/h is plenty); support an
  optional `KPM_GITHUB_TOKEN` env / `token` key in a global `config.toml` later
  — not v1.
- Shared HTTP client: 30 s timeout for API calls, no timeout but an inactivity
  watchdog for downloads, embedded CA pool, 2 retries with backoff on network
  errors. Releases marked draft/prerelease are skipped when resolving "latest"
  (both APIs' `/latest` endpoints already do this — rely on it).
- "Latest" comparison is **tag string inequality**, not semver ordering
  (`installed != latest` → update available; pins make ordering unnecessary and
  forge tags aren't reliably semver). The one normalization is `tagsEqual`: a
  single leading `v` is insignificant, so `v1.2.0` and `1.2.0` are the same
  release (case-sensitive otherwise); the raw tag is always what gets displayed.

## 6. CLI surface

```
kpm add <url> [--asset <glob>] [--forge github|forgejo] [--name <id>] [--installed <ver>]
kpm remove <id>              # unregister only (deletes packages.d file; data untouched)
kpm list                     # offline table: id, installed, staged, latest_seen, pin
kpm check [--notify]         # query forges for all packages, update state + status.txt
kpm update [<id>...] [--all] [--reboot] [--notify]
kpm unstage                  # cancel a pending staging: remove the kpm-staged tgz, clear staged_* fields
kpm pin <id> <tag>           # sets pin; if installed != tag, marks update available (downgrade allowed)
kpm unpin <id>
kpm registry add <url> [--name <n>] [--ref <branch>] [--path <p>] [--forge github|forgejo]  # (v0.3.0, REGISTRY.md)
kpm registry remove <name>   # forget a registry + its cache; installed packages unaffected
kpm registry list            # name, url, ref, cache age, package count
kpm registry refresh [<name>] # refetch registry.toml (all by default); the only registry network path
                              #   besides registry add's one-time forge probe
kpm search [<term>]          # list/filter packages across cached registries; marks installed/updatable/min_kpm
kpm install <id> [--pin <tag>] [--installed <ver>] [--yes] [--adopt]  # copy a def from a registry into packages.d
kpm sync [<id>...] [--overwrite]  # re-copy registry defs for registry-managed packages (never touches pin)
kpm log [-n N]               # print last N lines of kpm.log (default 12)
kpm status                   # print status.txt + pending staging info; fast/offline
kpm version
```

Registry commands (v0.3.0) are specified in full by REGISTRY.md; §9 there is
authoritative. `registry list` and `search` are read-only (no lock); `registry
add`/`remove`/`refresh`, `install`, and `sync` are mutating. Registry network
access happens ONLY in `registry refresh`, plus an optional one-time
forge-detection probe during `registry add` (skipped with `--forge`) —
`search`/`install`/`sync` read the on-device cache exclusively.

Behavior notes:
- `--notify`: emit NickelDBus toasts at start/finish/error if `qndb` exists.
- Every command that changes anything writes both `kpm.log` and `status.txt`.
  `status.txt` is a short, dialog-friendly summary (≤ ~10 lines), e.g.:

  ```
  kpm 1.2.0 — checked 2026-07-19 10:30
  nickelhardcover  v0.5.0 -> v0.5.1  UPDATE AVAILABLE
  kpm              1.2.0             up to date
  1 update available. Use "Update all" to install (reboots).
  ```
- `check` and `update` wait for connectivity first: a HEAD request against the
  first configured package's forge host every ~1 s (each capped at a 2 s
  timeout) until a ~30 s total budget elapses, then fail with a clear "no
  network" status.
- Exit codes: 0 ok / nothing to do, 1 error, 2 partial (some packages failed),
  3 confirmation required (`uninstall`/`install` without `--yes`).
  `update`: if every selected package fails and nothing is staged, exit 1; a
  mix of staged + failed is exit 2.
- A single-instance lock (`.adds/kpm/lock`) serializes mutating commands;
  read-only commands (`list`/`status`/`log`/`version`/`search`/`registry list`)
  run without it and never write state.

## 7. The update pipeline (`kpm update`)

The single-slot constraint is solved by **merging**: all pending packages are
combined into ONE staged `KoboRoot.tgz`.

1. **Resolve targets.** For each selected package: pinned tag or latest release
   (re-check unless checked within the last 5 minutes — `check` freshness is in
   state). Skip packages already up to date; skip packages whose
   `staged_version` equals the target (already staged, awaiting reboot).
2. **Download** each asset to `cache/<id>-<tag>.tgz`. Stream to a `.part` file,
   then rename. Verify: gzip integrity + walk the entire tar (this also
   **captures the member manifest** for state/uninstall). Reject empty archives
   and any entry with an absolute path or `..` traversal. Warn (log, don't
   block) about entries outside `usr/`, `etc/`, `opt/`, `mnt/onboard/`.
3. **Merge & stage.** Stream all verified tgzs (alphabetical by package id,
   BUT if `kpm` itself is among them, its entries go LAST so nothing can clobber
   the new binary) into `/mnt/onboard/.kobo/KoboRoot.tgz.kpm-part` via
   `archive/tar` + `compress/gzip`, then rename to `KoboRoot.tgz`. Duplicate
   paths across packages are fine — later entries win at extraction; log a
   `WARN` when it happens. If a `KoboRoot.tgz` already exists that kpm didn't
   stage (per state), refuse and report — never clobber a manual install.
4. **Record**: set `staged_version` + manifest per package, log `STAGE` lines,
   write status.txt ("2 packages staged — reboot to install"), clear cache
   files that were merged.
5. **Reboot** (only with `--reboot`, which the NickelMenu entry passes): log
   `REBOOT`, `sync`, sleep 2 s, exec `/sbin/reboot` (fallback `busybox reboot`,
   then `reboot`). The boot-time installer applies the merged tgz; the next kpm
   invocation promotes staged → installed (§4).

Failure of any single package's download/verify → that package is skipped and
reported; remaining packages still stage (exit code 2).

## 8. NickelMenu integration (shipped as `/mnt/onboard/.adds/nm/kpm`)

NickelMenu uses `:` as the field separator (`menu_item:location:label:action:arg`),
so a `menu_item` label must **not** contain a `:` — use ` - ` instead (a colon
in the label mis-splits the line and breaks the entire config):

```
# kpm — Kobo package manager
menu_item :main :kpm - Check for updates :nickel_wifi :autoconnect
  chain_success :cmd_spawn :quiet:/mnt/onboard/.adds/kpm/bin/kpm check --notify
  chain_success :dbg_toast :Checking for updates…
menu_item :main :kpm - Update all :nickel_wifi :autoconnect
  chain_success :cmd_spawn :quiet:/mnt/onboard/.adds/kpm/bin/kpm update --all --reboot --notify
  chain_success :dbg_toast :Updating — will reboot when done
menu_item :main :kpm - Status :cmd_output :9000 :/mnt/onboard/.adds/kpm/bin/kpm status
```

Rationale: the two network actions are `cmd_spawn` (unbounded runtime, feedback
via toast if NickelDBus is present, always via `status.txt`); the status dialog
is `cmd_output` and strictly offline so it always beats the 10 s cap. Users
without NickelDBus tap "Status" after checking.

## 9. Self-update

kpm is just a package. Its own `packages.d/kpm.toml` points at kpm's release
repo (`source` placeholder until the repo exists; `asset = "KoboRoot.tgz"`).
`check`/`update` treat it like anything else. Two special cases:

- **Merge ordering** (§7.3): kpm's entries are merged last.
- The running binary is never replaced at runtime — the new binary arrives via
  the boot-time extraction, which is atomic from kpm's perspective. FAT32 has
  no text-file-busy issues, and kpm isn't running during rcS anyway.
- `installed_version` for kpm is seeded from the compiled-in version string on
  first run rather than `--installed`.

Initial bootstrap: the user side-loads kpm's own KoboRoot.tgz once (standard
`.kobo/` copy + reboot); after that it maintains itself.

## 10. Repository layout, build & release

```
Kobo/projects/kpm/
  PLAN.md                    # this document
  README.md                  # user-facing: install, register, menu usage, CLI ref
  go.mod                     # module kpm; Go ≥ 1.22; dep: BurntSushi/toml only
  cmd/kpm/main.go            # flag parsing (stdlib flag + subcommand dispatch), wiring
  internal/config/           # packages.d load/save, add-URL parsing, TOML types
  internal/state/            # state.json load/atomic-save, reconcile (staged→installed)
  internal/forge/            # Forge interface, github.go, forgejo.go, http.go (client+CA)
  internal/forge/cacert.pem  # embedded Mozilla CA bundle (go:embed)
  internal/tgz/              # verify/walk (manifest capture), merge-stream writer
  internal/device/           # paths, connectivity wait, qndb toasts, reboot, log/status writers
  internal/version/          # Version set via -ldflags -X
  build/package.go           # `go run ./build` → dist/: builds ARM binary AND assembles
                             # KoboRoot.tgz IN GO (archive/tar; correct ./ prefixes, 0755
                             # on bin/kpm, root:root headers) — no tar.exe/WSL needed on Windows
  res/nm-config              # the NickelMenu drop-in from §8
  releases/<version>/KoboRoot.tgz   # per repo convention (build output copied here)
```

Packaging note: rcS extraction overwrites files unconditionally, so kpm's own
tgz must NOT contain a user's live `packages.d/*.toml` other than `kpm.toml`
(overwriting `kpm.toml` on self-update is acceptable: it's forge coordinates +
pin, and pin round-trips through state — simpler: keep the pin field ONLY in
packages.d, and have the updater preserve user pins by re-reading the file
before overwrite… **decision: kpm.toml ships with `pin = ""` and `kpm pin kpm
<tag>` stores kpm's pin in state.json instead of the TOML, exactly to survive
self-update overwrite. All OTHER packages' pins live in their TOML.**)

Dev-loop testing on Windows host:
- `go vet ./...`, `go test ./...` — unit tests with `httptest` fixtures for both
  forge APIs (recorded JSON from real Codeberg/GitHub responses), tar-merge
  tests (duplicate paths, traversal rejection, ordering), URL-parse tests,
  state reconcile tests.
- `go run ./build` must produce `dist/kpm` (verify with `GOOS=linux GOARCH=arm`)
  and `dist/KoboRoot.tgz` (test unpacks it and asserts exact member list).
- On-device testing is manual and out of scope for the implementation agent;
  README gets a "first install & smoke test" section (telnet: `kpm version`,
  `kpm add`, `kpm check`, inspect `kpm.log`).

## 11. Stretch goal (design only in v1): uninstall

Implemented in v0.2.0 — see UNINSTALL.md (which supersedes this section; `extra_paths` was renamed).

Not implemented in v1, but v1 lays the groundwork by capturing manifests:

- `kpm uninstall <id>`: delete every path from the recorded `manifest`
  (rootfs is ext4, mounted rw; kpm is root — direct `rm` works, no tgz trick
  needed), then remove empty parent dirs it created, then the package's
  `[uninstall].extra_paths` (with confirmation flag `--purge` for those),
  finally unregister and log `UNINSTALL`.
- Guardrails: refuse paths outside `usr/local`, `opt`, `mnt/onboard/.adds`,
  `etc/udev`, `mnt/onboard/.kobo` allowlist; never follow symlinks; dry-run
  `--dry-run` prints the deletion list.
- Packages installed before kpm existed have no manifest → uninstall refuses
  unless the package is updated through kpm at least once first.

## 12. Explicit non-goals (v1)

- No dependency resolution between packages, no repo indexes/registries —
  sources are individual release pages.
- No firmware 5.x support (matches the ecosystem, e.g. NickelHardcover).
- No signature verification of assets (forges serve over TLS; noted in README).
- No GUI beyond NickelMenu entries + dialogs.
- No partial rootfs sandboxing — installing a package is trusting its author,
  same as manual KoboRoot.tgz installs. kpm's verify step only blocks
  path-traversal and warns on unusual paths.

## 13. Implementation order (for the coding agent)

1. `go.mod`, `internal/version`, `cmd/kpm` skeleton with subcommand dispatch,
   `internal/device` paths (overridable root via `KPM_ROOT` env for tests).
2. `internal/config` (TOML types, add-URL parser) + tests.
3. `internal/forge` (client, CA embed, both forges) + httptest tests.
4. `internal/tgz` (walk/verify/manifest, merge writer) + tests.
5. `internal/state` (atomic save, reconcile) + tests.
6. Commands: `add`, `remove`, `list`, `pin`, `unpin`, `log`, `status`, `version`.
7. Commands: `check`, then `update` (pipeline §7).
8. `build/package.go`, `res/nm-config`, README.
9. Full pass: `go vet`, `go test ./...`, cross-compile, unpack-and-assert the
   built KoboRoot.tgz.
