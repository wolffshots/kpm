package uninstall

import (
	"strings"
	"testing"

	"kpm/internal/config"
)

// deletePaths returns the plan's deletion targets as a set.
func deletePaths(p Plan) map[string]Delete {
	m := map[string]Delete{}
	for _, d := range p.Deletes {
		m[d.Path] = d
	}
	return m
}

// skipReason returns the skip reason recorded for path, or "".
func skipReason(p Plan, path string) string {
	for _, s := range p.Skipped {
		if s.Path == path {
			return s.Reason
		}
	}
	return ""
}

func TestComputeManifestUnionExtraMinusKeep(t *testing.T) {
	manifest := []string{
		"usr/local/NickelHardcover/lib.so",
		"mnt/onboard/.adds/NickelHardcover/data.txt",
	}
	cfg := config.Uninstall{
		ExtraPaths: []string{"/mnt/onboard/.adds/NickelHardcover/extra"},
		KeepPaths:  []string{"/mnt/onboard/.adds/NickelHardcover/data.txt"},
	}
	plan, err := Compute(manifest, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	dels := deletePaths(plan)
	if _, ok := dels["/usr/local/NickelHardcover/lib.so"]; !ok {
		t.Error("manifest file missing from deletes")
	}
	if _, ok := dels["/mnt/onboard/.adds/NickelHardcover/extra"]; !ok {
		t.Error("extra_paths entry missing from deletes")
	}
	if _, ok := dels["/mnt/onboard/.adds/NickelHardcover/data.txt"]; ok {
		t.Error("kept path must not be deleted")
	}
	if r := skipReason(plan, "/mnt/onboard/.adds/NickelHardcover/data.txt"); r != "kept" {
		t.Errorf("kept path reason = %q, want kept", r)
	}
}

func TestComputePurgeGating(t *testing.T) {
	cfg := config.Uninstall{
		ExtraPaths: []string{"/opt/pkg/bin"},
		PurgePaths: []string{"/mnt/onboard/.adds/pkg/config/**"},
	}
	// Without --purge, purge_paths are absent from deletes.
	plan, err := Compute([]string{"usr/local/pkg/f"}, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deletePaths(plan)["/mnt/onboard/.adds/pkg/config"]; ok {
		t.Error("purge path present without --purge")
	}
	// With --purge, it is deleted, recursively, flagged purge.
	plan, err = Compute([]string{"usr/local/pkg/f"}, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := deletePaths(plan)["/mnt/onboard/.adds/pkg/config"]
	if !ok {
		t.Fatal("purge path missing with --purge")
	}
	if !d.Recursive || !d.Purge {
		t.Errorf("purge path flags: recursive=%v purge=%v, want both true", d.Recursive, d.Purge)
	}
}

func TestComputeMarkerMethod(t *testing.T) {
	cfg := config.Uninstall{
		Method:     config.MethodMarker,
		MarkerFile: "/mnt/onboard/.adds/nm/uninstall",
		PurgePaths: []string{"/mnt/onboard/.adds/nm/pkgconfig"},
	}
	// Manifest is ignored for marker; marker set; needs_reboot defaults true.
	plan, err := Compute([]string{"usr/local/x/should-be-ignored"}, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Marker != "/mnt/onboard/.adds/nm/uninstall" {
		t.Errorf("marker = %q", plan.Marker)
	}
	if !plan.NeedsReboot {
		t.Error("marker method should default needs_reboot=true")
	}
	if _, ok := deletePaths(plan)["/usr/local/x/should-be-ignored"]; ok {
		t.Error("manifest must be ignored for marker method")
	}
	if _, ok := deletePaths(plan)["/mnt/onboard/.adds/nm/pkgconfig"]; !ok {
		t.Error("purge path should still apply for marker method")
	}
}

func TestComputeMarkerMissingFile(t *testing.T) {
	cfg := config.Uninstall{Method: config.MethodMarker}
	if _, err := Compute(nil, cfg, false); err == nil {
		t.Fatal("marker method without marker_file must error")
	}
}

func TestComputeNoManifestFallbackToExtra(t *testing.T) {
	cfg := config.Uninstall{ExtraPaths: []string{"/opt/legacy/bin"}}
	plan, err := Compute(nil, cfg, false)
	if err != nil {
		t.Fatalf("fallback to extra_paths should succeed: %v", err)
	}
	if _, ok := deletePaths(plan)["/opt/legacy/bin"]; !ok {
		t.Error("extra_paths fallback missing")
	}
}

func TestComputeNoManifestNoExtraRefuses(t *testing.T) {
	if _, err := Compute(nil, config.Uninstall{}, false); err == nil {
		t.Fatal("no manifest and no extra_paths must refuse")
	}
}

func TestComputeBadMethod(t *testing.T) {
	if _, err := Compute([]string{"usr/local/x"}, config.Uninstall{Method: "bogus"}, false); err == nil {
		t.Fatal("bad method must error")
	}
}

func TestComputeAllowPathsDenylisted(t *testing.T) {
	cfg := config.Uninstall{AllowPaths: []string{"/lib"}}
	if _, err := Compute([]string{"usr/local/x"}, cfg, false); err == nil {
		t.Fatal("denylisted allow_paths entry must error")
	}
}

// finding 1: allow_paths can no longer reach sensitive rootfs trees or the book
// library — each of these is refused at Compute.
func TestComputeAllowPathsSensitiveRejected(t *testing.T) {
	for _, ap := range []string{"/etc/passwd", "/etc", "/var", "/root", "/dev", "/mnt/onboard"} {
		cfg := config.Uninstall{AllowPaths: []string{ap}}
		if _, err := Compute([]string{"usr/local/x"}, cfg, false); err == nil {
			t.Errorf("allow_paths=[%q] must be rejected on the hard denylist", ap)
		}
	}
}

// A legitimate allow_paths entry (not on the denylist) still extends the
// allowlist so a path under it becomes deletable.
func TestComputeAllowPathsLegitExtends(t *testing.T) {
	cfg := config.Uninstall{
		AllowPaths: []string{"/srv/pkgdata"}, // not denied, not built-in allowlisted
		ExtraPaths: []string{"/srv/pkgdata/cache"},
	}
	plan, err := Compute(nil, cfg, false)
	if err != nil {
		t.Fatalf("legit allow_paths must be accepted: %v", err)
	}
	if _, ok := deletePaths(plan)["/srv/pkgdata/cache"]; !ok {
		t.Errorf("allow_paths should make /srv/pkgdata/cache deletable: %+v", plan.Deletes)
	}
	// The /etc/udev/rules.d carve-out is a valid target too.
	cfg = config.Uninstall{ExtraPaths: []string{"/etc/udev/rules.d/99-pkg.rules"}}
	plan, err = Compute(nil, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deletePaths(plan)["/etc/udev/rules.d/99-pkg.rules"]; !ok {
		t.Errorf("/etc/udev/rules.d entry should be deletable: %+v", plan.Deletes)
	}
}

// --- path policy ---

func TestClassifyDenylistBeatsAllowExtra(t *testing.T) {
	// Even with /bin listed as an extra allow prefix, it stays denied.
	if v := classify("/bin/sh", []string{"/bin"}); v != vDenied {
		t.Errorf("/bin/sh = %v, want vDenied", v)
	}
	for _, p := range []string{"/sbin/x", "/lib/y", "/drivers/z", "/etc/init.d/kpm", "/etc/inittab", "/"} {
		if v := classify(p, nil); v != vDenied {
			t.Errorf("classify(%q) = %v, want vDenied", p, v)
		}
	}
}

func TestClassifyKoboImageformatsException(t *testing.T) {
	if v := classify("/usr/local/Kobo/imageformats/libnm.so", nil); v != vAllowed {
		t.Errorf("imageformats libnm = %v, want vAllowed", v)
	}
	if v := classify("/usr/local/Kobo/libnickel.so", nil); v != vDenied {
		t.Errorf("Kobo lib = %v, want vDenied", v)
	}
}

func TestClassifyKpmSelfDenied(t *testing.T) {
	for _, p := range []string{
		"/mnt/onboard/.adds/kpm/bin/kpm",
		"/mnt/onboard/.adds/kpm/state.json",
		"/mnt/onboard/.adds/kpm/kpm.log",
	} {
		if v := classify(p, nil); v != vDenied {
			t.Errorf("classify(%q) = %v, want vDenied", p, v)
		}
	}
}

func TestClassifyAllowlistAndUnlisted(t *testing.T) {
	for _, p := range []string{
		"/mnt/onboard/.adds/x", "/mnt/onboard/.kobo/y", "/usr/local/z",
		"/usr/bin/tool", "/usr/lib/lib.so", "/opt/pkg", "/etc/udev/rules.d/99.rules", "/etc/dbus-1/x",
	} {
		if v := classify(p, nil); v != vAllowed {
			t.Errorf("classify(%q) = %v, want vAllowed", p, v)
		}
	}
	// Outside any allow/deny root: skipped with a WARN, not denied.
	for _, p := range []string{"/home/user/x", "/data/x", "/mnt/sd/x"} {
		if v := classify(p, nil); v != vNotAllowed {
			t.Errorf("classify(%q) = %v, want vNotAllowed", p, v)
		}
	}
}

// The hardened denylist protects sensitive rootfs trees and the book library
// even against allow_paths (finding 1).
func TestClassifyHardenedDenylist(t *testing.T) {
	for _, p := range []string{
		"/etc/passwd", "/etc/shadow", "/etc/fstab", "/etc/hosts", "/etc/inittab", "/etc/init.d/kpm",
		"/var", "/var/log/x", "/root", "/root/.ssh/id", "/dev", "/dev/null",
		"/proc/1/mem", "/sys/class/x",
		"/mnt/onboard", "/mnt/onboard/book.epub", "/mnt/onboard/Books/x", "/mnt/onboard/.kobo-x/y",
	} {
		if v := classify(p, nil); v != vDenied {
			t.Errorf("classify(%q) = %v, want vDenied", p, v)
		}
		// allow_paths must not be able to override the hard denylist.
		if v := classify(p, []string{p}); v != vDenied {
			t.Errorf("classify(%q, allowExtra=self) = %v, want vDenied", p, v)
		}
	}
	// The /etc carve-outs and onboard allow-subtrees still classify as allowed.
	for _, p := range []string{
		"/etc/udev/rules.d/99.rules", "/etc/dbus-1/system.d/x",
		"/mnt/onboard/.adds/pkg/f", "/mnt/onboard/.kobo/pkg/f",
	} {
		if v := classify(p, nil); v != vAllowed {
			t.Errorf("classify(%q) = %v, want vAllowed", p, v)
		}
	}
}

func TestComputeSkipsUnlistedManifestPath(t *testing.T) {
	plan, err := Compute([]string{"data/blob", "usr/local/pkg/f"}, config.Uninstall{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deletePaths(plan)["/usr/local/pkg/f"]; !ok {
		t.Error("allowlisted manifest file should be deleted")
	}
	if r := skipReason(plan, "/data/blob"); r != "not-allowlisted" {
		t.Errorf("/data/blob reason = %q, want not-allowlisted", r)
	}
}

// A denylisted manifest path is a plan-time skip (reason "denylist"), never a
// deletion — but it does not abort Compute.
func TestComputeSkipsDenylistedManifestPath(t *testing.T) {
	plan, err := Compute([]string{"etc/passwd", "usr/local/pkg/f"}, config.Uninstall{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deletePaths(plan)["/etc/passwd"]; ok {
		t.Error("/etc/passwd must never be a deletion target")
	}
	if r := skipReason(plan, "/etc/passwd"); r != "denylist" {
		t.Errorf("/etc/passwd reason = %q, want denylist", r)
	}
}

func TestCleanDeviceAbsRejectsTraversal(t *testing.T) {
	for _, raw := range []string{"usr/../etc", "/opt/../../x", "../x", "usr/local/../../.."} {
		if _, err := cleanDeviceAbs(raw); err == nil {
			t.Errorf("cleanDeviceAbs(%q) should reject traversal", raw)
		}
	}
	got, err := cleanDeviceAbs("usr/local/x")
	if err != nil || got != "/usr/local/x" {
		t.Errorf("cleanDeviceAbs(usr/local/x) = %q, %v", got, err)
	}
}

func TestSplitRecursive(t *testing.T) {
	if b, r := splitRecursive("/opt/foo/**"); b != "/opt/foo" || !r {
		t.Errorf("splitRecursive(/opt/foo/**) = %q,%v", b, r)
	}
	if b, r := splitRecursive("/opt/foo"); b != "/opt/foo" || r {
		t.Errorf("splitRecursive(/opt/foo) = %q,%v", b, r)
	}
}

// C2: FAT32 onboard paths compare case-insensitively; rootfs stays sensitive.
func TestClassifyOnboardCaseInsensitive(t *testing.T) {
	// Self-deny survives an uppercased BIN component.
	if v := classify("/mnt/onboard/.adds/kpm/BIN/kpm", nil); v != vDenied {
		t.Errorf("uppercase BIN self-deny = %v, want vDenied", v)
	}
	// An uppercased onboard root still classifies like lowercase (allowed).
	if v := classify("/MNT/ONBOARD/.adds/foo", nil); v != vAllowed {
		t.Errorf("uppercase onboard = %v, want vAllowed", v)
	}
	// Off-onboard (ext4) stays case-sensitive: /usr/local/KOBO != /usr/local/Kobo,
	// so it is NOT denied (it's just an ordinary /usr/local path).
	if v := classify("/usr/local/KOBO/x", nil); v != vAllowed {
		t.Errorf("case-sensitive off-onboard /usr/local/KOBO = %v, want vAllowed", v)
	}
	if v := classify("/usr/local/Kobo/x", nil); v != vDenied {
		t.Errorf("exact-case /usr/local/Kobo = %v, want vDenied", v)
	}
}

// C3: the marker path is policy-checked; a denied marker is a Compute error.
func TestComputeMarkerDeniedPathErrors(t *testing.T) {
	cfg := config.Uninstall{Method: config.MethodMarker, MarkerFile: "/etc/init.d/evil"}
	if _, err := Compute(nil, cfg, false); err == nil {
		t.Fatal("a marker on a denied path must be a config error")
	}
	// A marker outside any allowlist is also refused.
	cfg = config.Uninstall{Method: config.MethodMarker, MarkerFile: "/var/tmp/x"}
	if _, err := Compute(nil, cfg, false); err == nil {
		t.Fatal("a marker outside the allowlist must be a config error")
	}
}

// C4: an allowlist root is never emitted as an rmdir target.
func TestComputeRmdirsExcludesAllowRoots(t *testing.T) {
	plan, err := Compute([]string{"usr/local/pkg/f"}, config.Uninstall{}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range plan.Rmdirs {
		if d == "/usr/local" {
			t.Errorf("allowlist root /usr/local must not be an rmdir target: %v", plan.Rmdirs)
		}
	}
	// The package's own dir is still a candidate.
	found := false
	for _, d := range plan.Rmdirs {
		if d == "/usr/local/pkg" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected /usr/local/pkg in rmdirs: %v", plan.Rmdirs)
	}
}

// C6: "/**/" (trailing slash) is recursive; a bare "**" component is rejected.
func TestSplitRecursiveTrailingSlashAndBareStars(t *testing.T) {
	if b, r := splitRecursive("/opt/foo/**/"); b != "/opt/foo" || !r {
		t.Errorf("splitRecursive(/opt/foo/**/) = %q,%v", b, r)
	}
	cfg := config.Uninstall{ExtraPaths: []string{"/opt/foo/**/"}}
	plan, err := Compute(nil, cfg, false)
	if err != nil {
		t.Fatalf("trailing-slash recursive should parse: %v", err)
	}
	d, ok := deletePaths(plan)["/opt/foo"]
	if !ok || !d.Recursive {
		t.Errorf("/opt/foo should be a recursive delete: %+v", plan.Deletes)
	}
	// A bare ** component (not the /** suffix) is a config error.
	if _, err := cleanDeviceAbs("/opt/**/x"); err == nil {
		t.Error("a mid-path ** component must be rejected")
	}
}

func TestComputeRmdirsDeepestFirst(t *testing.T) {
	plan, err := Compute([]string{"usr/local/Foo/sub/f", "usr/local/Foo/sub/g"}, config.Uninstall{}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Rmdirs must be ordered deepest-first.
	prev := 1 << 30
	for _, d := range plan.Rmdirs {
		c := strings.Count(d, "/")
		if c > prev {
			t.Errorf("rmdirs not deepest-first: %v", plan.Rmdirs)
			break
		}
		prev = c
	}
	// The deepest shared dir must be a candidate for cleanup.
	found := false
	for _, d := range plan.Rmdirs {
		if d == "/usr/local/Foo/sub" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected /usr/local/Foo/sub in rmdirs: %v", plan.Rmdirs)
	}
}
