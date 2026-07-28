package apptree

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kpm/internal/device"
	"kpm/internal/testenv"
)

// sandbox wires a KPM_SYSROOT/KPM_ROOT pair so every device-absolute path lands
// inside a temp tree, exactly as internal/uninstall's tests do.
func sandbox(t *testing.T) (device.Paths, string) {
	t.Helper()
	sysroot := t.TempDir()
	t.Setenv("KPM_SYSROOT", sysroot)
	t.Setenv("KPM_ROOT", filepath.Join(sysroot, "mnt", "onboard"))
	return device.New(), sysroot
}

// hostOf maps a device path into the sandbox.
func hostOf(sysroot, devAbs string) string {
	return filepath.Join(sysroot, filepath.FromSlash(strings.TrimPrefix(devAbs, "/")))
}

func TestApplyWritesTreeAndManifest(t *testing.T) {
	p, sysroot := sandbox(t)
	z := makeZip(t,
		zent{name: "koreader/reader.lua", body: "reader"},
		zent{name: "koreader/plugins/x.koplugin/main.lua", body: "plugin"},
		zent{name: "koreader/dictionaries/", body: ""},
		zent{name: "koreader.png", body: "icon"},
	)
	plan, err := Compute(z, "koreader", "/mnt/onboard/.adds/koreader")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Apply(p, plan, z)
	if err != nil {
		t.Fatal(err)
	}

	for dev, want := range map[string]string{
		"/mnt/onboard/.adds/koreader/reader.lua":                  "reader",
		"/mnt/onboard/.adds/koreader/plugins/x.koplugin/main.lua": "plugin",
	} {
		b, err := os.ReadFile(hostOf(sysroot, dev))
		if err != nil {
			t.Fatalf("%s: %v", dev, err)
		}
		if string(b) != want {
			t.Errorf("%s = %q, want %q", dev, b, want)
		}
	}
	// The empty explicit directory entry must exist.
	if fi, err := os.Stat(hostOf(sysroot, "/mnt/onboard/.adds/koreader/dictionaries")); err != nil || !fi.IsDir() {
		t.Errorf("the empty dictionaries/ entry must be created: %v", err)
	}
	// Nothing outside root — the KFMon icon belongs at the partition root, which
	// kpm may not write.
	if _, err := os.Stat(filepath.Join(sysroot, "mnt", "onboard", "koreader.png")); err == nil {
		t.Error("an entry outside root must never be written")
	}

	// Manifest: tar-style, no leading slash, dirs AND files.
	joined := strings.Join(res.Manifest, "\n")
	for _, want := range []string{
		"mnt/onboard/.adds/koreader/dictionaries",
		"mnt/onboard/.adds/koreader/reader.lua",
		"mnt/onboard/.adds/koreader/plugins/x.koplugin/main.lua",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("manifest missing %q:\n%s", want, joined)
		}
	}
	for _, m := range res.Manifest {
		if strings.HasPrefix(m, "/") {
			t.Errorf("manifest member %q must be tar-style (no leading slash)", m)
		}
	}
	if len(res.Written) != 2 {
		t.Errorf("Written = %v, want the two files", res.Written)
	}
}

func TestApplyIsAtomicPerFile(t *testing.T) {
	p, sysroot := sandbox(t)
	z := makeZip(t, zent{name: "a.lua", body: "v1"}, zent{name: "b.lua", body: "v1"})
	plan, err := Compute(z, "", "/mnt/onboard/.adds/x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p, plan, z); err != nil {
		t.Fatal(err)
	}
	if n := countTemps(t, filepath.Join(sysroot, "mnt", "onboard", ".adds", "x")); n != 0 {
		t.Errorf("%d .kpm-*.tmp survived a successful run", n)
	}

	// Force a mid-copy failure on the SECOND file by starving the byte budget,
	// then assert the first file's previous bytes are untouched — a per-file
	// atomic write never leaves a half-written or truncated file behind.
	defer restoreCaps(t)
	big := strings.Repeat("z", 64)
	z2 := makeZip(t, zent{name: "b.lua", body: big})
	plan2, err := Compute(z2, "", "/mnt/onboard/.adds/x")
	if err != nil {
		t.Fatal(err)
	}
	maxBytes = 8 // below the real stream, above the plan's declared total check
	if _, err := Apply(p, plan2, z2); err == nil {
		t.Fatal("Apply must fail when a stream exceeds the byte limit")
	}
	b, err := os.ReadFile(hostOf(sysroot, "/mnt/onboard/.adds/x/b.lua"))
	if err != nil || string(b) != "v1" {
		t.Errorf("the previous file's bytes must survive a failed write, got %q (%v)", b, err)
	}
	if n := countTemps(t, filepath.Join(sysroot, "mnt", "onboard", ".adds", "x")); n != 0 {
		t.Errorf("%d .kpm-*.tmp survived a FAILED run; every error path must sweep", n)
	}
}

