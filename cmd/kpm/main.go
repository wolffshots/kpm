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
  kpm log [-n N]
  kpm status
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
// classified by name; the "registry" group splits per subcommand: add/remove/
// refresh mutate, list is read-only (REGISTRY.md §9.1).
func isMutating(cmd string, args []string) bool {
	if cmd == "registry" {
		sub := ""
		if len(args) > 0 {
			sub = args[0]
		}
		return sub == "add" || sub == "remove" || sub == "refresh"
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
		if len(args) > 0 {
			fmt.Fprintln(os.Stderr, "usage: kpm version")
			return exitError
		}
		fmt.Println(version.Version)
		return exitOK
	case "-h", "--help", "help":
		fmt.Print(usage)
		return exitOK
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

	readOnly   bool     // read-only command: no lock, no state/status writes (B1)
	locked     bool     // holds the single-instance lock (mutating commands)
	unreadable []string // package defs skipped this run (E2), for the status line
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

// releaseLock removes the lock file (best-effort) if this invocation holds it.
func (a *App) releaseLock() {
	if a != nil && a.locked {
		os.Remove(a.paths.LockFile())
		a.locked = false
	}
}

// Lock timing constants (B1). A device kpm run never holds the lock longer than
// a single download, so a lock older than staleLockAge is presumed abandoned.
const (
	lockWait     = 5 * time.Second
	lockRetry    = 250 * time.Millisecond
	staleLockAge = 10 * time.Minute
)

// acquireLock takes an exclusive lock file, retrying for up to lockWait and
// breaking a lock older than staleLockAge. Returns (false, nil) if the lock is
// held by a live instance within the wait window (B1).
func acquireLock(path string) (bool, error) {
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
			f.Close()
			return true, nil
		}
		if !os.IsExist(err) {
			return false, err
		}
		// Held: break it if stale, otherwise wait and retry.
		if info, e := os.Stat(path); e == nil && time.Since(info.ModTime()) > staleLockAge {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(lockRetry)
	}
}

// seedSelf records kpm's own installed_version from the compiled-in version
// string on first run, if kpm.toml is registered and no version is recorded
// yet (§9). This never uses --installed.
func (a *App) seedSelf() error {
	if _, err := os.Stat(a.paths.PackageFile(selfID)); err != nil {
		return nil // kpm not registered (e.g. tests)
	}
	ps := a.state.Get(selfID)
	if ps.InstalledVersion != "" {
		return nil
	}
	ps.InstalledVersion = version.Version
	ps.InstalledAt = time.Now().UTC().Format(state.TimeFormat)
	if err := a.state.Save(); err != nil {
		return fmt.Errorf("seed self version: %w", err)
	}
	return nil
}

// reconcile promotes staged versions once the boot installer has run. It saves
// state before logging INSTALLED so a save failure fails the command and does
// not emit duplicate INSTALLED lines on retry (B5).
func (a *App) reconcile() error {
	_, err := os.Stat(a.paths.StagedTgz())
	promos := a.state.Reconcile(err == nil)
	if len(promos) == 0 {
		return nil
	}
	if serr := a.state.Save(); serr != nil {
		return fmt.Errorf("reconcile: save state: %w", serr)
	}
	for _, pr := range promos {
		a.paths.Log("INSTALLED", fmt.Sprintf("%s  %s", pr.ID, pr.Version))
	}
	return nil
}
