package main

import (
	"fmt"
	"os"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/forge"
	"kpm/internal/state"
	"kpm/internal/uninstall"
)

// selfID is kpm's own package id.
const selfID = "kpm"

// loadPackages reads every registered package from packages.d. Unreadable defs
// are skipped: a mutating command WARNs to the log (and buildStatus notes them);
// a read-only command reports to stderr without writing (E2/B1).
func (a *App) loadPackages() ([]*config.Package, error) {
	pkgs, unreadable, err := config.LoadAll(a.paths.PackagesDir())
	if err != nil {
		return nil, err
	}
	a.unreadable = unreadable
	for _, f := range unreadable {
		if a.readOnly {
			fmt.Fprintf(os.Stderr, "kpm: package definition unreadable, skipping: %s\n", f)
		} else {
			a.paths.Log("WARN", "package definition unreadable: "+f)
		}
	}
	return pkgs, nil
}

// loadPackage reads one package by id, or returns an error if not registered.
func (a *App) loadPackage(id string) (*config.Package, error) {
	path := a.paths.PackageFile(id)
	p, err := config.Load(path, id)
	if err != nil {
		return nil, fmt.Errorf("package %q not registered (%v)", id, err)
	}
	return p, nil
}

// effectivePin returns the pin in force for a package. kpm's pin lives in
// state.json (to survive self-update overwrite, §10); all others in TOML.
func (a *App) effectivePin(p *config.Package) string {
	if p.ID == selfID {
		return a.state.Get(selfID).Pin
	}
	return p.Pin
}

// effectiveSource returns the source in force for a package. kpm's own source
// lives in state.json so it survives a self-update that overwrites kpm.toml
// (§10, SELF-SOURCE §1), mirroring effectivePin. State wins when set; a non-kpm
// package (or an un-adopted kpm with empty state) falls back to the TOML.
func (a *App) effectiveSource(p *config.Package) string {
	if p.ID == selfID {
		if s := a.state.Get(selfID).Source; s != "" {
			return s
		}
	}
	return p.Source
}

// effectiveForge returns the forge in force for a package, from state for an
// adopted kpm and from the TOML otherwise (SELF-SOURCE §1), mirroring
// effectiveSource.
func (a *App) effectiveForge(p *config.Package) string {
	if p.ID == selfID {
		if f := a.state.Get(selfID).Forge; f != "" {
			return f
		}
	}
	return p.Forge
}

// configured reports whether a package resolves to a usable host/owner/repo,
// honoring kpm's state-resident source override (SELF-SOURCE §1). Callers use
// this instead of p.Configured() so an adopted kpm is not seen as unconfigured.
func (a *App) configured(p *config.Package) bool {
	return config.SourceConfigured(a.effectiveSource(p))
}

// forgeFor returns the Forge client for a package, surfacing an unknown forge
// identifier as a clear error naming the file that holds it — packages.d/<id>.toml
// for a normal package, state.json for an adopted kpm (D9, SELF-SOURCE §1).
func (a *App) forgeFor(p *config.Package) (forge.Forge, error) {
	fk := a.effectiveForge(p)
	f, err := forge.For(fk, a.client)
	if err != nil {
		where := fmt.Sprintf("packages.d/%s.toml", p.ID)
		if p.ID == selfID {
			where = "state.json"
		}
		return nil, fmt.Errorf("unknown forge %q in %s", fk, where)
	}
	return f, nil
}

// persistDef routes a def about to be written so kpm's own adoption identity
// stays out of the tarball-clobbered kpm.toml. For selfID it moves source/forge
// into state.json (durable across a self-update that overwrites kpm.toml, §10 —
// mirroring the pin) and blanks source/forge/pin in the def so the written
// kpm.toml matches the shipped, sourceless template and a clobber is a no-op
// (SELF-SOURCE §5/§6). For every other id it is a no-op: their source/forge stay
// in their TOML exactly as before. Shared by install and sync.
func (a *App) persistDef(id string, pkg *config.Package, ps *state.PackageState) {
	if id != selfID {
		return
	}
	ps.Source = pkg.Source // durable adoption identity (§10)
	ps.Forge = pkg.Forge
	pkg.Source = "" // keep kpm.toml sourceless, matching the shipped def
	pkg.Forge = ""
	pkg.Pin = "" // kpm's pin also lives in state.json (§10)
}

// verifyManifest checks that each manifest member exists on disk after an
// install promotion, returning the members that are absent (A). It lives at the
// cmd layer (not internal/state, whose tests do no filesystem access) and reuses
// the uninstall path plumbing: each member is cleaned to an absolute device path
// and mapped through HostPath, then Lstat'd.
//
// The check is deliberately weak: existence-only, never a checksum (mods rewrite
// their own files), and Lstat not Stat (a member can legitimately be a relative
// symlink). Manifest entries can't distinguish a directory from a file (the
// normalizer strips the trailing slash), so existence is the only honest
// assertion. An empty or nil manifest passes (nil result) — hand-added packages
// and kpm itself carry none. A member that fails to clean (malformed state) is
// skipped, not reported: that is a manifest-data problem, not a missing file.
func verifyManifest(paths device.Paths, members []string) []string {
	var missing []string
	for _, m := range members {
		abs, err := uninstall.CleanDeviceAbs("/" + m)
		if err != nil {
			continue
		}
		if _, err := os.Lstat(paths.HostPath(abs)); err != nil {
			missing = append(missing, m)
		}
	}
	return missing
}

// notifier reports progress via NickelDBus toast when --notify is set and qndb
// is present. It always no-ops otherwise.
type notifier struct{ on bool }

func (n notifier) toast(msg string) {
	if n.on {
		device.Toast(2000, msg)
	}
}
