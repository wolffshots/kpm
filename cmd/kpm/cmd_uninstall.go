package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/uninstall"
)

func (a *App) cmdUninstall(args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	purge := fs.Bool("purge", false, "also delete user data/config (purge_paths)")
	dryRun := fs.Bool("dry-run", false, "print the plan and exit without changes")
	yes := fs.Bool("yes", false, "apply the plan (required; uninstall is destructive)")
	force := fs.Bool("force", false, "proceed even if run_before fails")
	keepReg := fs.Bool("keep-registration", false, "keep packages.d/<id>.toml after removal")
	reboot := fs.Bool("reboot", false, "reboot after a successful removal")
	notify := fs.Bool("notify", false, "emit NickelDBus toasts")
	flags, pos := splitArgs(args, nil)
	if err := fs.Parse(flags); err != nil {
		return exitError
	}
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "usage: kpm uninstall <id> [--purge] [--dry-run] [--yes] [--force] [--keep-registration] [--reboot] [--notify]")
		return exitError
	}
	id := pos[0]
	n := notifier{on: *notify}

	if !config.ValidID(id) {
		fmt.Fprintf(os.Stderr, "kpm uninstall: invalid package id %q\n", id)
		return exitError
	}

	// Self-uninstall is refused — kpm must remove itself by hand (README).
	if id == selfID {
		fmt.Fprintln(os.Stderr, "kpm uninstall: refusing to uninstall kpm itself; see the README \"Removing kpm\" section for manual removal")
		return exitError
	}

	// Off-device safety: deletions resolve against KPM_SYSROOT. If KPM_ROOT is
	// overridden (a dev/CI sandbox) but KPM_SYSROOT is not, HostPath would map
	// deletions onto the real "/", so refuse — even on Linux, where the non-Linux
	// guard below wouldn't catch it. On a real device both are unset (G5).
	if os.Getenv("KPM_SYSROOT") == "" {
		if os.Getenv("KPM_ROOT") != "" {
			fmt.Fprintln(os.Stderr, "kpm uninstall: KPM_ROOT is set but KPM_SYSROOT is not — refusing to delete against the real rootfs; set KPM_SYSROOT")
			return exitError
		}
		if runtime.GOOS != "linux" {
			fmt.Fprintln(os.Stderr, "kpm uninstall: set KPM_SYSROOT when running off-device")
			return exitError
		}
	}

	pkg, err := a.loadPackage(id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm uninstall:", err)
		return exitError
	}

	// A pending staged update would reinstall the package on the next reboot.
	ps := a.state.Get(id)
	if ps.StagedVersion != "" {
		if _, err := os.Stat(a.paths.StagedTgz()); err == nil {
			fmt.Fprintf(os.Stderr,
				"kpm uninstall: %s has a staged update awaiting reboot; reboot to install it or remove %s first\n",
				id, a.paths.StagedTgz())
			return exitError
		}
	}

	plan, err := uninstall.Compute(ps.Manifest, pkg.Uninstall, *purge)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm uninstall:", err)
		return exitError
	}

	printPlan(os.Stdout, id, plan, *purge, len(pkg.Uninstall.PurgePaths) > 0)

	if *dryRun {
		return exitOK
	}
	if !*yes {
		fmt.Println("\nThis is destructive. Re-run with --yes to apply.")
		return exitConfirm
	}

	n.toast("kpm: uninstalling " + id + "…")

	// run_before: abort on failure unless --force (§3).
	if plan.RunBefore != "" {
		if err := runHook(plan.RunBefore); err != nil {
			a.paths.Log("WARN", fmt.Sprintf("%s  run_before failed: %v", id, err))
			if !*force {
				fmt.Fprintf(os.Stderr, "kpm uninstall: run_before failed: %v (use --force to proceed)\n", err)
				a.paths.WriteStatus(fmt.Sprintf("kpm: uninstall %s aborted — run_before failed", id))
				return exitError
			}
		}
	}

	res := uninstall.Execute(a.paths, plan)

	// run_after: non-fatal (§3).
	if plan.RunAfter != "" {
		if err := runHook(plan.RunAfter); err != nil {
			a.paths.Log("WARN", fmt.Sprintf("%s  run_after failed: %v", id, err))
		}
	}

	a.logUninstall(id, plan, res)

	if !res.OK() {
		// Partial: a deletion failed, or an execution-time safety skip left a
		// file on disk (symlinked parent, C7). Keep the registration AND state so
		// the leftover is not orphaned and the uninstall can be retried. Plan-time
		// policy skips do NOT reach here — they are the accepted policy.
		_ = a.state.Save()
		msg := fmt.Sprintf("kpm: uninstall %s partial — %d failed, %d skipped for safety, see kpm.log",
			id, len(res.Failed), len(res.Skipped))
		a.paths.WriteStatus(msg)
		fmt.Fprintln(os.Stderr, msg)
		for _, s := range res.Skipped {
			fmt.Fprintf(os.Stderr, "  skipped %s (%s)\n", s.Path, s.Reason)
		}
		n.toast(msg)
		return exitPartial
	}

	// Success: remove the packages.d file FIRST, then clear state. If the file
	// removal fails, keep state intact so the uninstall can be retried (C8).
	if !*keepReg {
		if err := os.Remove(a.paths.PackageFile(id)); err != nil && !os.IsNotExist(err) {
			msg := fmt.Sprintf("kpm: uninstall %s could not remove registration (%v) — retry", id, err)
			a.paths.WriteStatus(msg)
			fmt.Fprintln(os.Stderr, msg)
			return exitPartial
		}
	}
	delete(a.state.Packages, id)
	if err := a.state.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "kpm uninstall: state:", err)
		return exitError
	}

	status := fmt.Sprintf("kpm: uninstalled %s — %d removed, %d dir(s), %d purged",
		id, len(res.Deleted), len(res.Removed), len(res.Purged))
	if plan.NeedsReboot && !*reboot {
		status += "\nReboot required to complete removal."
	}
	a.paths.WriteStatus(status)
	fmt.Println(status)
	n.toast(fmt.Sprintf("kpm: uninstalled %s", id))

	if *reboot {
		a.paths.Log("REBOOT", "after uninstall "+id)
		n.toast("kpm: rebooting…")
		if err := device.Reboot(); err != nil {
			fmt.Fprintln(os.Stderr, "kpm uninstall: reboot failed:", err)
			return exitError
		}
		return exitOK
	}
	return exitOK
}

