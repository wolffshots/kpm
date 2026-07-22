package main

// uicontract_test.go pins the exact JSON/exit-code contract the graphical UI
// (the NickelKPM Qt hook under hook/) consumes, so a future kpm change cannot
// silently break the on-device UI. Unlike json_test.go's per-command goldens,
// these tests drive each of the SEVEN command shapes the hook actually issues
// (hook/src/kpmprocess.cc) end to end through the real cmd* entry points, and
// assert the PRESENCE and TYPE of every field the hook reads — a rename or a
// type flip is caught even when the golden string would still "look" valid.
//
// The fields the hook reads (derived from hook/src/{browsedialog,detaildialog,
// configdialog,kpmprocess}.cc and hook/src/widgets/{packagerow,configrow}.cc —
// code is ground truth):
//
//   search  --json  (KpmProcess::search, browse call; read-only, no lock):
//     top-level: packages[] (array), staged{} (OBJECT: .count int),
//                registries[] (array; each .refreshed = string|null),
//                device_fw(str|null) — the device firmware, reference for
//                fw_untested (D)
//     packages[i]: id(str), name(str), description(str|null), source(str),
//                registry(str|null), installed(str|null), pinned(str|null),
//                staged(BOOL), uninstallable(bool), min_kpm(str|null),
//                min_kpm_ok(bool), tested_fw(str|null), fw_untested(BOOL) (D),
//                missing_files(array|null) — absent members after post-install
//                verification, null when clean (A).  latest/update are
//                DELIBERATELY ABSENT here (registry defs carry no versions) —
//                the hook merges them in from check (browsedialog.cc mergeCheck).
//   check   --json  (KpmProcess::check; takes the lock):
//     packages[i]: id(str), latest(str|null), update(BOOL)  [+installed,pinned,error]
//   registry refresh --json (KpmProcess::registryRefresh):
//     payload is IGNORED by the hook (browsedialog.cc onRefreshDone voids it) —
//     contract is only: exit 0/2 + a parseable trailing BEGIN_JSON line.
//   install <id> --yes --json / update <id> --json / update --all --json /
//   uninstall <id> --yes --json / sync --json  (the mutations):
//     top-level: staged(BOOL), reboot_required(BOOL)  [+changed[],failed[]]
//     NOTE the shape split the hook relies on: `staged` is an OBJECT in the
//     search payload but a BOOL in every mutation payload; both are exercised.
//     sync always reports staged=false,reboot_required=false (a def re-copy
//     stages nothing) — see block 11.
//
// The hook maps a mutating command that hits the single-instance lock — whose
// stderr contains "another kpm instance" — to its "kpm is busy" dialog
// (kpmprocess.cc processFinished). That exact substring is pinned below.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/forge"
	"kpm/internal/registry"
)

// ---- contract type assertions ------------------------------------------

// decodeJSON parses the BEGIN_JSON payload into a generic map so field presence
// and type can be asserted structurally (json numbers -> float64, null -> nil).
func decodeJSON(t *testing.T, payload string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatalf("payload is not a JSON object: %v\n%s", err, payload)
	}
	return m
}

func present(t *testing.T, m map[string]any, key string) any {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("contract field %q absent: %v", key, m)
	}
	return v
}

// wantStr asserts a required non-null string (id/name/source).
func wantStr(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v := present(t, m, key)
	s, ok := v.(string)
	if !ok {
		t.Fatalf("field %q must be a string, got %T (%v)", key, v, v)
	}
	return s
}

// wantStrOrNull asserts a string-or-null field (description/registry/installed/
// pinned/latest/min_kpm/refreshed).
func wantStrOrNull(t *testing.T, m map[string]any, key string) {
	t.Helper()
	v := present(t, m, key)
	if v == nil {
		return
	}
	if _, ok := v.(string); !ok {
		t.Fatalf("field %q must be string or null, got %T (%v)", key, v, v)
	}
}

func wantBool(t *testing.T, m map[string]any, key string) bool {
	t.Helper()
	v := present(t, m, key)
	b, ok := v.(bool)
	if !ok {
		t.Fatalf("field %q must be a bool, got %T (%v)", key, v, v)
	}
	return b
}

