# kpm — Package Registry Design (implemented in v0.3.0)

**Status: implemented in v0.3.0. §9 pins the implementation decisions and
supersedes earlier sections where they conflict.** This document specifies how kpm
pulls package definitions ("plugin manifests") from one or more separate
git repos, so package defs can live outside the kpm repo, be community-edited
via PRs, and ship curated `[uninstall]` recipes. It also pins down what v0.2.x
must keep stable so this lands without breaking changes.

## 1. Concept

A **registry** is a git repo on any supported forge (GitHub or Forgejo/Gitea,
e.g. Codeberg) containing package definitions in **exactly the same TOML
schema as `packages.d/*.toml`** (PLAN.md §3 + UNINSTALL.md §2). kpm fetches it
over raw-file HTTPS — no git client, no clone, no new APIs beyond one raw GET.

The registry contains *definitions only* (forge coordinates, asset globs,
uninstall recipes) — never binaries. Trusting a registry means trusting its
maintainers to point at the right sources; actual software still comes from
each package's own release page over TLS, same as today.

## 2. Registry repo format

One file at the repo root: **`registry.toml`** (path overridable per
registry). Single-file because: one raw GET fetches everything (no directory
listing API → fully forge-agnostic), it's PR-able and human-readable, and Kobo
registries will realistically hold tens of packages, not thousands.

```toml
# registry.toml — schema_version 1
schema_version = 1

[packages.nickelhardcover]
name    = "NickelHardcover"
source  = "codeberg.org/StrayRose/NickelHardcover"
forge   = "forgejo"
asset   = "KoboRoot.tgz"
min_kpm = "0.2.0"                 # optional; skip+warn if running kpm is older

  [packages.nickelhardcover.uninstall]
  method      = "manifest"
  purge_paths = ["/mnt/onboard/.adds/NickelHardcover/**"]

[packages.nickelmenu]
name   = "NickelMenu"
source = "github.com/pgaskin/NickelMenu"
forge  = "github"
asset  = "KoboRoot.tgz"

  [packages.nickelmenu.uninstall]
  method       = "marker"
  marker_file  = "/mnt/onboard/.adds/nm/uninstall"
  needs_reboot = true
  purge_paths  = ["/mnt/onboard/.adds/nm/**"]

[packages.kpm]
name   = "kpm"
source = "github.com/<owner>/kpm"   # canonical home once published
forge  = "github"
asset  = "KoboRoot.tgz"
```

Rules:
- Table key = package id (`[a-z0-9-]+`), same as packages.d filenames.
- Per-package fields are identical to the local schema **except**: no `pin`
  (pins are a local decision, never distributed) and the additional optional
  `min_kpm` (compared against the running version; semver-ish string compare
  on dotted numerics is sufficient).
- `schema_version` gates parsing: unknown major → refuse with "update kpm";
  unknown *fields* are ignored (forward compatibility).
- Raw fetch URLs: GitHub `https://raw.githubusercontent.com/{o}/{r}/{ref}/registry.toml`
  (resolves both branch and tag refs). Forgejo/Gitea distinguishes them, so kpm
  tries `https://{host}/{o}/{r}/raw/branch/{ref}/registry.toml` then falls
  through to `.../raw/tag/{ref}/registry.toml` on a 404 — `--ref` may name a
  branch or a tag on either forge. Forge type detection reuses the existing
  `forge.Probe`.

## 3. Client configuration

New global config file `/mnt/onboard/.adds/kpm/config.toml` (this is the same
file already earmarked in PLAN.md §5 for an optional GitHub token — one global
config, two uses):

```toml
[[registries]]
name = "main"                          # local nickname, unique
url  = "github.com/<owner>/kobo-registry"
ref  = "main"                          # branch or tag
path = "registry.toml"                 # default; overridable
```

Multiple `[[registries]]` allowed; earlier entries win on package-id conflicts
(a conflict is logged as WARN once per refresh). Managed by CLI, hand-editable.

