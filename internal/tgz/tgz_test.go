package tgz

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

type entry struct {
	name string
	data string
}

// writeTestTgz builds a gzip'd tar with the given entries.
func writeTestTgz(t *testing.T, path string, entries []entry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.data)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gw.Close()
	f.Close()
}

func TestVerifyCapturesManifest(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.tgz")
	writeTestTgz(t, p, []entry{
		{"./usr/local/App/bin/app", "binary"},
		{"./mnt/onboard/.adds/app/config", "cfg"},
	})
	res, err := Verify(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"usr/local/App/bin/app", "mnt/onboard/.adds/app/config"}
	sort.Strings(want)
	got := append([]string(nil), res.Manifest...)
	sort.Strings(got)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("manifest = %v", res.Manifest)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", res.Warnings)
	}
}

func TestVerifyWarnsUnusualPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "b.tgz")
	writeTestTgz(t, p, []entry{
		{"./usr/local/App/x", "ok"},
		{"./home/root/weird", "hmm"},
	})
	res, err := Verify(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != "home/root/weird" {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestVerifyRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.tgz")
	writeTestTgz(t, p, nil)
	if _, err := Verify(p); err == nil {
		t.Error("expected empty-archive error")
	}
}

func TestVerifyRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"../escape", "./../escape", "/etc/passwd"} {
		dir := t.TempDir()
		p := filepath.Join(dir, "trav.tgz")
		writeTestTgz(t, p, []entry{{bad, "x"}})
		if _, err := Verify(p); err == nil {
			t.Errorf("expected traversal rejection for %q", bad)
		}
	}
}

func TestMergeOrderingAndDupes(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.tgz")
	b := filepath.Join(dir, "b.tgz")
	writeTestTgz(t, a, []entry{{"./usr/shared", "from-a"}, {"./usr/onlyA", "a"}})
	writeTestTgz(t, b, []entry{{"./usr/shared", "from-b"}, {"./usr/onlyB", "b"}})

	out := filepath.Join(dir, "merged.tgz")
	dups, err := Merge([]string{a, b}, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(dups) != 1 || dups[0] != "usr/shared" {
		t.Errorf("dups = %v", dups)
	}

	// The merged archive must contain both b's shared copy (last wins on
	// extraction) and all unique files. Verify the last-written "usr/shared"
	// is from b.
	f, _ := os.Open(out)
	defer f.Close()
	gr, _ := gzip.NewReader(f)
	tr := tar.NewReader(gr)
	var lastShared string
	count := 0
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		count++
		if hdr.Name == "./usr/shared" {
			buf := make([]byte, hdr.Size)
			tr.Read(buf)
			lastShared = string(buf)
		}
	}
	if count != 4 {
		t.Errorf("expected 4 entries in merged tar, got %d", count)
	}
	if lastShared != "from-b" {
		t.Errorf("last usr/shared = %q, want from-b", lastShared)
	}
}

// F4: a pax global header entry is not a member — skipped by Verify and Merge.
func TestVerifyAndMergeSkipGlobalHeader(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "g.tgz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	// A pax global header, then a real member.
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeXGlobalHeader,
		Name:     "pax_global_header",
		PAXRecords: map[string]string{
			"comment": "global",
		},
		Format: tar.FormatPAX,
	}); err != nil {
		t.Fatal(err)
	}
	hdr := &tar.Header{Name: "./usr/local/App/f", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg}
	tw.WriteHeader(hdr)
	tw.Write([]byte("abc"))
	tw.Close()
	gw.Close()
	f.Close()

	res, err := Verify(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Manifest) != 1 || res.Manifest[0] != "usr/local/App/f" {
		t.Errorf("global header must be excluded from manifest: %v", res.Manifest)
	}

	out := filepath.Join(dir, "m.tgz")
	if _, err := Merge([]string{p}, out); err != nil {
		t.Fatal(err)
	}
	f2, _ := os.Open(out)
	defer f2.Close()
	gr, _ := gzip.NewReader(f2)
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			t.Error("merge must not copy the pax global header")
		}
	}
}

// F5: shared directory entries across sources are not reported as duplicates,
// but shared files still are.
func TestMergeDirEntriesNotDup(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.tgz")
	b := filepath.Join(dir, "b.tgz")
	writeTestTgzTyped(t, a, []typedEntry{
		{"./usr/local/Shared/", "", tar.TypeDir},
		{"./usr/local/Shared/a", "a", tar.TypeReg},
	})
	writeTestTgzTyped(t, b, []typedEntry{
		{"./usr/local/Shared/", "", tar.TypeDir},
		{"./usr/local/Shared/b", "b", tar.TypeReg},
	})
	out := filepath.Join(dir, "m.tgz")
	dups, err := Merge([]string{a, b}, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(dups) != 0 {
		t.Errorf("shared directory should not be a dup: %v", dups)
	}
}

type typedEntry struct {
	name string
	data string
	typ  byte
}

func writeTestTgzTyped(t *testing.T, path string, entries []typedEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.data)), Typeflag: e.typ}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if len(e.data) > 0 {
			tw.Write([]byte(e.data))
		}
	}
	tw.Close()
	gw.Close()
	f.Close()
}

func TestMergeKpmLastOrdering(t *testing.T) {
	// This mirrors the caller's ordering guarantee: sources are passed in the
	// order the caller decides; Merge preserves it. Here we assert Merge writes
	// entries in source order by checking the byte content of a shared path.
	dir := t.TempDir()
	other := filepath.Join(dir, "other.tgz")
	kpm := filepath.Join(dir, "kpm.tgz")
	writeTestTgz(t, other, []entry{{"./usr/x", "other"}})
	writeTestTgz(t, kpm, []entry{{"./usr/x", "kpm"}})
	out := filepath.Join(dir, "m.tgz")
	if _, err := Merge([]string{other, kpm}, out); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(out)
	defer f.Close()
	gr, _ := gzip.NewReader(f)
	tr := tar.NewReader(gr)
	var last string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		buf := make([]byte, hdr.Size)
		tr.Read(buf)
		last = string(buf)
	}
	if last != "kpm" {
		t.Errorf("kpm entry should be written last, got %q", last)
	}
}
