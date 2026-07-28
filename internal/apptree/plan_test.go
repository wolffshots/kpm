package apptree

// plan_test.go is the hostile-archive surface (APP-TREE.md §13). Every case here
// asks the same question the review lens asks: can a zip make the extractor write
// outside dest? Each rule is pinned to be a HARD ERROR — the whole install fails,
// nothing is skipped, and no plan is returned that a later Apply could act on.

import (
	"archive/zip"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kpm/internal/uninstall"
)

const testDest = "/mnt/onboard/.adds/x"

// zent describes one entry to write into a test zip. attrs, when non-zero,
// overrides the external attributes verbatim so a hostile unix mode (symlink,
// device, setuid) can be planted regardless of what SetMode would produce.
type zent struct {
	name  string
	body  string
	attrs uint32
}

// makeZip writes a zip with exactly these entries and returns its path. It uses
// the raw header so an entry name that Go's convenience helpers would sanitize
// (backslash, control byte) still lands in the archive verbatim — the whole point
// is to feed the planner what an attacker would.
func makeZip(t *testing.T, ents ...zent) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range ents {
		h := &zip.FileHeader{Name: e.name, Method: zip.Store}
		if e.attrs != 0 {
			h.CreatorVersion = 3 << 8 // unix
			h.ExternalAttrs = e.attrs
		} else {
			h.SetMode(0o644)
		}
		h.UncompressedSize64 = uint64(len(e.body))
		h.CompressedSize64 = uint64(len(e.body)) // Store: the stream IS the content
		h.CRC32 = crc32.ChecksumIEEE([]byte(e.body))
		w, err := zw.CreateRaw(h)
		if err != nil {
			t.Fatalf("CreateRaw(%q): %v", e.name, err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

// wantErr asserts Compute failed with a message containing want, and that it
// returned no plan at all (a partial plan is a plan someone could Apply).
func wantErr(t *testing.T, zipPath, root, dest, want string) {
	t.Helper()
	p, err := Compute(zipPath, root, dest)
	if err == nil {
		t.Fatalf("Compute must refuse; got a plan with %d entries", len(p.Entries))
	}
	if p != nil {
		t.Errorf("Compute returned a plan alongside its error: %+v", p)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err, want)
	}
}

func TestPlanRejectsAbsolutePath(t *testing.T) {
	for _, name := range []string{"/etc/passwd", "//host/share/x", `C:\etc\passwd`, "C:/etc/passwd"} {
		t.Run(name, func(t *testing.T) {
			z := makeZip(t, zent{name: name, body: "x"})
			p, err := Compute(z, "", testDest)
			if err == nil {
				t.Fatalf("Compute must refuse %q; got %d entries", name, len(p.Entries))
			}
		})
	}
}

func TestPlanRejectsTraversal(t *testing.T) {
	// Both forms: the naked escape, and the one path.Clean would launder into a
	// harmless-looking name. Rule 4 runs BEFORE Clean for exactly this reason.
	for _, name := range []string{"../../etc/passwd", "a/../../b", "a/../b"} {
		t.Run(name, func(t *testing.T) {
			wantErr(t, makeZip(t, zent{name: name, body: "x"}), "", testDest, "path traversal in zip")
		})
	}
}

func TestPlanRejectsBackslash(t *testing.T) {
	wantErr(t, makeZip(t, zent{name: `dir\file`, body: "x"}), "", testDest, "contains a backslash")
	// ...including a traversal spelled with the host's separator, which a
	// Clean-only check on Windows would resolve and a Linux check would not.
	wantErr(t, makeZip(t, zent{name: `..\..\etc\passwd`, body: "x"}), "", testDest, "contains a backslash")
}

func TestPlanRejectsControlBytes(t *testing.T) {
	wantErr(t, makeZip(t, zent{name: "a\x00b", body: "x"}), "", testDest, "control character")
	wantErr(t, makeZip(t, zent{name: "a\x01b", body: "x"}), "", testDest, "control character")
	wantErr(t, makeZip(t, zent{name: "a\nb", body: "x"}), "", testDest, "control character")
}

func TestPlanRejectsSymlinkEntry(t *testing.T) {
	// S_IFLNK|0777. Rejected OUTRIGHT — not validated the way tgz.checkLinkTarget
	// validates a tar link target, because a symlink inside the tree is the vector
	// that turns a later per-file write into a write outside dest.
	wantErr(t, makeZip(t,
		zent{name: "ok.lua", body: "x"},
		zent{name: "evil", body: "../../../../etc", attrs: 0o120777 << 16},
	), "", testDest, "disallowed symlink entry in zip")

	// A symlink whose target is entirely benign is refused just the same: the
	// rule is the entry TYPE, not the target.
	wantErr(t, makeZip(t,
		zent{name: "libfoo.so", body: "libfoo.so.1", attrs: 0o120777 << 16},
	), "", testDest, "disallowed symlink entry in zip")
}

// TestPlanRejectsSymlinkWithNonUnixCreator pins the hardening that archive/zip
// itself does not do: FileHeader.Mode() reads the unix bits only when the archive
// CLAIMS a unix creator, which an attacker controls. entryMode reads them
// regardless, so flipping the creator byte does not smuggle a symlink through.
func TestPlanRejectsSymlinkWithNonUnixCreator(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	h := &zip.FileHeader{
		Name:           "evil",
		Method:         zip.Store,
		CreatorVersion: 0, // MSDOS — Mode() ignores ExternalAttrs>>16 for this
		ExternalAttrs:  0o120777 << 16,
	}
	w, err := zw.CreateRaw(h)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("../../etc"))
	zw.Close()
	f.Close()

	// Sanity: stdlib really does miss it, so this test is not vacuous.
	zr, err := zip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	if zr.File[0].Mode()&os.ModeSymlink != 0 {
		t.Skip("archive/zip now honours ExternalAttrs for non-unix creators; the extra guard is redundant")
	}
	zr.Close()

	wantErr(t, p, "", testDest, "disallowed symlink entry in zip")
}

func TestPlanRejectsDeviceAndSetuid(t *testing.T) {
	cases := []struct {
		name  string
		attrs uint32
		want  string
	}{
		{"chardev", 0o020666 << 16, "disallowed device/fifo entry in zip"},
		{"blockdev", 0o060666 << 16, "disallowed device/fifo entry in zip"},
		{"fifo", 0o010666 << 16, "disallowed device/fifo entry in zip"},
		{"socket", 0o140666 << 16, "disallowed device/fifo entry in zip"},
		{"setuid", 0o104755 << 16, "setuid/setgid entry in zip"},
		{"setgid", 0o102755 << 16, "setuid/setgid entry in zip"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantErr(t, makeZip(t, zent{name: c.name, body: "x", attrs: c.attrs}), "", testDest, c.want)
		})
	}
}

