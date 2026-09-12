package uninstall

// content_test.go pins the content-dir exemption (fonts plan §3.1). This is the
// first time the onboard guard has widened since it was hardened, so every case
// here asks the same question: does the book library stay denied? The answer must
// survive FAT32 case folding and prefix confusion, because a Kobo's user
// partition is FAT32 and "fonts.bak" is a thing users create.

import (
	"strings"
	"testing"

	"kpm/internal/config"
)

// TestClassifyContentDir is the table in §3.1, stated as code. The left column is
// what 0.11.0 answered; every "want" here is what 0.12.0 must answer.
func TestClassifyContentDir(t *testing.T) {
	cases := []struct {
		path string
		want verdict
		why  string
	}{
		{"/mnt/onboard/fonts/X.ttf", vAllowed, "a font kpm installed must be deletable"},
		{"/mnt/onboard/fonts", vAllowed, "the dir itself is allowed (but is an allow root)"},
		// FAT32 folds case (C2): these are the SAME file on the device.
		{"/mnt/onboard/Fonts/x.TTF", vAllowed, "FAT32 case fold"},
		{"/mnt/onboard/FONTS", vAllowed, "FAT32 case fold on the dir itself"},
		// Prefix confusion: a sibling whose name merely starts with "fonts".
		{"/mnt/onboard/fontsx/a", vDenied, "fontsx is not fonts"},
		{"/mnt/onboard/fonts.bak/a", vDenied, "fonts.bak is a user backup, not a content dir"},
		{"/mnt/onboard/fontsx", vDenied, "fontsx is not fonts"},
		// The library and the partition root are untouched by any of this.
		{"/mnt/onboard/book.epub", vDenied, "the book library stays denied"},
		{"/mnt/onboard/Books/x", vDenied, "the book library stays denied"},
		{"/mnt/onboard", vDenied, "the partition root stays denied"},
		{"/mnt/onboard/.kobo/custom-dict", vAllowed, "unchanged: already under .kobo"},
	}
	for _, c := range cases {
		if got := classify(c.path, nil); got != c.want {
			t.Errorf("classify(%q) = %v, want %v (%s)", c.path, got, c.want, c.why)
		}
		// allow_paths still cannot override a denial.
		if c.want == vDenied {
			if got := classify(c.path, []string{c.path}); got != vDenied {
				t.Errorf("classify(%q, allowExtra=self) = %v, want vDenied — allow_paths must not open the library", c.path, got)
			}
		}
	}
}

// The content dir is a SHARED root: many packages plus the user's own files live
// in it, so it is never rmdir'd and never recursively deleted.
func TestContentDirIsAllowRoot(t *testing.T) {
	if !isAllowRoot("/mnt/onboard/fonts", nil) {
		t.Error("a content dir must be an allow root")
	}
	if !isAllowRoot("/mnt/onboard/FONTS", nil) {
		t.Error("isAllowRoot must fold case on the FAT32 partition")
	}
	if isAllowRoot("/mnt/onboard/fonts/x.ttf", nil) {
		t.Error("a FILE in a content dir is not a root")
	}
}

// A def that declares a recursive purge of the content dir is refused at plan
// time with the shared-root error. Before this change the entry classified
// vDenied and was silently skipped; once the dir is allowlisted, only isAllowRoot
// stands between `fonts/**` and every font on the device, the user's included.
func TestComputeRefusesRecursivePurgeOfContentDir(t *testing.T) {
	_, err := Compute(nil, config.Uninstall{
		Method:     config.MethodManifest,
		ExtraPaths: []string{"/mnt/onboard/fonts/mine.ttf"},
		PurgePaths: []string{"/mnt/onboard/fonts/**"},
	}, true)
	if err == nil {
		t.Fatal("a recursive delete of a content dir must be refused")
	}
	if !strings.Contains(err.Error(), "shared root") {
		t.Errorf("error = %q, want the shared-root refusal", err)
	}
}

