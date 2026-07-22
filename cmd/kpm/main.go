// Command kpm is the Kobo package manager. See PLAN.md for the design.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"kpm/internal/device"
	"kpm/internal/forge"
	"kpm/internal/state"
	"kpm/internal/version"
)

const usage = `kpm — Kobo package manager

Usage:
  kpm add <url> [--asset <glob>] [--forge github|forgejo] [--name <id>] [--installed <ver>]
  kpm remove <id>              (unregister only — see "kpm uninstall" to delete files)
  kpm uninstall <id> [--purge] [--dry-run] [--yes] [--force] [--keep-registration] [--reboot] [--notify]
  kpm list
  kpm check [--notify]
  kpm update [<id>...] [--all] [--reboot] [--notify]
  kpm unstage                  (cancel a pending staged update)
  kpm pin <id> <tag>
  kpm unpin <id>
  kpm registry add <url> [--name <n>] [--ref <branch>] [--path <p>] [--forge github|forgejo]
  kpm registry remove <name>
  kpm registry list
  kpm registry refresh [<name>]
  kpm search [<term>]
  kpm install <id> [--pin <tag>] [--installed <ver>] [--yes] [--adopt]
  kpm sync [<id>...] [--overwrite]
  kpm config list <id>
  kpm config show <id> <name-or-index>
  kpm config set <id> <name> --key K [--section S] --value V
  kpm config set <id> <name> --line N --value V [--append|--delete]
  kpm log [-n N]
  kpm status
  kpm ui                       (open the graphical package manager; used by NickelMenu)
  kpm version
`

// exit codes per §6.
const (
	exitOK      = 0
	exitError   = 1
	exitPartial = 2
	exitConfirm = 3 // uninstall requires --yes
)

// mutatingCmds are the commands that change state/files: they acquire the
// single-instance lock and run reconcile/seedSelf. Read-only commands
// (list/status/log/version/search) run without the lock and never write (B1).
var mutatingCmds = map[string]bool{
	"add": true, "remove": true, "uninstall": true, "check": true,
	"update": true, "pin": true, "unpin": true, "unstage": true,
	"install": true, "sync": true,
}

// isMutating decides whether a command acquires the lock. Most commands are
// classified by name; the "registry" and "config" groups split per subcommand:
// registry add/remove/refresh and config set mutate, their list/show are
// read-only (REGISTRY.md §9.1, CONFIG.md §3.2).
func isMutating(cmd string, args []string) bool {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch cmd {
	case "registry":
		return sub == "add" || sub == "remove" || sub == "refresh"
	case "config":
		return sub == "set"
	}
	return mutatingCmds[cmd]
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches one kpm invocation and guarantees the lock is released on
// every exit path (B1).
func run(argv []string) int {
	if len(argv) < 1 {
		fmt.Fprint(os.Stderr, usage)
		return exitError
	}
	cmd := argv[0]
	args := argv[1:]

	// version/help are fast/offline and need no state, dirs, or lock.
	switch cmd {
	case "version":
		flags, pos := splitArgs(args, nil)
		flags, jsonMode := takeJSON(flags)
		if len(flags) > 0 || len(pos) > 0 {
			fmt.Fprintln(os.Stderr, "usage: kpm version [--json]")
			return exitError
		}
		fmt.Println(version.Version)
		if jsonMode {
			// commit is null: no VCS revision is compiled in (JSON-OUTPUT.md §2.4).
			jsonLine(jsonVersion{Version: version.Version, Commit: nil})
		}
		return exitOK
	case "-h", "--help", "help":
		fmt.Print(usage)
		return exitOK
	case "ui":
		// UI launch trigger for the NickelKPM hook: no state, no lock (§ui).
		return cmdUI(args)
	}

	app, err := newApp(isMutating(cmd, args))
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm:", err)
		return exitError
	}
	defer app.releaseLock()

	switch cmd {
	case "add":
		return app.cmdAdd(args)
	case "remove":
		return app.cmdRemove(args)
	case "uninstall":
		return app.cmdUninstall(args)
	case "list":
		return app.cmdList(args)
	case "check":
		return app.cmdCheck(args)
	case "update":
		return app.cmdUpdate(args)
	case "unstage":
		return app.cmdUnstage(args)
	case "pin":
		return app.cmdPin(args)
	case "unpin":
		return app.cmdUnpin(args)
	case "registry":
		return app.cmdRegistry(args)
	case "config":
		return app.cmdConfig(args)
	case "search":
		return app.cmdSearch(args)
	case "install":
		return app.cmdInstall(args)
	case "sync":
		return app.cmdSync(args)
	case "log":
		return app.cmdLog(args)
	case "status":
		return app.cmdStatus(args)
	default:
		fmt.Fprintf(os.Stderr, "kpm: unknown command %q\n\n%s", cmd, usage)
		return exitError
	}
}