// TestPlanRejectsOutsideAllowlist pins that a destination outside the writable
// allowlist fails the WHOLE install, before a single entry is inspected.
func TestPlanRejectsOutsideAllowlist(t *testing.T) {
	z := makeZip(t, zent{name: "y", body: "x"})
	for _, dest := range []string{
		"/mnt/onboard/books/y", // the book library
		"/mnt/onboard",         // the partition root (where the KFMon icons live)
		"/etc/init.d",          // firmware boot scripts
		"/usr/local/Kobo/x",    // Nickel's own tree
		"/mnt/onboard/.adds/kpm/x",
		"/mnt/onboard/.adds/KPM/x", // FAT32 is case-insensitive
	} {
		t.Run(dest, func(t *testing.T) {
			if p, err := Compute(z, "", dest); err == nil {
				t.Fatalf("dest %q must be refused; got %d entries", dest, len(p.Entries))
			}
		})
	}
}

// TestPlanRejectsKpmReservedTree is the app-tree analogue of firstReservedPath:
// no archive may place anything in kpm's own tree or over kpm's NickelMenu
// drop-in. The zip carrying .adds/kpm/bin/kpm cannot be planted, because every
// dest from which it would resolve there is itself refused; and the per-entry
// policy denies the path directly.
func TestPlanRejectsKpmReservedTree(t *testing.T) {
	z := makeZip(t,
		zent{name: ".adds/kpm/bin/kpm", body: "pwned"},
		zent{name: ".adds/nm/kpm", body: "pwned"},
	)
	for _, dest := range []string{"/mnt/onboard", "/mnt/onboard/.adds", "/mnt/onboard/.adds/kpm"} {
		if p, err := Compute(z, "", dest); err == nil {
			t.Errorf("dest %q must be refused; got %d entries", dest, len(p.Entries))
		}
	}
	// The per-entry rule stands on its own, independent of the dest refusals.
	for _, denied := range []string{
		"/mnt/onboard/.adds/kpm/bin/kpm",
		"/mnt/onboard/.adds/KPM/state.json",
		"/mnt/onboard/books/novel.epub",
		"/mnt/onboard/koreader.png",
		"/etc/passwd",
		"/usr/local/Kobo/libnickel.so",
	} {
		if _, err := uninstall.InstallPath(denied); err == nil {
			t.Errorf("InstallPath(%q) must refuse", denied)
		}
	}
	for _, allowed := range []string{
		"/mnt/onboard/.adds/koreader/reader.lua",
		"/mnt/onboard/.adds/nm/koreader",
		"/usr/local/Kobo/imageformats/libx.so",
	} {
		if _, err := uninstall.InstallPath(allowed); err != nil {
			t.Errorf("InstallPath(%q) must allow: %v", allowed, err)
		}
	}
}

