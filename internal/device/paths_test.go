package device

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// D3b: WaitForNetwork returns true promptly once the host responds.
func TestWaitForNetworkReachable(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	if !WaitForNetwork(srv.Client(), host) {
		t.Error("WaitForNetwork should succeed against a responding host")
	}
}

func TestPathsHonorKpmRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KPM_ROOT", root)
	p := New()
	if p.Root != root {
		t.Fatalf("root = %q, want %q", p.Root, root)
	}
	if got, want := p.Bin(), filepath.Join(root, ".adds", "kpm", "bin", "kpm"); got != want {
		t.Errorf("Bin() = %q, want %q", got, want)
	}
	if got, want := p.StagedTgz(), filepath.Join(root, ".kobo", "KoboRoot.tgz"); got != want {
		t.Errorf("StagedTgz() = %q, want %q", got, want)
	}
	if !strings.HasSuffix(p.NmConfig(), filepath.Join(".adds", "nm", "kpm")) {
		t.Errorf("NmConfig() = %q", p.NmConfig())
	}
}

func TestLogAndStatusAndTail(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KPM_ROOT", root)
	p := New()
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := p.Log("CHECK", "nh event"); err != nil {
			t.Fatal(err)
		}
	}
	if lines := p.TailLog(5); len(lines) != 5 {
		t.Errorf("TailLog(5) returned %d lines", len(lines))
	}
	if err := p.WriteStatus("hello"); err != nil {
		t.Fatal(err)
	}
	if got := p.ReadStatus(); got != "hello\n" {
		t.Errorf("ReadStatus = %q", got)
	}
}

// G1: TailLog must not panic on n <= 0 (callers clamp, but guard defensively).
func TestTailLogNonPositive(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KPM_ROOT", root)
	p := New()
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		p.Log("CHECK", "x")
	}
	if lines := p.TailLog(0); lines != nil {
		t.Errorf("TailLog(0) = %v, want nil", lines)
	}
	if lines := p.TailLog(-5); lines != nil {
		t.Errorf("TailLog(-5) = %v, want nil", lines)
	}
	if lines := p.TailLog(100); len(lines) != 5 {
		t.Errorf("TailLog(100) should return all 5 lines, got %d", len(lines))
	}
}

// G3: kpm.log rotates to kpm.log.1 once it grows past the size cap.
func TestLogRotation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KPM_ROOT", root)
	p := New()
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	// Write more than logMaxBytes so the NEXT Log rotates.
	big := strings.Repeat("x", logMaxBytes+10)
	if err := os.WriteFile(p.LogFile(), []byte(big+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Log("CHECK", "after rotation"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.LogFile() + ".1"); err != nil {
		t.Errorf("kpm.log.1 should exist after rotation: %v", err)
	}
	info, err := os.Stat(p.LogFile())
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() >= int64(logMaxBytes) {
		t.Errorf("current log should be small after rotation, got %d bytes", info.Size())
	}
	// kpm log reads only the current file.
	lines := p.TailLog(12)
	for _, l := range lines {
		if strings.Contains(l, "xxxxxxxxxx") {
			t.Error("TailLog must not read the rotated .1 content")
		}
	}
}