Cache: each registry's fetched file is stored at
`cache/registry-<name>.toml` with fetch time recorded in `state.json`
(`registries.<name>.last_fetched`, `etag` if the server sends one). Cache is
the working copy: all reads (search/install) hit the cache; network only on
`refresh` or when the cache is missing/stale (> 24 h) *and* a network-needing
command is already running. Offline with a cache → full functionality except
refresh. The `cache/` dir currently gets cleared after staging (PLAN.md §7.4);
that clearing must become selective (only `*.tgz` artifacts), or registry
caches move to a sibling dir — implementer's choice, noted here so it isn't
missed.

## 4. CLI

```
kpm registry add <url> [--name <n>] [--ref <branch>] [--path <p>]
kpm registry remove <name>            # forgets registry + its cache; installed packages unaffected
kpm registry list                     # name, url, ref, last refreshed, package count
kpm registry refresh [<name>]         # refetch (all registries by default)
kpm search [<term>]                   # list/filter available packages across cached registries;
                                      #   marks installed ones and available updates to their defs
kpm install <id> [--installed <ver>] [--pin <tag>]
                                      # copy def from registry into packages.d, provenance-stamped;
                                      #   then behaves exactly like an added package (check/update as usual)
kpm sync [<id>...]                    # re-copy registry defs for registry-managed packages (see §5)
```

`kpm add` (raw URL) stays unchanged for unregistered packages. `install` on an
id that already exists locally as hand-added → refuse with guidance (either
`remove` first, or keep the local def).

NickelMenu surface is unchanged: registry operations are CLI/telnet-first, and
once installed, registry packages flow through the existing "Check for
updates" / "Update all" entries identically to hand-added ones. (A later
nicety could chain `registry refresh` into the check entry; not in scope.)

## 5. Local defs vs registry defs: provenance and sync

When `install` copies a def into `packages.d/<id>.toml`, it stamps provenance:

```toml
registry = "main"        # absent on hand-added packages
```

- **`pin` stays purely local** — never written by, and never overwritten from,
  a registry.
- `kpm sync` re-copies every field *except* `pin` (and `registry` itself) from
  the current cached registry def, for packages whose `registry` field is set.
  This is how curated uninstall-recipe fixes and changed asset globs propagate.
  `sync` is explicit and reports a per-package diff summary; it is deliberately
  NOT automatic during `check`/`update` — silently changing install/uninstall
  behavior underneath the user is worse than being a command behind.
- A user who hand-edits a registry-managed def can delete the `registry` line
  to detach it; `sync` then skips it. `sync` detects local drift (cached def ≠
  local def before syncing) and says so rather than silently clobbering.
