package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"kpm/internal/config"
	"kpm/internal/state"
)

func (a *App) cmdRemove(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: kpm remove <id>")
		return exitError
	}
	id := args[0]
	if !config.ValidID(id) {
		fmt.Fprintf(os.Stderr, "kpm remove: invalid package id %q\n", id)
		return exitError
	}
	path := a.paths.PackageFile(id)
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(os.Stderr, "kpm remove: %q not registered\n", id)
		return exitError
	}
	if err := os.Remove(path); err != nil {
		fmt.Fprintln(os.Stderr, "kpm remove:", err)
		return exitError
	}
	a.paths.Log("REMOVE", id)
	fmt.Printf("unregistered %s (data left untouched — use \"kpm uninstall %s\" to delete its files)\n", id, id)
	return exitOK
}

func (a *App) cmdList(args []string) int {
	if _, pos := splitArgs(args, nil); len(pos) > 0 {
		fmt.Fprintln(os.Stderr, "usage: kpm list")
		return exitError
	}
	pkgs, err := a.loadPackages()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm list:", err)
		return exitError
	}
	if len(pkgs) == 0 {
		fmt.Println("no packages registered")
		return exitOK
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tINSTALLED\tSTAGED\tLATEST\tPIN")
	for _, p := range pkgs {
		ps := a.state.Get(p.ID)
		latest := dash(ps.LatestSeen)
		if !a.configured(p) {
			latest = unconfiguredNote(p.ID) // F7
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			p.ID,
			dash(ps.InstalledVersion),
			dash(ps.StagedVersion),
			latest,
			dash(a.effectivePin(p)))
	}
	w.Flush()
	return exitOK
}

func (a *App) cmdPin(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: kpm pin <id> <tag>")
		return exitError
	}
	id, tag := args[0], args[1]
	if !config.ValidID(id) {
		fmt.Fprintf(os.Stderr, "kpm pin: invalid package id %q\n", id)
		return exitError
	}
	p, err := a.loadPackage(id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm pin:", err)
		return exitError
	}
	if id == selfID {
		// kpm's pin lives in state to survive self-update overwrite (§10).
		a.state.Get(selfID).Pin = tag
		if err := a.state.Save(); err != nil {
			fmt.Fprintln(os.Stderr, "kpm pin:", err)
			return exitError
		}
	} else {
		p.Pin = tag
		if err := config.Save(a.paths.PackageFile(id), p); err != nil {
			fmt.Fprintln(os.Stderr, "kpm pin:", err)
			return exitError
		}
	}
	a.paths.Log("PIN", fmt.Sprintf("%s  %s", id, tag))
	ps := a.state.Get(id)
	if ps.InstalledVersion != tag {
		fmt.Printf("pinned %s to %s (differs from installed %s — update available)\n",
			id, tag, dash(ps.InstalledVersion))
	} else {
		fmt.Printf("pinned %s to %s\n", id, tag)
	}
	return exitOK
}

func (a *App) cmdUnpin(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: kpm unpin <id>")
		return exitError
	}
	id := args[0]
	if !config.ValidID(id) {
		fmt.Fprintf(os.Stderr, "kpm unpin: invalid package id %q\n", id)
		return exitError
	}
	p, err := a.loadPackage(id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm unpin:", err)
		return exitError
	}
	if id == selfID {
		a.state.Get(selfID).Pin = ""
		if err := a.state.Save(); err != nil {
			fmt.Fprintln(os.Stderr, "kpm unpin:", err)
			return exitError
		}
	} else {
		p.Pin = ""
		if err := config.Save(a.paths.PackageFile(id), p); err != nil {
			fmt.Fprintln(os.Stderr, "kpm unpin:", err)
			return exitError
		}
	}
	a.paths.Log("UNPIN", id)
	fmt.Printf("unpinned %s (tracking latest)\n", id)
	return exitOK
}

func (a *App) cmdLog(args []string) int {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	n := fs.Int("n", 12, "number of lines")
	flags, pos := splitArgs(args, map[string]bool{"n": true})
	if err := fs.Parse(flags); err != nil {
		return exitError
	}
	if len(pos) > 0 {
		fmt.Fprintln(os.Stderr, "usage: kpm log [-n N]")
		return exitError
	}
	if *n <= 0 {
		*n = 12 // clamp non-positive counts to the default (G1)
	}
	lines := a.paths.TailLog(*n)
	if len(lines) == 0 {
		fmt.Println("(log is empty)")
		return exitOK
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	return exitOK
}

func (a *App) cmdStatus(args []string) int {
	if _, pos := splitArgs(args, nil); len(pos) > 0 {
		fmt.Fprintln(os.Stderr, "usage: kpm status")
		return exitError
	}
	s := a.paths.ReadStatus()
	if s == "" {
		fmt.Println("kpm: no status yet — run \"kpm check\"")
	} else {
		fmt.Print(s)
	}
	// Pending staging info.
	if _, err := os.Stat(a.paths.StagedTgz()); err == nil {
		var staged []string
		for id, ps := range a.state.Packages {
			if ps.StagedVersion != "" {
				staged = append(staged, fmt.Sprintf("%s %s", id, ps.StagedVersion))
			}
		}
		if len(staged) > 0 {
			fmt.Println("PENDING: reboot to install staged:", join(staged))
		}
	}
	return exitOK
}

// updateTarget returns the tag a package should move to and whether an update
// is available (installed differs from target, and it is not already staged).
func updateTarget(pkg *config.Package, ps *state.PackageState, pin string) (target string, available bool) {
	if pin != "" {
		target = pin
	} else {
		target = ps.LatestSeen
	}
	if target == "" {
		return target, false
	}
	if tagsEqual(ps.StagedVersion, target) && ps.StagedVersion != "" {
		return target, false // already staged, awaiting reboot
	}
	return target, !tagsEqual(ps.InstalledVersion, target)
}

// tagsEqual compares two release tags treating a single leading 'v' as
// insignificant (so "v1.2.0" and "1.2.0" are the same release), but is
// otherwise case-sensitive. Display always shows the raw tag (F6).
func tagsEqual(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