func TestPlanRejectsSharedRootDest(t *testing.T) {
	z := makeZip(t, zent{name: "y", body: "x"})
	for _, dest := range []string{"/mnt/onboard/.adds", "/mnt/onboard/.kobo", "/usr/local", "/opt", "/usr/local/Kobo/imageformats"} {
		if p, err := Compute(z, "", dest); err == nil {
			t.Errorf("shared root %q must be refused as dest; got %d entries", dest, len(p.Entries))
		}
	}
	// A package-specific subdirectory of the same root is fine.
	if _, err := Compute(z, "", "/mnt/onboard/.adds/koreader"); err != nil {
		t.Errorf("a package subdirectory must be accepted: %v", err)
	}
}

func TestPlanRejectsDuplicateDest(t *testing.T) {
	wantErr(t, makeZip(t,
		zent{name: "a/b.lua", body: "1"},
		zent{name: "a/b.lua", body: "2"},
	), "", testDest, "zip maps two entries to")

	// FAT32 folds case, so these two collide on the device even though the zip
	// treats them as distinct.
	wantErr(t, makeZip(t,
		zent{name: "A.lua", body: "1"},
		zent{name: "a.lua", body: "2"},
	), "", testDest, "zip maps two entries to")

	// A file colliding with a directory entry is the same ambiguity.
	wantErr(t, makeZip(t,
		zent{name: "a/", body: ""},
		zent{name: "a", body: "1"},
	), "", testDest, "zip maps two entries to")

	// Off the onboard partition the comparison stays case-SENSITIVE, matching
	// the ext4 rootfs — so this must NOT be a duplicate.
	if _, err := Compute(makeZip(t,
		zent{name: "A.lua", body: "1"},
		zent{name: "a.lua", body: "2"},
	), "", "/usr/local/x"); err != nil {
		t.Errorf("rootfs paths are case-sensitive; these are distinct: %v", err)
	}
}

func TestPlanCaps(t *testing.T) {
	t.Run("entries", func(t *testing.T) {
		defer restoreCaps(t)
		maxEntries = 2
		wantErr(t, makeZip(t,
			zent{name: "a", body: "1"}, zent{name: "b", body: "2"}, zent{name: "c", body: "3"},
		), "", testDest, "zip contains more than 2 entries")
	})
	t.Run("bytes", func(t *testing.T) {
		defer restoreCaps(t)
		maxBytes = 4
		wantErr(t, makeZip(t,
			zent{name: "a", body: "123"}, zent{name: "b", body: "456"},
		), "", testDest, "over the 4-byte limit")
	})
	t.Run("depth", func(t *testing.T) {
		defer restoreCaps(t)
		maxDepth = 2
		wantErr(t, makeZip(t, zent{name: "a/b/c", body: "1"}), "", testDest, "nested deeper than 2 levels")
	})
	t.Run("component length", func(t *testing.T) {
		defer restoreCaps(t)
		maxNameLen = 4
		wantErr(t, makeZip(t, zent{name: "abcdef", body: "1"}), "", testDest, "path component longer than 4 bytes")
	})
}