func wantArray(t *testing.T, m map[string]any, key string) []any {
	t.Helper()
	v := present(t, m, key)
	a, ok := v.([]any)
	if !ok {
		t.Fatalf("field %q must be an array, got %T (%v)", key, v, v)
	}
	return a
}

// wantArrayOrNull asserts an array-or-null field (missing_files — null/absent
// when the package verified clean, a JSON array naming absent members otherwise).
func wantArrayOrNull(t *testing.T, m map[string]any, key string) {
	t.Helper()
	v := present(t, m, key)
	if v == nil {
		return
	}
	if _, ok := v.([]any); !ok {
		t.Fatalf("field %q must be array or null, got %T (%v)", key, v, v)
	}
}

func wantObject(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v := present(t, m, key)
	o, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("field %q must be an object, got %T (%v)", key, v, v)
	}
	return o
}

func wantNumber(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v := present(t, m, key)
	n, ok := v.(float64)
	if !ok {
		t.Fatalf("field %q must be a number, got %T (%v)", key, v, v)
	}
	return n
}

func wantAbsent(t *testing.T, m map[string]any, key string) {
	t.Helper()
	if _, ok := m[key]; ok {
		t.Fatalf("field %q must be ABSENT from this payload (contract §2.1): %v", key, m)
	}
}

// assertSearchPkg walks one search-payload package and pins every field the
// hook's PackageRow/DetailDialog read, plus the deliberate absence of
// latest/update (merged from check, not present in search).
func assertSearchPkg(t *testing.T, pkg map[string]any) {
	t.Helper()
	wantStr(t, pkg, "id")
	wantStr(t, pkg, "name")
	wantStrOrNull(t, pkg, "description")
	wantStr(t, pkg, "source")
	wantStrOrNull(t, pkg, "registry")
	wantStrOrNull(t, pkg, "installed")
	wantStrOrNull(t, pkg, "pinned")
	wantBool(t, pkg, "staged") // per-package staged is a BOOL (packagerow.cc)
	wantBool(t, pkg, "uninstallable")
	wantStrOrNull(t, pkg, "min_kpm")
	wantBool(t, pkg, "min_kpm_ok")
	wantBool(t, pkg, "has_config")           // drives the DetailDialog Settings button (CONFIG.md §4)
	wantStrOrNull(t, pkg, "tested_fw")       // advisory firmware-compat metadata (D)
	wantBool(t, pkg, "fw_untested")          // server-computed "untested on your firmware" (D)
	wantArrayOrNull(t, pkg, "missing_files") // post-install verification state (A)
	wantAbsent(t, pkg, "latest")
	wantAbsent(t, pkg, "update")
}

// ---- captureStderr (mirror of captureStdout) ----------------------------

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		for {
			n, err := r.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if err != nil {
				break
			}
		}
		done <- string(buf)
	}()
	fn()
	w.Close()
	os.Stderr = old
	return <-done
}

