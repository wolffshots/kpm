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

// D: Firmware() reads the 3rd comma-separated field of the first line of
// .kobo/version, or ("", false) on any shortfall — it never errors.
func TestFirmware(t *testing.T) {
	cases := []struct {
		name    string
		content string
		write   bool // false: no version file at all
		wantFw  string
		wantOK  bool
	}{
		{name: "real line", content: "N123456789,3.0,4.45.23697,...,...\n", write: true, wantFw: "4.45.23697", wantOK: true},
		{name: "no trailing newline", content: "serial,rev,4.38.23429", write: true, wantFw: "4.38.23429", wantOK: true},
		{name: "first line only", content: "serial,rev,4.40.12345\nsecond line ignored\n", write: true, wantFw: "4.40.12345", wantOK: true},
		{name: "whitespace trimmed", content: "serial,rev,  4.41.1  ,x\n", write: true, wantFw: "4.41.1", wantOK: true},
		{name: "too few fields", content: "serial,rev\n", write: true, wantFw: "", wantOK: false},
		{name: "empty firmware field", content: "serial,rev,   ,x\n", write: true, wantFw: "", wantOK: false},
		{name: "empty file", content: "", write: true, wantFw: "", wantOK: false},
		{name: "missing file", write: false, wantFw: "", wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("KPM_ROOT", root)
			p := New()
			if c.write {
				if err := os.MkdirAll(filepath.Dir(p.VersionFile()), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p.VersionFile(), []byte(c.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			fw, ok := p.Firmware()
			if fw != c.wantFw || ok != c.wantOK {
				t.Errorf("Firmware() = (%q, %v), want (%q, %v)", fw, ok, c.wantFw, c.wantOK)
			}
		})
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

// ---- INTEGRITY.md §8: status.txt -> extensionless status -----------------

// §8.1: the basename must be exactly "status" — an extension is what Nickel's
// ExcludeSyncFolders variant imports as a book.
func TestStatusFileIsExtensionless(t *testing.T) {
	t.Setenv("KPM_ROOT", t.TempDir())
	p := New()
	if got := filepath.Base(p.StatusFile()); got != "status" {
		t.Errorf("StatusFile() basename = %q, want %q", got, "status")
	}
	if got := filepath.Base(p.LegacyStatusFile()); got != "status.txt" {
		t.Errorf("LegacyStatusFile() basename = %q, want %q", got, "status.txt")
	}
}

// §8.3: the migration REMOVES the old file rather than orphaning it — and only
// after the new one exists, so the user is never left with nothing.
func TestWriteStatusRemovesLegacyFile(t *testing.T) {
	t.Setenv("KPM_ROOT", t.TempDir())
	p := New()
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.LegacyStatusFile(), []byte("old summary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteStatus("new summary"); err != nil {
		t.Fatal(err)
	}
	if got := p.ReadStatus(); got != "new summary\n" {
		t.Errorf("ReadStatus = %q, want the new summary", got)
	}
	if _, err := os.Stat(p.StatusFile()); err != nil {
		t.Errorf("the new status file must exist: %v", err)
	}
	if _, err := os.Stat(p.LegacyStatusFile()); !os.IsNotExist(err) {
		t.Errorf("status.txt must be removed by the migration, stat err = %v", err)
	}
}

// §8.3: between the upgrade and the first mutating command there is no new file
// yet, so "kpm status" must keep showing the last summary from the legacy one.
func TestReadStatusFallsBackToLegacy(t *testing.T) {
	t.Setenv("KPM_ROOT", t.TempDir())
	p := New()
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.LegacyStatusFile(), []byte("pre-0.10.0 summary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := p.ReadStatus(); got != "pre-0.10.0 summary\n" {
		t.Errorf("ReadStatus = %q, want the legacy contents", got)
	}
	// Reading must not migrate anything (it runs from read-only commands, B1).
	if _, err := os.Stat(p.LegacyStatusFile()); err != nil {
		t.Errorf("ReadStatus must not remove the legacy file: %v", err)
	}
}

// WriteReaderAtomic's limit is a HARD STOP with a clear error, never a silent
// truncation: it is the bound that actually holds when an app-tree zip's declared
// sizes are lies (APP-TREE.md §5.2 step 3).
func TestWriteReaderAtomicLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")

	// Under the limit: written, and no temp file survives.
	if err := WriteReaderAtomic(path, strings.NewReader("hello"), 16); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "hello" {
		t.Errorf("content = %q", b)
	}
	// Exactly the limit is fine (the limiter reads limit+1 to detect overflow).
	if err := WriteReaderAtomic(path, strings.NewReader("0123456789abcdef"), 16); err != nil {
		t.Errorf("a stream exactly at the limit must be accepted: %v", err)
	}

	// Over the limit: error, previous bytes intact, nothing left behind.
	err := WriteReaderAtomic(path, strings.NewReader(strings.Repeat("z", 64)), 16)
	if err == nil {
		t.Fatal("a stream over the limit must error, never truncate silently")
	}
	if b, _ := os.ReadFile(path); string(b) != "0123456789abcdef" {
		t.Errorf("the previous content must survive a rejected write, got %q", b)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".kpm-") {
			t.Errorf("temp file %s survived the rejected write", e.Name())
		}
	}
}
