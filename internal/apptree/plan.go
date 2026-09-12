// Package apptree installs a package that ships a raw directory tree in a
// release zip — KOReader, Plato — by extracting it straight into
// /mnt/onboard/.adds/<name>/, with no KoboRoot.tgz, no root unpack by the
// firmware, and no reboot (APP-TREE.md).
//
// # Why this package is split in two
//
// Compute reads the zip and produces a fully-validated file list without writing
// a single byte; Apply writes only paths Compute already cleared, and re-checks
// each of them immediately before the write that uses it. That is the same
// two-layer discipline internal/uninstall adopted after the 0.6.2 brick risk
// (commit c329d38), where a recursive delete entered through an allowed ancestor
// and then walked into /usr/local/Kobo because the policy was applied once, at
// the top, instead of at every node. Here kpm is the WRITER rather than handing a
// tarball to rcS, so every rule below is a hard error that aborts the install —
// never a skip, never a warning. A package that ships a file kpm cannot place is
// not installed correctly, and a half-extracted KOReader is worse than none.
//
// # Why there is no transaction
//
// There is no atomic directory swap, and a staging-directory-plus-rename scheme
// was considered and rejected: user data lives INSIDE dest (KOReader keeps
// settings/, docsettings/, statistics/ there), so swapping dest aside would
// orphan it, and copying it back costs another 80+ MiB and is not atomic either.
//
// The property that replaces atomicity is idempotent re-extraction:
//
//   - Per-file writes are atomic, so no file is ever half-written. A crash leaves
//     a mixture of old and new WHOLE files.
//   - state.json is written AFTER extraction completes. A crash before that leaves
//     installed_version at the old value, so the next `kpm update` re-runs the
//     whole extraction and the stale-file prune against the still-correct old
//     manifest, converging exactly.
//   - The prune runs LAST, so a crash can never leave files deleted but not
//     replaced.
//   - A state-save failure after a successful extraction leaves the package
//     reporting an available update until a save succeeds — self-healing, and
//     strictly safer than the alternative.
//
// This is the load-bearing argument for the whole design. Do not re-litigate it
// on noticing there is no transaction.
package apptree

import (
	"archive/zip"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"kpm/internal/config"
	"kpm/internal/uninstall"
)

// Resource caps. All vars, not consts, so tests lower them instead of building
// giant fixtures — the same reason internal/tgz's caps are vars.
var (
	// maxEntries bounds the entry count. KOReader is 1045, Plato 209.
	maxEntries = 20000
	// maxBytes bounds total content. KOReader is 83.9 MiB uncompressed. It is
	// checked against the zip's DECLARED sizes here and enforced again during the
	// copy in Apply: declared sizes are attacker-controlled, the copy limiter is
	// what actually holds.
	maxBytes int64 = 256 << 20
	// maxDepth bounds path nesting. KOReader's deepest entry is 7 components.
	maxDepth = 16
	// maxNameLen bounds one path component. KOReader's longest full name is 73.
	maxNameLen = 255
	// maxPathLen bounds the WHOLE composed device path. maxDepth x maxNameLen is
	// 4095 bytes before dest is prepended, over Linux's PATH_MAX of 4096: without
	// this, Compute accepted a path Apply could not create, and Apply failed
	// "file name too long" only AFTER creating the 16 leading directories — orphans
	// no manifest records and no prune will ever remove.
	maxPathLen = 4095
)

// Entry is one validated file (or explicit directory) to extract.
type Entry struct {
	ZipIndex int    // index into zip.Reader.File
	Rel      string // path relative to Dest, slash-separated, root-stripped
	DevAbs   string // cleaned absolute device path (= Dest + "/" + Rel)
	Size     int64  // declared uncompressed size
	Dir      bool   // explicit zip directory entry
}

// Plan is a fully-validated extraction: nothing in it has yet been written, and
// nothing outside it may be written.
type Plan struct {
	Dest    string
	Entries []Entry  // files only; explicit directory entries are folded into Dirs
	Dirs    []string // every directory to create, shallowest-first, device-absolute
	Ignored []string // zip entries outside `root` — reported, never extracted
	Bytes   int64    // sum of declared uncompressed sizes
}