// The rmdir pass must never list a content dir: it is an ancestor of every font
// this package deletes, and removing it would take the user's fonts' home with
// it. (rmdir refuses a non-empty dir at execution, but a package whose fonts were
// the only ones there would otherwise delete the directory Nickel reads.)
func TestComputeNeverRmdirsContentDir(t *testing.T) {
	plan, err := Compute([]string{"mnt/onboard/fonts/A.ttf", "mnt/onboard/fonts/B.ttf"},
		config.Uninstall{Method: config.MethodManifest}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Deletes) != 2 {
		t.Fatalf("both fonts must be deletable now, got %+v (skipped %+v)", plan.Deletes, plan.Skipped)
	}
	for _, d := range plan.Rmdirs {
		if pathEqual(d, "/mnt/onboard/fonts") {
			t.Errorf("the rmdir pass listed the content dir: %v", plan.Rmdirs)
		}
	}
}

func TestInstallDestAcceptsContentDir(t *testing.T) {
	if _, err := InstallDest("/mnt/onboard/fonts"); err != nil {
		t.Errorf("InstallDest(fonts) = %v, want nil — a content dir is the one shared root that is a valid dest", err)
	}
	// The refusal it is an exception to still holds for every other shared root.
	for _, raw := range []string{"/mnt/onboard/.adds", "/mnt/onboard", "/usr/local", "/mnt/onboard/.adds/nm"} {
		if _, err := InstallDest(raw); err == nil {
			t.Errorf("InstallDest(%q) must stay refused", raw)
		}
	}
	// ...and so does the library denial.
	if _, err := InstallDest("/mnt/onboard/Books"); err == nil {
		t.Error("InstallDest must never accept the book library")
	}
}

func TestContentDirFor(t *testing.T) {
	font := &config.Install{Method: config.MethodAppTree, Dest: "/mnt/onboard/fonts"}
	c, ok := ContentDirFor(font)
	if !ok || c.Kind != "font" {
		t.Fatalf("ContentDirFor(fonts) = %+v, %v; want the font row", c, ok)
	}
	// Case folds, because the device does.
	if _, ok := ContentDirFor(&config.Install{Method: config.MethodAppTree, Dest: "/mnt/onboard/Fonts"}); !ok {
		t.Error("ContentDirFor must fold case on the FAT32 partition")
	}
	for name, i := range map[string]*config.Install{
		"nil block":      nil,
		"tgz def":        {Dest: "/mnt/onboard/fonts"},
		"app mod":        {Method: config.MethodAppTree, Dest: "/mnt/onboard/.adds/koreader"},
		"below a row":    {Method: config.MethodAppTree, Dest: "/mnt/onboard/fonts/sub"},
		"prefix sibling": {Method: config.MethodAppTree, Dest: "/mnt/onboard/fontsx"},
	} {
		if _, ok := ContentDirFor(i); ok {
			t.Errorf("ContentDirFor(%s) reported a content dir", name)
		}
	}
}

func TestIsContentDir(t *testing.T) {
	if !IsContentDir("/mnt/onboard/fonts") {
		t.Error("IsContentDir(fonts) must be true")
	}
	for _, p := range []string{"/mnt/onboard/fonts/x.ttf", "/mnt/onboard/fontsx", "/mnt/onboard/.adds/koreader"} {
		if IsContentDir(p) {
			t.Errorf("IsContentDir(%q) must be false", p)
		}
	}
}

// ---- wave 2 review fixes ------------------------------------------------

// A padded path is REFUSED, not trimmed. Trimming aliased two device paths onto
// one: the writer used the padded name and the deleter the trimmed one, so an
// uninstall removed a file the package never installed.
func TestCleanDeviceAbsRefusesPadding(t *testing.T) {
	for _, raw := range []string{
		"/mnt/onboard/fonts/Mine.ttf ",
		"/mnt/onboard/fonts/Mine.ttf\u00a0", // no-break space: legal on vfat
		" /mnt/onboard/fonts/Mine.ttf",
		"mnt/onboard/fonts/Mine.ttf\t",
		"/mnt/onboard/fonts/Mine.ttf\n",
	} {
		if got, err := cleanDeviceAbs(raw); err == nil {
			t.Errorf("cleanDeviceAbs(%q) = %q, want a refusal: trimming aliases it onto another file", raw, got)
		}
	}
	// A space INSIDE the name is a normal filename and must still work.
	if _, err := cleanDeviceAbs("/mnt/onboard/fonts/My Font.ttf"); err != nil {
		t.Errorf("an interior space is a legal filename: %v", err)
	}
}

