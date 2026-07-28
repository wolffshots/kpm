package apptree

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"kpm/internal/device"
	"kpm/internal/uninstall"
)

// freeSpaceSlack is the headroom required beyond the plan's footprint. A Kobo
// whose onboard partition is full of books is the realistic ENOSPC case, and a
// mid-extraction ENOSPC is much worse than a clean refusal.
const freeSpaceSlack = 16 << 20

// tempPrefix is the per-file staging name. It shares WriteFileAtomic's prefix so
// one sweep rule covers both, and mirrors cleanCache's .part discipline.
const tempPrefix = ".kpm-"

// availableBytes is statfsAvail behind a var so a test can pose a filesystem
// this host does not have — the free-space arithmetic is the one guard that is
// only reachable on linux (freespace_other.go returns ok=false), and a guard no
// test on the dev host can exercise is a guard that silently rots.
var availableBytes = statfsAvail

// Result is the outcome of applying a Plan.
type Result struct {
	Written  []string // device-absolute paths written (files only)
	Dirs     []string // directories created
	Manifest []string // tar-style members (no leading "/") for state.json
}

// Apply extracts a Plan. It writes ONLY paths the Plan already cleared, and
// re-validates every one of them immediately before the write that uses it —
// layer two of the 0.6.2 discipline, because the plan-time check could have been
// invalidated by anything running concurrently as root.
//
// Nothing is deleted here. A failure at any point leaves the previous version
// intact plus some new whole files, never a hole; the caller's stale-file prune
// runs afterwards, and only after state has been recorded (see the package doc).
func Apply(p device.Paths, plan *Plan, zipPath string) (Result, error) {
	var res Result
	if plan == nil {
		return res, fmt.Errorf("nil plan")
	}
	destHost := p.HostPath(plan.Dest)

	// 1. Free space, against the nearest EXISTING ancestor (dest may not exist).
	if avail, bsize, ok := availableBytes(nearestExisting(destHost)); ok {
		if need := plan.footprint(bsize) + freeSpaceSlack; avail < need {
			return res, fmt.Errorf("%s needs %d bytes free, %d available", plan.Dest, need, avail)
		}
	}

	// 2. Sweep temp files a previous crash left under dest, before writing any
	// new ones — the same hygiene cleanCache applies to .part downloads.
	sweepTemps(destHost)

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return res, fmt.Errorf("zip: %w", err)
	}
	defer zr.Close()

	// 3. Create directories, shallowest-first.
	for _, dir := range plan.Dirs {
		if err := checkWritable(p, dir, plan.Dest); err != nil {
			return res, err
		}
		if err := os.MkdirAll(p.HostPath(dir), 0o755); err != nil {
			return res, err
		}
		res.Dirs = append(res.Dirs, dir)
	}

	// 4. Extract each file. budget enforces maxBytes across the WHOLE extraction
	// against the real streams, not the zip's declared sizes: a thousand entries
	// each declaring one byte and delivering 256 MiB must not write 256 GB.
	budget := maxBytes
	touched := map[string]bool{}
	for _, e := range plan.Entries {
		if e.ZipIndex < 0 || e.ZipIndex >= len(zr.File) {
			return res, fmt.Errorf("internal error: plan references zip entry %d of %d", e.ZipIndex, len(zr.File))
		}
		// Re-validate immediately before the write (layer two).
		if err := checkWritable(p, e.DevAbs, plan.Dest); err != nil {
			sweepTemps(destHost)
			return res, err
		}
		host := p.HostPath(e.DevAbs)
		rc, err := zr.File[e.ZipIndex].Open()
		if err != nil {
			sweepTemps(destHost)
			return res, fmt.Errorf("zip entry %q: %w", e.Rel, err)
		}
		limited := &io.LimitedReader{R: rc, N: budget + 1}
		// Modes are FIXED at 0644/0755 and the zip's own exec bits are
		// deliberately discarded: /mnt/onboard is FAT32 mounted with a fixed mode,
		// so per-file permission bits do not exist on the target and binaries are
		// executable regardless. Honouring them would be a no-op on device and a
		// host/target divergence in tests. Do not "fix" this.
		werr := device.WriteReaderAtomic(host, limited, maxBytes)
		rc.Close()
		if werr == nil && limited.N <= 0 {
			werr = fmt.Errorf("zip content exceeds the %d-byte limit", maxBytes)
		}
		if werr != nil {
			sweepTemps(destHost)
			return res, fmt.Errorf("write %s: %w", e.DevAbs, werr)
		}
		budget -= (budget + 1) - limited.N
		if err := os.Chmod(host, 0o644); err != nil && !os.IsNotExist(err) {
			sweepTemps(destHost)
			return res, err
		}
		res.Written = append(res.Written, e.DevAbs)
		touched[filepath.Dir(host)] = true
	}

	// 5. One directory fsync per directory written into, so the renames are
	// durable on FAT32 without paying a sync per file.
	for dir := range touched {
		device.FsyncDir(dir)
	}

	res.Manifest = plan.Manifest()
	return res, nil
}

// checkWritable re-applies the two guards that matter at write time: the path
// policy (so a plan built from a hand-edited state or a racing edit still cannot
// escape), and the symlinked-parent refusal (so nothing redirects a write out of
// dest between planning and writing).
func checkWritable(p device.Paths, devAbs, dest string) error {
	if !uninstall.PathUnder(devAbs, dest) {
		return fmt.Errorf("refusing to write %s: outside %s", devAbs, dest)
	}
	if _, err := uninstall.InstallPath(devAbs); err != nil {
		return fmt.Errorf("refusing to write %s: not a writable location", devAbs)
	}
	if bad, ok := uninstall.SymlinkedInstallParent(p, devAbs); ok {
		return fmt.Errorf("refusing to write through symlinked parent %s", bad)
	}
	return nil
}

// nearestExisting walks up from host until a path that exists is found, so the
// free-space probe works before dest is created.
func nearestExisting(host string) string {
	for {
		if _, err := os.Stat(host); err == nil {
			return host
		}
		parent := filepath.Dir(host)
		if parent == host {
			return host
		}
		host = parent
	}
}

// sweepTemps removes every .kpm-*.tmp under root: leftovers from a crash mid-copy
// (or from this run's failed write). Best-effort — a temp file kpm cannot remove
// is inert clutter, not a reason to fail an install.
func sweepTemps(root string) {
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil //nolint:nilerr // best-effort sweep
		}
		name := filepath.Base(p)
		if strings.HasPrefix(name, tempPrefix) && strings.HasSuffix(name, ".tmp") {
			os.Remove(p)
		}
		return nil
	})
}