// Compute reads the zip at zipPath and produces the validated Plan for extracting
// the subtree at `root` into `dest`. It writes nothing.
//
// It is named Compute rather than Plan (as APP-TREE.md §5.1 spells it) only
// because Go forbids a type and a function sharing a name in one package; the
// name mirrors uninstall.Compute, the sibling this package's split is modelled on.
func Compute(zipPath, root, dest string) (*Plan, error) {
	// The destination is re-validated here even though ParseManifest already did
	// it: the local packages.d snapshot is user-editable, so the parse-time check
	// is not the last word on where kpm writes as root.
	absDest, err := uninstall.InstallDest(dest)
	if err != nil {
		return nil, err
	}
	if err := config.ValidateInstallRoot(root); err != nil {
		return nil, err
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("zip: %w", err)
	}
	defer zr.Close()

	if len(zr.File) > maxEntries {
		return nil, fmt.Errorf("zip contains more than %d entries", maxEntries)
	}

	// FAT32 is case-insensitive, so two entries differing only in case would
	// collide on the device. Decide the folding rule once from dest, reusing the
	// policy engine's own containment predicate, and key EVERY map in this
	// function through it — a map that folds and one that does not disagree about
	// what the device will hold.
	fold := uninstall.PathUnder(absDest, "/mnt/onboard")
	// Nickel reads a content directory FLAT: a font in fonts/sub/ is ignored, so a
	// nested zip would install cleanly and silently do nothing. Refusing at plan
	// time also keeps per-file ownership (D4) to one directory level.
	flat := uninstall.IsContentDir(absDest)
	key := func(devAbs string) string {
		if fold {
			return strings.ToLower(devAbs)
		}
		return devAbs
	}
	seen := map[string]string{} // collision key -> first DevAbs that claimed it

	p := &Plan{Dest: absDest}
	// dirSet maps the collision key to the first spelling that claimed it. Keying
	// it case-sensitively while `seen` folded put both Sub/ and sub/ in the plan,
	// so the manifest carried a path that would not exist on the device.
	dirSet := map[string]string{key(absDest): absDest} // dest itself is always created
	var declared int64

	for i, zf := range zr.File {
		name := zf.Name

		// 1. Backslash. On the Linux target it is a filename byte; on a Windows
		// host it is a separator. Accepting it means the host and the device
		// disagree about what was written — refuse the whole class.
		if strings.ContainsRune(name, '\\') {
			return nil, fmt.Errorf("zip entry %q contains a backslash", name)
		}
		// 2. Control bytes (NUL included).
		for j := 0; j < len(name); j++ {
			if name[j] < 0x20 {
				return nil, fmt.Errorf("zip entry %q contains a control character", name)
			}
		}
		// 3. Absolute, drive-letter or UNC.
		if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "//") || hasDriveLetter(name) {
			return nil, fmt.Errorf("absolute path in zip: %q", name)
		}
		// 4. Traversal, BEFORE path.Clean: Clean resolves a/../b to b and would
		// launder a name written to escape. cleanDeviceAbs splits the same way.
		for _, seg := range strings.Split(name, "/") {
			if seg == ".." {
				return nil, fmt.Errorf("path traversal in zip: %q", name)
			}
		}

		// 5. root selection. Everything outside root is recorded and never
		// extracted; the root's own directory entry contributes nothing.
		rel, keep := stripRoot(name, root)
		if !keep {
			p.Ignored = append(p.Ignored, name)
			continue
		}
		isDir := strings.HasSuffix(name, "/")
		rel = strings.Trim(rel, "/")
		if rel == "" {
			continue // the root itself; dest is already in dirSet
		}
		rel = path.Clean(rel)
		// An entry that cleans to "." addresses DEST ITSELF (a zip entry literally
		// named "." or "root/."). It is already a directory in dirSet, and letting
		// it through as a file entry would queue a write of a regular file over the
		// destination directory. Drop it the same way the root's own entry is
		// dropped.
		if rel == "." {
			continue
		}

		segs := strings.Split(rel, "/")
		if len(segs) > maxDepth {
			return nil, fmt.Errorf("zip entry %q is nested deeper than %d levels", name, maxDepth)
		}
		for _, s := range segs {
			if len(s) > maxNameLen {
				return nil, fmt.Errorf("zip entry %q has a path component longer than %d bytes", name, maxNameLen)
			}
		}

		// 6. Entry type. Symlinks are rejected OUTRIGHT, not validated the way
		// tgz.checkLinkTarget validates a tar link: /mnt/onboard is FAT32 and
		// cannot hold a symlink at all, so nothing legitimate is lost, and a
		// symlink inside the tree is exactly the vector that turns a later
		// per-file write into a write outside dest. All three real assets contain
		// zero symlink entries.
		mode := entryMode(zf)
		switch {
		case mode&os.ModeSymlink != 0:
			return nil, fmt.Errorf("disallowed symlink entry in zip: %q", name)
		case mode&(os.ModeDevice|os.ModeCharDevice|os.ModeNamedPipe|os.ModeSocket) != 0:
			return nil, fmt.Errorf("disallowed device/fifo entry in zip: %q", name)
		case mode&(os.ModeSetuid|os.ModeSetgid) != 0:
			return nil, fmt.Errorf("setuid/setgid entry in zip: %q", name)
		}
		if mode&os.ModeDir != 0 {
			isDir = true
		}

		// 6a. The flat rule (content dirs only). An explicit directory entry is
		// refused too: it would create the subdirectory the files are refused for.
		if flat && (isDir || strings.Contains(rel, "/")) {
			return nil, fmt.Errorf("zip entry %q is in a subdirectory, but %s is read flat", name, absDest)
		}

		// 7. Compose and re-verify containment. Belt and braces after Clean: if
		// this fires it is a bug in THIS function, not user input.
		devAbs := path.Join(absDest, rel)
		if len(devAbs) > maxPathLen {
			return nil, fmt.Errorf("zip entry %q installs to a path of %d bytes, longer than the %d-byte limit",
				name, len(devAbs), maxPathLen)
		}
		if !uninstall.PathUnder(devAbs, absDest) {
			return nil, fmt.Errorf("internal error: zip entry %q escapes dest %q", name, absDest)
		}
		// Belt and braces alongside the rel == "." drop above: nothing may target
		// dest itself, which is a directory this plan creates.
		if devAbs == absDest {
			return nil, fmt.Errorf("internal error: zip entry %q resolves to dest %q", name, absDest)
		}
		// 8. Path policy, per entry — no allow_paths extension: a registry may
		// widen where kpm deletes, never where it writes. This is what makes
		// /mnt/onboard/koreader.png impossible to write even with root = "", and
		// what keeps every entry out of kpm's own tree.
		clean, err := uninstall.InstallPath(devAbs)
		if err != nil {
			return nil, fmt.Errorf("zip entry %q would install to %q, which is not a writable location", name, devAbs)
		}
		// Use the value the policy engine returned, and refuse when it differs from
		// what this function composed. Discarding it let the two disagree about
		// WHICH file a path names: the engine cleaned "Mine.ttf " to "Mine.ttf"
		// and approved that, while the plan went on to write, own and record the
		// padded name -- so the uninstall deleted the user's unpadded file instead.
		// The engine refuses padding outright now, so no input reaches this branch
		// today and no test can cover it -- it is here so that a future change to
		// the normalizer fails loudly instead of reintroducing silent aliasing.
		if clean != devAbs {
			return nil, fmt.Errorf("zip entry %q normalizes to %q, which is not the path it names (%q)", name, clean, devAbs)
		}

		// 9. Duplicate destinations. A last-write-wins ambiguity in a tree the
		// user will run as root is not something to resolve silently.
		if prev, dup := seen[key(devAbs)]; dup {
			return nil, fmt.Errorf("zip maps two entries to %q", prev)
		}
		seen[key(devAbs)] = devAbs

		if isDir {
			if _, known := dirSet[key(devAbs)]; !known {
				dirSet[key(devAbs)] = devAbs
			}
			continue
		}

		size := int64(zf.UncompressedSize64)
		if zf.UncompressedSize64 > uint64(maxBytes) || size < 0 {
			return nil, fmt.Errorf("zip declares %d bytes of content, over the %d-byte limit", zf.UncompressedSize64, maxBytes)
		}
		declared += size
		if declared > maxBytes {
			return nil, fmt.Errorf("zip declares %d bytes of content, over the %d-byte limit", declared, maxBytes)
		}

		p.Entries = append(p.Entries, Entry{ZipIndex: i, Rel: rel, DevAbs: devAbs, Size: size})
		// Directories are collected from BOTH sources and both are
		// verified-necessary: KOReader's zip has zero explicit directory entries,
		// so every directory must be inferred from file paths; Plato's has 13,
		// one of which (dictionaries/) is EMPTY and is expected to exist.
		for d := path.Dir(devAbs); uninstall.PathUnder(d, absDest); d = path.Dir(d) {
			if _, known := dirSet[key(d)]; known {
				break
			}
			dirSet[key(d)] = d
		}
	}
	p.Bytes = declared

	// A path claimed as a regular file by one entry and needed as a DIRECTORY by
	// another is a contradiction the plan cannot express: `x` and `x/y` both
	// survived rule 9 (only entries were registered there; the parent of `x/y` is
	// INFERRED), so the plan held x in Entries and in Dirs at once. Apply then
	// wrote part of the tree and aborted with "rename: file exists" — the
	// half-extracted state this package's split exists to prevent — and the
	// manifest listed x twice, once as each. Fail at plan time instead.
	for _, e := range p.Entries {
		if d, isDir := dirSet[key(e.DevAbs)]; isDir {
			return nil, fmt.Errorf("zip maps %q to both a file and a directory", d)
		}
	}

	// Every inferred directory goes through rules 7-8 too.
	for _, d := range dirSet {
		if !uninstall.PathUnder(d, absDest) {
			return nil, fmt.Errorf("internal error: directory %q escapes dest %q", d, absDest)
		}
		clean, err := uninstall.InstallPath(d)
		if err != nil {
			return nil, fmt.Errorf("zip would create directory %q, which is not a writable location", d)
		}
		if clean != d {
			return nil, fmt.Errorf("directory %q normalizes to %q, which is not the path it names", d, clean)
		}
		p.Dirs = append(p.Dirs, d)
	}
	sortShallowestFirst(p.Dirs)

	if len(p.Entries) == 0 {
		return nil, fmt.Errorf("zip contains no files under root %q", root)
	}
	return p, nil
}

