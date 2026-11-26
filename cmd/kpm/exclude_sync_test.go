package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"kpm/internal/device"
)

// wikiFull is the KOReader wiki value (\.(?!kobo|adobe).+|([^.][^/]*/)+\..+)
// as a full match of p, case-insensitive, written out by hand because Go's
// regexp has no lookahead. p is the folder path after /mnt/onboard/.
func wikiFull(p string) bool {
	if len(p) > 1 && p[0] == '.' {
		rest := strings.ToLower(p[1:])
		if !strings.HasPrefix(rest, "kobo") && !strings.HasPrefix(rest, "adobe") {
			return true
		}
	}
	// ([^.][^/]*/)+ then \..+ : one or more visible components, then a rest
	// that starts with a dot and has one more character at least.
	comps := strings.Split(p, "/")
	for k := 1; k < len(comps); k++ {
		c := comps[k-1]
		if c == "" || c[0] == '.' {
			return false
		}
		rest := strings.Join(comps[k:], "/")
		if len(rest) > 1 && rest[0] == '.' {
			return true
		}
	}
	return false
}

// wikiHides applies the wiki value with Nickel's template (nickelTemplate):
// on line 4 the value may match the path up to a "/", on line 6 it must match
// the whole path.
func wikiHides(p string, line int) bool {
	if line == device.Line6 {
		return wikiFull(p)
	}
	comps := strings.Split(p, "/")
	for i := 1; i <= len(comps); i++ {
		if wikiFull(strings.Join(comps[:i], "/")) {
			return true
		}
	}
	return false
}

func presetMatcher(t *testing.T, line int) *regexp.Regexp {
	t.Helper()
	res, bad := nickelMatchers(presetDotFolders, line)
	if len(res) != 1 || bad != 0 {
		t.Fatalf("preset: %d patterns, %d untestable", len(res), bad)
	}
	return res[0]
}

// TestPresetDotFoldersMatchesWiki: with Nickel's template of each firmware
// line, the kpm preset hides exactly the folders that the wiki value hides.
func TestPresetDotFoldersMatchesWiki(t *testing.T) {
	names := []string{"Books", "x.y", "a", "k", ".adds", ".kobo", ".kobo-images", ".kobox", ".kob", ".kobx",
		".k", ".ko", ".kx", ".adobe", ".adobe-digital-editions", ".adob", ".adobx", ".a", ".ad", ".x",
		".calibre", ".Kobo", ".ADOBE", ".K", "..", ".a.b", "kpm"}
	var paths []string
	for _, a := range names {
		paths = append(paths, a)
		for _, b := range names {
			paths = append(paths, a+"/"+b)
			for _, c := range names {
				paths = append(paths, a+"/"+b+"/"+c)
			}
		}
	}
	for _, line := range []int{device.Line4, device.Line6} {
		re := presetMatcher(t, line)
		for _, p := range paths {
			if got, want := re.MatchString(nickelMountPrefix+p), wikiHides(p, line); got != want {
				t.Errorf("line %d, %s: preset hides=%v, wiki hides=%v", line, p, got, want)
			}
		}
	}
}

// TestPresetDotFoldersCases names the folders that matter on a real device.
func TestPresetDotFoldersCases(t *testing.T) {
	for _, line := range []int{device.Line4, device.Line6} {
		re := presetMatcher(t, line)
		for p, hidden := range map[string]bool{
			".adds": true, ".adds/kpm": true, ".adds/kpm/cache": true, ".calibre": true,
			"Books/.covers": true, "Books/a/.x": true, "Books/.covers/x": true,
			".kobo": false, ".kobo/kepub": false, ".kobo-images": false, ".adobe-digital-editions": false,
			".Kobo": false, ".KOBO/kepub": false, ".ADDS": true, "Books/.Covers": true, "Books": false, "Books/a": false, "fonts": false, "Books/a.b": false,
		} {
			if got := re.MatchString(nickelMountPrefix + p); got != hidden {
				t.Errorf("line %d, %s: hidden=%v, want %v", line, p, got, hidden)
			}
		}
	}
	// Nickel splits the value on ";", QSettings reads a "," outside quotes as a
	// list separator, and the value must not need quotes.
	if strings.ContainsAny(presetDotFolders, `,;"`) {
		t.Error("the preset holds a character that breaks the value")
	}
}

// TestNickelMatchersSplit: each ";" item is its own pattern, and an item that
// Go cannot compile is counted, not dropped in silence.
func TestNickelMatchersSplit(t *testing.T) {
	res, bad := nickelMatchers(`\.calibre;;(?!x)y;Books/old`, device.Line4)
	if len(res) != 2 || bad != 1 {
		t.Fatalf("%d patterns, %d untestable", len(res), bad)
	}
	if !res[0].MatchString("/mnt/onboard/.Calibre/x") || res[0].MatchString("/mnt/onboard/a/.calibre") {
		t.Error("line 4: the item matches from the root, up to a folder boundary, case-insensitive")
	}
	res6, _ := nickelMatchers(`\.calibre`, device.Line6)
	if res6[0].MatchString("/mnt/onboard/.calibre/x") || !res6[0].MatchString("/mnt/onboard/.calibre") {
		t.Error("line 6: the item must match the whole path")
	}
}