func TestApplySweepsStaleTemps(t *testing.T) {
	p, sysroot := sandbox(t)
	dest := filepath.Join(sysroot, "mnt", "onboard", ".adds", "x")
	if err := os.MkdirAll(filepath.Join(dest, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := []string{
		filepath.Join(dest, ".kpm-123.tmp"),
		filepath.Join(dest, "sub", ".kpm-abc.tmp"),
	}
	keep := filepath.Join(dest, "user-data.txt")
	for _, s := range append(append([]string{}, stale...), keep) {
		if err := os.WriteFile(s, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	z := makeZip(t, zent{name: "a.lua", body: "v1"})
	plan, err := Compute(z, "", "/mnt/onboard/.adds/x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p, plan, z); err != nil {
		t.Fatal(err)
	}
	for _, s := range stale {
		if _, err := os.Stat(s); err == nil {
			t.Errorf("a crashed run's %s must be swept", filepath.Base(s))
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("the sweep must only touch .kpm-*.tmp, never user data")
	}
}

func TestApplyRefusesSymlinkedParent(t *testing.T) {
	p, sysroot := sandbox(t)
	outside := filepath.Join(sysroot, "opt", "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(sysroot, "mnt", "onboard", ".adds", "x")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "sub")); err != nil {
		testenv.SkipUnsupported(t, "symlink creation unsupported on this host: %v", err)
	}

	z := makeZip(t, zent{name: "sub/pwned.lua", body: "pwned"})
	plan, err := Compute(z, "", "/mnt/onboard/.adds/x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p, plan, z); err == nil {
		t.Fatal("Apply must refuse to write through a symlinked parent")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.lua")); err == nil {
		t.Fatal("a write landed OUTSIDE dest through the symlink")
	}
}

func TestApplyModesAreFixed(t *testing.T) {
	p, sysroot := sandbox(t)
	// The zip declares 0777 and 0600; both must land as 0644, because
	// /mnt/onboard is FAT32 with a fixed mount mode and per-file bits do not
	// exist there.
	z := makeZip(t,
		zent{name: "exec.sh", body: "#!/bin/sh", attrs: 0o100777 << 16},
		zent{name: "private", body: "x", attrs: 0o100600 << 16},
	)
	plan, err := Compute(z, "", "/mnt/onboard/.adds/x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p, plan, z); err != nil {
		t.Fatal(err)
	}
	if runtimeIsWindows() {
		testenv.SkipUnsupported(t, "Windows does not model unix permission bits")
	}
	for _, name := range []string{"exec.sh", "private"} {
		fi, err := os.Stat(hostOf(sysroot, "/mnt/onboard/.adds/x/"+name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o644 {
			t.Errorf("%s mode = %o, want 0644 (zip bits are deliberately discarded)", name, fi.Mode().Perm())
		}
	}
	fi, err := os.Stat(hostOf(sysroot, "/mnt/onboard/.adds/x"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("dir mode = %o, want 0755", fi.Mode().Perm())
	}
}

// TestApplyLimitsCopy pins the copy limiter itself: the plan is built while the
// cap is generous and the cap is then lowered, so the ONLY thing that can stop
// the write is the limiter around the copy. Remove it and the file lands.
func TestApplyLimitsCopy(t *testing.T) {
	p, sysroot := sandbox(t)
	defer restoreCaps(t)
	z := makeZip(t, zent{name: "big.bin", body: strings.Repeat("A", 4096)})
	plan, err := Compute(z, "", "/mnt/onboard/.adds/x")
	if err != nil {
		t.Fatal(err)
	}
	maxBytes = 64
	if _, err := Apply(p, plan, z); err == nil {
		t.Fatal("Apply must refuse a stream that exceeds the cap")
	}
	if _, err := os.Stat(hostOf(sysroot, "/mnt/onboard/.adds/x/big.bin")); err == nil {
		t.Error("an over-limit file must never be renamed into place")
	}
	if n := countTemps(t, filepath.Join(sysroot, "mnt", "onboard", ".adds", "x")); n != 0 {
		t.Errorf("%d temp files survived the over-limit abort", n)
	}
}

// TestApplyRejectsLyingDeclaredSize pins the companion fact: a zip header's
// declared size is attacker-controlled, so a plan built from it can be a lie. An
// entry that declares one byte and delivers 4 KiB must not produce a written file.
//
// (archive/zip's own size/CRC check is what reports the mismatch — but it reports
// it only at EOF, AFTER the bytes have flowed, which is precisely why the copy
// limiter above has to exist as well: it bounds how much a liar can make kpm
// write before the mismatch surfaces.)
func TestApplyRejectsLyingDeclaredSize(t *testing.T) {
	p, sysroot := sandbox(t)
	z := lyingZip(t, "big.bin", strings.Repeat("A", 4096), 1)
	plan, err := Compute(z, "", "/mnt/onboard/.adds/x")
	if err != nil {
		t.Fatalf("the plan sees only the lie, so it must succeed: %v", err)
	}
	if plan.Bytes != 1 {
		t.Fatalf("plan.Bytes = %d, want the declared lie (1)", plan.Bytes)
	}
	if _, err := Apply(p, plan, z); err == nil {
		t.Fatal("Apply must refuse an entry whose stream does not match its header")
	}
	if _, err := os.Stat(hostOf(sysroot, "/mnt/onboard/.adds/x/big.bin")); err == nil {
		t.Error("a mismatched entry must never be renamed into place")
	}
}

// TestApplyRefusesTamperedPlan is layer two of the 0.6.2 discipline, isolated:
// Apply must re-validate every path immediately before the write that uses it,
// so a Plan that was correct when computed cannot be turned into a write outside
// the policy afterwards. state.json and packages.d are user-editable and the
// device runs kpm as root, so "the plan was already checked" is not enough.
func TestApplyRefusesTamperedPlan(t *testing.T) {
	build := func(t *testing.T) (device.Paths, string, *Plan, string) {
		t.Helper()
		p, sysroot := sandbox(t)
		z := makeZip(t, zent{name: "a.lua", body: "payload"})
		plan, err := Compute(z, "", "/mnt/onboard/.adds/x")
		if err != nil {
			t.Fatal(err)
		}
		return p, sysroot, plan, z
	}

	// (a) An entry redirected OUTSIDE dest: caught by the containment re-check.
	t.Run("entry escapes dest", func(t *testing.T) {
		p, sysroot, plan, z := build(t)
		plan.Entries[0].DevAbs = "/mnt/onboard/.adds/other/pwned.lua"
		if _, err := Apply(p, plan, z); err == nil {
			t.Fatal("Apply must refuse an entry outside dest")
		}
		if _, err := os.Stat(hostOf(sysroot, "/mnt/onboard/.adds/other/pwned.lua")); err == nil {
			t.Fatal("a write landed outside dest")
		}
	})

	// (b) dest itself widened so the entry stays "inside" it but lands somewhere
	// the policy denies: caught by the per-path policy re-check. This is the only
	// way to reach that rule, which is exactly why it is defence in depth.
	for _, target := range []string{
		"/mnt/onboard/books/pwned.epub",
		"/mnt/onboard/.adds/kpm/bin/kpm",
		"/mnt/onboard/koreader.png",
	} {
		t.Run("policy re-check "+target, func(t *testing.T) {
			p, sysroot, plan, z := build(t)
			plan.Dest = "/mnt/onboard"
			plan.Dirs = nil
			plan.Entries[0].DevAbs = target
			if _, err := Apply(p, plan, z); err == nil {
				t.Fatalf("Apply must refuse a write to %s", target)
			}
			if _, err := os.Stat(hostOf(sysroot, target)); err == nil {
				t.Fatalf("a write landed at the denied path %s", target)
			}
		})
	}

	// (c) A tampered DIRECTORY must be refused before MkdirAll runs.
	t.Run("directory escapes policy", func(t *testing.T) {
		p, sysroot, plan, z := build(t)
		plan.Dest = "/mnt/onboard"
		plan.Dirs = []string{"/mnt/onboard/books/pwned"}
		if _, err := Apply(p, plan, z); err == nil {
			t.Fatal("Apply must refuse to create a directory outside the policy")
		}
		if _, err := os.Stat(hostOf(sysroot, "/mnt/onboard/books/pwned")); err == nil {
			t.Fatal("a directory was created at a denied path")
		}
	})
}

// TestApplyTotalBudgetIsEnforced pins the cross-file budget: a thousand entries
// each declaring one byte must not be able to write a thousand times the cap.
func TestApplyTotalBudgetIsEnforced(t *testing.T) {
	p, _ := sandbox(t)
	defer restoreCaps(t)
	var ents []zent
	for _, n := range []string{"a", "b", "c", "d"} {
		ents = append(ents, zent{name: n, body: strings.Repeat("x", 32)})
	}
	z := makeZip(t, ents...)
	plan, err := Compute(z, "", "/mnt/onboard/.adds/x")
	if err != nil {
		t.Fatal(err)
	}
	// Each file is under the cap; their sum is not.
	maxBytes = 100
	if _, err := Apply(p, plan, z); err == nil {
		t.Fatal("Apply must enforce the byte cap across the WHOLE extraction, not per file")
	}
}

func TestApplyRejectsPlanReferencingMissingEntry(t *testing.T) {
	p, _ := sandbox(t)
	z := makeZip(t, zent{name: "a.lua", body: "v1"})
	plan, err := Compute(z, "", "/mnt/onboard/.adds/x")
	if err != nil {
		t.Fatal(err)
	}
	plan.Entries[0].ZipIndex = 99
	if _, err := Apply(p, plan, z); err == nil {
		t.Fatal("a plan whose index is out of range must not be applied")
	}
}

// lyingZip writes one stored entry whose header declares declaredSize but whose
// stream is the full body.
func lyingZip(t *testing.T, name, body string, declaredSize uint64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lie.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	h := &zip.FileHeader{Name: name, Method: zip.Store}
	h.SetMode(0o644)
	h.UncompressedSize64 = declaredSize
	h.CompressedSize64 = uint64(len(body))
	w, err := zw.CreateRaw(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	f.Close()
	return path
}

func countTemps(t *testing.T, root string) int {
	t.Helper()
	n := 0
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		base := filepath.Base(p)
		if strings.HasPrefix(base, ".kpm-") && strings.HasSuffix(base, ".tmp") {
			n++
		}
		return nil
	})
	return n
}

func runtimeIsWindows() bool { return os.PathSeparator == '\\' }

// ---- free space (APP-TREE.md §5.2 step 1) --------------------------------

// stubAvail poses a filesystem with avail bytes free and a bsize-byte allocation
// block, and restores the real statfs afterwards.
func stubAvail(t *testing.T, avail, bsize int64) {
	t.Helper()
	prev := availableBytes
	availableBytes = func(string) (int64, int64, bool) { return avail, bsize, true }
	t.Cleanup(func() { availableBytes = prev })
}

// TestApplyAccountsForFilesystemBlockRounding is the ENOSPC case that needs no
// hostile input. plan.Bytes is a sum of raw file sizes, but every file occupies
// a whole allocation block: on the Kobo's FAT32 partition that is a 32 KiB
// cluster, so 100 one-byte files cost 3.2 MiB, not 100 bytes. Checking the raw
// sum let a device with plenty of "free space" by that measure pass the check
// and then fill the user partition to zero mid-extraction — after which Nickel
// cannot write its database or save a reading position.
func TestApplyAccountsForFilesystemBlockRounding(t *testing.T) {
	p, sysroot := sandbox(t)
	var ents []zent
	for i := 0; i < 100; i++ {
		ents = append(ents, zent{name: fmt.Sprintf("f%03d", i), body: "x"})
	}
	z := makeZip(t, ents...)
	plan, err := Compute(z, "", "/mnt/onboard/.adds/x")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Bytes != 100 {
		t.Fatalf("fixture: plan.Bytes = %d, want 100 raw bytes", plan.Bytes)
	}

	// 17 MiB free, 32 KiB clusters. The raw sum says 100 bytes + 16 MiB slack
	// fits with a megabyte to spare; the real footprint is 100 clusters =
	// 3.2 MiB + 16 MiB, which does not.
	const cluster = 32 << 10
	stubAvail(t, 17<<20, cluster)
	if _, err := Apply(p, plan, z); err == nil {
		t.Fatal("Apply must refuse when the per-file cluster cost does not fit")
	}
	if _, err := os.Stat(hostOf(sysroot, "/mnt/onboard/.adds/x/f000")); err == nil {
		t.Error("the refusal must happen before any file is written")
	}

	// The same plan on a filesystem that really does have room still installs:
	// the rounding must not turn into a blanket refusal.
	stubAvail(t, 64<<20, cluster)
	if _, err := Apply(p, plan, z); err != nil {
		t.Fatalf("a plan that fits must still apply: %v", err)
	}
}

// TestApplyCountsDirectoriesAgainstFreeSpace: a directory costs a block too, and
// KOReader's tree is ~200 of them. A plan of empty directories has zero declared
// bytes, so without this term its footprint is reported as nothing at all.
func TestApplyCountsDirectoriesAgainstFreeSpace(t *testing.T) {
	p, _ := sandbox(t)
	ents := []zent{{name: "f", body: "x"}}
	for i := 0; i < 200; i++ {
		ents = append(ents, zent{name: fmt.Sprintf("d%03d/", i), body: ""})
	}
	z := makeZip(t, ents...)
	plan, err := Compute(z, "", "/mnt/onboard/.adds/x")
	if err != nil {
		t.Fatal(err)
	}
	const cluster = 32 << 10
	// 16 MiB slack + one file cluster fits in 17 MiB; 201 directory clusters
	// (6.3 MiB) do not.
	stubAvail(t, 17<<20, cluster)
	if _, err := Apply(p, plan, z); err == nil {
		t.Fatal("Apply must count the directories it creates against free space")
	}
}

// TestPlanFootprintRounding pins the arithmetic itself, including the degenerate
// block sizes a caller must never be able to turn into a division by zero or a
// silently-zero footprint.
func TestPlanFootprintRounding(t *testing.T) {
	plan := &Plan{
		Dest:    "/mnt/onboard/.adds/x",
		Bytes:   1 + 4096 + 4097,
		Entries: []Entry{{Size: 1}, {Size: 4096}, {Size: 4097}},
		Dirs:    []string{"/mnt/onboard/.adds/x", "/mnt/onboard/.adds/x/sub"},
	}
	// 4096-byte blocks: 1 -> 1 block, 4096 -> 1 block, 4097 -> 2 blocks, plus
	// 2 directories = 6 blocks.
	if got, want := plan.footprint(4096), int64(6*4096); got != want {
		t.Errorf("footprint(4096) = %d, want %d", got, want)
	}
	// A zero-length file costs no cluster.
	empty := &Plan{Entries: []Entry{{Size: 0}}, Dirs: []string{"/x"}}
	if got, want := empty.footprint(4096), int64(4096); got != want {
		t.Errorf("footprint of an empty file + one dir = %d, want %d", got, want)
	}
	// A nonsensical block size degrades to the raw sum rather than to zero.
	if got := plan.footprint(0); got < plan.Bytes {
		t.Errorf("footprint(0) = %d, must never under-report the raw sum %d", got, plan.Bytes)
	}
	if got := plan.footprint(-1); got < plan.Bytes {
		t.Errorf("footprint(-1) = %d, must never under-report the raw sum %d", got, plan.Bytes)
	}
}
