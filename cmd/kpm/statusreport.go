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

// buildStatus renders the short, dialog-friendly summary written to the status
// file and printed by "kpm check"/"kpm status" (§6).
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
		case !a.configured(p):
			fmt.Fprintf(tw, "%s\t%s\t%s\n", p.ID, installed, unconfiguredNote(p.ID))
		case ps.StagedVersion != "":
			staged++
			// A rolling package staged at its own permanent tag would otherwise
			// render "latest -> latest STAGED (reboot)" — the exact confusion
			// §3.7 removes, one state later. Name the reason instead.
			if ps.InstalledVersion != "" && tagsEqual(ps.InstalledVersion, ps.StagedVersion) {
				fmt.Fprintf(tw, "%s\t%s\tSTAGED: release rebuilt (reboot)\n", p.ID, installed)
			} else {
				fmt.Fprintf(tw, "%s\t%s -> %s\tSTAGED (reboot)\n", p.ID, installed, ps.StagedVersion)
			}
		case avail:
			available++
			// A rolling package whose tag has not moved would otherwise render a
			// confusing "latest -> latest"; name the real reason instead
			// (INTEGRITY.md §3.7). Every other case is unchanged.
			if tagsEqual(ps.InstalledVersion, target) {
				fmt.Fprintf(tw, "%s\t%s\tUPDATE AVAILABLE (release rebuilt)\n", p.ID, installed)
			} else {
				fmt.Fprintf(tw, "%s\t%s -> %s\tUPDATE AVAILABLE\n", p.ID, installed, target)
			}
		default:
			fmt.Fprintf(tw, "%s\t%s\tup to date\n", p.ID, installed)
		}
	}
	tw.Flush()

	// B: a pair that is already installed is REPORTED, never blocked — refusing
	// to update either one would strand both and undo nothing (§1.2 item 3,
	// §4.5). One line per distinct unordered pair, sorted.
	for _, pair := range a.conflictPairs(pkgs) {
		fmt.Fprintf(&b, "CONFLICT: %s and %s cannot both be installed — uninstall one.\n", pair[0], pair[1])
	}

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
	if n := len(a.skipped.All()); n > 0 {
		fmt.Fprintf(&b, "%d package definition(s) unreadable — see log.\n", n)
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
