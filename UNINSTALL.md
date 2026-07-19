# kpm — Uninstall Design (v0.2.0)

Implements and supersedes PLAN.md §11. Goal: `kpm uninstall <id>` with per-package
customization via the `[uninstall]` table in `packages.d/<id>.toml` (parsed but
inert since v1). Everything here follows the existing architecture: all
filesystem access through `internal/device`'s overridable root so the logic is
unit-testable on the Windows host, all mutations logged to `kpm.log` and
summarized in `status.txt`.

## 1. Why per-package customization is needed

Real packages remove differently:

- **NickelHardcover-style** (the common case): delete exactly what the tgz
  installed → the recorded manifest is the source of truth.
- **NickelMenu-style**: has an *official* mechanism — create the file
  `/mnt/onboard/.adds/nm/uninstall` and reboot; NickelMenu removes itself on
  next boot. Deleting its files out from under a running Nickel is the wrong
  move. → a **marker** method.
- **Pre-kpm installs**: no manifest recorded → allow a hand-maintained path
  list to substitute for it.
- **User data vs software**: config/annotation/data dirs should survive a
  normal uninstall and only go with an explicit `--purge`.
- **Dangerous manifests**: a package may legitimately *overwrite* firmware
  files (e.g. patched system libs). Deleting those would break Nickel. kpm
  cannot know a file pre-existed, so safety comes from path policy (§4), with a
  per-package escape hatch.

## 2. `[uninstall]` schema (all fields optional)

```toml
[uninstall]
method       = "manifest"      # "manifest" (default) | "marker"
extra_paths  = []              # additional software artifacts to always delete
purge_paths  = []              # user data/config; deleted ONLY with --purge
keep_paths   = []              # subtract from the deletion set (protect user-edited shipped files)
allow_paths  = []              # per-package extension of the deletable-path allowlist (§4)
marker_file  = ""              # method="marker": file to create (required for marker)
needs_reboot = false           # default true when method="marker", else false
run_before   = ""              # absolute path/command run via /bin/sh -c before removal; nonzero exit aborts
run_after    = ""              # same, after removal; nonzero exit is logged (WARN), not fatal
```

