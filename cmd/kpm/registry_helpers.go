package main

import (
	"fmt"
	"os"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/registry"
)

// loadRegistryConfig reads config.toml (an empty config when the file is absent),
// dropping any [[registries]] entry whose name fails ValidID with a WARN — a
// hand-edited config could otherwise smuggle a name like "evil/../.." that
// escapes CacheDir via RegistryCache (C2). The WARN goes to the log for mutating
// commands and to stderr for read-only ones (no state/log writes there), the
// same split as loadPackages.
func (a *App) loadRegistryConfig() (*registry.Config, error) {
	cfg, err := registry.LoadConfig(a.paths.ConfigFile())
	if err != nil {
		return nil, err
	}
	kept := cfg.Registries[:0]
	for _, r := range cfg.Registries {
		if !config.ValidID(r.Name) {
			msg := fmt.Sprintf("skipping registry with invalid name %q (need [a-z0-9-]+)", r.Name)
			if a.readOnly {
				fmt.Fprintln(os.Stderr, "kpm:", msg)
			} else {
				a.paths.Log("WARN", msg)
			}
			continue
		}
		kept = append(kept, r)
	}
	cfg.Registries = kept
	return cfg, nil
}

// saveRegistryConfig marshals cfg (preserving unknown keys) and writes it
// atomically (§9.5).
func (a *App) saveRegistryConfig(cfg *registry.Config) error {
	b, err := registry.MarshalConfig(a.paths.ConfigFile(), cfg)
	if err != nil {
		return err
	}
	return device.WriteFileAtomic(a.paths.ConfigFile(), b)
}

// cachedManifest reads and parses one registry's cached registry.toml. A missing
// cache is a distinct, user-actionable error (§9.2).
func (a *App) cachedManifest(r registry.Registry) (*registry.Manifest, error) {
	b, err := os.ReadFile(a.paths.RegistryCache(r.Name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no cached data for registry %q — run: kpm registry refresh", r.Name)
		}
		return nil, err
	}
	return registry.ParseManifest(b)
}

// cachedManifests loads every configured registry's cached manifest in config
// order, returning the ones present (for the merged view) and the names whose
// cache is missing or unparseable (for staleness/error reporting).
func (a *App) cachedManifests(cfg *registry.Config) (mans []registry.NamedManifest, missing []string) {
	for _, r := range cfg.Registries {
		m, err := a.cachedManifest(r)
		if err != nil {
			missing = append(missing, r.Name)
			continue
		}
		mans = append(mans, registry.NamedManifest{Name: r.Name, Manifest: m})
	}
	return mans, missing
}