// restoreCaps puts the package caps back after a test lowers them.
func restoreCaps(t *testing.T) {
	t.Helper()
	maxEntries, maxBytes, maxDepth, maxNameLen = 20000, 256<<20, 16, 255
}

// TestPlanRootStripAndIgnore is the KOReader shape: two zip roots, only one of
// which is installed. koreader.png is the KFMon launcher icon and belongs at the
// partition root, which kpm may not write — so it must land in Ignored, never in
// Entries, and never make the install fail.
func TestPlanRootStripAndIgnore(t *testing.T) {
	z := makeZip(t,
		zent{name: "koreader/reader.lua", body: "1"},
		zent{name: "koreader/plugins/x.koplugin/main.lua", body: "2"},
		zent{name: "koreader", body: ""}, // the root's own entry
		zent{name: "koreader.png", body: "icon"},
		zent{name: "README.md", body: "readme"},
	)
	p, err := Compute(z, "koreader", "/mnt/onboard/.adds/koreader")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 2 {
		t.Fatalf("Entries = %v, want the two files under koreader/", p.Entries)
	}
	if p.Entries[0].Rel != "reader.lua" || p.Entries[0].DevAbs != "/mnt/onboard/.adds/koreader/reader.lua" {
		t.Errorf("root prefix not stripped: %+v", p.Entries[0])
	}
	want := map[string]bool{"koreader.png": true, "README.md": true}
	if len(p.Ignored) != 2 || !want[p.Ignored[0]] || !want[p.Ignored[1]] {
		t.Errorf("Ignored = %v, want the two entries outside root", p.Ignored)
	}
	for _, e := range p.Entries {
		if strings.Contains(e.DevAbs, "koreader.png") {
			t.Fatal("the KFMon icon must never be extracted — it is outside root and outside the allowlist")
		}
	}
}

// TestPlanFlatZip is the Plato shape: no root folder, so root == "" and the
// whole zip lands under dest.
func TestPlanFlatZip(t *testing.T) {
	z := makeZip(t,
		zent{name: "plato.sh", body: "1"},
		zent{name: "css/epub.css", body: "2"},
	)
	p, err := Compute(z, "", "/mnt/onboard/.adds/plato")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 2 || len(p.Ignored) != 0 {
		t.Fatalf("Entries=%v Ignored=%v", p.Entries, p.Ignored)
	}
	if p.Entries[1].DevAbs != "/mnt/onboard/.adds/plato/css/epub.css" {
		t.Errorf("DevAbs = %q", p.Entries[1].DevAbs)
	}
	if p.Bytes != 2 {
		t.Errorf("Bytes = %d, want 2", p.Bytes)
	}
}

// TestPlanDirsFromExplicitEntries pins Plato's empty dictionaries/ directory:
// the app expects it to exist, and an extractor that only created parents of
// files would not create it.
func TestPlanDirsFromExplicitEntries(t *testing.T) {
	z := makeZip(t,
		zent{name: "plato.sh", body: "1"},
		zent{name: "dictionaries/", body: ""},
	)
	p, err := Compute(z, "", "/mnt/onboard/.adds/plato")
	if err != nil {
		t.Fatal(err)
	}
	if !hasDir(p.Dirs, "/mnt/onboard/.adds/plato/dictionaries") {
		t.Errorf("Dirs = %v, want the empty explicit dictionaries/ entry", p.Dirs)
	}
	if len(p.Entries) != 1 {
		t.Errorf("a directory entry must not become a file entry: %v", p.Entries)
	}
}