Renames vs PLAN.md §11: the old `extra_paths` concept ("config dirs, purge
only") is now `purge_paths`; `extra_paths` now means "always-delete additions".

All configured paths are absolute device paths (`/mnt/onboard/...`,
`/usr/local/...`). `extra_paths`/`purge_paths`/`keep_paths` entries may end in
`/**` to mean "this directory recursively"; otherwise they name a single file
or an empty-dir-to-remove. No other globbing.

Semantics by method:

- **manifest**: deletion set = recorded manifest ∪ `extra_paths`, minus
  `keep_paths`. If no manifest exists (pre-kpm install), fall back to
  `extra_paths` alone; if that is also empty, refuse with guidance ("update the
  package through kpm once, or configure [uninstall].extra_paths").
- **marker**: create `marker_file` (parent dirs created as needed; content:
  single line `uninstall requested by kpm <version> <timestamp>`); the file
  deletion set is empty. `extra_paths`/`purge_paths`/`keep_paths` still apply
  (e.g. purge NickelMenu's config dir alongside the marker with `--purge`).
  `marker_file` unset → config error. The marker path is subject to the path
  policy (§4) exactly like a deletion candidate: it must be within an allowed
  (built-in or `allow_paths`) prefix, never a denylisted one, or Compute errors.
  Marker creation is idempotent: if the file already exists it is left untouched
  (never truncated/rewritten) and treated as success; a directory in its place
  is an error.

`run_before`/`run_after` exist for cases like stopping a daemon or deregistering
a udev rule. Trust model is identical to installing the package (its tgz already
ran arbitrary code paths as root); noted in README.

## 3. CLI

```
kpm uninstall <id> [--purge] [--dry-run] [--yes] [--force] [--keep-registration] [--reboot] [--notify]
```

Flow:

1. Refuse `kpm uninstall kpm` (self-uninstall) with a message pointing at the
   README's manual-removal instructions. Refuse if `<id>` has a pending staged
   version (state says staged and `.kobo/KoboRoot.tgz` still present) — the
   reboot would reinstall it; tell the user to reboot or remove the staged tgz
   first (`update` guard logic already distinguishes kpm-staged tgzs).
2. Compute the **uninstall plan** (pure function; §5): ordered actions +
   skipped-path warnings.
3. `--dry-run`: print the plan and exit 0. Without `--yes`: print the plan and
   exit code 3 ("re-run with --yes to apply") — uninstall is destructive and
   kpm runs non-interactively, so explicit confirmation is a flag, not a
   prompt.
4. With `--yes`: run `run_before` (abort on failure unless `--force`), apply
   actions, run `run_after`, write `kpm.log` (`UNINSTALL` verb; `PURGE` lines
   for purge deletions; `WARN` for skips/failures) and `status.txt`.
5. On success: clear the package from `state.json`; delete
   `packages.d/<id>.toml` unless `--keep-registration`.
6. `--reboot` reboots after success (existing device.Reboot). If
   `needs_reboot` is true and `--reboot` wasn't given, the final status line
   and toast say a reboot is required to complete removal.
7. Partial failures (some paths failed to delete): finish the rest, keep state
   entry (so it can be retried), exit 2.

Exit codes: 0 ok/dry-run, 1 error/refused, 2 partial, 3 confirmation required.

No NickelMenu entries are shipped for uninstall — it stays CLI-only (telnet)
in this version, deliberately: a destructive one-tap menu item is a footgun.
`res/nm-config` is unchanged.

## 4. Path policy (safety)

Policy applies to every deletion candidate regardless of origin (manifest,
`extra_paths`, `purge_paths`). Evaluation order per path, after cleaning and
resolving to an absolute device path:

1. **Hard denylist — always refused, even via `allow_paths`** (deleting these
   can brick Nickel/boot): anything under `/bin`, `/sbin`, `/lib`,
   `/etc/init.d`, `/etc/inittab`, `/drivers`, and anything under
   `/usr/local/Kobo` EXCEPT `/usr/local/Kobo/imageformats` (NickelHook mods
   like libnm.so/libndb.so legitimately live there and must be removable).
   Also refuse `/`, any path shorter than 2 components, and kpm's own
   `.adds/kpm/bin`, `state.json`, `kpm.log`.
2. **Built-in allowlist — deletable**: under `/mnt/onboard/.adds`,
   `/mnt/onboard/.kobo`, `/usr/local` (minus the Kobo exception above),
   `/usr/bin`, `/usr/lib`, `/opt`, `/etc/udev/rules.d`, `/etc/dbus-1`.
3. **Per-package `allow_paths`** extends (2) with additional prefixes; cannot
   override (1).
4. Anything else → **skipped with a logged WARN**, uninstall continues (the
   package's remaining files are still removed). `--force` does NOT bypass
   path policy — it only bypasses the `run_before` failure abort.

Traversal/symlink rules: reject `..` components; use lstat everywhere; never
follow symlinks (delete the link itself when it's the candidate); recursive
`/**` deletion must not cross out of the stated prefix via symlinked dirs
(walk with lstat, don't descend into symlinks). A single-file delete is also
aborted (skipped, "symlinked parent") if any intermediate directory under the
allowlisted prefix is a symlink, so a deletion can't be redirected out of the
package's tree.

**Case sensitivity:** paths at or under `/mnt/onboard` (FAT32) are compared
case-insensitively for every policy decision (classify, allow/deny, keep,
rmdir-root protection); rootfs (ext4) paths stay case-sensitive. So
`/mnt/onboard/.adds/kpm/BIN/kpm` is still self-denied, while `/usr/local/KOBO`
is *not* the denied `/usr/local/Kobo`. Original casing is preserved for the
actual deletion; only comparisons fold case.

## 5. Plan computation & execution order

`internal/uninstall` package:

- `Compute(manifest []string, cfg config.Uninstall, purge bool) (Plan, error)` —
  pure, fully unit-tested. Produces:
  1. optional `run_before`
  2. optional marker creation
  3. file deletions (manifest files + extras [+ purge set]), keep-set removed
  4. directory removals: every directory that appeared in the manifest or
     became a candidate parent, deepest-first, **rmdir only if empty** —
     shared dirs like `/mnt/onboard/.adds` or `/usr/local/Kobo/imageformats`
     survive naturally because other packages' files keep them non-empty
  5. optional `run_after`
  plus `Skipped []SkippedPath` (path + reason: denylist / not-allowlisted /
  kept).
- `Execute(dev *device.Device, plan Plan) Result` — applies via the device
  layer; missing files are fine (log as "already absent", not an error);
  collects per-action outcomes for the log/status/exit code. `keep_paths` are
  honored *inside* recursive (`/**`) deletes too: the walk checks the keep set
  at every node, preserves any match (recorded as `Kept`), and a directory that
  still holds a kept survivor is left in place by the rmdir-if-empty pass.
- Manifest paths are stored tar-style (`usr/local/...` — no leading slash);
  normalize to absolute device paths in one place.

## 6. State, packaging, docs, versioning

- `internal/config`: replace the v1 placeholder `Uninstall` struct with the §2
  schema; unknown-field tolerance same as the rest of the config (BurntSushi
  strictness unchanged from v1 behavior). Validate on load: bad `method`,
  marker without `marker_file`, denylisted `allow_paths` entries → clear errors
  at uninstall time (registration/`check`/`update` must not start failing on a
  bad `[uninstall]` block — packages must remain updatable regardless).
- `kpm remove` (unregister-only) is unchanged and its help text should now
  point at `kpm uninstall` for actual removal.
- README: new Uninstall section — usage, the two methods with a worked
  NickelMenu marker example (`marker_file = "/mnt/onboard/.adds/nm/uninstall"`,
  `needs_reboot = true`) and a NickelHardcover manifest example with
  `purge_paths = ["/mnt/onboard/.adds/NickelHardcover/**"]`; manual removal
  instructions for kpm itself (delete `.adds/kpm`, `.adds/nm/kpm`).
- PLAN.md: at the top of §11 add one line: "Implemented in v0.2.0 — see
  UNINSTALL.md (which supersedes this section; `extra_paths` was renamed)."
- Version: 0.2.1 (see FIXES-0.2.1.md). `go run ./build` self-check member list
  is unchanged; copy the artifact to `releases/0.2.1/KoboRoot.tgz` built with
  `-X kpm/internal/version.Version=0.2.1`.

## 7. Tests (host-side, all through KPM_ROOT/device root override)

- Plan computation: manifest∪extra−keep math; purge on/off; marker method
  (incl. missing marker_file error); no-manifest fallback to extra_paths;
  no-manifest+no-extras refusal.
- Path policy: denylist beats allow_paths; imageformats exception; skip+warn
  for unlisted paths; `..` rejection; `/**` suffix handling.
- Execution in a temp sandbox: creates real files/dirs, runs Execute, asserts
  exact survivors — shared-dir survival (two packages in one parent dir),
  deepest-first empty-dir cleanup, missing-file tolerance, symlink
  non-traversal (skip symlink cases on Windows hosts where creation needs
  privileges — guard with a helper that t.Skips if symlink creation fails).
- CLI: exit code 3 without `--yes`; `--dry-run` mutates nothing; state cleared
  and packages.d file removed on success; `--keep-registration`; staged-pending
  refusal; self-uninstall refusal; partial-failure exit 2.
- Config: schema round-trip, validation errors, and that a bad `[uninstall]`
  block does not break `list`/`check`/`update`.
