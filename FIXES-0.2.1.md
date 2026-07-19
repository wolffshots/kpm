# kpm v0.2.1 — Deep-review fix specification

Every finding from the 2026-07-19 four-lens deep review, with a prescribed fix.
Work through ALL of them. Each fix keeps the existing architecture (PLAN.md,
UNINSTALL.md) unless stated; where behavior/docs change, update PLAN.md /
UNINSTALL.md / README.md in the same pass. Version: **0.2.1**, artifact to
`releases/0.2.1/KoboRoot.tgz`.

Conventions: "policy" = internal/uninstall/policy.go; "the guard" =
guardExistingTgz in cmd/kpm/cmd_update.go. Every fix needs a test unless
marked (doc-only) or inherently untestable on the host; put tests beside the
code they cover in the existing style.

---

## A. Criticals

**A1. Re-staging drops previously staged packages, then falsely promotes them.**
Where: cmd/kpm/cmd_update.go (resolveAndDownload skip at ~:186, stage() at
~:280, cache cleanup ~:109), internal/state/reconcile.go.
Fix: when a kpm-staged `KoboRoot.tgz` already exists (guard says ours), include
that existing tgz as the FIRST merge source in `stage()`, before the run's new
targets (kpm-last ordering still applies to new targets; the re-merged old tgz
staying first is correct — later entries win). The staged tgz is a valid tgz
kpm itself wrote, so `tgz.Merge` can stream it. Do NOT log duplicate-path
warnings for collisions between the re-merge source and new targets when the
new target is a re-stage of the same package at a different version (suppress
dup warnings originating from the re-merge source entirely — they're expected).
Result: every package with `staged_version` set has its payload present in the
final staged tgz, so Reconcile's promote-all remains correct.
Tests: stage A; then stage B in a second run; assert merged tgz contains both
A's and B's members and both promote correctly after simulated reboot (tgz
removed). Also: re-stage A at a NEWER tag in run 2 → new payload wins, no
false dup warning.

**A2. NickelMenu labels contain `:` — config invalid, entire menu fails.**
Where: res/nm-config, PLAN.md §8. NickelMenu labels must not contain colons.
Fix: relabel to `kpm - Check for updates`, `kpm - Update all`, `kpm - Status`
(plain ASCII hyphen). Update PLAN.md §8 to match and add a comment line in
res/nm-config noting labels must not contain ':'. Also verify the two
`dbg_toast` chain messages contain no ':' (em dash is fine).
Test: extend the build self-check/test to assert no `menu_item` line's label
field contains a colon (parse fields by splitting on ':' after trimming
space-padded separators — 5 fields exactly for menu_item lines).

## B. State lifecycle & concurrency (majors)

**B1. No single-instance lock.**
Fix: exclusive lock file `.adds/kpm/lock` acquired in newApp via
`os.OpenFile(O_CREATE|O_EXCL)` containing pid+timestamp. Retry for up to 5 s
(250 ms interval). Stale-lock recovery: if the file is older than 10 minutes,
remove and retake (device has no long-running kpm operations beyond a
download; document the constant). On acquire failure: mutating commands
(add/remove/pin/unpin/check/update/uninstall) fail exit 1 with "another kpm
instance is running"; read-only commands (list/status/log/version) proceed
WITHOUT the lock but skip reconcile/seedSelf and all state/status writes
(pure read). Release (remove file) on all exit paths of run(); best-effort.
Tests: lock held → mutating command errors; read-only command still prints;
stale lock is broken.

**B2. Manifest overwritten at stage time.**
Fix: add `staged_manifest` to state (`PackageState.StagedManifest`); stage
records there; Reconcile moves StagedManifest → Manifest on promotion and
clears it; uninstall keeps using Manifest (installed). Migration: absent field
= empty slice, no migration needed (unknown JSON fields already tolerated on
load).
Tests: stage-then-delete-tgz-manually leaves Manifest untouched… (see B4 for
the delete semantics) — at minimum: staging does not modify Manifest;
promotion installs StagedManifest.

**B3. No fsync before critical renames; corrupt state.json bricks every command.**
Fix: (a) `File.Sync()` before Close in: state.Save temp file, tgz.Merge output,
download `.part` files, build/package.go output (host-side, cheap). (b)
state.Load: on JSON unmarshal error, rename the bad file to
`state.json.corrupt-<unixts>`, log WARN, continue with fresh empty state
(versions become re-seedable via `add --installed`/self-seed; document in
README troubleshooting).
Tests: corrupt state.json → command succeeds, corrupt file renamed, WARN
logged.

**B4. Reconcile promotes on any tgz disappearance / any stat error; guard
trusts state presence, not content.**
Fix: (a) record `staged_sha256` (and size) in state when staging. (b) The
guard: if a tgz exists and hash matches → ours (re-stage per A1); exists and
hash differs → foreign: refuse with the existing message (manual install
protected). (c) reconcile: only `os.IsNotExist` counts as "gone"; any other
stat error → skip reconcile this invocation (log WARN once). (d) Manual
deletion of the staged tgz still promotes (accepted PLAN §4 behavior) — but
document in README that deleting the staged file yourself desyncs kpm and the
supported way to cancel a staging is a new command: add `kpm unstage`
(mutating, locks): removes the staged tgz IF hash matches, clears all
staged_* fields, logs `UNSTAGE` per package. Update PLAN §6 command list +
README.
Tests: hash mismatch → guard refuses; stat error path (use a file where a
parent is a file to force a non-IsNotExist error, or skip if not portable);
unstage clears fields and removes file.

**B5. state.Save() errors ignored (cmd_update.go:72, main.go seedSelf/reconcile).**
Fix: propagate: log ERROR and fail the command (exit 1) — a state write
failure is not ignorable on this design. Reconcile's promotion logging happens
only after a successful save (avoid duplicate INSTALLED lines on retry:
acceptable to log after save).

## C. Uninstall safety (majors + related minors)

**C1. keep_paths ignored inside recursive deletes.**
Fix: thread the keep set into Execute: `removeTree` (and the single-file
delete path) receives a `func(abs string) bool` kept-predicate built from the
plan's keepSpecs (translate host path back to device path for the check, or
precompute kept device-paths — implementer's choice, but the predicate must
apply at every node visited, and a directory containing kept survivors is
naturally preserved by rmdir-if-empty). A kept path inside a recursive target
is reported in Result as Kept, not deleted.
Tests: the exact review scenario — extra_paths Foo/**, keep_paths
Foo/user.cfg → user.cfg survives, siblings deleted, Foo dir survives
(non-empty).

**C2. FAT32 case-insensitivity defeats policy on onboard paths.**
Fix: in policy (classify, under, self-deny checks, isKept, and rmdir-root
protection), compare paths case-insensitively **when the path is under
/mnt/onboard** (both operands lowercased for comparison only; original casing
preserved for display/deletion). Rootfs (ext4) comparisons stay
case-sensitive. Centralize in a `pathEqual/pathUnder(a, b)` helper pair that
applies the rule; use everywhere policy compares paths.
Tests: `/mnt/onboard/.adds/kpm/BIN/kpm` is denied (self-deny);
`/MNT/ONBOARD/.adds/foo` classifies like lowercase; `/usr/local/KOBO/x` is
NOT denied (case-sensitive off-onboard).

**C3. marker_file bypasses policy and truncates.**
Fix: in Compute, classify the marker path with the package's allow_paths; it
must be vAllowed, else config error. In Execute: if the marker file already
exists, do NOT truncate/write — treat as success (idempotent, `AlreadyGone`-
style outcome "already present"); if the path exists as a directory → error.
Tests: marker at denied path → Compute error; existing marker file untouched
(content preserved); marker in allowed location created with parents.

**C4. (minor) Allowlist roots eligible for rmdir.**
Fix: computeRmdirs never emits an allowlist root itself (built-in or
allow_paths entry). Test included.

**C5. (minor) Backslash normalization wrong for Linux target.**
Fix: only apply the `\`→`/` rewrite in cleanDeviceAbs when
`runtime.GOOS == "windows"` (host tests); on the target, backslash is a
literal filename byte. Adjust tests to exercise via forward-slash inputs so
they pass on both hosts.

**C6. (minor) Trailing slash after `/**` silently changes meaning.**
Fix: splitRecursive also accepts `/**/` (trailing slash) as recursive; and a
path whose final component is exactly `**` without the recursive suffix form
→ config validation error ("use /** for recursive deletion").

**C7. (minor) Single-file delete follows symlinked parent.**
Fix: before deleting a non-recursive candidate, lstat each intermediate
component under the top allowlisted prefix and abort that action (Skipped,
reason "symlinked parent") if any component is a symlink. Do the same for the
recursive root. Test with symlinks where creatable (reuse the existing
t.Skip-on-privilege-failure helper).

**C8. (minor) TOML delete-failure ordering leaves half-removed package.**
Fix: in cmd_uninstall, remove the packages.d file BEFORE clearing state; if
file removal fails, keep state intact and exit 2 with a clear message
(retryable). If file removal succeeds and state save fails → B5 semantics.

## D. Network & forge (majors + minors)

**D1. GitHub client ignores host.**
Fix: `github.Client` errors immediately ("github forge only supports
github.com; use --forge forgejo for self-hosted") if host != "github.com";
`kpm add` surfaces the same error at registration time. Test both.

**D2. Downloads can hang pre-body.**
Fix: on the shared Transport set `DialContext` with 15 s timeout,
`TLSHandshakeTimeout: 15 s`, `ResponseHeaderTimeout: 30 s`. Keep the body
watchdog as is.

**D3. `update` doesn't wait for connectivity.**
Fix: cmdUpdate calls the same WaitForNetwork gate as cmdCheck before
resolving. Also **D3b**: WaitForNetwork total budget: make it explicit —
attempts every 1 s with a 2 s HEAD timeout until a 30 s total deadline;
update PLAN §6 wording ("waits up to ~30 s").

**D4. Download retries non-transient failures.**
Fix: mirror getJSON semantics — HTTP status >= 400 is a permanent error (no
retry); local write errors (ENOSPC etc.) are permanent; only
transport/network/read errors retry. Retry sleeps become context-aware
(select on ctx.Done()).

**D5. Probe false-positives on any-200 sites.**
Fix: Probe parses the body as JSON `{"version": ...}` and requires the field
to be present (the existing test fixture already models this). Non-JSON →
"could not detect forge; pass --forge".

**D6. CA pool unguarded.**
Fix: client construction fails loudly (panic at init or error from a
`newTransport() (…, error)`) if `AppendCertsFromPEM` returns false or the
resulting pool is empty when the system pool is also nil. Test with a garbage
PEM via a construction-time hook or exported helper.

**D7. 404/rate-limit messages.**
Fix: 404 on release endpoints → "repository not found, or it has no releases
yet (checked <host>/<owner>/<repo>)" — no raw API URL. GitHub 403 whose body
mentions rate limit → "GitHub API rate limit exceeded; try again later".

**D8. `.git` suffix on add URLs.**
Fix: strip a single trailing `.git` from the repo segment in addurl parsing.
Test `github.com/o/r.git` → repo `r`, id `r`.

**D9. Unknown `forge` values silently become Forgejo.**
Fix: `forge.For` returns an error for anything but "github"/"forgejo";
callers surface it ("unknown forge %q in packages.d/<id>.toml").

## E. Config & registry-compat (major + minors)

**E1. Save() drops unknown fields; add writes noise [uninstall] block.**
Fix: config.Save preserves unknown keys: decode the existing file (if any)
into `map[string]any`, overlay the known struct fields (including nested
[uninstall] as a map, omitting zero-value keys entirely — no empty-string
marker_file/run_before/run_after, no empty [uninstall] table), write the
merged map. Field deletion semantics: pin="" removes the `pin` key rather
than writing an empty string? No — keep `pin = ""` explicit (it's in the v1
shipped template); just never inject keys the struct didn't set AND never
drop keys the struct doesn't know. Comments are still lost on rewrite
(TOML lib limitation) — add one line to README ("kpm pin/unpin rewrites the
file; comments are not preserved") and REGISTRY.md §7 note that comment
preservation is not guaranteed, only field preservation.
Tests: round-trip a def containing `registry`, `min_kpm`, an unknown
[uninstall] key, and an unknown top-level table → all survive pin/unpin; no
empty [uninstall] noise appears in a fresh `add`.

**E2. LoadAll aborts everything on one bad file.**
Fix: a file that fails TOML decode is skipped with a WARN (log + status line
"1 package definition unreadable: <file>") instead of failing LoadAll;
affected package simply doesn't exist that run. README's [uninstall]
fault-tolerance claim then becomes true — reword it accurately.

**E3. No id validation outside add.**
Fix: validate `ValidID` at the top of remove/pin/unpin/uninstall (and
LoadAll skips files whose stem is not a ValidID, with WARN). Test traversal
attempt `kpm remove ../../foo` → error, nothing deleted.

## F. Update-flow polish (minors)

**F1. latest_seen poisoned by pins; stale-window edge; future timestamps.**
Fix: only write `LatestSeen` when the package is UNpinned (resolve of actual
latest). Pinned packages: leave LatestSeen alone; `list` shows the pin in a
separate PIN column (already displayed) and LatestSeen keeps meaning "latest".
The 5-minute freshness path must only be used for unpinned packages whose
LatestSeen was set unpinned — with the above, that's automatic. fresh():
negative `time.Since` (future timestamp) → treat as stale.

**F2. Exit codes.**
Fix: update: all-selected-failed (nothing staged, ≥1 failure) → exit 1;
mixed → 2. With --reboot: reboot still runs when ≥1 package staged; if the
reboot call itself fails, return 2 if there were failures else 1. Honor
--reboot when there are no new targets but a kpm-staged tgz is pending
(reboot to install it) — log REBOOT with "installing pending staging".

**F3. Cache hygiene.**
Fix: at the start of each update run (after lock): delete `cache/*.part`
unconditionally and any `cache/*.tgz` older than 7 days (mtime). Registry
compat (REGISTRY.md §7.5): only touch `*.tgz`/`*.part` patterns.

**F4. pax/global headers.**
Fix: tgz.Verify and tgz.Merge skip `tar.TypeXGlobalHeader` entries entirely
(not copied, not in manifest, not dup-checked). Per-entry PAX (`x`) headers
are handled by the Go reader/writer pair automatically — leave as is.

**F5. Duplicate-path warnings for directory entries.**
Fix: dup detection in Merge ignores entries whose Typeflag is TypeDir.

**F6. Self-update tag format mismatch.**
Fix: add `tagsEqual(a, b)`: true if a == b after stripping ONE leading 'v'
from each side (case-sensitive otherwise). Use it for every up-to-date
comparison (updateTarget, resolveAndDownload, check available-count). Display
still shows raw tags. Document in PLAN §5.

**F7. Placeholder self source breaks fresh installs.**
Fix: ship `kpm.toml` with `source = ""` and a comment line explaining how to
set it when kpm's release repo exists. check/update treat a package with
empty source as "unconfigured": skipped silently in resolution, shown in
list/status as `self-update not configured` for kpm (or `unconfigured` for
others), never an ERROR, never affects exit code. README bootstrap section
updated truthfully. build/package.go template updated.

**F8. Check toast ignores failures.**
Fix: --notify toast (and status.txt summary line) reflects failures:
"2 updates available, 1 check failed" / "check failed (see log)" when
everything failed. Exit code already 2 — keep.

## G. Device layer & CLI polish (minors)

**G1. `kpm log -n <negative|0>` panics.** Clamp n <= 0 → default 12; n >
lines → all. Test.
**G2. status.txt atomic.** temp+rename like state.Save (with the B3 fsync).
**G3. Log rotation.** Before appending, if kpm.log > 256 KiB, rename to
kpm.log.1 (overwriting any previous .1) and start fresh. `kpm log` reads
only the current file. README notes the rotation.
**G4. Unexpected positionals.** check/log/status/list/version error with
usage (exit 1) on unexpected positional args.
**G5. Host-safety.** If runtime.GOOS != "linux" and KPM_ROOT is unset,
newApp errors ("set KPM_ROOT when running off-device") instead of writing
C:\mnt\onboard. Same for KPM_SYSROOT on uninstall execution paths.
**G6. Reboot comment.** Fix the stale "never returns" comment.

## H. Docs & release

**H1.** README: fix smoke-test output (`0.2.1`, the `- -> v0.5.1` dash
column), placeholder/self-update truth (F7), state-corruption recovery (B3),
unstage (B4), comment-loss note (E1), [uninstall] tolerance wording (E2),
log rotation (G3), lock behavior (B1).
**H2.** PLAN.md: §6 add `unstage`; §8 new labels; WaitForNetwork budget; §5
tagsEqual note. UNINSTALL.md: marker idempotency + policy check (C3), keep
semantics in recursive deletes (C1), case rule (C2).
**H3.** Version 0.2.1 everywhere (build default, releases/0.2.1/KoboRoot.tgz);
`go vet ./...` clean, `go test ./...` green, `go run ./build` self-check
passes, `go mod tidy -diff` empty, artifact copied.

## Explicitly NOT in scope
Registry implementation (REGISTRY.md), NickelMenu uninstall entries, asset
signing, GitHub Enterprise support (D1 refuses it), comment-preserving TOML.