// splitArgs separates flag arguments from positionals so the two may
// intersperse (stdlib flag stops at the first positional). valueFlags names
// the flags that consume a following value when written as "--flag value".
func splitArgs(args []string, valueFlags map[string]bool) (flags, positionals []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			name := a[1:]
			if len(name) > 0 && name[0] == '-' {
				name = name[1:]
			}
			if !containsByte(a, '=') && valueFlags[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positionals = append(positionals, a)
	}
	return flags, positionals
}

func containsByte(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}

// App wires the shared dependencies for a single kpm invocation.
type App struct {
	paths  device.Paths
	state  *state.State
	client *forge.Client

	readOnly   bool          // read-only command: no lock, no state/status writes (B1)
	locked     bool          // holds the single-instance lock (mutating commands)
	lockStop   chan struct{} // closed by releaseLock to stop the heartbeat
	unreadable []string      // package defs skipped this run (E2), for the status line
}

// newApp resolves paths, ensures directories, loads state, and — for mutating
// commands — acquires the single-instance lock and runs the staged->installed
// reconcile and self-seed (§4, B1).
func newApp(mutating bool) (*App, error) {
	// Off-device safety: refuse to write under a fake /mnt/onboard on a
	// non-Linux host unless KPM_ROOT points somewhere real (G5).
	if runtime.GOOS != "linux" && os.Getenv("KPM_ROOT") == "" {
		return nil, errors.New("set KPM_ROOT when running off-device")
	}
	p := device.New()
	if err := p.EnsureDirs(); err != nil {
		return nil, err
	}
	st, err := state.Load(p.StateFile())
	if err != nil {
		return nil, err
	}
	if st.CorruptBackup != "" {
		p.Log("WARN", "state.json was unreadable; renamed to "+filepath.Base(st.CorruptBackup)+" and started fresh")
	}
	a := &App{paths: p, state: st, client: forge.NewClient()}

	if !mutating {
		a.readOnly = true // pure read: no lock, no reconcile/seedSelf, no writes
		return a, nil
	}

	locked, err := acquireLock(p.LockFile())
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("another kpm instance is running")
	}
	a.locked = true
	a.startLockHeartbeat()

	if err := a.reconcile(); err != nil {
		a.releaseLock()
		return nil, err
	}
	if err := a.seedSelf(); err != nil {
		a.releaseLock()
		return nil, err
	}
	return a, nil
}

// releaseLock stops the heartbeat and removes the lock file (best-effort) if
// this invocation holds it.
func (a *App) releaseLock() {
	if a != nil && a.locked {
		if a.lockStop != nil {
			close(a.lockStop)
			a.lockStop = nil
		}
		os.Remove(a.paths.LockFile())
		a.locked = false
	}
}

// startLockHeartbeat refreshes the lock's mtime every lockRefresh while the
// command runs, so an operation legitimately longer than staleLockAge (a large
// download on slow Wi-Fi) is not mistaken for an abandoned lock and broken by a
// concurrent instance.
func (a *App) startLockHeartbeat() {
	a.lockStop = make(chan struct{})
	stop := a.lockStop
	go func() {
		t := time.NewTicker(lockRefresh)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				a.refreshLock()
			}
		}
	}()
}

// Lock timing constants (B1). A live run refreshes the lock every lockRefresh;
// a lock not refreshed within staleLockAge is presumed abandoned and broken.
const (
	lockWait     = 5 * time.Second
	lockRetry    = 250 * time.Millisecond
	lockRefresh  = 2 * time.Minute
	staleLockAge = 10 * time.Minute
)

