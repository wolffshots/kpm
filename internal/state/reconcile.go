package state

import "time"

// Promotion records one staged -> installed promotion for logging.
type Promotion struct {
	ID      string
	Version string
}

// Reconcile promotes staged versions to installed when the staged
// KoboRoot.tgz is gone (rcS extracted and deleted it). If it still exists,
// staging is pending and nothing changes. Returns the promotions made; the
// caller is responsible for logging INSTALLED and saving.
func (s *State) Reconcile(stagedTgzExists bool) []Promotion {
	if stagedTgzExists {
		return nil // reboot hasn't happened yet
	}
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
	// The staged tgz is gone; its content identity no longer applies (B4).
	s.StagedSHA256 = ""
	s.StagedSize = 0
	return promos
}
