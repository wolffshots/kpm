package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wikiHides is the KOReader wiki value (\.(?!kobo|adobe).+|([^.][^/]*/)+\..+)
// written out by hand, because Go's regexp has no lookahead. p is the folder
// path after /mnt/onboard/.
func wikiHides(p string) bool {
	if len(p) > 1 && p[0] == '.' && !strings.HasPrefix(p[1:], "kobo") && !strings.HasPrefix(p[1:], "adobe") {
		return true
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

// TestPresetDotFoldersMatchesWiki: on each folder that Nickel visits (no
// parent folder is hidden), the kpm preset hides exactly what the wiki value
// hides.
func TestPresetDotFoldersMatchesWiki(t *testing.T) {
	re, err := nickelMatcher(presetDotFolders)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"Books", "x.y", "a", "k", ".adds", ".kobo", ".kobo-images", ".kobox", ".kob", ".kobx",
		".k", ".ko", ".kx", ".adobe", ".adobe-digital-editions", ".adob", ".adobx", ".a", ".ad", ".x",
		".calibre", ".Kobo", "..", ".a.b"}
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
	checked := 0
	for _, p := range paths {
		visited := true
		comps := strings.Split(p, "/")
		for i := 1; i < len(comps); i++ {
			if wikiHides(strings.Join(comps[:i], "/")) {
				visited = false
				break
			}
		}
		if !visited {
			continue
		}
		checked++
		if got, want := re.MatchString(nickelMountPrefix+p), wikiHides(p); got != want {
			t.Errorf("%s: preset hides=%v, wiki hides=%v", p, got, want)
		}
	}
	if checked < 1000 {
		t.Fatalf("only %d paths checked", checked)
	}
}

// TestPresetDotFoldersCases names the folders that matter on a real device.
func TestPresetDotFoldersCases(t *testing.T) {
	re, err := nickelMatcher(presetDotFolders)
	if err != nil {
		t.Fatal(err)
	}
	for p, hidden := range map[string]bool{
		".adds": true, ".calibre": true, "Books/.covers": true, "Books/a/.x": true,
		".kobo": false, ".kobo-images": false, ".adobe-digital-editions": false,
		"Books": false, "Books/a": false, "fonts": false, "Books/a.b": false,
	} {
		if got := re.MatchString(nickelMountPrefix + p); got != hidden {
			t.Errorf("%s: hidden=%v, want %v", p, got, hidden)
		}
	}
	// QSettings reads a "," outside quotes as a list separator, and the value
	// must not need quotes.
	if strings.ContainsAny(presetDotFolders, `,;"`) {
		t.Error("the preset holds a character that needs QSettings quotes")
	}
}

func TestQSettingsEscape(t *testing.T) {
	v, ok := qsettingsUnescape(qsettingsEscape(presetDotFolders))
	if !ok || v != presetDotFolders {
		t.Fatalf("round trip: %q %v", v, ok)
	}
	for raw, want := range map[string]string{
		`(\\.(?!kobo|adobe).+)`: `(\.(?!kobo|adobe).+)`,
		`"a\"b"`:                `a"b`,
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
	merged := qsettingsEscape(`((\.(?!kobo|adobe).+)|` + presetDotFolders + `)`)
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