// acquireLock takes an exclusive lock file, retrying for up to lockWait and
// breaking a lock older than staleLockAge. Returns (false, nil) if the lock is
// held by a live instance within the wait window (B1).
//
// A stale lock is broken by RENAME-then-claim, not remove-then-create: the
// staler renames it to a private name and only proceeds if that rename
// succeeded, so two racing processes can't both delete each other's fresh lock
// and both "win" (the old remove-by-path TOCTOU).
func acquireLock(path string) (bool, error) {
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
			f.Sync()
			f.Close()
			return true, nil
		}
		if !os.IsExist(err) {
			return false, err
		}
		// Held: break it if stale, otherwise wait and retry.
		if info, e := os.Stat(path); e == nil && time.Since(info.ModTime()) > staleLockAge {
			// Claim the stale lock by renaming it aside. Only the process whose
			// rename succeeds gets to remove it and loop back to re-create; a
			// loser's rename fails (the file is already gone) and it just
			// retries against whatever lock now exists.
			aside := fmt.Sprintf("%s.stale-%d", path, os.Getpid())
			if rerr := os.Rename(path, aside); rerr == nil {
				os.Remove(aside)
			}
			continue
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(lockRetry)
	}
}

// refreshLock bumps the lock file's mtime so a legitimately long operation (a
// large download on slow Wi-Fi) is not mistaken for a stale lock and broken by a
// concurrent instance. Best-effort: a failure just means the staleness clock
// isn't reset this tick. Called only from the heartbeat goroutine, which runs
// only between lock acquisition and release.
func (a *App) refreshLock() {
	now := time.Now()
	_ = os.Chtimes(a.paths.LockFile(), now, now)
}

// seedSelf records and reconciles kpm's own state from the running binary and
// its adopted def. It runs only when kpm.toml is registered and only on mutating
// commands, after a.reconcile() has promoted any just-installed staged
// self-update (SELF-VERSION §1, SELF-SOURCE §2). It first migrates kpm's own
// source/forge into state.json (see migrateSelfSource), then reconciles the
// installed_version:
//
//   - Empty record: seed it, exactly as before (any binary, including "dev").
//   - Stale record: overwrite from the running binary, refresh InstalledAt,
//     save, and log INFO — so an out-of-band update (USB sideload, manual
//     KoboRoot.tgz) self-heals on the next mutating command. A normalized-equal
//     record (tagsEqual, e.g. "v0.4.1" vs "0.4.1") is left untouched: no write,
//     no log.
//   - A "dev" binary never reconciles an existing record, so host sandboxes /
//     `go run` against a copied device tree cannot pollute state.
//
// The version reconcile never uses --installed.
func (a *App) seedSelf() error {
	if _, err := os.Stat(a.paths.PackageFile(selfID)); err != nil {
		return nil // kpm not registered (e.g. tests)
	}
	if err := a.migrateSelfSource(); err != nil {
		return err
	}
	ps := a.state.Get(selfID)
	if ps.InstalledVersion == "" {
		ps.InstalledVersion = version.Version
		ps.InstalledAt = time.Now().UTC().Format(state.TimeFormat)
		if err := a.state.Save(); err != nil {
			return fmt.Errorf("seed self version: %w", err)
		}
		return nil
	}
	// Existing record: a "dev" binary never overwrites it (§1.5), and a
	// normalized-equal record is a no-op (no churn, no log).
	if version.Version == "dev" || tagsEqual(ps.InstalledVersion, version.Version) {
		return nil
	}
	old := ps.InstalledVersion
	ps.InstalledVersion = version.Version
	ps.InstalledAt = time.Now().UTC().Format(state.TimeFormat)
	if err := a.state.Save(); err != nil { // save before logging (B5)
		return fmt.Errorf("reconcile self version: %w", err)
	}
	a.paths.Log("INFO", fmt.Sprintf("%s  self version reconciled %s -> %s (running binary is authoritative)", selfID, old, version.Version))
	return nil
}

