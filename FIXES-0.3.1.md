# kpm v0.3.1 — Registry deep-review fix specification

Every finding from the 2026-07-19 three-lens registry review, with prescribed
fixes. Version **0.3.1**, artifact to `releases/0.3.1/KoboRoot.tgz`. Same
conventions as FIXES-0.2.1.md: each fix needs a test unless doc-only; update
README/PLAN.md/REGISTRY.md wherever behavior changes.

## A. Critical cluster — sync write semantics and hashing

**A1. Replace-semantics save for full-def writes.**
Root cause: `config.Save` is merge-only; `overlayUninstall`
(internal/config/config.go:193) never deletes keys, so sync/adopt cannot
remove `[uninstall]` fields a registry dropped (stale root-executed hooks
persist while the synced hash claims they're gone).
Fix: add `config.SaveReplace(path, pkg)` (or a mode flag on Save): unknown
top-level keys and unknown `[uninstall]` keys are still preserved, but ALL
known keys are treated authoritatively — delete every known key from the
existing maps first, then overlay non-zero values (so a zero-value known
field disappears; `needs_reboot == nil` deletes the key; empty slices delete
the key). Use SaveReplace in `sync` apply and `install`/`install --adopt`
def writes. `add`/`pin`/`unpin` keep the existing merge Save (they edit a
def loaded from the same file — merge is correct there).
Tests: registry v1 def (marker method, marker_file, needs_reboot=true,
run_before) synced, then registry v2 (manifest method, no hooks) synced →
local file has method="manifest" and NO marker_file/needs_reboot/run_before
keys; an unknown custom key inside [uninstall] survives both syncs; adopt
over a hand-added def with extra [uninstall] fields produces exactly the
registry def plus unknown keys.

**A2. Sync decision tree (fixes the `--overwrite` no-op and self-heals stale
hashes).**
Where: cmd/kpm/cmd_sync.go (~:138-156).
Fix — compute `localHash = HashDef(DefFromPackage(local))` and use this tree:
1. `localHash == remoteHash` → up to date; if `synced != remoteHash`, update
   `synced_def_sha256 = remoteHash` (heals empty/stale/legacy hashes,
   including a future canonical-encoding change — content-equal defs are
   always "up to date" regardless of the stored hash).
2. else if `synced == ""` → apply (no baseline; current behavior, keep).
3. else if `localHash == synced` → apply (clean upstream change).
4. else → local drift: skip + report (exit 2) unless `--overwrite` → apply.
"Apply" = SaveReplace + on success set `synced = remoteHash`. Report the
per-field diff as today.
Tests: hand-edit local def, remote unchanged → plain sync skips with drift
message; `sync --overwrite` RESTORES the file (this is the exact case the
old tests missed); double sync after any apply → up to date; stale/empty
synced with identical content → up to date and hash backfilled.

**A3. Hash normalization for slices.**
Where: internal/registry/resolve.go HashDef.
Fix: before encoding, normalize the def — every zero-length slice
(non-nil-but-empty) becomes nil, in both the registry-parsed and
local-derived paths (do it inside HashDef so all callers get it). Do NOT
normalize `needs_reboot` (nil vs explicit false are semantically different
for the marker method and round-trip consistently).
Test: registry def containing `purge_paths = []` → install → sync twice →
"up to date" both times, no drift (the review's reproduced false-drift case).

## B. Refresh and URL bugs

**B1. 304-with-missing-cache deadlock.**
Where: cmd/kpm/cmd_registry.go refreshOne (~:280) / storeRefresh (~:301).
Fix: only send If-None-Match when the cache file exists (stat before
building the request). Belt-and-braces: if a 304 arrives and the cache file
is missing, treat it as an error instructing retry — but with the stat
guard this is unreachable; implement the guard, keep storeRefresh's 304
branch as is.
Test: state has an etag, cache file deleted → refresh performs an
unconditional fetch (assert the test server saw no If-None-Match) and
rewrites the cache.

**B2. Forgejo tag refs can never refresh (`/raw/branch/` resolves branches
only — verified live against Codeberg; tags need `/raw/tag/`).**
Where: internal/registry/manifest.go RawURL (~:73-92), refreshOne.
Fix: for forgejo, produce both candidate URLs (`/raw/branch/<ref>/` then
`/raw/tag/<ref>/`); refreshOne tries them in order, falling through to the
second only on ErrNotFound (any other error aborts normally). GitHub keeps
its single raw.githubusercontent.com form (accepts both ref kinds). Docs'
"branch or tag" becomes true.
Tests: httptest server 404s the branch path and serves the tag path →
refresh succeeds; both-404 → clean not-found error.

**B3. Unbounded FetchRaw body read.**
Where: internal/forge/http.go FetchRaw (~:189).
Fix: wrap the body in `io.LimitReader(body, 4<<20 + 1)`; if more than 4 MiB
was read, fail with "registry manifest exceeds 4 MiB". Test with an
oversized httptest response.

## C. Validation & CLI polish

**C1.** `install` refuses defs with empty `source`, `forge`, or `asset`
(exit 1, message naming the missing fields and the registry) — no more
silent "unconfigured" packages from registry typos. `search` marks such
entries `invalid def`. Tests both.
**C2.** LoadConfig skips `[[registries]]` entries whose name fails ValidID,
with a WARN naming the entry — closes the hand-edited `evil/../..` cache
escape. RegistryCache also rejects (error/panic-guard) names failing
ValidID as defense in depth. Test.
**C3.** `registry remove/refresh/list` and `search` parse their flags
properly: unknown flags error (exit 1); `registry refresh` accepts at most
one positional (the name) and errors on flags like `--name` (hint: "pass
the name as a positional"); `registry list` and `search` reject unexpected
extra positionals per the G4 convention (search keeps its single optional
term). Tests.
**C4.** `registry add` rejects URLs containing `/releases` (message:
`registry URLs must not include /releases — use --ref <branch-or-tag>`)
instead of silently discarding the parsed tag. Test.
**C5.** `install --adopt` on an already-registry-managed id: when the
winning registry differs from recorded provenance, the pre-write summary
(and the final output) must state `provenance: <old> -> <new>`; same-registry
adopt stays a quiet re-install. Doc the behavior in README. Test the
provenance-change disclosure.
**C6.** `search` STATUS accuracy: `installed` only when
`installed_version != ""`; a def-only package shows `registered`. The
def-update check compares against the package's recorded PROVENANCE
registry def (matching sync), falling back to the winner only when
provenance is absent — eliminates the search-says-update/sync-says-clean
disagreement under shadowing. min_kpm annotation only shown when the
package is not already installed. Tests.
**C7.** `sync <id>` where the id is not registered at all → exit 1 (usage
error), not 2; partial failures among valid targets stay exit 2. Skip the
`state.Save()` when nothing changed. Test the exit codes.
**C8.** refreshOne must not create a `registries` state entry until a
successful fetch/304 (no empty `{}` entries from failed refreshes). Test.
**C9.** Dev builds (`version.Version == "dev"`): min_kpm gate passes with a
printed note `(dev build: skipping min_kpm check)` instead of refusing
everything. Test.
**C10.** (doc-only) REGISTRY.md §9: note that stored sync hashes are tied
to the canonical encoding, and that A2's content-equality check makes an
encoding change self-healing (one transparent hash backfill, no false
drift).

## D. Documentation truth sweep

**D1.** "Network only in `registry refresh`" (README ×2, REGISTRY.md §9.2,
PLAN.md §6 note) → amend to "…plus an optional one-time forge-detection
probe during `registry add` (skip it with `--forge`)".
**D2.** Ensure every "branch or tag" mention is consistent with B2's now-real
tag support (`--ref` help text, README, REGISTRY.md §3).
**D3.** README exit-codes section: exit 3 is "confirmation required
(`uninstall`/`install` without `--yes`)"; PLAN.md §6 exit-code note gains
code 3.
**D4.** README read-only command list adds `search` and `registry list`.
**D5.** `cache/` descriptions (README, PLAN.md §2) updated: holds package
downloads (`*.tgz`/`*.part`, swept selectively) AND registry caches
(`registry-<name>.toml`, never swept).
**D6.** TestCleanCache gains an assertion that a `registry-<name>.toml`
file in cache/ survives the sweep (closing the noted test gap).

## E. Release

Version 0.3.1 (build default + ldflags), artifact copied to
`releases/0.3.1/KoboRoot.tgz`. Definition of done: `go vet ./...` clean,
`go test -count=1 ./...` green including every new test above,
`go run ./build` self-check passes, `go mod tidy -diff` empty.

## Not in scope
Registry def signing, auto-refresh, GitHub Enterprise, comment-preserving
TOML, NickelMenu surface changes (res/nm-config untouched).
