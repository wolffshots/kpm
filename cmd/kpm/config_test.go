package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/modconfig"
)

// config_test.go covers `kpm config list|show|set` end to end against a fake
// /mnt/onboard (newUninstallApp's KPM_SYSROOT sandbox): the offline read path
// (packages.d snapshot + filesystem), the surgical edit + atomic write path, and
// the path-policy / sysroot / symlink guards (CONFIG.md §5.1).

// registerConfigPkg writes a packages.d def carrying config declarations.
func registerConfigPkg(t *testing.T, a *App, id string, cfgs []config.ModConfig) {
	t.Helper()
	pkg := &config.Package{
		Name: id, Source: "codeberg.org/o/" + id, Forge: config.ForgeForgejo,
		Asset: "KoboRoot.tgz", Configs: cfgs,
	}
	if err := config.Save(a.paths.PackageFile(id), pkg); err != nil {
		t.Fatal(err)
	}
}

// writeDeviceFile writes content at a device-absolute path inside the sandbox.
func writeDeviceFile(t *testing.T, sysroot, devPath, content string) string {
	t.Helper()
	host := filepath.Join(sysroot, filepath.FromSlash(strings.TrimPrefix(devPath, "/")))
	if err := os.MkdirAll(filepath.Dir(host), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(host, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return host
}

// readDeviceFile reads a device-absolute path inside the sandbox.
func readDeviceFile(t *testing.T, sysroot, devPath string) string {
	t.Helper()
	host := filepath.Join(sysroot, filepath.FromSlash(strings.TrimPrefix(devPath, "/")))
	b, err := os.ReadFile(host)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const clockPath = "/mnt/onboard/.adds/nickelclock/settings.ini"
const notePath = "/mnt/onboard/.adds/nickelnote/content.template"

func clockConfig() config.ModConfig {
	return config.ModConfig{Name: "Settings", Path: clockPath, Format: config.FormatINI, Reload: config.ReloadReboot, Description: "Clock and battery display options."}
}
func noteConfig() config.ModConfig {
	return config.ModConfig{Name: "Note content", Path: notePath, Format: config.FormatText, Reload: config.ReloadAuto, Create: true}
}

const clockBody = "[General]\nMargin = 10\n\n[Clock]\nEnabled = true\nPlacement = Footer\n"

// ---- list --------------------------------------------------------------

func TestConfigListExistsAndNotCreated(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelclock", []config.ModConfig{clockConfig()})
	writeDeviceFile(t, sysroot, clockPath, clockBody)

	out := captureStdout(t, func() {
		if code := a.cmdConfig([]string{"list", "nickelclock", "--json"}); code != exitOK {
			t.Fatalf("list exit %d", code)
		}
	})
	got := lastJSON(t, out)
	want := `{"id":"nickelclock","configs":[{"name":"Settings","path":"/mnt/onboard/.adds/nickelclock/settings.ini","format":"ini","reload":"reboot","exists":true,"can_create":false,"editable":true,"description":"Clock and battery display options."}]}`
	if got != want {
		t.Errorf("config list --json\n got: %s\nwant: %s", got, want)
	}
}

func TestConfigListMissingFile(t *testing.T) {
	a, _ := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelnote", []config.ModConfig{noteConfig()})
	out := captureStdout(t, func() { a.cmdConfig([]string{"list", "nickelnote", "--json"}) })
	got := lastJSON(t, out)
	if !strings.Contains(got, `"exists":false`) || !strings.Contains(got, `"can_create":true`) {
		t.Errorf("missing creatable file must report exists:false can_create:true: %s", got)
	}
}

// ---- show --------------------------------------------------------------

func TestConfigShowINIByNameAndIndex(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelclock", []config.ModConfig{clockConfig()})
	writeDeviceFile(t, sysroot, clockPath, clockBody)

	want := `{"id":"nickelclock","file":{"name":"Settings","format":"ini","reload":"reboot","exists":true},"entries":[{"section":"General","key":"Margin","line":2,"value":"10","sensitive":false},{"section":"Clock","key":"Enabled","line":5,"value":"true","sensitive":false},{"section":"Clock","key":"Placement","line":6,"value":"Footer","sensitive":false}],"truncated":false}`

	for _, sel := range []string{"Settings", "settings", "1"} {
		out := captureStdout(t, func() { a.cmdConfig([]string{"show", "nickelclock", sel, "--json"}) })
		if got := lastJSON(t, out); got != want {
			t.Errorf("show %q\n got: %s\nwant: %s", sel, got, want)
		}
	}
}

func TestConfigShowTextKeyNull(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelnote", []config.ModConfig{noteConfig()})
	writeDeviceFile(t, sysroot, notePath, "line one\nline two")
	out := captureStdout(t, func() { a.cmdConfig([]string{"show", "nickelnote", "Note content", "--json"}) })
	got := lastJSON(t, out)
	want := `{"id":"nickelnote","file":{"name":"Note content","format":"text","reload":"auto","exists":true},"entries":[{"section":"","key":null,"line":1,"value":"line one","sensitive":false},{"section":"","key":null,"line":2,"value":"line two","sensitive":false}],"truncated":false}`
	if got != want {
		t.Errorf("show text\n got: %s\nwant: %s", got, want)
	}
}

func TestConfigShowMissingSelector(t *testing.T) {
	a, _ := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelclock", []config.ModConfig{clockConfig()})
	if code := a.cmdConfig([]string{"show", "nickelclock", "Nope"}); code != exitError {
		t.Errorf("unknown selector exit %d, want %d", code, exitError)
	}
}

// ---- set: ini ----------------------------------------------------------

func TestConfigSetINISurgical(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelclock", []config.ModConfig{clockConfig()})
	writeDeviceFile(t, sysroot, clockPath, clockBody)

	out := captureStdout(t, func() {
		if code := a.cmdConfig([]string{"set", "nickelclock", "Settings", "--section", "Clock", "--key", "Enabled", "--value", "false", "--json"}); code != exitOK {
			t.Fatalf("set exit %d", code)
		}
	})
	got := lastJSON(t, out)
	// reboot_required true because reload == "reboot".
	if want := `{"changed":["nickelclock"],"failed":[],"staged":false,"reboot_required":true}`; got != want {
		t.Errorf("set --json\n got: %s\nwant: %s", got, want)
	}
	after := readDeviceFile(t, sysroot, clockPath)
	if want := strings.Replace(clockBody, "Enabled = true", "Enabled = false", 1); after != want {
		t.Errorf("only the target line should change\n got: %q\nwant: %q", after, want)
	}
}

func TestConfigSetININewKey(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelclock", []config.ModConfig{clockConfig()})
	writeDeviceFile(t, sysroot, clockPath, clockBody)
	if code := a.cmdConfig([]string{"set", "nickelclock", "Settings", "--section", "Clock", "--key", "Position", "--value", "Right"}); code != exitOK {
		t.Fatalf("set new key exit %d", code)
	}
	after := readDeviceFile(t, sysroot, clockPath)
	if !strings.Contains(after, "Position = Right") {
		t.Errorf("new key not written: %q", after)
	}
}

// ---- set: text ---------------------------------------------------------

func TestConfigSetTextLineAppendDelete(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelnote", []config.ModConfig{noteConfig()})
	writeDeviceFile(t, sysroot, notePath, "one\ntwo\n")

	// Replace line 1.
	if code := a.cmdConfig([]string{"set", "nickelnote", "Note content", "--line", "1", "--value", "ONE"}); code != exitOK {
		t.Fatalf("set line exit %d", code)
	}
	if got := readDeviceFile(t, sysroot, notePath); got != "ONE\ntwo\n" {
		t.Fatalf("set line got %q", got)
	}
	// Append.
	if code := a.cmdConfig([]string{"set", "nickelnote", "Note content", "--append", "--value", "three"}); code != exitOK {
		t.Fatalf("append exit %d", code)
	}
	if got := readDeviceFile(t, sysroot, notePath); got != "ONE\ntwo\nthree\n" {
		t.Fatalf("append got %q", got)
	}
	// Delete line 2.
	if code := a.cmdConfig([]string{"set", "nickelnote", "Note content", "--line", "2", "--delete"}); code != exitOK {
		t.Fatalf("delete exit %d", code)
	}
	if got := readDeviceFile(t, sysroot, notePath); got != "ONE\nthree\n" {
		t.Fatalf("delete got %q", got)
	}
}

// ---- create semantics --------------------------------------------------

func TestConfigSetCreatesWhenAllowed(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelnote", []config.ModConfig{noteConfig()}) // create=true
	if code := a.cmdConfig([]string{"set", "nickelnote", "Note content", "--append", "--value", "hello"}); code != exitOK {
		t.Fatalf("create-on-append exit %d", code)
	}
	if got := readDeviceFile(t, sysroot, notePath); got != "hello\n" {
		t.Errorf("created file content %q", got)
	}
}

func TestConfigSetRefusesCreateWhenNotAllowed(t *testing.T) {
	a, _ := newUninstallApp(t)
	// create defaults to false.
	registerConfigPkg(t, a, "nickelclock", []config.ModConfig{clockConfig()})
	if code := a.cmdConfig([]string{"set", "nickelclock", "Settings", "--key", "Enabled", "--value", "false"}); code != exitError {
		t.Errorf("missing file + create=false must refuse, exit %d", code)
	}
}

// A --value carrying an embedded newline must be refused end to end, and must
// leave the target file byte-for-byte unchanged (no structure smuggling).
func TestConfigSetRefusesNewlineValue(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelclock", []config.ModConfig{clockConfig()})
	writeDeviceFile(t, sysroot, clockPath, clockBody)

	if code := a.cmdConfig([]string{"set", "nickelclock", "Settings", "--section", "Clock", "--key", "Enabled", "--value", "false\n[Evil]\nHacked = 1"}); code != exitError {
		t.Errorf("newline value must be refused, exit %d", code)
	}
	if got := readDeviceFile(t, sysroot, clockPath); got != clockBody {
		t.Errorf("file must be untouched after a rejected injection: %q", got)
	}
}

// A --value large enough to push the file past MaxSize must be refused before the
// write, leaving the original file untouched (not written then unreadable).
func TestConfigSetRefusesOversizeResult(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelclock", []config.ModConfig{clockConfig()})
	writeDeviceFile(t, sysroot, clockPath, clockBody)

	big := strings.Repeat("A", modconfig.MaxSize+100)
	if code := a.cmdConfig([]string{"set", "nickelclock", "Settings", "--section", "Clock", "--key", "Enabled", "--value", big}); code != exitError {
		t.Errorf("oversize result must be refused, exit %d", code)
	}
	if got := readDeviceFile(t, sysroot, clockPath); got != clockBody {
		t.Errorf("file must be untouched after a rejected oversize write, got %d bytes", len(got))
	}
}

// ---- guards ------------------------------------------------------------

// A declared path outside the deletable/writable allowlist (.adds/.kobo) is
// refused by the policy re-check, both for reading and writing.
func TestConfigPathPolicyRejection(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	writeDeviceFile(t, sysroot, "/etc/evil.ini", "[S]\nk = v\n")
	registerConfigPkg(t, a, "bad", []config.ModConfig{
		{Name: "Evil", Path: "/etc/evil.ini", Format: config.FormatINI},
	})
	if code := a.cmdConfig([]string{"show", "bad", "Evil"}); code != exitError {
		t.Errorf("show of a denied path must fail, exit %d", code)
	}
	if code := a.cmdConfig([]string{"set", "bad", "Evil", "--key", "k", "--value", "x"}); code != exitError {
		t.Errorf("set of a denied path must fail, exit %d", code)
	}
	// The denied file was not modified.
	if got := readDeviceFile(t, sysroot, "/etc/evil.ini"); got != "[S]\nk = v\n" {
		t.Errorf("denied file must be untouched: %q", got)
	}
}

// A symlinked parent directory must abort the write (C7).
func TestConfigSetSymlinkParentRefused(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	// .adds/nickelnote is a symlink to an outside dir; a write through it would
	// escape the package tree.
	realDir := filepath.Join(sysroot, "opt", "outside")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkParent := filepath.Join(sysroot, filepath.FromSlash("mnt/onboard/.adds"))
	if err := os.MkdirAll(linkParent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(linkParent, "nickelnote")); err != nil {
		t.Skipf("symlink unsupported on this host: %v", err)
	}
	registerConfigPkg(t, a, "nickelnote", []config.ModConfig{noteConfig()})
	if code := a.cmdConfig([]string{"set", "nickelnote", "Note content", "--append", "--value", "x"}); code != exitError {
		t.Errorf("symlinked-parent write must be refused, exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(realDir, "content.template")); err == nil {
		t.Error("write must not land through the symlink")
	}
}

// The KPM_SYSROOT write guard: KPM_ROOT set but KPM_SYSROOT unset refuses the
// write against the real rootfs (copied from cmd_uninstall.go).
func TestConfigSetSysrootGuard(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelnote", []config.ModConfig{noteConfig()})
	writeDeviceFile(t, sysroot, notePath, "x\n")
	t.Setenv("KPM_SYSROOT", "") // KPM_ROOT stays set
	if code := a.cmdConfig([]string{"set", "nickelnote", "Note content", "--line", "1", "--value", "y"}); code != exitError {
		t.Errorf("KPM_ROOT-without-KPM_SYSROOT must refuse writes, exit %d", code)
	}
}
