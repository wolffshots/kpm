package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromoteStagedWhenTgzGone(t *testing.T) {
	s := &State{Packages: map[string]*PackageState{
		"nh": {InstalledVersion: "v0.5.0", StagedVersion: "v0.5.1", StagedAt: "t", Manifest: []string{"usr/x"}},
	}}
	promos := s.PromoteStaged() // committed tgz gone -> installed
	if len(promos) != 1 || promos[0].ID != "nh" || promos[0].Version != "v0.5.1" {
		t.Fatalf("promotions = %+v", promos)
	}
	ps := s.Packages["nh"]
	if ps.InstalledVersion != "v0.5.1" || ps.StagedVersion != "" || ps.StagedAt != "" {
		t.Errorf("post-promotion state wrong: %+v", ps)
	}
	if ps.InstalledAt == "" {
		t.Error("installed_at should be set")
	}
	if len(ps.Manifest) != 1 {
		t.Error("manifest should be retained after promotion")
	}
}

// B6: RollbackStaged discards an uncommitted staging without promoting it.
func TestRollbackStagedDoesNotPromote(t *testing.T) {
	s := &State{Packages: map[string]*PackageState{
		"nh": {InstalledVersion: "v0.5.0", StagedVersion: "v0.5.1", StagedManifest: []string{"usr/new"}},
	}}
	s.RollbackStaged()
	ps := s.Packages["nh"]
	if ps.InstalledVersion != "v0.5.0" {
		t.Errorf("rollback must not promote: installed = %q", ps.InstalledVersion)
	}
	if ps.StagedVersion != "" || ps.StagedManifest != nil {
		t.Errorf("staged fields should be cleared: %+v", ps)
	}
}

func TestPromoteStagedIgnoresUnstaged(t *testing.T) {
	s := &State{Packages: map[string]*PackageState{
		"nh": {InstalledVersion: "v0.5.0"},
	}}
	if promos := s.PromoteStaged(); promos != nil {
		t.Errorf("no staged version -> no promotions, got %+v", promos)
	}
}

// B2: staging records StagedManifest; PromoteStaged promotes it to Manifest.
func TestPromoteStagedManifest(t *testing.T) {
	s := &State{Packages: map[string]*PackageState{
		"nh": {
			InstalledVersion: "v1",
			Manifest:         []string{"usr/old"},
			StagedVersion:    "v2",
			StagedManifest:   []string{"usr/new"},
		},
	}}
	promos := s.PromoteStaged()
	if len(promos) != 1 {
		t.Fatalf("promotions = %+v", promos)
	}
	ps := s.Packages["nh"]
	if len(ps.Manifest) != 1 || ps.Manifest[0] != "usr/new" {
		t.Errorf("staged manifest not promoted to installed: %v", ps.Manifest)
	}
	if ps.StagedManifest != nil {
		t.Errorf("staged manifest should be cleared: %v", ps.StagedManifest)
	}
}

// B4/B6: promotion clears the staged tgz identity and commit flag.
func TestPromoteStagedClearsIdentity(t *testing.T) {
	s := &State{
		Packages:        map[string]*PackageState{"nh": {StagedVersion: "v2"}},
		StagedSHA256:    "abc",
		StagedSize:      123,
		StagedCommitted: true,
	}
	s.PromoteStaged()
	if s.StagedSHA256 != "" || s.StagedSize != 0 || s.StagedCommitted {
		t.Errorf("staged identity should be cleared after promotion: %q/%d/%v", s.StagedSHA256, s.StagedSize, s.StagedCommitted)
	}
}

// B3: a corrupt state.json is renamed aside and Load returns a fresh state.
func TestLoadRecoversFromCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("corrupt state must not fail Load: %v", err)
	}
	if len(s.Packages) != 0 {
		t.Errorf("recovered state should be empty: %+v", s.Packages)
	}
	if s.CorruptBackup == "" {
		t.Error("CorruptBackup should be set")
	}
	if !strings.Contains(s.CorruptBackup, ".corrupt-") {
		t.Errorf("backup name = %q", s.CorruptBackup)
	}
	if _, err := os.Stat(s.CorruptBackup); err != nil {
		t.Errorf("backup file should exist: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("corrupt state.json should have been renamed away")
	}
	// A fresh save must work on the recovered state.
	s.Get("x").InstalledVersion = "v1"
	if err := s.Save(); err != nil {
		t.Fatalf("save after recovery: %v", err)
	}
}

// M2: a well-formed state.json with a null package entry (e.g. an out-of-band
// USB edit) must not leave a nil *PackageState in the map — the many
// `range Packages` loops dereference it directly and would panic inside newApp,
// wedging every command. Load drops nil entries.
func TestLoadDropsNilPackageEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"packages":{"nickelmenu":null,"nh":{"installed_version":"v1"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := s.Packages["nickelmenu"]; ok {
		t.Error("null package entry should have been dropped")
	}
	// A direct range-deref (the shape that panicked) must be safe now.
	for _, ps := range s.Packages {
		_ = ps.StagedVersion
	}
	if s.Packages["nh"].InstalledVersion != "v1" {
		t.Error("non-null entries must survive")
	}
}

func TestSaveLoadAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, err := Load(path) // missing file -> empty
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Packages) != 0 {
		t.Error("fresh state should be empty")
	}
	s.Get("nh").InstalledVersion = "v1"
	s.Get("kpm").Pin = "v2"
	s.LastCheck = "2026-07-19T10:30:00Z"
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Get("nh").InstalledVersion != "v1" || got.Get("kpm").Pin != "v2" {
		t.Errorf("round-trip failed: %+v", got)
	}
	if got.LastCheck != "2026-07-19T10:30:00Z" {
		t.Errorf("last_check = %q", got.LastCheck)
	}
}