// footprint estimates the space this plan really consumes on a filesystem with
// bsize-byte allocation blocks: every file occupies whole blocks, and so does
// every directory.
//
// Bytes — the sum of the declared sizes — is what the free-space check used, and
// it is wrong by the whole of the per-file slack. /mnt/onboard is FAT32 with
// 32 KiB clusters on a Kobo-sized partition, so KOReader's 1044 entries can cost
// ~32 MiB more than they declare, and a tree of small files costs a multiple of
// its size. Checking the raw sum let a device with "enough" free space pass and
// then run the user partition to zero bytes mid-extraction, after which Nickel
// cannot write its database or save a reading position.
//
// A zero-length file is charged nothing, which is what FAT32 does (no cluster is
// allocated). A non-positive or implausible bsize degrades to the raw sum rather
// than to zero: an under-report here is the failure mode, so the fallback must
// never be smaller than what the old check would have used.
func (p *Plan) footprint(bsize int64) int64 {
	if bsize <= 0 || bsize > 1<<24 {
		return p.Bytes
	}
	total := int64(len(p.Dirs)) * bsize
	for _, e := range p.Entries {
		if e.Size <= 0 {
			continue
		}
		total += ((e.Size + bsize - 1) / bsize) * bsize
	}
	if total < p.Bytes {
		return p.Bytes // unreachable; a rounded sum is never smaller
	}
	return total
}