// TestPlanDirsInferredFromFiles pins the KOReader shape: zero explicit directory
// entries, so every directory must be inferred from file paths, shallowest-first.
func TestPlanDirsInferredFromFiles(t *testing.T) {
	z := makeZip(t,
		zent{name: "koreader/plugins/x.koplugin/main.lua", body: "1"},
		zent{name: "koreader/reader.lua", body: "2"},
	)
	p, err := Compute(z, "koreader", "/mnt/onboard/.adds/koreader")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/mnt/onboard/.adds/koreader",
		"/mnt/onboard/.adds/koreader/plugins",
		"/mnt/onboard/.adds/koreader/plugins/x.koplugin",
	}
	if len(p.Dirs) != len(want) {
		t.Fatalf("Dirs = %v, want %v", p.Dirs, want)
	}
	for i, w := range want {
		if p.Dirs[i] != w {
			t.Errorf("Dirs[%d] = %q, want %q (shallowest-first)", i, p.Dirs[i], w)
		}
	}
	// No directory may sit above dest.
	for _, d := range p.Dirs {
		if !uninstall.PathUnder(d, p.Dest) {
			t.Errorf("directory %q is outside dest %q", d, p.Dest)
		}
	}
}

func TestPlanManifestIsTarStyle(t *testing.T) {
	z := makeZip(t, zent{name: "a/b.lua", body: "1"})
	p, err := Compute(z, "", "/mnt/onboard/.adds/koreader")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range p.Manifest() {
		if strings.HasPrefix(m, "/") {
			t.Errorf("manifest member %q must have no leading slash (tar-style, as tgz.Verify produces)", m)
		}
		// And it must map back through the uninstall plumbing unchanged.
		if _, err := uninstall.CleanDeviceAbs(m); err != nil {
			t.Errorf("manifest member %q does not clean: %v", m, err)
		}
	}
	got := strings.Join(p.Manifest(), ",")
	want := "mnt/onboard/.adds/koreader/a,mnt/onboard/.adds/koreader/a/b.lua"
	if got != want {
		t.Errorf("Manifest() = %q, want %q", got, want)
	}
	// dest itself must NOT be a member: it would dedupe away the package's own
	// purge_paths recursive entry and silently disable --purge.
	for _, m := range p.Manifest() {
		if "/"+m == p.Dest {
			t.Error("dest must never appear in the manifest (it would break --purge)")
		}
	}
	// ...but it IS created, so it stays in Dirs.
	if !hasDir(p.Dirs, p.Dest) {
		t.Error("dest must still be created")
	}
}

func TestPlanRejectsBadRoot(t *testing.T) {
	z := makeZip(t, zent{name: "a", body: "1"})
	for _, root := range []string{"/abs", "rel/", "a/../b", `a\b`, ".."} {
		if p, err := Compute(z, root, testDest); err == nil {
			t.Errorf("root %q must be refused; got %d entries", root, len(p.Entries))
		}
	}
}

func TestPlanRejectsEmptyResult(t *testing.T) {
	// A zip whose entries are all outside root installs nothing. Silently
	// recording a successful install of no files would report the package as
	// installed while the device has none of it.
	wantErr(t, makeZip(t, zent{name: "other/x", body: "1"}), "koreader", testDest, "contains no files under root")
}

func hasDir(dirs []string, want string) bool {
	for _, d := range dirs {
		if d == want {
			return true
		}
	}
	return false
}

// TestPlanIgnoresEntriesResolvingToDest: an entry named "." (or "<root>/.")
// addresses the destination DIRECTORY. Queued as a file it would attempt to
// write a regular file over dest; it is dropped exactly like the root's own
// directory entry.
func TestPlanIgnoresEntriesResolvingToDest(t *testing.T) {
	p, err := Compute(makeZip(t,
		zent{name: ".", body: "clobber"},
		zent{name: "./ok.lua", body: "x"},
	), "", testDest)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range p.Entries {
		if e.DevAbs == p.Dest {
			t.Fatalf("an entry resolves to dest itself: %+v", e)
		}
	}
	if len(p.Entries) != 1 || p.Entries[0].Rel != "ok.lua" {
		t.Errorf("Entries = %+v, want just ok.lua", p.Entries)
	}
	// The same, with a root prefix.
	p2, err := Compute(makeZip(t,
		zent{name: "r/.", body: "clobber"},
		zent{name: "r/ok.lua", body: "x"},
	), "r", testDest)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range p2.Entries {
		if e.DevAbs == p2.Dest {
			t.Fatalf("an entry resolves to dest itself: %+v", e)
		}
	}
	if len(p2.Entries) != 1 {
		t.Errorf("Entries = %+v, want just ok.lua", p2.Entries)
	}
}