func TestQSettingsEscape(t *testing.T) {
	v, ok := qsettingsUnescape(qsettingsEscape(presetDotFolders))
	if !ok || v != presetDotFolders {
		t.Fatalf("round trip: %q %v", v, ok)
	}
	// A value with ";" must go in quotes: QSettings reads an unquoted ";" as
	// the start of a comment (the Libra Colour kept only "custom" of an
	// unquoted "custom;preset").
	merged := `(\.x);` + presetDotFolders
	esc := qsettingsEscape(merged)
	if esc[0] != '"' || esc[len(esc)-1] != '"' {
		t.Errorf("a value with ; is not quoted: %s", esc)
	}
	if v, ok := qsettingsUnescape(esc); !ok || v != merged {
		t.Errorf("quoted round trip: %q %v", v, ok)
	}
	for raw, want := range map[string]string{
		`(\\.(?!kobo|adobe).+)`: `(\.(?!kobo|adobe).+)`,
		`"a\"b"`:                `a"b`,
		`a\\.b;c\\.d`:           `a\.b`, // the rest is a comment
		`"a;b" ; note`:          `a;b`,
	} {
		if got, ok := qsettingsUnescape(raw); !ok || got != want {
			t.Errorf("%s: got %q %v, want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{`a,b`, `\n`, `"open`, `a\`} {
		if _, ok := qsettingsUnescape(raw); ok {
			t.Errorf("%s: kpm must not claim to read it", raw)
		}
	}
}

const testConf = "[General]\r\nfoo=1\r\n\r\n[FeatureSettings]\r\nOther=true\r\n\r\n[Reading]\r\nbar=2\r\n"

func excludeApp(t *testing.T, conf string) (*App, string) {
	t.Helper()
	a := newTestApp(t)
	for _, d := range []string{".adds/kpm", ".kobo/Kobo", ".kobo/kepub", "Books/.covers", ".calibre"} {
		if err := os.MkdirAll(filepath.Join(a.paths.Root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if conf != "" {
		if err := os.WriteFile(a.paths.NickelConf(), []byte(conf), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return a, a.paths.NickelConf()
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestExcludeSyncSetAndOff(t *testing.T) {
	a, conf := excludeApp(t, testConf)
	if code := a.cmdExcludeSync([]string{"set", "dot-folders", "--dry-run"}); code != exitOK {
		t.Fatalf("dry run exit %d", code)
	}
	if readFile(t, conf) != testConf {
		t.Fatal("a dry run changed the conf")
	}

	if code := a.cmdExcludeSync([]string{"set", "dot-folders"}); code != exitOK {
		t.Fatalf("set exit %d", code)
	}
	got := readFile(t, conf)
	line := "ExcludeSyncFolders = " + qsettingsEscape(presetDotFolders) + "\r\n"
	want := strings.Replace(testConf, "Other=true\r\n", "Other=true\r\n"+line, 1)
	if got != want {
		t.Fatalf("conf after set:\n%q\nwant:\n%q", got, want)
	}
	if v := excludeSyncVerdict(a.paths); v != syncExcludeOK {
		t.Errorf("doctor verdict %q, want ok", v)
	}
	backups, _ := os.ReadDir(excludeSyncBackupDir(a.paths))
	if len(backups) != 1 || readFile(t, filepath.Join(excludeSyncBackupDir(a.paths), backups[0].Name())) != testConf {
		t.Fatalf("backup: %v", backups)
	}
	c, _ := readExcludeSyncConf(a.paths)
	rec, _ := loadExcludeSyncRecord(a.paths)
	if s := excludeSyncSource(c, rec); s != excludeSourceKpm {
		t.Errorf("source %q", s)
	}
	rep := excludeSyncReport(a.paths, c.value, excludeSourceKpm)
	hidden := map[string]bool{}
	for _, f := range rep.Folders {
		hidden[f.Path] = *f.Hidden
	}
	if !hidden[".adds"] || !hidden[".calibre"] || !hidden["Books/.covers"] || hidden[".kobo"] {
		t.Errorf("folders: %v", hidden)
	}

	// The same preset again changes nothing.
	if code := a.cmdExcludeSync([]string{"set", "dot-folders"}); code != exitOK || readFile(t, conf) != want {
		t.Fatalf("second set: exit %d", code)
	}

	if code := a.cmdExcludeSync([]string{"off"}); code != exitOK {
		t.Fatalf("off exit %d", code)
	}
	if readFile(t, conf) != testConf {
		t.Fatalf("off did not give back the original:\n%q", readFile(t, conf))
	}
	if _, err := os.Stat(excludeSyncRecordFile(a.paths)); !os.IsNotExist(err) {
		t.Error("off must remove the record")
	}
}

func TestExcludeSyncMergesCustom(t *testing.T) {
	custom := `ExcludeSyncFolders=(\\.(?!kobo|adobe).+)`
	orig := strings.Replace(testConf, "Other=true\r\n", "Other=true\r\n"+custom+"\r\n", 1)
	a, conf := excludeApp(t, orig)

	c, _ := readExcludeSyncConf(a.paths)
	if s := excludeSyncSource(c, nil); s != excludeSourceCustom {
		t.Fatalf("source %q", s)
	}
	if rep := excludeSyncReport(a.paths, c.value, excludeSourceCustom); rep.Testable {
		t.Error("a lookahead value is not testable in Go")
	}
	if code := a.cmdExcludeSync([]string{"off"}); code != exitError || readFile(t, conf) != orig {
		t.Fatal("off must leave a custom value alone")
	}

	if code := a.cmdExcludeSync([]string{"set", "dot-folders"}); code != exitOK {
		t.Fatalf("set exit %d", code)
	}
	merged := qsettingsEscape(`(\.(?!kobo|adobe).+);` + presetDotFolders)
	if !strings.Contains(readFile(t, conf), "ExcludeSyncFolders="+merged+"\r\n") {
		t.Fatalf("merged conf:\n%s", readFile(t, conf))
	}
	c, _ = readExcludeSyncConf(a.paths)
	rec, _ := loadExcludeSyncRecord(a.paths)
	if s := excludeSyncSource(c, rec); s != excludeSourceKpmCustom {
		t.Errorf("source %q", s)
	}
	// Set again: the merge starts from the first value, so nothing changes.
	before := readFile(t, conf)
	if code := a.cmdExcludeSync([]string{"set", "dot-folders"}); code != exitOK || readFile(t, conf) != before {
		t.Fatal("a second set must change nothing")
	}

	if code := a.cmdExcludeSync([]string{"off"}); code != exitOK {
		t.Fatalf("off exit %d", code)
	}
	if readFile(t, conf) != orig {
		t.Fatalf("off did not give back the custom value:\n%q", readFile(t, conf))
	}
}

func TestExcludeSyncNewSectionAndErrors(t *testing.T) {
	a, conf := excludeApp(t, "[General]\nfoo=1\n")
	if code := a.cmdExcludeSync([]string{"set", "dot-folders"}); code != exitOK {
		t.Fatalf("set exit %d", code)
	}
	want := "[General]\nfoo=1\n[FeatureSettings]\nExcludeSyncFolders = " + qsettingsEscape(presetDotFolders) + "\n"
	if got := readFile(t, conf); got != want {
		t.Fatalf("got %q", got)
	}
	if code := a.cmdExcludeSync([]string{"set", "nope"}); code != exitError {
		t.Error("an unknown preset must fail")
	}

	b, conf2 := excludeApp(t, "")
	if code := b.cmdExcludeSync([]string{"set", "dot-folders"}); code != exitError {
		t.Error("a missing conf must fail")
	}
	if _, err := os.Stat(conf2); !os.IsNotExist(err) {
		t.Error("kpm must not create Kobo eReader.conf")
	}
}

func TestExcludeSyncIsMutating(t *testing.T) {
	for args, want := range map[string]bool{
		"":                          false,
		"--json":                    false,
		"set dot-folders":           true,
		"--json set dot-folders":    true,
		"off":                       true,
		"set dot-folders --dry-run": false,
	} {
		if got := isMutating("exclude-sync", strings.Fields(args)); got != want {
			t.Errorf("%q: mutating=%v, want %v", args, got, want)
		}
	}
}

// TestUIContractExcludeSync pins what the Settings tab row reads
// (hook/src/settingspage.cc applySync): exclude-sync --json gives "source" as a
// string, and set/off give the shared mutation shape.
func TestUIContractExcludeSync(t *testing.T) {
	a, _ := excludeApp(t, testConf)
	read := func() map[string]any {
		var code int
		out := captureStdout(t, func() { code = a.cmdExcludeSync([]string{"--json"}) })
		if code != exitOK {
			t.Fatalf("exclude-sync --json exit %d", code)
		}
		m := decodeJSON(t, lastJSON(t, out))
		wantStrOrNull(t, m, "value")
		wantBool(t, m, "testable")
		wantArray(t, m, "folders")
		return m
	}
	if s := wantStr(t, read(), "source"); s != excludeSourceNone {
		t.Fatalf("source %q", s)
	}
	for _, args := range [][]string{{"set", "dot-folders", "--json"}, {"off", "--json"}} {
		var code int
		out := captureStdout(t, func() { code = a.cmdExcludeSync(args) })
		if code != exitOK {
			t.Fatalf("%v exit %d", args, code)
		}
		m := decodeJSON(t, lastJSON(t, out))
		assertMutationShape(t, m)
		if !wantBool(t, m, "reboot_required") {
			t.Errorf("%v: a change needs a restart", args)
		}
		if args[0] == "set" {
			if s := wantStr(t, read(), "source"); s != excludeSourceKpm {
				t.Errorf("source after set %q", s)
			}
		}
	}
}
