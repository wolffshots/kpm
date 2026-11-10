package main

// dictionary_test.go drives the `file` install method end to end: a dictionary
// is placed byte for byte in custom-dict, live, with no reboot, under the same
// per-file ownership rules as a font.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/forge"
)

const dictDest = "/mnt/onboard/.kobo/custom-dict/dicthtml-test.zip"

func dictPkg(t *testing.T, a *App, host, id, dest string) {
	t.Helper()
	pkg := &config.Package{
		Name: id, Source: host + "/o/" + id, Forge: config.ForgeForgejo,
		Asset:     id + "-app.zip",
		Install:   &config.Install{Method: config.MethodFile, Dest: dest},
		Uninstall: config.Uninstall{Method: config.MethodManifest},
	}
	if err := config.Save(a.paths.PackageFile(id), pkg); err != nil {
		t.Fatal(err)
	}
}

// updateTo points the app at a fresh server publishing tag with the given asset,
// and forgets the last check so the next update resolves again.
func updateTo(t *testing.T, a *App, tag, id string, body []byte) string {
	t.Helper()
	srv := mixedServer(t, tag, map[string][]byte{id: body})
	a.client = forge.NewClientWithHTTP(srv.Client())
	a.state.Get(id).LastChecked = ""
	return strings.TrimPrefix(srv.URL, "https://")
}

func TestDictionaryInstallsLiveWithoutReboot(t *testing.T) {
	body := []byte("PK\x03\x04 a dictionary, placed byte for byte")
	a, sysroot, host := appTreeApp(t, map[string][]byte{"dict-a": body})
	dictPkg(t, a, host, "dict-a", dictDest)

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"dict-a", "--json"}) })
	if code != exitOK {
		t.Fatalf("update exit = %d", code)
	}
	m := decodeJSON(t, lastJSON(t, out))
	if wantBool(t, m, "staged") != false || wantBool(t, m, "reboot_required") != false {
		t.Errorf("a dictionary install stages nothing and needs no reboot: %v", m)
	}
	if got, _ := readDev(t, sysroot, dictDest); got != string(body) {
		t.Errorf("dest holds %q", got)
	}
	ps := a.state.Get("dict-a")
	if want := []string{strings.TrimPrefix(dictDest, "/")}; !reflect.DeepEqual(ps.Manifest, want) || ps.InstalledVersion != "v1" {
		t.Errorf("state = %+v", ps)
	}
	entries, _ := os.ReadDir(a.paths.CacheDir())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".file") {
			t.Errorf("the asset was renamed into place, so no cache copy may remain: %s", e.Name())
		}
	}
}

func TestDictionaryUpdateReplacesOwnFile(t *testing.T) {
	a, sysroot, host := appTreeApp(t, map[string][]byte{"dict-a": []byte("v1")})
	dictPkg(t, a, host, "dict-a", dictDest)
	if code := a.cmdUpdate([]string{"dict-a"}); code != exitOK {
		t.Fatalf("v1 exit = %d", code)
	}
	dictPkg(t, a, updateTo(t, a, "v2", "dict-a", []byte("v2")), "dict-a", dictDest)
	if code := a.cmdUpdate([]string{"dict-a"}); code != exitOK {
		t.Fatalf("v2 exit = %d", code)
	}
	if got, _ := readDev(t, sysroot, dictDest); got != "v2" {
		t.Errorf("dest holds %q, want v2", got)
	}
}

// A dictionary the user copied over USB is theirs: kpm neither overwrites it now
// nor deletes it at a later uninstall.
func TestDictionaryRefusesUserFile(t *testing.T) {
	a, sysroot, host := appTreeApp(t, map[string][]byte{"dict-a": []byte("kpm's")})
	dictPkg(t, a, host, "dict-a", dictDest)
	mine := hostFile(t, sysroot, dictDest)
	if err := os.MkdirAll(filepath.Dir(mine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("the user's own"), 0o644); err != nil {
		t.Fatal(err)
	}
	errOut := captureStderr(t, func() {
		if code := a.cmdUpdate([]string{"dict-a"}); code == exitOK {
			t.Fatal("a file kpm did not install must never be overwritten")
		}
	})
	if !strings.Contains(errOut, "kpm did not install it") {
		t.Errorf("the refusal must explain itself:\n%s", errOut)
	}
	if b, _ := os.ReadFile(mine); string(b) != "the user's own" {
		t.Errorf("the user's dictionary was overwritten: %q", b)
	}
}

// A def that renames its file between versions leaves no stale copy behind.
func TestDictionaryRenamePrunesOldFile(t *testing.T) {
	a, sysroot, host := appTreeApp(t, map[string][]byte{"dict-a": []byte("v1")})
	dictPkg(t, a, host, "dict-a", dictDest)
	if code := a.cmdUpdate([]string{"dict-a"}); code != exitOK {
		t.Fatalf("v1 exit = %d", code)
	}
	renamed := "/mnt/onboard/.kobo/custom-dict/dicthtml-renamed.zip"
	dictPkg(t, a, updateTo(t, a, "v2", "dict-a", []byte("v2")), "dict-a", renamed)
	if code := a.cmdUpdate([]string{"dict-a"}); code != exitOK {
		t.Fatalf("v2 exit = %d", code)
	}
	if _, ok := readDev(t, sysroot, dictDest); ok {
		t.Error("the old file must be pruned")
	}
	if got, _ := readDev(t, sysroot, renamed); got != "v2" {
		t.Errorf("renamed dest holds %q", got)
	}
}

func TestUIContractUninstallDictionary(t *testing.T) {
	a, sysroot, host := appTreeApp(t, map[string][]byte{"dict-a": []byte("d")})
	dictPkg(t, a, host, "dict-a", dictDest)
	if code := a.cmdUpdate([]string{"dict-a"}); code != exitOK {
		t.Fatalf("setup update exit = %d", code)
	}
	var code int
	out := captureStdout(t, func() { code = a.cmdUninstall([]string{"dict-a", "--yes", "--json"}) })
	if code != exitOK {
		t.Fatalf("uninstall exit = %d", code)
	}
	m := decodeJSON(t, lastJSON(t, out))
	if wantBool(t, m, "staged") != false || wantBool(t, m, "reboot_required") != false {
		t.Errorf("a dictionary uninstall stages nothing and needs no reboot: %v", m)
	}
	if _, ok := readDev(t, sysroot, dictDest); ok {
		t.Error("the dictionary must be removed now")
	}
	if _, err := os.Stat(hostFile(t, sysroot, "/mnt/onboard/.kobo/custom-dict")); err != nil {
		t.Errorf("the shared custom-dict dir must stay: %v", err)
	}
}

func TestKindDictionary(t *testing.T) {
	if got := kindOf(&config.Install{Method: config.MethodFile, Dest: dictDest}); got != "dictionary" {
		t.Errorf("kind = %q, want dictionary", got)
	}
}
