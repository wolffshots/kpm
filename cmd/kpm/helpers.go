package main

import (
	"fmt"
	"os"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/forge"
	"kpm/internal/registry"
	"kpm/internal/state"
	"kpm/internal/uninstall"
)

// selfID is kpm's own package id.
const selfID = "kpm"

// loadPackages reads every registered package from packages.d. Skipped defs are
// reported: a mutating command WARNs to the log (and buildStatus notes them); a
// read-only command reports to stderr without writing (E2/B1). The two skip
// causes get different wording because they mean different things — see
// config.SkippedDefs — and a.skipped keeps them distinguishable for the
// conflict-gated refusal (§4.3).
func (a *App) loadPackages() ([]*config.Package, error) {
	pkgs, skipped, err := config.LoadAll(a.paths.PackagesDir())
	if err != nil {
		return nil, err
	}
	a.skipped = skipped
	report := func(msg string) {
		if a.readOnly {
			fmt.Fprintln(os.Stderr, "kpm:", msg)
		} else {
			a.paths.Log("WARN", msg)
		}
	}
	for _, f := range skipped.Unparseable {
		report("package definition unreadable: " + f)
	}
	for _, f := range skipped.InvalidName {
		report("not a package definition (filename is not a valid package id), ignoring: " + f)
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

// adoptSelfFromEntry writes kpm's own def from a registry entry and records its
// adoption identity durably in state.json — the single write shared by
// `install kpm --adopt` and `adopt-self` (kpm-self-enrol-plan §2). persistDef
// funnels source/forge into state and blanks source/forge/pin in the def so the
// written kpm.toml stays sourceless, matching the shipped template (SELF-SOURCE
// §5). The pin argument is persisted only when non-empty, so a caller passing the
// current state pin (adopt-self) or an empty local pin (install with no --pin)
// PRESERVES the existing state-resident pin rather than clobbering it (§10). It
// does NOT save state; the caller persists (so install can fold in --installed in
// one write).
func (a *App) adoptSelfFromEntry(entry *registry.Entry, pin string) error {
	ps := a.state.Get(selfID)
	pkg := entry.Def.ToPackage(selfID, entry.Registry, pin)
	a.persistDef(selfID, pkg, ps)
	if err := config.SaveReplace(a.paths.PackageFile(selfID), pkg); err != nil {
		return err
	}
	hash, err := registry.HashDef(entry.Def)
	if err != nil {
		return err
	}
	ps.SyncedDefSHA256 = hash
	if pin != "" {
		ps.Pin = pin // kpm's pin belongs in state.json (§10), read by effectivePin
	}
	return nil
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

// adoptFingerprintBaseline records the current release fingerprint as the
// baseline for a rolling package that is already installed but has no recorded
// fingerprint — the packages that predate 0.10.0, or whose def only just turned
// rolling. Without it, the first check after the def flips would report an
// update for every such package and stage a reboot for a payload the device
// already has.
//
// The cost is one missed rebuild, once, per package — strictly better than
// today's permanently missed rebuilds. It mirrors the sync hash backfill of
// REGISTRY.md §9.7 rule (1): heal a legitimately-empty baseline transparently
// rather than reading it as a change. It writes only in-memory state; both
// callers (check and update) already Save, and both are mutating commands, so
// the B1 read-only rule is untouched.
//
// It reports whether it adopted, so `update` can explain the resulting no-op to
// a user who deliberately asked for that package (m1).
func (a *App) adoptFingerprintBaseline(p *config.Package, ps *state.PackageState, fp string) bool {
	if p.EffectiveUpdateCheck() != config.UpdateCheckRolling {
		return false
	}
	if ps.InstalledVersion == "" || ps.InstalledFingerprint != "" || fp == "" {
		return false
	}
	ps.InstalledFingerprint = fp
	a.paths.Log("INFO", fmt.Sprintf("%s  rolling baseline adopted (%s)", p.ID, fp))
	return true
}

// warnRollingDegraded logs the once-per-run WARN for a rolling package whose
// forge supplied neither a published_at nor an asset size, and reports whether
// it fired. Such a package silently degrades to plain tag comparison — today's
// behaviour — rather than becoming un-updatable (INTEGRITY.md §3.2/§3.7).
func (a *App) warnRollingDegraded(p *config.Package, fp string) bool {
	if fp != "" || p.EffectiveUpdateCheck() != config.UpdateCheckRolling {
		return false
	}
	a.paths.Log("WARN", fmt.Sprintf(
		"%s  update_check = %q but the forge supplies no published_at or asset size — falling back to tag comparison",
		p.ID, config.UpdateCheckRolling))
	return true
}

// notifier reports progress via NickelDBus toast when --notify is set and qndb
// is present. It always no-ops otherwise.
type notifier struct{ on bool }

func (n notifier) toast(msg string) {
	if n.on {
		device.Toast(2000, msg)
	}
}
