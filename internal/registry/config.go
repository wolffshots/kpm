// Package registry implements kpm's package registry: the global config.toml of
// [[registries]] entries, fetching and caching each registry's registry.toml,
// parsing it into package definitions, resolving cross-registry conflicts, and
// computing the sync/drift state that keeps installed defs in step with their
// registry. All logic here is pure (no device paths, no network): the cmd layer
// wires it to internal/device roots and internal/forge, so it is host-testable.
package registry

import (
	"bytes"
	"os"

	"github.com/BurntSushi/toml"
)

// Defaults for optional registry fields (REGISTRY.md §9.5).
const (
	DefaultRef  = "main"
	DefaultPath = "registry.toml"
)

// Registry is one [[registries]] entry in config.toml.
type Registry struct {
	Name  string `toml:"name"`  // local nickname, unique, ValidID-shaped
	URL   string `toml:"url"`   // host/owner/repo
	Ref   string `toml:"ref"`   // branch or tag (default "main")
	Path  string `toml:"path"`  // file in the repo (default "registry.toml")
	Forge string `toml:"forge"` // "github" | "forgejo", detected at add time
}

// Config is the whole config.toml document.
type Config struct {
	Registries []Registry `toml:"registries"`
}

// Find returns the registry with the given name and whether it exists.
func (c *Config) Find(name string) (Registry, bool) {
	for _, r := range c.Registries {
		if r.Name == name {
			return r, true
		}
	}
	return Registry{}, false
}

// LoadConfig reads config.toml at path. A missing file is an empty config, not
// an error (REGISTRY.md §9.5).
func LoadConfig(path string) (*Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// SaveConfig writes cfg to path, preserving unknown top-level keys (e.g. a future
// github_token) and unknown per-registry keys (matched by name), exactly like the
// package-def Save (E1/§9.5). Comments are not preserved (a TOML-library limit).
// The bytes are returned so the caller can write them atomically via device.
func MarshalConfig(path string, cfg *Config) ([]byte, error) {
	m := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		_ = toml.Unmarshal(b, &m) // best-effort: preserve unknown keys
	}

	// Index existing registry tables by name so unknown per-entry keys survive.
	existing := map[string]map[string]any{}
	if arr, ok := m["registries"].([]map[string]any); ok {
		for _, e := range arr {
			if name, _ := e["name"].(string); name != "" {
				existing[name] = e
			}
		}
	}

	var arr []map[string]any
	for _, r := range cfg.Registries {
		e := existing[r.Name]
		if e == nil {
			e = map[string]any{}
		}
		e["name"] = r.Name
		e["url"] = r.URL
		e["ref"] = r.Ref
		e["path"] = r.Path
		e["forge"] = r.Forge
		arr = append(arr, e)
	}
	if len(arr) > 0 {
		m["registries"] = arr
	} else {
		delete(m, "registries")
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