// logUninstall writes the UNINSTALL/PURGE/WARN lines for one removal (§3).
func (a *App) logUninstall(id string, plan uninstall.Plan, res uninstall.Result) {
	a.paths.Log("UNINSTALL", fmt.Sprintf("%s  method=%s  %d removed, %d dir(s), %d purged",
		id, plan.Method, len(res.Deleted), len(res.Removed), len(res.Purged)))
	if res.Marker != "" {
		a.paths.Log("UNINSTALL", fmt.Sprintf("%s  marker %s", id, res.Marker))
	}
	for _, p := range res.Purged {
		a.paths.Log("PURGE", fmt.Sprintf("%s  %s", id, p))
	}
	for _, k := range res.Kept {
		a.paths.Log("WARN", fmt.Sprintf("%s  kept %s (keep_paths)", id, k))
	}
	for _, s := range plan.Skipped {
		a.paths.Log("WARN", fmt.Sprintf("%s  skipped %s (%s)", id, s.Path, s.Reason))
	}
	for _, s := range res.Skipped {
		a.paths.Log("WARN", fmt.Sprintf("%s  skipped %s (%s)", id, s.Path, s.Reason))
	}
	for _, f := range res.Failed {
		a.paths.Log("WARN", fmt.Sprintf("%s  failed %s: %v", id, f.Path, f.Err))
	}
}

// printPlan renders the ordered uninstall plan for the user (and --dry-run).
func printPlan(w io.Writer, id string, plan uninstall.Plan, purge, hasPurgePaths bool) {
	fmt.Fprintf(w, "uninstall plan for %s (method: %s)\n", id, plan.Method)
	if plan.RunBefore != "" {
		fmt.Fprintf(w, "  run before  %s\n", plan.RunBefore)
	}
	if plan.Marker != "" {
		fmt.Fprintf(w, "  marker      %s\n", plan.Marker)
	}
	if len(plan.Deletes) == 0 && plan.Marker == "" {
		fmt.Fprintln(w, "  (nothing to delete)")
	}
	for _, d := range plan.Deletes {
		tag := ""
		if d.Recursive {
			tag += " (recursive)"
		}
		if d.Purge {
			tag += " (purge)"
		}
		fmt.Fprintf(w, "  delete      %s%s\n", d.Path, tag)
	}
	for _, dir := range plan.Rmdirs {
		fmt.Fprintf(w, "  rmdir       %s (if empty)\n", dir)
	}
	if plan.RunAfter != "" {
		fmt.Fprintf(w, "  run after   %s\n", plan.RunAfter)
	}
	for _, s := range plan.Skipped {
		fmt.Fprintf(w, "  skip        %s (%s)\n", s.Path, s.Reason)
	}
	if hasPurgePaths && !purge {
		fmt.Fprintln(w, "  (user data retained; pass --purge to also remove purge_paths)")
	}
	if plan.NeedsReboot {
		fmt.Fprintln(w, "reboot required to complete removal")
	}
}

// runHook runs a configured run_before/run_after command via /bin/sh -c.
func runHook(command string) error {
	c := exec.Command("/bin/sh", "-c", command)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