// A padded manifest member classifies malformed and is skipped, so the trimmed
// neighbour — the user's own file — is never deleted.
func TestComputeSkipsPaddedManifestMember(t *testing.T) {
	plan, err := Compute([]string{"mnt/onboard/fonts/Mine.ttf\u00a0"},
		config.Uninstall{Method: config.MethodManifest}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range plan.Deletes {
		t.Errorf("a padded member must never become a deletion: %q", d.Path)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].Reason != "malformed" {
		t.Errorf("Skipped = %+v, want one malformed entry", plan.Skipped)
	}
}

// CRITICAL 2: a dest UNDER a content dir escaped every content guard — no flat
// rule, no ownership check, no reboot, kind "mod". 0.11.0 refused it.
func TestInstallDestRefusesInsideContentDir(t *testing.T) {
	for _, raw := range []string{
		"/mnt/onboard/fonts/MyFonts",
		"/mnt/onboard/fonts/a/b",
		"/mnt/onboard/FONTS/MyFonts", // FAT32 fold
	} {
		if _, err := InstallDest(raw); err == nil {
			t.Errorf("InstallDest(%q) must be refused: it is inside a content dir", raw)
		}
	}
	// The dir itself stays the one valid content dest.
	if _, err := InstallDest("/mnt/onboard/fonts"); err != nil {
		t.Errorf("InstallDest(fonts) = %v, want nil", err)
	}
}

// MAJOR 3: a recipe that names the content dir itself must fail at PARSE, not at
// removal — a Compute-time refusal fails the whole uninstall and leaves the
// package unremovable. purge_paths = "<dest>/**" is the shape every app-tree def
// uses, so this is what a curator copying the koreader entry writes.
func TestValidateUninstallDeclRejectsContentDir(t *testing.T) {
	bad := []config.Uninstall{
		{PurgePaths: []string{"/mnt/onboard/fonts/**"}},
		{ExtraPaths: []string{"/mnt/onboard/fonts/**"}},
		{ExtraPaths: []string{"/mnt/onboard/fonts"}},
		{PurgePaths: []string{"/mnt/onboard/FONTS/**"}},
		{Method: config.MethodMarker, MarkerFile: "/mnt/onboard/fonts/uninstall"},
	}
	for _, u := range bad {
		if err := ValidateUninstallDecl(u); err == nil {
			t.Errorf("ValidateUninstallDecl(%+v) must be refused", u)
		}
	}
	// A single FILE in a content dir stays legal (3.1), and so does everything
	// outside one.
	ok := []config.Uninstall{
		{ExtraPaths: []string{"/mnt/onboard/fonts/A.ttf"}},
		{PurgePaths: []string{"/mnt/onboard/.adds/koreader/**"}},
		{Method: config.MethodMarker, MarkerFile: "/mnt/onboard/.adds/nm/uninstall"},
		{},
	}
	for _, u := range ok {
		if err := ValidateUninstallDecl(u); err != nil {
			t.Errorf("ValidateUninstallDecl(%+v) = %v, want nil", u, err)
		}
	}
}

// A config file cannot live in a content dir: it holds content, nothing else.
// Before the content-dir work this path was denied outright.
func TestConfigPathRejectsContentDir(t *testing.T) {
	if _, err := ConfigPath("/mnt/onboard/fonts/settings.ini"); err == nil {
		t.Error("a config path inside a content dir must be refused")
	}
	if _, err := ConfigPath("/mnt/onboard/.adds/koreader/settings.ini"); err != nil {
		t.Errorf("an ordinary config path must still load: %v", err)
	}
}

// MINOR 9: FAT folds ASCII. Unicode simple folding reads U+017F as "s" and U+212A
// as "k", which made two names the device keeps apart compare equal — and turned
// a directory at the partition root into a writable content dir.
func TestPathFoldingIsASCIIOnly(t *testing.T) {
	for _, p := range []string{
		"/mnt/onboard/font\u017f",   // long s
		"/mnt/onboard/font\u017f/x", // and under it
		"/mnt/onboard/.\u212Aobo/x", // Kelvin sign: NOT .kobo
		"/mnt/onboard/.\u212Aobo",
	} {
		if v := classify(p, nil); v != vDenied {
			t.Errorf("classify(%q) = %v, want vDenied — only ASCII folds on FAT", p, v)
		}
	}
	if IsContentDir("/mnt/onboard/font\u017f") {
		t.Error("a long-s name is not the fonts content dir")
	}
	// foldPath is the map-key form of pathEqual, so the two must agree: a key that
	// folded U+212A to "k" merged two paths pathEqual now reports different.
	if foldPath("/mnt/onboard/\u212Aobo") == foldPath("/mnt/onboard/kobo") {
		t.Error("foldPath must fold ASCII only, like pathEqual")
	}
	if foldPath("/mnt/onboard/FONTS") != foldPath("/mnt/onboard/fonts") {
		t.Error("foldPath must still fold ASCII case")
	}
	// ASCII folding itself is untouched.
	if !pathEqual("/mnt/onboard/FONTS", "/mnt/onboard/fonts") {
		t.Error("ASCII case must still fold on the onboard partition")
	}
	if classify("/mnt/onboard/.KOBO/x", nil) != vAllowed {
		t.Error("an ASCII-uppercase .kobo must still be allowed")
	}
}

// MINOR 10: the symlinked-parent guard resolves the longest allowlist prefix. A
// content dir is allowlisted by classify but is not in allowPrefixes, so without
// it in the candidate list the guard found no prefix and never ran.
func TestLongestAllowPrefixKnowsContentDirs(t *testing.T) {
	if got := longestAllowPrefix("/mnt/onboard/fonts/A.ttf", nil); got != "/mnt/onboard/fonts" {
		t.Errorf("longestAllowPrefix = %q, want the content dir", got)
	}
	if got := longestAllowPrefix("/mnt/onboard/.adds/koreader/x", nil); got != "/mnt/onboard/.adds" {
		t.Errorf("longestAllowPrefix = %q, want .adds — unchanged", got)
	}
}

// MINOR 7: a manifest that records the content dir itself must not plan as a
// deletion. Execute would not remove it and the rmdir pass declines it, so the
// only effect was a confirmation prompt reading "delete /mnt/onboard/fonts".
func TestComputeSkipsContentDirMember(t *testing.T) {
	plan, err := Compute([]string{"mnt/onboard/fonts", "mnt/onboard/fonts/A.ttf"},
		config.Uninstall{Method: config.MethodManifest}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range plan.Deletes {
		if pathEqual(d.Path, "/mnt/onboard/fonts") {
			t.Error("the content dir must never be a deletion target")
		}
	}
	if len(plan.Deletes) != 1 {
		t.Errorf("Deletes = %+v, want only the font file", plan.Deletes)
	}
	if len(plan.Skipped) != 1 || !pathEqual(plan.Skipped[0].Path, "/mnt/onboard/fonts") {
		t.Errorf("Skipped = %+v, want the content dir reported as skipped", plan.Skipped)
	}
}

// MINOR 6: a tgz package can ship a font with no [install] block at all, so only
// the manifest can say whether removing it changes what Nickel renders.
func TestManifestHasContent(t *testing.T) {
	if !ManifestHasContent([]string{"usr/local/pkg/lib.so", "mnt/onboard/fonts/A.ttf"}) {
		t.Error("a font in the manifest is content")
	}
	if ManifestHasContent([]string{"mnt/onboard/fonts"}) {
		t.Error("the directory itself is owned by nobody and is not content")
	}
	if ManifestHasContent([]string{"mnt/onboard/.adds/koreader/reader.lua"}) {
		t.Error("an ordinary app-tree member is not content")
	}
}