// Manifest renders the plan's members in the tar-style device-relative form
// state.json records and internal/uninstall consumes ("mnt/onboard/.adds/x/y",
// no leading slash) — the exact convention tgz.Verify produces. One convention,
// one code path, no translation layer (APP-TREE.md §5.2 step 6).
//
// Directories come first (shallowest-first) so a manifest reads top-down.
//
// Dest ITSELF is deliberately excluded. uninstall.Compute dedupes candidates by
// cleaned path with the manifest visited first, so a manifest member equal to
// dest would swallow the package's own `purge_paths = ["<dest>/**"]` entry — the
// recursive purge would be dropped as "already seen" and `--purge` would silently
// degrade to a plain uninstall, leaving every byte of user data behind while
// reporting success. Dest is still removed when it ends up empty: it is an
// ancestor of every deleted member, so computeRmdirs adds it to the deepest-first
// rmdir pass. Do not "complete" the manifest by adding it back.
func (p *Plan) Manifest() []string {
	out := make([]string, 0, len(p.Dirs)+len(p.Entries))
	for _, d := range p.Dirs {
		if d == p.Dest {
			continue
		}
		out = append(out, strings.TrimPrefix(d, "/"))
	}
	for _, e := range p.Entries {
		out = append(out, strings.TrimPrefix(e.DevAbs, "/"))
	}
	return out
}