- If a package id disappears from its registry, `sync` warns but leaves the
  local def installed and functional (it's self-contained).

Self-update tie-in: once kpm has a published home repo, the registry carries
the canonical kpm def, and `kpm sync` fixes up the bootstrap placeholder
source shipped in kpm's own tgz (PLAN.md §9) — resolving that open loose end
without a new mechanism.

## 6. Trust model (unchanged in spirit, stated explicitly)

Adding a registry = trusting its maintainers to the same degree as running
`kpm add` on each of its entries yourself: defs choose the source repo and the
uninstall recipe (including `run_before`/`run_after` hooks, which execute as
root). Mitigations, consistent with existing non-goals (PLAN.md §12):
- TLS everywhere via the embedded CA bundle; no plaintext fallback.
- `install` prints the def it is about to write (source, asset, uninstall
  method, hooks) before writing it; `--yes` skips the pause — mirroring the
  uninstall confirmation pattern (exit 3 without `--yes`).
- Uninstall path policy (UNINSTALL.md §4) still applies at uninstall time
  regardless of what a registry def claims — the hard denylist is not
  overridable by distributed `allow_paths`.
- No def signing in this version; noted in README like the asset-signing
  non-goal.

## 7. What v0.2.x must keep stable (design-for-it-now checklist)

These are the only obligations the current codebase carries so this feature
can land later without breakage — no registry code should be written yet:

1. **The package TOML schema is an interchange format.** Any future change to
   `packages.d` fields (incl. `[uninstall]`) must remain backward compatible:
   new fields optional, old fields never repurposed. (`registry` and `min_kpm`
   become reserved field names — don't reuse them for anything else.)
2. **Unknown TOML fields must not be errors** when loading package defs (v1
   already behaves this way; keep it). `config.Save` (used by `pin`/`unpin`)
   also **preserves** unknown top-level keys and unknown `[uninstall]` keys by
   merging over the existing file — field preservation is guaranteed, but
   comment preservation is **not** (a TOML-library limitation): `pin`/`unpin`
   rewrite the file and drop comments.
3. **Package ids remain `[a-z0-9-]+`** and filename == id.
4. **`state.json` stays a flexible map** — adding a top-level `registries` key
   must be a non-event (stdlib JSON with omitempty already satisfies this).
5. **Cache clearing** after staging should not assume everything in `cache/`
   is a download artifact (see §3) — the start-of-run cache hygiene only touches
   `*.part` (always) and `*.tgz` (older than 7 days), never other patterns.

## 8. Implementation sketch (for the eventual agent)

- `internal/registry`: raw-URL builder per forge, fetch+cache+ETag, parse
  (reusing `internal/config` types for the package payload), conflict
  resolution across registries, sync/diff logic. Pure logic separated from
  device I/O, tests via `httptest` + KPM_ROOT sandbox, same as v1 patterns.
- `internal/config`: `config.toml` load/save (`[[registries]]`,
  future `github_token`), `registry` provenance field on package defs.
- `cmd/kpm`: subcommand group `registry`, plus `search`, `install`, `sync`.
- Version target 0.3.0; README section "Registries" with a walkthrough:
  create the repo, commit `registry.toml`, `kpm registry add`, `search`,
  `install nickelmenu`, `sync`.
- Estimated to reuse ~everything: forge client, TOML types, atomic state
  writes, log/status conventions.

## 9. v0.3.0 implementation decisions (authoritative; reconciled with v0.2.1)

1. **Lock classification** (B1 lock from FIXES-0.2.1): `registry add/remove/
   refresh`, `install`, and `sync` are mutating (acquire the lock, may write
   state/config/cache/packages.d). `registry list` and `search` are read-only
   (no lock, no reconcile, no writes — same split as list/status/log).
2. **Network happens ONLY in `kpm registry refresh`** (gated by the same
   WaitForNetwork used by check/update), plus an optional one-time
   forge-detection probe during `registry add` (skipped with `--forge`).
   `search`/`install`/`sync` read the
   cached `cache/registry-<name>.toml` exclusively; if a needed cache is
   missing, error: `no cached data for registry "<name>" — run: kpm registry
   refresh`. This supersedes §3's "auto-refresh when stale during a
   network-needing command" — on-device predictability beats freshness; a
   staleness note (`cached 3d ago`) is shown in `search`/`registry list`
   output instead. The >24h staleness rule is dropped.
3. **Refresh & caching**: refresh sends `If-None-Match` when an etag is
   recorded; 304 keeps the cache and updates `last_fetched`. Fetched TOML is
   parsed BEFORE the cache file is replaced (atomic temp+fsync+rename, like
   status.txt) — a bad fetch never clobbers a good cache. `schema_version`
   missing or != 1 → refuse that registry with "update kpm" / "unsupported
   registry schema", keep the old cache. Raw URL per forge as §2; forge type
   is detected once at `registry add` (github.com → github, else Probe) and
   stored in config.toml as `forge` per registry entry.
4. **State**: new top-level `registries` map in state.json:
   `{"<name>": {"last_fetched": <ts>, "etag": "<v>"}}` — plus per-package
   `synced_def_sha256` (see 7). Unknown-field tolerance unchanged.
5. **config.toml**: `/mnt/onboard/.adds/kpm/config.toml`, `[[registries]]`
   entries with `name`, `url` (host/owner/repo), `ref` (default "main"),
   `path` (default "registry.toml"), `forge`. Load: missing file = empty
   list. Save: map-merge preserving unknown keys, exactly like the E1
   package-def Save. Names must be unique and ValidID-shaped; URL parsing
   reuses the addurl host/owner/repo logic (no /releases forms).
6. **install flow**: `kpm install <id> [--pin <tag>] [--installed <ver>]
   [--yes] [--adopt]`. Without `--yes`: print the def about to be written
   (registry, source, forge, asset, min_kpm, full [uninstall] incl. hooks)
   and exit 3 — same confirm pattern as uninstall. Existing hand-added local
   def → refuse (exit 1) unless `--adopt`, which takes the def over: writes
   the registry def + `registry = "<name>"` while preserving the local `pin`
   and all state (installed version, manifest). `--adopt` is how kpm's own
   def gets a real source once a registry carries it (§5 tie-in). id
   conflicts across registries: earlier registry in config order wins;
   shadowed entries are invisible to install/search (WARN once per refresh).
7. **sync & drift**: when install/sync writes a registry def, record
   `synced_def_sha256` in state = SHA-256 of the canonical encoding of the
   registry def (excluding pin/registry keys; zero-length slices normalized to
   nil so `purge_paths = []` never reads as drift). `kpm sync [<id>...]` for
   each registry-managed package compares `localHash` (the local def minus
   pin/registry) against `remoteHash` and the stored `synced_def_sha256`:
   (1) `localHash == remoteHash` → up to date, and if the stored hash differs it
   is backfilled (heals empty/stale/legacy hashes — content-equal defs are
   always up to date regardless of the stored hash); (2) else no stored hash →
   apply; (3) else `localHash == synced` (clean upstream change) → apply;
   (4) else local drift → report and SKIP unless `--overwrite`. Apply writes the
   registry def with **replace-semantics** (dropped known fields disappear;
   unknown local keys are preserved), reports the changed fields, and updates the
   stored hash. Package id gone from its registry → WARN, leave local def intact.
   `pin` is never read from or written to a registry def anywhere. Stored hashes
   are tied to the canonical encoding; because rule (1) keys off content
   equality, a future encoding change self-heals as one transparent hash
   backfill with no false drift (C10). Sync of an unregistered id is a usage
   error (exit 1); partial skips/failures among valid targets stay exit 2.
8. **min_kpm**: numeric dotted compare (split on ".", missing segments = 0,
   single leading "v" stripped — reuse/extend the tagsEqual helper family).
   Running version older → `search` shows `requires kpm >= X`, `install`
   refuses (exit 1). kpm's own version string is the ldflags value.
9. **Log verbs** (all <= 9 chars, existing column format): `REGADD`,
   `REGREMOVE`, `REFRESH`, `INSTALL`, `SYNC`.
10. **Exit codes**: unchanged conventions — 0 ok, 1 error/refused, 2 partial
    (e.g. refresh where some registries failed, sync where some packages
    skipped/failed), 3 confirmation required (install without --yes).
11. **Cache-clearing compat**: v0.2.1's cleanCache already touches only
    `*.part` and `*.tgz`; registry caches (`registry-<name>.toml`) are
    untouched. `registry remove` deletes its cache file and state entry;
    installed packages are unaffected.
12. **Docs**: README gains a "Registries" walkthrough (create repo, commit
    registry.toml, registry add, refresh, search, install, sync, adopt);
    PLAN.md §6 command list extended; this doc's status header updated when
    done. Version 0.3.0, artifact to releases/0.3.0/KoboRoot.tgz.
13. **Tests** (httptest + KPM_ROOT sandbox, existing patterns): refresh
    success/304-etag/bad-TOML-keeps-old-cache/schema-gate; conflict
    resolution and shadowing WARN; search filtering + staleness note +
    min_kpm annotation; install confirm exit 3 / write / refuse-hand-added /
    --adopt preserving pin+state; sync apply/diff/drift-skip/--overwrite/
    missing-id WARN; min_kpm compare table; config.toml unknown-field
    round-trip; registry remove cleanup; read-only vs mutating lock split.