// releaseServer starts a fake Forgejo forge: it answers the network-probe HEAD,
// serves a latest-release JSON for owner/repo pairs NOT named in failRepos, and
// serves a real tgz for any /download/ path (so update can stage). A repo in
// failRepos 404s its release lookup, exercising the partial-failure path.
func releaseServer(t *testing.T, tag string, failRepos ...string) *httptest.Server {
	t.Helper()
	fails := map[string]bool{}
	for _, r := range failRepos {
		fails[r] = true
	}
	tgz := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/pkg/file": "payload"})
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/releases/latest"):
			// path: /api/v1/repos/<owner>/<repo>/releases/latest
			parts := strings.Split(r.URL.Path, "/")
			repo := ""
			for i, p := range parts {
				if p == "repos" && i+2 < len(parts) {
					repo = parts[i+2]
				}
			}
			if fails[repo] {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"tag_name":%q,"draft":false,"prerelease":false,`+
				`"assets":[{"name":"KoboRoot.tgz","size":%d,"browser_download_url":%q}]}`,
				tag, len(tgz), srv.URL+"/download/"+repo+"/KoboRoot.tgz")
		case strings.HasPrefix(r.URL.Path, "/download/"):
			w.Write(tgz)
		default:
			w.WriteHeader(http.StatusOK) // network probe / anything else
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fakeForgePkg(t *testing.T, a *App, host, id, installed string) {
	t.Helper()
	if err := config.Save(a.paths.PackageFile(id), &config.Package{
		Name: id, Source: host + "/o/" + id, Forge: config.ForgeForgejo, Asset: "KoboRoot.tgz",
	}); err != nil {
		t.Fatal(err)
	}
	if installed != "" {
		a.state.Get(id).InstalledVersion = installed
	}
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
}

// ---- 1. search --json ---------------------------------------------------

func TestUIContractSearch(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.6.0")
	seedRegistry(t, a, "main", jsonClockManifest)
	a.state.Get("nickelclock").InstalledVersion = "v0.4.0"
	a.state.Registry("main").LastFetched = "2026-07-20T09:00:00Z"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStdout(t, func() { code = a.cmdSearch([]string{"--json"}) })
	if code != exitOK {
		t.Fatalf("search --json exit = %d, want %d", code, exitOK)
	}
	m := decodeJSON(t, lastJSON(t, out))

	// top-level: staged is an OBJECT (with .count), registries carry .refreshed,
	// device_fw is the device firmware string-or-null (D).
	staged := wantObject(t, m, "staged")
	wantNumber(t, staged, "count")
	wantStrOrNull(t, m, "device_fw")
	regs := wantArray(t, m, "registries")
	if len(regs) != 1 {
		t.Fatalf("registries len = %d, want 1", len(regs))
	}
	reg0 := regs[0].(map[string]any)
	wantStrOrNull(t, reg0, "refreshed")
	if reg0["refreshed"] != "2026-07-20T09:00:00Z" {
		t.Errorf("registries[0].refreshed = %v, want the RFC3339 timestamp", reg0["refreshed"])
	}

	pkgs := wantArray(t, m, "packages")
	if len(pkgs) != 1 {
		t.Fatalf("packages len = %d, want 1", len(pkgs))
	}
	p0 := pkgs[0].(map[string]any)
	assertSearchPkg(t, p0)
	// Concrete, deterministic values the UI renders.
	if p0["id"] != "nickelclock" || p0["installed"] != "v0.4.0" || p0["staged"] != false {
		t.Errorf("unexpected package fields: %v", p0)
	}
	// Registry def only (no local packages.d def): kpm cannot uninstall it, so
	// uninstallable is false regardless of the registry's advertised recipe (M2).
	if p0["uninstallable"] != false || p0["min_kpm_ok"] != true {
		t.Errorf("uninstallable/min_kpm_ok = %v/%v, want false/true", p0["uninstallable"], p0["min_kpm_ok"])
	}
}

// ---- 2. check --json ----------------------------------------------------

func TestUIContractCheck(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.6.0")
	srv := releaseServer(t, "v0.5.0")
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")
	fakeForgePkg(t, a, host, "nickelclock", "v0.4.0")

	var code int
	out := captureStdout(t, func() { code = a.cmdCheck([]string{"--json"}) })
	if code != exitOK {
		t.Fatalf("check --json exit = %d, want %d", code, exitOK)
	}
	m := decodeJSON(t, lastJSON(t, out))
	present(t, m, "checked")
	pkgs := wantArray(t, m, "packages")
	if len(pkgs) != 1 {
		t.Fatalf("check packages len = %d, want 1", len(pkgs))
	}
	p0 := pkgs[0].(map[string]any)
	// The three fields browsedialog.cc mergeCheck reads: id, latest, update.
	if wantStr(t, p0, "id") != "nickelclock" {
		t.Errorf("check id = %v", p0["id"])
	}
	wantStrOrNull(t, p0, "latest")
	if p0["latest"] != "v0.5.0" {
		t.Errorf("check latest = %v, want v0.5.0", p0["latest"])
	}
	if wantBool(t, p0, "update") != true {
		t.Errorf("check update = %v, want true (v0.4.0 -> v0.5.0)", p0["update"])
	}
}

// ---- 3. registry refresh --json -----------------------------------------

func TestUIContractRegistryRefresh(t *testing.T) {
	a := newTestApp(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(nmManifest))
	}))
	t.Cleanup(srv.Close)
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")

	// A registry pointing at the fake host (seedRegistry hardcodes a real host).
	cfg, err := a.loadRegistryConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Registries = append(cfg.Registries, registry.Registry{
		Name: "main", URL: host + "/o/r", Ref: "main", Path: "registry.toml", Forge: config.ForgeForgejo,
	})
	if err := a.saveRegistryConfig(cfg); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStdout(t, func() { code = a.cmdRegistryRefresh([]string{"--json"}) })
	if code != exitOK {
		t.Fatalf("registry refresh --json exit = %d, want %d", code, exitOK)
	}
	// The hook ignores this payload, but it must still be a parseable trailing
	// BEGIN_JSON line (lastJSON enforces marker-is-last), and the §2.4 shape.
	m := decodeJSON(t, lastJSON(t, out))
	refreshed := wantArray(t, m, "refreshed")
	if len(refreshed) != 1 {
		t.Fatalf("refreshed len = %d, want 1", len(refreshed))
	}
	r0 := refreshed[0].(map[string]any)
	if wantStr(t, r0, "name") != "main" {
		t.Errorf("refreshed[0].name = %v", r0["name"])
	}
	wantNumber(t, r0, "packages")
	if len(wantArray(t, m, "failed")) != 0 {
		t.Errorf("failed should be empty on a clean refresh: %v", m["failed"])
	}
}

// ---- 4. install <id> --yes --json ---------------------------------------

func TestUIContractInstallYes(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.6.0")
	seedRegistry(t, a, "main", jsonClockManifest)

	var code int
	out := captureStdout(t, func() { code = a.cmdInstall([]string{"nickelclock", "--yes", "--json"}) })
	// --yes MUST bypass the exit-3 confirmation path (kpmprocess.cc passes --yes).
	if code == exitConfirm {
		t.Fatalf("install --yes hit the exit-3 confirm path — the UI would stall")
	}
	if code != exitOK {
		t.Fatalf("install --yes --json exit = %d, want %d", code, exitOK)
	}
	m := decodeJSON(t, lastJSON(t, out))
	assertMutationShape(t, m)
	// install only registers the def — it stages nothing (spec §2.3). The hook's
	// install chain relies on this: it drives the reboot prompt off the FOLLOWING
	// `update`, not off install.
	if wantBool(t, m, "staged") != false || wantBool(t, m, "reboot_required") != false {
		t.Errorf("install must report staged=false,reboot_required=false, got %v/%v",
			m["staged"], m["reboot_required"])
	}
	if changed := wantArray(t, m, "changed"); len(changed) != 1 || changed[0] != "nickelclock" {
		t.Errorf("install changed = %v, want [nickelclock]", m["changed"])
	}
}

// ---- 5. update <id> --json ----------------------------------------------

func TestUIContractUpdateOne(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.6.0")
	srv := releaseServer(t, "v1.1.0")
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")
	fakeForgePkg(t, a, host, "pkg", "v1.0.0")

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"pkg", "--json"}) })
	if code != exitOK {
		t.Fatalf("update pkg --json exit = %d, want %d", code, exitOK)
	}
	m := decodeJSON(t, lastJSON(t, out))
	assertMutationShape(t, m)
	// A staged update: both bools true drive the hook's reboot prompt.
	if wantBool(t, m, "staged") != true || wantBool(t, m, "reboot_required") != true {
		t.Errorf("staged update must report staged=true,reboot_required=true, got %v/%v",
			m["staged"], m["reboot_required"])
	}
	if changed := wantArray(t, m, "changed"); len(changed) != 1 || changed[0] != "pkg" {
		t.Errorf("update changed = %v, want [pkg]", m["changed"])
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err != nil {
		t.Errorf("update should have staged a tgz: %v", err)
	}
}

// ---- 6. update --all --json (partial = exit 2) --------------------------

func TestUIContractUpdateAllPartial(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.6.0")
	// "badpkg" 404s its release lookup; "goodpkg" resolves and stages.
	srv := releaseServer(t, "v2.0.0", "badpkg")
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")
	fakeForgePkg(t, a, host, "goodpkg", "v1.0.0")
	fakeForgePkg(t, a, host, "badpkg", "v1.0.0")

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"--all", "--json"}) })
	// One staged, one failed -> partial (exit 2), a payload the hook still renders.
	if code != exitPartial {
		t.Fatalf("update --all with one failure exit = %d, want %d (partial)", code, exitPartial)
	}
	m := decodeJSON(t, lastJSON(t, out))
	assertMutationShape(t, m)
	if wantBool(t, m, "staged") != true || wantBool(t, m, "reboot_required") != true {
		t.Errorf("partial-but-staged must still report reboot true, got %v/%v", m["staged"], m["reboot_required"])
	}
	changed := wantArray(t, m, "changed")
	if len(changed) != 1 || changed[0] != "goodpkg" {
		t.Errorf("changed = %v, want [goodpkg]", changed)
	}
	failed := wantArray(t, m, "failed")
	if len(failed) != 1 {
		t.Fatalf("failed len = %d, want 1", len(failed))
	}
	f0 := failed[0].(map[string]any)
	if wantStr(t, f0, "id") != "badpkg" {
		t.Errorf("failed[0].id = %v, want badpkg", f0["id"])
	}
	wantStr(t, f0, "error")
}

// ---- 7. uninstall <id> --yes --json -------------------------------------

func TestUIContractUninstallYes(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	mkfile(t, sysroot, "usr/local/pkg/lib.so")
	registerPkg(t, a, "pkg", config.Uninstall{}, []string{"usr/local/pkg/lib.so"})

	var code int
	out := captureStdout(t, func() { code = a.cmdUninstall([]string{"pkg", "--yes", "--json"}) })
	// --yes MUST bypass the exit-3 confirmation path.
	if code == exitConfirm {
		t.Fatalf("uninstall --yes hit the exit-3 confirm path — the UI would stall")
	}
	if code != exitOK {
		t.Fatalf("uninstall --yes --json exit = %d, want %d", code, exitOK)
	}
	m := decodeJSON(t, lastJSON(t, out))
	assertMutationShape(t, m)
	// uninstall stages nothing; reboot_required reflects the method (a manifest
	// delete needs no reboot). staged is a BOOL here (mutation payload).
	if wantBool(t, m, "staged") != false {
		t.Errorf("uninstall staged = %v, want false", m["staged"])
	}
	if changed := wantArray(t, m, "changed"); len(changed) != 1 || changed[0] != "pkg" {
		t.Errorf("uninstall changed = %v, want [pkg]", m["changed"])
	}
}

// assertMutationShape pins the top-level shape of every install/update/uninstall
// payload: the two bools the hook reads plus the changed/failed arrays.
func assertMutationShape(t *testing.T, m map[string]any) {
	t.Helper()
	wantBool(t, m, "staged")
	wantBool(t, m, "reboot_required")
	wantArray(t, m, "changed")
	wantArray(t, m, "failed")
}

// ---- 8-10. config list / show / set (CONFIG.md §3.3) --------------------
//
// CONTRACT: these blocks were the forward spec the Phase 2 ConfigDialog was built
// against; the hook now realizes them in hook/src/configdialog.cc and
// hook/src/widgets/configrow.cc (the ConfigDialog file-picker + entry rows), which
// join the other .cc files as ground truth. Field names/types stay pinned here so
// a kpm change cannot silently break the ConfigDialog rendering.
//
//   config list <id> --json   (KpmProcess::configList — read-only, no lock):
//     top-level: id(str), configs[] (array)
//     configs[i]: name(str), path(str), format(str), reload(str),
//                 exists(bool), can_create(bool), editable(bool),
//                 description(str|null)
//   config show <id> <sel> --json   (KpmProcess::configShow — read-only):
//     top-level: id(str), file{} (object), entries[] (array), truncated(bool)
//     file: name(str), format(str), reload(str), exists(bool)
//     entries[i]: section(str), key(str|null — null for text lines),
//                 line(number), value(str), sensitive(bool)
//   config set <id> <name> ... --json   (KpmProcess::configSet — mutating):
//     the shared mutation shape: changed[], failed[], staged(BOOL),
//     reboot_required(BOOL). reboot_required = (reload == "reboot").
//   config init <id> <sel> --json   (KpmProcess::configInit — mutating):
//     same mutation shape; seeds a missing file from its declared template.
//     list/show gain has_template(bool) so configdialog.cc can offer the
//     "Create from example" button without a second CLI call (CONFIG.md §3.x).

func TestUIContractConfigList(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelclock", []config.ModConfig{clockConfig()})
	writeDeviceFile(t, sysroot, clockPath, clockBody)

	var code int
	out := captureStdout(t, func() { code = a.cmdConfig([]string{"list", "nickelclock", "--json"}) })
	if code != exitOK {
		t.Fatalf("config list exit = %d, want %d", code, exitOK)
	}
	m := decodeJSON(t, lastJSON(t, out))
	if wantStr(t, m, "id") != "nickelclock" {
		t.Errorf("id = %v", m["id"])
	}
	files := wantArray(t, m, "configs")
	if len(files) != 1 {
		t.Fatalf("configs len = %d, want 1", len(files))
	}
	f0 := files[0].(map[string]any)
	wantStr(t, f0, "name")
	wantStr(t, f0, "path")
	wantStr(t, f0, "format")
	wantStr(t, f0, "reload")
	wantBool(t, f0, "exists")
	wantBool(t, f0, "can_create")
	wantBool(t, f0, "editable")
	wantBool(t, f0, "has_template") // drives the ConfigDialog "Create from example" button (CONFIG.md §3.x)
	wantStrOrNull(t, f0, "description")
}

func TestUIContractConfigShow(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelclock", []config.ModConfig{clockConfig()})
	writeDeviceFile(t, sysroot, clockPath, clockBody)

	var code int
	out := captureStdout(t, func() { code = a.cmdConfig([]string{"show", "nickelclock", "Settings", "--json"}) })
	if code != exitOK {
		t.Fatalf("config show exit = %d, want %d", code, exitOK)
	}
	m := decodeJSON(t, lastJSON(t, out))
	wantStr(t, m, "id")
	wantBool(t, m, "truncated")
	file := wantObject(t, m, "file")
	wantStr(t, file, "name")
	wantStr(t, file, "format")
	wantStr(t, file, "reload")
	wantBool(t, file, "exists")
	wantBool(t, file, "has_template") // drives the ConfigDialog "Create from example" button (CONFIG.md §3.x)
	entries := wantArray(t, m, "entries")
	if len(entries) == 0 {
		t.Fatal("entries should be non-empty")
	}
	e0 := entries[0].(map[string]any)
	wantStr(t, e0, "section")
	wantStrOrNull(t, e0, "key") // string for ini, null for text
	wantNumber(t, e0, "line")
	wantStr(t, e0, "value")
	wantBool(t, e0, "sensitive")
}

func TestUIContractConfigShowTextKeyNull(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelnote", []config.ModConfig{noteConfig()})
	writeDeviceFile(t, sysroot, notePath, "just one line")
	out := captureStdout(t, func() { a.cmdConfig([]string{"show", "nickelnote", "1", "--json"}) })
	m := decodeJSON(t, lastJSON(t, out))
	e0 := wantArray(t, m, "entries")[0].(map[string]any)
	if _, ok := e0["key"]; !ok {
		t.Fatal("key field must be present (null) for text entries")
	}
	if e0["key"] != nil {
		t.Errorf("text entry key must be null, got %v", e0["key"])
	}
}

func TestUIContractConfigSet(t *testing.T) {
	a, sysroot := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelclock", []config.ModConfig{clockConfig()})
	writeDeviceFile(t, sysroot, clockPath, clockBody)

	var code int
	out := captureStdout(t, func() {
		code = a.cmdConfig([]string{"set", "nickelclock", "Settings", "--section", "Clock", "--key", "Enabled", "--value", "false", "--json"})
	})
	if code != exitOK {
		t.Fatalf("config set exit = %d, want %d", code, exitOK)
	}
	m := decodeJSON(t, lastJSON(t, out))
	assertMutationShape(t, m)
	if wantBool(t, m, "staged") != false {
		t.Errorf("config set stages nothing, staged = %v", m["staged"])
	}
	if wantBool(t, m, "reboot_required") != true {
		t.Errorf("reload=reboot must set reboot_required true, got %v", m["reboot_required"])
	}
	if changed := wantArray(t, m, "changed"); len(changed) != 1 || changed[0] != "nickelclock" {
		t.Errorf("changed = %v, want [nickelclock]", m["changed"])
	}
}

// ---- 10b. config init <id> <sel> --json (CONFIG.md §3.x) ----------------
//
// CONTRACT: the ConfigDialog "Create from example" button (configdialog.cc,
// KpmProcess::configInit) seeds a missing file from its declared template. It is
// mutating and reuses the shared §2.3 mutation shape; staged is always false (a
// local file write stages nothing), reboot_required tracks reload == "reboot".
// The hook reads exactly changed[]/failed[]/staged/reboot_required, then re-reads
// the file so the seeded lines render for editing.

func TestUIContractConfigInit(t *testing.T) {
	a, _ := newUninstallApp(t)
	registerConfigPkg(t, a, "nickelnote", []config.ModConfig{noteTemplateConfig()})

	var code int
	out := captureStdout(t, func() {
		code = a.cmdConfig([]string{"init", "nickelnote", "Note content", "--json"})
	})
	if code != exitOK {
		t.Fatalf("config init exit = %d, want %d", code, exitOK)
	}
	m := decodeJSON(t, lastJSON(t, out))
	assertMutationShape(t, m)
	if wantBool(t, m, "staged") != false {
		t.Errorf("config init stages nothing, staged = %v", m["staged"])
	}
	// reload=auto → reboot_required false.
	if wantBool(t, m, "reboot_required") != false {
		t.Errorf("reload=auto init must report reboot_required false, got %v", m["reboot_required"])
	}
	if changed := wantArray(t, m, "changed"); len(changed) != 1 || changed[0] != "nickelnote" {
		t.Errorf("init changed = %v, want [nickelnote]", m["changed"])
	}
}

// ---- 11. sync --json (KpmProcess::sync — mutating, no network) ----------
//
// CONTRACT: the browse footer's Sync button re-copies registry defs into
// packages.d (propagating new [[configs]]/uninstall declarations to existing
// installs). It reuses the §2.3 mutation shape; `staged`/`reboot_required` are
// always false (a def re-copy stages nothing and needs no reboot). The hook
// (browsedialog.cc onActionDone) reads exactly those two bools plus changed[]/
// failed[], so a rename/type flip here would break the Sync flow.

func TestUIContractSync(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.8.1")
	seedRegistry(t, a, "main", syncDriftManifest)
	// A registry-managed local def that predates the registry's config addition.
	if err := config.Save(a.paths.PackageFile("samplemod"), &config.Package{
		Name: "Sample Mod", Source: "codeberg.org/o/samplemod", Forge: config.ForgeForgejo,
		Asset: "KoboRoot.tgz", Registry: "main",
	}); err != nil {
		t.Fatal(err)
	}
	a.state.Get("samplemod").InstalledVersion = "v1.0.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStdout(t, func() { code = a.cmdSync([]string{"--json"}) })
	if code != exitOK {
		t.Fatalf("sync --json exit = %d, want %d", code, exitOK)
	}
	m := decodeJSON(t, lastJSON(t, out))
	assertMutationShape(t, m)
	// sync stages nothing and never needs a reboot.
	if wantBool(t, m, "staged") != false || wantBool(t, m, "reboot_required") != false {
		t.Errorf("sync must report staged=false,reboot_required=false, got %v/%v",
			m["staged"], m["reboot_required"])
	}
	if changed := wantArray(t, m, "changed"); len(changed) != 1 || changed[0] != "samplemod" {
		t.Errorf("sync changed = %v, want [samplemod]", m["changed"])
	}
}

// ---- busy: the "another kpm instance" dialog trigger --------------------

// A second MUTATING command against a held single-instance lock must fail with
// stderr containing "another kpm instance" — the exact substring kpmprocess.cc
// matches to show its "kpm is busy" dialog instead of a raw error.
func TestUIContractBusyLockMessage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KPM_ROOT", root)
	p := device.New()
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	// Hold the lock with a FRESH mtime so it is not treated as stale/abandoned.
	if err := os.MkdirAll(filepath.Dir(p.LockFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.LockFile(), []byte("999 held\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var code int
	stderr := captureStderr(t, func() {
		// Drive the real binary entry point; install is one of the seven UI shapes.
		code = run([]string{"install", "nickelclock", "--yes", "--json"})
	})
	if code != exitError {
		t.Errorf("busy install exit = %d, want %d (hard error the hook surfaces)", code, exitError)
	}
	if !strings.Contains(strings.ToLower(stderr), "another kpm instance") {
		t.Errorf("stderr must contain the busy-dialog trigger \"another kpm instance\":\n%s", stderr)
	}
}
