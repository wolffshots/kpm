package main

import (
	"fmt"
	"os"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/forge"
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

// forgeFor returns the Forge client for a package, surfacing an unknown forge
// identifier as a clear error naming the offending TOML (D9).
func (a *App) forgeFor(p *config.Package) (forge.Forge, error) {
	f, err := forge.For(p.Forge, a.client)
	if err != nil {
		return nil, fmt.Errorf("unknown forge %q in packages.d/%s.toml", p.Forge, p.ID)
	}
	return f, nil
}

// notifier reports progress via NickelDBus toast when --notify is set and qndb
// is present. It always no-ops otherwise.
type notifier struct{ on bool }

func (n notifier) toast(msg string) {
	if n.on {
		device.Toast(2000, msg)
	}
}
