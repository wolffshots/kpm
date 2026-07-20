package uninstall

import (
	"fmt"
	"path"
	"sort"

	"kpm/internal/config"
)

// Delete is one deletion action in a Plan.
type Delete struct {
	Path      string // cleaned absolute device path
	Recursive bool   // /** entry: remove the whole subtree (lstat walk)
	Purge     bool   // came from purge_paths (logged PURGE, gated by --purge)
}

// Skipped records a candidate that path policy or keep_paths excluded.
type Skipped struct {
	Path   string
	Reason string // "denylist" | "not-allowlisted" | "kept"
}

// Plan is the computed, ordered uninstall (UNINSTALL.md §5). It is produced by
// the pure Compute and applied by Execute.
type Plan struct {
	Method      string
	RunBefore   string
	Marker      string   // marker file to create (marker) or delete (marker-remove), "" if none
	Deletes     []Delete // files/subtrees to remove, in order
	Rmdirs      []string // empty-dir cleanup, deepest-first
	RunAfter    string
	Skipped     []Skipped
	NeedsReboot bool

	// keeps and allowExtra are threaded to Execute so keep_paths are honored
	// inside recursive deletes (C1) and symlinked-parent checks know the
	// per-package allowlist extension (C7). Not part of the printed plan.
	keeps      []keepSpec
	allowExtra []string
}

// keepSpec is a normalized keep_paths entry.
type keepSpec struct {
	base      string
	recursive bool
}

// Compute produces the uninstall Plan from a recorded manifest and the package's
// [uninstall] config. It is pure (no filesystem access) and fully unit-tested.
func Compute(manifest []string, cfg config.Uninstall, purge bool) (Plan, error) {
	if err := cfg.Validate(); err != nil {
		return Plan{}, err
	}
	method := cfg.EffectiveMethod()

	// allow_paths cannot override the hard denylist (§4).
	var allowExtra []string
	for _, raw := range cfg.AllowPaths {
		abs, err := cleanDeviceAbs(raw)
		if err != nil {
			return Plan{}, fmt.Errorf("allow_paths: %w", err)
		}
		if classify(abs, nil) == vDenied {
			return Plan{}, fmt.Errorf("allow_paths entry %q is on the hard denylist and cannot be allowed", raw)
		}
		allowExtra = append(allowExtra, abs)
	}

	// No-manifest fallback / refusal (manifest method only, §2).
	if method == config.MethodManifest && len(manifest) == 0 && len(cfg.ExtraPaths) == 0 {
		return Plan{}, fmt.Errorf("no manifest recorded and no [uninstall].extra_paths configured; " +
			"update this package through kpm once, or set [uninstall].extra_paths")
	}

	keeps, err := buildKeeps(cfg.KeepPaths)
	if err != nil {
		return Plan{}, err
	}

	plan := Plan{Method: method, NeedsReboot: cfg.RebootRequired()}
	plan.RunBefore = cfg.RunBefore
	plan.RunAfter = cfg.RunAfter
	plan.keeps = keeps
	plan.allowExtra = allowExtra

	if method == config.MethodMarker || method == config.MethodMarkerRemove {
		m, err := cleanDeviceAbs(cfg.MarkerFile)
		if err != nil {
			return Plan{}, fmt.Errorf("marker_file: %w", err)
		}
		// The marker path is subject to policy too — it must be allowlisted
		// (built-in or via allow_paths), never denied (C3; MARKER-REMOVE §2).
		if classify(m, allowExtra) != vAllowed {
			return Plan{}, fmt.Errorf("marker_file %q is not within a deletable/writable path", cfg.MarkerFile)
		}
		plan.Marker = m
	}

	// Ordered candidate list: manifest (manifest method only), then extras,
	// then purge set (only with --purge).
	type cand struct {
		raw       string
		recursive bool
		purge     bool
	}
	var cands []cand
	if method == config.MethodManifest {
		for _, m := range manifest {
			cands = append(cands, cand{raw: m})
		}
	}
	for _, e := range cfg.ExtraPaths {
		base, rec := splitRecursive(e)
		cands = append(cands, cand{raw: base, recursive: rec})
	}
	if purge {
		for _, e := range cfg.PurgePaths {
			base, rec := splitRecursive(e)
			cands = append(cands, cand{raw: base, recursive: rec, purge: true})
		}
	}

	seen := map[string]bool{}
	for _, c := range cands {
		abs, err := cleanDeviceAbs(c.raw)
		if err != nil {
			return Plan{}, err
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		if isKept(abs, keeps) {
			plan.Skipped = append(plan.Skipped, Skipped{abs, "kept"})
			continue
		}
		switch classify(abs, allowExtra) {
		case vDenied:
			plan.Skipped = append(plan.Skipped, Skipped{abs, "denylist"})
		case vNotAllowed:
			plan.Skipped = append(plan.Skipped, Skipped{abs, "not-allowlisted"})
		case vAllowed:
			plan.Deletes = append(plan.Deletes, Delete{Path: abs, Recursive: c.recursive, Purge: c.purge})
		}
	}

	plan.Rmdirs = computeRmdirs(plan.Deletes, keeps, allowExtra)
	return plan, nil
}

// computeRmdirs gathers directories to rmdir-if-empty: each non-recursive
// deletion target (it may itself be an empty shipped dir) plus every allowlisted
// ancestor of every deletion. Deepest-first so children clear before parents;
// shared dirs survive because rmdir refuses non-empty dirs at execution.
func computeRmdirs(deletes []Delete, keeps []keepSpec, allowExtra []string) []string {
	set := map[string]bool{}
	add := func(p string) {
		if set[p] || isKept(p, keeps) {
			return
		}
		// Never rmdir an allowlist root itself (a shared mount point like
		// /usr/local or /mnt/onboard/.adds), even if it happens to be empty (C4).
		if isAllowRoot(p, allowExtra) {
			return
		}
		if classify(p, allowExtra) == vAllowed {
			set[p] = true
		}
	}
	for _, d := range deletes {
		if !d.Recursive {
			add(d.Path)
		}
		for _, a := range ancestors(d.Path) {
			add(a)
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		ci, cj := components(out[i]), components(out[j])
		if ci != cj {
			return ci > cj // deepest first
		}
		return out[i] > out[j]
	})
	return out
}

// ancestors returns the parent directories of abs, closest first, stopping
// before the filesystem root.
func ancestors(abs string) []string {
	var out []string
	for {
		parent := path.Dir(abs)
		if parent == abs || parent == "/" || parent == "." {
			break
		}
		out = append(out, parent)
		abs = parent
	}
	return out
}

func buildKeeps(raw []string) ([]keepSpec, error) {
	var out []keepSpec
	for _, k := range raw {
		base, rec := splitRecursive(k)
		abs, err := cleanDeviceAbs(base)
		if err != nil {
			return nil, fmt.Errorf("keep_paths: %w", err)
		}
		out = append(out, keepSpec{base: abs, recursive: rec})
	}
	return out, nil
}

func isKept(abs string, keeps []keepSpec) bool {
	for _, k := range keeps {
		if pathEqual(abs, k.base) || (k.recursive && under(abs, k.base)) {
			return true
		}
	}
	return false
}

// isAllowRoot reports whether p is exactly a built-in allowlist root or an
// allow_paths entry (as opposed to something under one) — such roots are shared
// and must never be rmdir'd (C4).
func isAllowRoot(p string, allowExtra []string) bool {
	for _, a := range allowPrefixes {
		if pathEqual(p, a) {
			return true
		}
	}
	for _, a := range allowExtra {
		if pathEqual(p, a) {
			return true
		}
	}
	return false
}