// stripRoot applies the root prefix rule: with root == "" everything is kept
// as-is; otherwise an entry is kept only when it IS root (its own directory
// entry, which strips to "") or begins with root + "/". Everything else is
// outside the declared subtree.
func stripRoot(name, root string) (rel string, keep bool) {
	if root == "" {
		return name, true
	}
	trimmed := strings.TrimSuffix(name, "/")
	if trimmed == root {
		return "", true
	}
	if strings.HasPrefix(name, root+"/") {
		return name[len(root)+1:], true
	}
	return "", false
}

// hasDriveLetter reports whether name starts with a Windows drive specifier
// ("C:..."), which is absolute on the host kpm's tests run on.
func hasDriveLetter(name string) bool {
	if len(name) < 2 || name[1] != ':' {
		return false
	}
	c := name[0]
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// entryMode returns the entry's file mode with the unix type and setuid/setgid
// bits taken from the external attributes REGARDLESS of the creator version.
//
// archive/zip's FileHeader.Mode() reads those bits only when the creator claims
// Unix (CreatorVersion>>8 == 3), which a hostile archive controls. Reading them
// unconditionally can only ever add a rejection: a genuinely MSDOS-authored entry
// leaves the high 16 bits zero, so no type bit is set and nothing legitimate is
// refused.
func entryMode(zf *zip.File) os.FileMode {
	mode := zf.Mode()
	unix := zf.ExternalAttrs >> 16
	const (
		sIFMT   = 0xf000
		sIFLNK  = 0xa000
		sIFSOCK = 0xc000
		sIFBLK  = 0x6000
		sIFCHR  = 0x2000
		sIFIFO  = 0x1000
		sISUID  = 0x800
		sISGID  = 0x400
	)
	switch unix & sIFMT {
	case sIFLNK:
		mode |= os.ModeSymlink
	case sIFSOCK:
		mode |= os.ModeSocket
	case sIFBLK:
		mode |= os.ModeDevice
	case sIFCHR:
		mode |= os.ModeDevice | os.ModeCharDevice
	case sIFIFO:
		mode |= os.ModeNamedPipe
	}
	if unix&sISUID != 0 {
		mode |= os.ModeSetuid
	}
	if unix&sISGID != 0 {
		mode |= os.ModeSetgid
	}
	return mode
}

// sortShallowestFirst orders device paths parent-before-child, so MkdirAll never
// races a deeper path ahead of its parent and the order is deterministic.
func sortShallowestFirst(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		ci, cj := strings.Count(paths[i], "/"), strings.Count(paths[j], "/")
		if ci != cj {
			return ci < cj
		}
		return paths[i] < paths[j]
	})
}