// TestPlanRefusesFileAndDirectoryCollision closes the Compute/Apply contract
// hole fuzzing found: a regular-file entry `x` and a file `x/y` both survived
// rule 9, because only ENTRIES were registered in the collision map and `x` was
// then INFERRED as a directory. The plan held x in Dirs and in Entries at once —
// a self-inconsistent manifest — and Apply aborted with `rename: file exists`
// after writing part of the tree. That is exactly the half-extracted state this
// package's split exists to prevent, so it must fail at PLAN time.
func TestPlanRefusesFileAndDirectoryCollision(t *testing.T) {
	// file first, then a path under it
	wantErr(t, makeZip(t,
		zent{name: "x", body: "1"},
		zent{name: "x/y", body: "2"},
	), "", testDest, "both a file and a directory")
	// ...and the other order, which the same map has to catch
	wantErr(t, makeZip(t,
		zent{name: "x/y", body: "2"},
		zent{name: "x", body: "1"},
	), "", testDest, "both a file and a directory")
	// A deeper collision: `a/b` is a file and `a/b/c` needs it as a directory.
	wantErr(t, makeZip(t,
		zent{name: "a/b", body: "1"},
		zent{name: "a/b/c", body: "2"},
	), "", testDest, "both a file and a directory")
	// The FAT32 case rule applies here too: X and x are one directory entry.
	wantErr(t, makeZip(t,
		zent{name: "X", body: "1"},
		zent{name: "x/y", body: "2"},
	), "", testDest, "both a file and a directory")
}

// TestPlanRefusesOverlongTotalPath: maxDepth (16) x maxNameLen (255) is 4095
// bytes before dest is prepended, over Linux's PATH_MAX of 4096. Compute
// accepted such a path and Apply then failed "file name too long" AFTER creating
// the 16 leading directories — orphans no manifest records. Bound the total.
func TestPlanRefusesOverlongTotalPath(t *testing.T) {
	seg := strings.Repeat("a", maxNameLen)
	parts := make([]string, maxDepth)
	for i := range parts {
		parts[i] = seg
	}
	name := strings.Join(parts, "/") // 16*255 + 15 = 4095, plus dest = 4116
	z := makeZip(t, zent{name: name, body: "x"})
	wantErr(t, z, "", testDest, "longer than")
}

// TestPlanFoldsInferredDirectoryCase: `seen` folds case for the FAT32 partition
// but dirSet did not, so Sub/a.txt + sub/b.txt planned TWO directories and put
// both spellings in the manifest — one of which is not the on-disk name. Fold it
// so the manifest only ever names paths that exist.
func TestPlanFoldsInferredDirectoryCase(t *testing.T) {
	z := makeZip(t,
		zent{name: "Sub/a.txt", body: "1"},
		zent{name: "sub/b.txt", body: "2"},
	)
	p, err := Compute(z, "", testDest)
	if err != nil {
		t.Fatalf("two distinct files must still plan: %v", err)
	}
	n := 0
	for _, d := range p.Dirs {
		if d != p.Dest {
			n++
		}
	}
	if n != 1 {
		t.Errorf("Dirs = %v, want ONE directory (FAT32 folds Sub and sub together)", p.Dirs)
	}
	// The manifest must not carry a spelling that will not exist on the device.
	if got := len(p.Manifest()); got != 3 {
		t.Errorf("Manifest = %v, want one directory plus the two files", p.Manifest())
	}
	// A rootfs dest is case-SENSITIVE, so both directories are real there.
	p2, err := Compute(z, "", "/opt/x")
	if err != nil {
		t.Fatal(err)
	}
	n = 0
	for _, d := range p2.Dirs {
		if d != p2.Dest {
			n++
		}
	}
	if n != 2 {
		t.Errorf("Dirs = %v, want TWO directories on a case-sensitive rootfs dest", p2.Dirs)
	}
}
