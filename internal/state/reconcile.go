package state

import "time"

// Promotion records one staged -> installed promotion for logging.
type Promotion struct {
	ID      string
	Version string
}

// PromoteStaged promotes every package's staged version to installed: the boot
// installer consumed the committed staged tgz. Returns the promotions made; the
// caller logs INSTALLED and saves. Callers must only invoke this once the staged
// tgz was committed live (StagedCommitted) and has since disappeared (B6).
func (s *State) PromoteStaged() []Promotion {
	var promos []Promotion
	now := time.Now().UTC().Format(TimeFormat)
	for id, ps := range s.Packages {
		if ps.StagedVersion == "" {
			continue
		}
		ps.InstalledVersion = ps.StagedVersion
		ps.InstalledAt = now
		// The staged manifest is what actually landed on disk — promote it to
		// the installed manifest so uninstall targets the right files (B2).
		if ps.StagedManifest != nil {
			ps.Manifest = ps.StagedManifest
		}
		ps.StagedVersion = ""
		ps.StagedAt = ""
		ps.StagedManifest = nil
		promos = append(promos, Promotion{ID: id, Version: ps.InstalledVersion})
	}
	s.clearStagedIdentity()
	return promos
}

// RollbackStaged clears staged fields WITHOUT promoting: a staging was prepared
// (per-package staged fields recorded) but its tgz never went live before it
// vanished, so nothing was installed and the intent must be discarded (B6).
func (s *State) RollbackStaged() {
	for _, ps := range s.Packages {
		ps.StagedVersion = ""
		ps.StagedAt = ""
		ps.StagedManifest = nil
	}
	s.clearStagedIdentity()
}

// clearStagedIdentity forgets the staged tgz's content identity and commit flag.
func (s *State) clearStagedIdentity() {
	s.StagedSHA256 = ""
	s.StagedSize = 0
	s.StagedCommitted = false
}