// migrateSelfSource captures an adopted kpm's source/forge into state.json the
// first time a fixed (>=0.5.0) binary runs while the adoption still lives in
// kpm.toml — the adopted-but-not-yet-clobbered case (SELF-SOURCE §2.1). Once in
// state, the identity survives the self-update that overwrites kpm.toml (§10),
// mirroring the pin, so kpm stops un-adopting itself on every self-update.
//
// It is a no-op when the source is already in state (never overwrites a durable
// value), when the loaded kpm.toml has no usable source (a never-adopted kpm, or
// an already-clobbered device that needs the one-time re-adopt of §2.2), and
// under a "dev" binary (host sandboxes / `go run` against a copied device tree
// must not pollute state).
func (a *App) migrateSelfSource() error {
	if version.Version == "dev" {
		return nil
	}
	if a.state.Get(selfID).Source != "" {
		return nil // already durable in state
	}
	p, err := a.loadPackage(selfID)
	if err != nil {
		return nil // unreadable def: nothing to migrate
	}
	if !p.Configured() {
		return nil // never adopted, or already clobbered (§2.2)
	}
	ps := a.state.Get(selfID)
	ps.Source = p.Source
	ps.Forge = p.Forge
	if err := a.state.Save(); err != nil { // save before logging (B5)
		return fmt.Errorf("migrate self source: %w", err)
	}
	a.paths.Log("INFO", fmt.Sprintf("%s  self source migrated to state (%s)", selfID, p.Source))
	return nil
}

// reconcile promotes staged versions once the boot installer has run. It saves
// state before logging INSTALLED so a save failure fails the command and does
// not emit duplicate INSTALLED lines on retry (B5).
//
// A staged tgz is still pending only when it is present AND provably the one we
// staged (content hash). If it is gone — or a foreign tgz now occupies the slot
// — our committed staging was consumed, so we promote (B6, fixes the foreign-tgz
// freeze). A transient stat/hash error is surfaced, not treated as "gone", so a
// filesystem hiccup can never falsely promote. An uncommitted staging that
// vanished (a crash before the tgz went live) is rolled back, never promoted.
func (a *App) reconcile() error {
	pending, err := a.stagedTgzPending()
	if err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	if pending {
		return nil // reboot hasn't happened yet
	}
	if !a.state.StagedCommitted {
		// Staged fields may have been recorded, but the tgz never went live
		// (interrupted stage). Nothing was installed: discard the intent.
		if a.stateHasStaged() {
			a.state.RollbackStaged()
			if serr := a.state.Save(); serr != nil {
				return fmt.Errorf("reconcile: save state: %w", serr)
			}
		}
		return nil
	}
	promos := a.state.PromoteStaged()
	if len(promos) == 0 {
		// Committed flag set but nothing staged (e.g. already promoted): clear
		// the stale identity so the guard doesn't see a phantom staging.
		a.state.RollbackStaged()
		return a.state.Save()
	}
	if serr := a.state.Save(); serr != nil {
		return fmt.Errorf("reconcile: save state: %w", serr)
	}
	for _, pr := range promos {
		a.paths.Log("INSTALLED", fmt.Sprintf("%s  %s", pr.ID, pr.Version))
	}
	return nil
}

// stagedTgzPending reports whether kpm's staged tgz is still awaiting the boot
// installer: it exists AND its content hash matches what we recorded at stage
// time. A missing tgz, or a foreign tgz in the slot, means ours was consumed. A
// stat or hash error (not "not exist") is returned so the caller can abort
// rather than guess.
func (a *App) stagedTgzPending() (bool, error) {
	fi, err := os.Stat(a.paths.StagedTgz())
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // gone -> consumed
		}
		return false, err // transient: do not guess
	}
	if a.state.StagedSHA256 == "" {
		// A tgz is present but we recorded no identity for it (e.g. after
		// corrupt-state recovery). We can't prove it's ours, so don't promote on
		// unprovable evidence — treat as pending; the guard/unstage handle it.
		return true, nil
	}
	sum, size, err := sha256AndSize(a.paths.StagedTgz())
	if err != nil {
		return false, err
	}
	return size == a.state.StagedSize && fi.Size() == size && sum == a.state.StagedSHA256, nil
}

// stateHasStaged reports whether any package currently carries a staged version.
func (a *App) stateHasStaged() bool {
	for _, ps := range a.state.Packages {
		if ps.StagedVersion != "" {
			return true
		}
	}
	return false
}
