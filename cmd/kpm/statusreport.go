package main

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"kpm/internal/config"
	"kpm/internal/state"
	"kpm/internal/version"
)

// buildStatus renders the short, dialog-friendly summary written to status.txt
// and printed by "kpm check"/"kpm status" (§6).
func (a *App) buildStatus(pkgs []*config.Package) string {
	var b strings.Builder
	fmt.Fprintf(&b, "kpm %s — checked %s\n", version.Version, humanTime(a.state.LastCheck))

	tw := tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
	available, staged := 0, 0
	for _, p := range pkgs {
		ps := a.state.Get(p.ID)
		installed := dash(ps.InstalledVersion)
		target, avail := updateTarget(p, ps, a.effectivePin(p))
		switch {
		case !p.Configured():
			fmt.Fprintf(tw, "%s\t%s\t%s\n", p.ID, installed, unconfiguredNote(p.ID))
		case ps.StagedVersion != "":
			staged++
			fmt.Fprintf(tw, "%s\t%s -> %s\tSTAGED (reboot)\n", p.ID, installed, ps.StagedVersion)
		case avail:
			available++
			fmt.Fprintf(tw, "%s\t%s -> %s\tUPDATE AVAILABLE\n", p.ID, installed, target)
		default:
			fmt.Fprintf(tw, "%s\t%s\tup to date\n", p.ID, installed)
		}
	}
	tw.Flush()

	switch {
	case staged > 0 && available > 0:
		fmt.Fprintf(&b, "%d staged (reboot to install), %d more available.\n", staged, available)
	case staged > 0:
		fmt.Fprintf(&b, "%d package(s) staged — reboot to install.\n", staged)
	case available > 0:
		fmt.Fprintf(&b, "%d update available. Use \"Update all\" to install (reboots).\n", available)
	default:
		b.WriteString("Everything up to date.\n")
	}
	if len(a.unreadable) > 0 {
		fmt.Fprintf(&b, "%d package definition(s) unreadable — see log.\n", len(a.unreadable))
	}
	return b.String()
}

// unconfiguredNote is the status/list text for a package with no usable source
// (F7): kpm's own self-update reads specially.
func unconfiguredNote(id string) string {
	if id == selfID {
		return "self-update not configured"
	}
	return "unconfigured"
}

// humanTime formats an RFC3339 state timestamp as "2006-01-02 15:04" local-ish
// (kept in the stored zone). Returns "never" for empty.
func humanTime(rfc string) string {
	if rfc == "" {
		return "never"
	}
	t, err := time.Parse(state.TimeFormat, rfc)
	if err != nil {
		return rfc
	}
	return t.Format("2006-01-02 15:04")
}
