package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReconcilePromotesWhenTgzGone(t *testing.T) {
	s := &State{Packages: map[string]*PackageState{
		"nh": {InstalledVersion: "v0.5.0", StagedVersion: "v0.5.1", StagedAt: "t", Manifest: []string{"usr/x"}},
	}}
	promos := s.Reconcile(false) // tgz gone -> installed
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

func TestReconcileNoopWhenTgzPresent(t *testing.T) {
	s := &State{Packages: map[string]*PackageState{
		"nh": {InstalledVersion: "v0.5.0", StagedVersion: "v0.5.1"},
	}}
	if promos := s.Reconcile(true); promos != nil {
		t.Errorf("expected no promotions while tgz present, got %+v", promos)
	}
	if s.Packages["nh"].StagedVersion != "v0.5.1" {
		t.Error("staged version must be preserved while tgz present")
	}
}

func TestReconcileIgnoresUnstaged(t *testing.T) {
	s := &State{Packages: map[string]*PackageState{
		"nh": {InstalledVersion: "v0.5.0"},
	}}
	if promos := s.Reconcile(false); promos != nil {
		t.Errorf("no staged version -> no promotions, got %+v", promos)
	}
}

// B2: staging records StagedManifest; Reconcile promotes it to Manifest.
func TestReconcilePromotesStagedManifest(t *testing.T) {
	s := &State{Packages: map[string]*PackageState{
		"nh": {
			InstalledVersion: "v1",
			Manifest:         []string{"usr/old"},
			StagedVersion:    "v2",
			StagedManifest:   []string{"usr/new"},
		},
	}}
	promos := s.Reconcile(false)
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

// B4: Reconcile clears the staged tgz identity when the tgz is gone.
func TestReconcileClearsStagedHash(t *testing.T) {
	s := &State{
		Packages:     map[string]*PackageState{"nh": {StagedVersion: "v2"}},
		StagedSHA256: "abc",
		StagedSize:   123,
	}
	s.Reconcile(false)
	if s.StagedSHA256 != "" || s.StagedSize != 0 {
		t.Errorf("staged hash/size should be cleared after promotion: %q/%d", s.StagedSHA256, s.StagedSize)
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
