// Package state manages state.json: installed/staged versions, per-package
// manifests, and the reconcile step that promotes staged -> installed after
// the boot-time installer consumes the staged KoboRoot.tgz.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"kpm/internal/device"
)

// TimeFormat is the RFC3339 timestamp used throughout state.json.
const TimeFormat = time.RFC3339

// PackageState is the per-package machine state.
type PackageState struct {
	InstalledVersion string   `json:"installed_version,omitempty"`
	InstalledAt      string   `json:"installed_at,omitempty"`
	StagedVersion    string   `json:"staged_version,omitempty"`
	StagedAt         string   `json:"staged_at,omitempty"`
	LatestSeen       string   `json:"latest_seen,omitempty"`
	LastChecked      string   `json:"last_checked,omitempty"`
	Manifest         []string `json:"manifest,omitempty"`
	// StagedManifest holds the member manifest captured at stage time; it is
	// promoted to Manifest by Reconcile so uninstall keeps using the installed
	// (not the pending) manifest until the reboot install actually happens (B2).
	StagedManifest []string `json:"staged_manifest,omitempty"`
	// Pin holds kpm's own pin, kept here (not in kpm.toml) so it survives
	// self-update overwrite (§10). Other packages pin via their TOML.
	Pin string `json:"pin,omitempty"`
	// Source/Forge hold kpm's own adoption identity, kept here (not in
	// kpm.toml) so they survive self-update overwrite (§10), mirroring Pin.
	// Populated only for kpm; other packages read source/forge from their TOML.
	Source string `json:"source,omitempty"`
	Forge  string `json:"forge,omitempty"`
	// SyncedDefSHA256 is the SHA-256 of the canonical registry def last written
	// into this package's local TOML by install/sync (excluding pin/registry).
	// It lets sync tell "up to date" from local drift (REGISTRY.md §9.7).
	SyncedDefSHA256 string `json:"synced_def_sha256,omitempty"`
}

// RegistryState records the cache freshness of one configured registry
// (REGISTRY.md §9.4). LastFetched is an RFC3339 timestamp; Etag is the server's
// ETag if it sent one (used for conditional If-None-Match refresh).
type RegistryState struct {
	LastFetched string `json:"last_fetched,omitempty"`
	Etag        string `json:"etag,omitempty"`
}

// State is the whole state.json document.
type State struct {
	Packages  map[string]*PackageState `json:"packages"`
	LastCheck string                   `json:"last_check,omitempty"`
	// Registries holds per-registry cache metadata (REGISTRY.md §9.4). It is a
	// flexible top-level map; adding it is a non-event for old state.json (§7.4).
	Registries map[string]*RegistryState `json:"registries,omitempty"`
	// StagedSHA256/StagedSize identify the merged KoboRoot.tgz kpm last staged,
	// so the guard can prove a present tgz is ours by content, not by trusting
	// state presence (B4). Cleared on promotion/unstage.
	StagedSHA256 string `json:"staged_sha256,omitempty"`
	StagedSize   int64  `json:"staged_size,omitempty"`
	// StagedCommitted records that the staged tgz was actually moved into the
	// live boot slot. The per-package staged fields are saved BEFORE the tgz
	// goes live (so a crash can't leave installed files unrecorded), then this
	// is set true once the move succeeds. Reconcile promotes staged->installed
	// only when a committed tgz has since disappeared; an uncommitted staging
	// that vanished is rolled back, never promoted (B6).
	StagedCommitted bool `json:"staged_committed,omitempty"`

	path string // where it was loaded from, for Save
	// CorruptBackup is set (not serialized) when Load recovered from an
	// unreadable state.json by renaming it aside; the caller logs a WARN (B3).
	CorruptBackup string `json:"-"`
}

// Load reads state.json at path (returning an empty State if it does not exist).
// A corrupt (unparseable) state.json is not fatal: it is renamed aside to
// state.json.corrupt-<unixts> and Load returns a fresh empty state with
// CorruptBackup set so the caller can WARN (B3). Versions become re-seedable
// via "add --installed" / self-seed.
func Load(path string) (*State, error) {
	s := &State{Packages: map[string]*PackageState{}, path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
		fresh := &State{Packages: map[string]*PackageState{}, path: path}
		if rerr := os.Rename(path, backup); rerr == nil {
			fresh.CorruptBackup = backup
		} else if werr := os.WriteFile(backup, b, 0o644); werr == nil {
			// Couldn't rename aside (e.g. cross-link); copy the bytes so the
			// forensic backup survives the fresh state's next Save.
			fresh.CorruptBackup = backup
		} else {
			// Could neither move nor copy the corrupt file. Starting fresh would
			// let the next Save overwrite the only copy, so fail instead of
			// destroying it — the user can inspect/remove state.json by hand.
			return nil, fmt.Errorf("state.json is corrupt and could not be backed up (%v); inspect %s", err, path)
		}
		return fresh, nil
	}
	if s.Packages == nil {
		s.Packages = map[string]*PackageState{}
	}
	s.path = path
	return s, nil
}

// Get returns (creating if needed) the state for id.
func (s *State) Get(id string) *PackageState {
	ps := s.Packages[id]
	if ps == nil {
		ps = &PackageState{}
		s.Packages[id] = ps
	}
	return ps
}

// Registry returns (creating if needed) the cache metadata for a registry name.
func (s *State) Registry(name string) *RegistryState {
	if s.Registries == nil {
		s.Registries = map[string]*RegistryState{}
	}
	rs := s.Registries[name]
	if rs == nil {
		rs = &RegistryState{}
		s.Registries[name] = rs
	}
	return rs
}

// Save writes state.json atomically (temp file + rename).
func (s *State) Save() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil { // durable before the rename (B3)
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return err
	}
	device.FsyncDir(dir) // durable directory entry on FAT32 (B3)
	return nil
}
