package uninstall

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/version"
)

// FailedPath records a deletion that errored.
type FailedPath struct {
	Path string
	Err  error
}

// Result is the outcome of applying a Plan.
type Result struct {
	Deleted     []string  // software artifacts removed
	Purged      []string  // user-data paths removed (--purge)
	Removed     []string  // empty directories rmdir'd
	AlreadyGone []string  // targets that were already absent (not an error)
	Kept        []string  // paths preserved by keep_paths inside a recursive delete (C1)
	Skipped     []Skipped // execution-time skips (e.g. symlinked parent, C7)
	Failed      []FailedPath
	Marker      string // marker file created (marker) or deleted (marker-remove), "" if none
}

// OK reports whether the plan applied cleanly: no deletion failures AND no
// execution-time safety skips. An execution-time skip (the symlinked-parent
// case, C7) leaves a real file on disk with no other way to reach it, so it must
// block clean unregistration just like a failure. Plan-time policy skips
// (denylist / not-allowlisted) are the user's accepted policy and live on the
// Plan, not the Result — they do not affect OK().
func (r Result) OK() bool { return len(r.Failed) == 0 && len(r.Skipped) == 0 }

// Execute applies plan's marker creation (or deletion, for marker-remove),
// file deletions, and empty-directory cleanup through the device layer's
// HostPath mapping. run_before/run_after are
// NOT run here (the CLI runs them, honoring --force). Missing files are fine.
func Execute(p device.Paths, plan Plan) Result {
	var r Result

	if plan.Marker != "" {
		if bad, ok := symlinkedParent(p, plan.Marker, plan.allowExtra); ok {
			// Never create or delete a marker through a symlinked parent, which
			// would resolve outside the allowlisted tree (C7/M2).
			r.Failed = append(r.Failed, FailedPath{plan.Marker, fmt.Errorf("symlinked parent %s", bad)})
		} else if plan.Method == config.MethodMarkerRemove {
			// marker-remove: DELETE the shipped trigger file; its absence makes the
			// package remove itself on the next boot (MARKER-REMOVE §2).
			host := p.HostPath(plan.Marker)
			if info, err := os.Lstat(host); err == nil {
				// A directory in the marker's place is an error, mirroring the
				// marker method's dir-in-the-way rule (MARKER-REMOVE §2).
				if info.IsDir() {
					r.Failed = append(r.Failed, FailedPath{plan.Marker, fmt.Errorf("marker path exists as a directory")})
				} else if err := os.Remove(host); err != nil {
					r.Failed = append(r.Failed, FailedPath{plan.Marker, err})
				} else {
					r.Marker = plan.Marker
				}
			} else if os.IsNotExist(err) {
				// Idempotent: already absent — the package is already uninstalling
				// or was removed manually (MARKER-REMOVE §2).
				r.AlreadyGone = append(r.AlreadyGone, plan.Marker)
			} else {
				r.Failed = append(r.Failed, FailedPath{plan.Marker, err})
			}
		} else {
			host := p.HostPath(plan.Marker)
			if info, err := os.Lstat(host); err == nil {
				// Idempotent: an existing marker is left untouched (never truncated),
				// but a directory in its place is an error (C3).
				if info.IsDir() {
					r.Failed = append(r.Failed, FailedPath{plan.Marker, fmt.Errorf("marker path exists as a directory")})
				} else {
					r.Marker = plan.Marker // already present
				}
			} else if !os.IsNotExist(err) {
				r.Failed = append(r.Failed, FailedPath{plan.Marker, err})
			} else if err := os.MkdirAll(filepath.Dir(host), 0o755); err != nil {
				r.Failed = append(r.Failed, FailedPath{plan.Marker, err})
			} else {
				content := fmt.Sprintf("uninstall requested by kpm %s %s\n",
					version.Version, time.Now().UTC().Format(time.RFC3339))
				if err := os.WriteFile(host, []byte(content), 0o644); err != nil {
					r.Failed = append(r.Failed, FailedPath{plan.Marker, err})
				} else {
					r.Marker = plan.Marker
				}
			}
		}
	}

	kept := func(dev string) bool { return isKept(dev, plan.keeps) }

	for _, d := range plan.Deletes {
		// Never delete through a symlinked parent directory (C7).
		if bad, ok := symlinkedParent(p, d.Path, plan.allowExtra); ok {
			r.Skipped = append(r.Skipped, Skipped{d.Path, "symlinked parent " + bad})
			continue
		}
		host := p.HostPath(d.Path)
		info, err := os.Lstat(host)
		if err != nil {
			if os.IsNotExist(err) {
				r.AlreadyGone = append(r.AlreadyGone, d.Path)
				continue
			}
			r.Failed = append(r.Failed, FailedPath{d.Path, err})
			continue
		}
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			if d.Recursive {
				survived, err := removeTree(host, d.Path, kept, plan.allowExtra, &r)
				if err != nil {
					r.Failed = append(r.Failed, FailedPath{d.Path, err})
					continue
				}
				if survived {
					// A descendant was preserved (keep_paths or policy), so the
					// directory still exists — do not report it as deleted (#8).
					continue
				}
			} else {
				// A plain (non-recursive) directory target is left to the
				// rmdir-if-empty pass so shared dirs survive.
				continue
			}
		} else {
			// Regular file or symlink: remove the entry itself (never follow).
			if err := os.Remove(host); err != nil {
				r.Failed = append(r.Failed, FailedPath{d.Path, err})
				continue
			}
		}
		if d.Purge {
			r.Purged = append(r.Purged, d.Path)
		} else {
			r.Deleted = append(r.Deleted, d.Path)
		}
	}

	// Deepest-first empty-directory cleanup.
	for _, dir := range plan.Rmdirs {
		// Never rmdir through a symlinked parent: os.Lstat/os.Remove resolve
		// intermediate symlinks, so this would remove a directory outside the
		// package tree (C7/M2).
		if _, ok := symlinkedParent(p, dir, plan.allowExtra); ok {
			continue
		}
		host := p.HostPath(dir)
		info, err := os.Lstat(host)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if err := os.Remove(host); err == nil {
			r.Removed = append(r.Removed, dir)
		}
	}

	return r
}

// removeTree deletes root and everything under it using lstat at every step so
// it never follows a symlink out of the subtree: a symlink (even to a dir) is
// unlinked, not descended into. A node matching keep_paths is preserved and
// recorded (C1); returned survived=true so ancestors are not rmdir'd. dev is the
// device path parallel to the host path root. classify is re-applied at every
// node so a recursive delete can never escape into a denied path (kpm's own
// files, firmware trees) even when it entered from an allowed ancestor (C1).
func removeTree(host, dev string, kept func(string) bool, allowExtra []string, r *Result) (survived bool, err error) {
	info, err := os.Lstat(host)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if kept(dev) {
		r.Kept = append(r.Kept, dev)
		return true, nil
	}
	if classify(dev, allowExtra) != vAllowed {
		// An AppleDouble sidecar (._foo the FAT partition collects from Finder, or
		// any dotfile) is inert metadata, never real package payload: removing it
		// with the tree it sits in is safe and must never record a policy skip that
		// blocks a clean directory removal (SELF-SOURCE §3a / B). Every other
		// denied/unlisted descendant is preserved and recorded as a safety skip so
		// it blocks clean unregistration (C1).
		if config.IsSidecar(filepath.Base(host)) {
			return false, os.Remove(host)
		}
		r.Skipped = append(r.Skipped, Skipped{dev, "policy"})
		return true, nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, os.Remove(host) // unlink the symlink itself
	}
	if info.IsDir() {
		entries, err := os.ReadDir(host)
		if err != nil {
			return false, err
		}
		anyKept := false
		for _, e := range entries {
			s, err := removeTree(filepath.Join(host, e.Name()), dev+"/"+e.Name(), kept, allowExtra, r)
			if err != nil {
				return false, err
			}
			if s {
				anyKept = true
			}
		}
		if anyKept {
			return true, nil // survivors remain; don't remove this dir
		}
		return false, os.Remove(host)
	}
	return false, os.Remove(host)
}

// symlinkedParent reports the deepest intermediate directory (under the
// allowlisted prefix, excluding the leaf) that is a symlink, if any (C7).
func symlinkedParent(p device.Paths, dev string, allowExtra []string) (string, bool) {
	prefix := longestAllowPrefix(dev, allowExtra)
	if prefix == "" {
		return "", false
	}
	rel := strings.Trim(dev[len(prefix):], "/")
	if rel == "" {
		return "", false
	}
	segs := strings.Split(rel, "/")
	cur := prefix
	for i := 0; i < len(segs)-1; i++ { // parents only, not the leaf
		cur = cur + "/" + segs[i]
		if info, err := os.Lstat(p.HostPath(cur)); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return cur, true
		}
	}
	return "", false
}

// longestAllowPrefix returns the longest allowlist prefix (built-in or
// per-package) that dev is under, or "" if none.
func longestAllowPrefix(dev string, allowExtra []string) string {
	best := ""
	consider := func(a string) {
		if under(dev, a) && len(a) > len(best) {
			best = a
		}
	}
	for _, a := range allowPrefixes {
		consider(a)
	}
	for _, a := range allowExtra {
		consider(a)
	}
	return best
}
