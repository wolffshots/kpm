package main

// rolling_test.go pins feature A (INTEGRITY.md §3): rolling-tag update
// detection for a repo whose release tag never changes. Before it, such a repo
// installed once and reported up to date FOREVER, silently — the KoboCloud case
// (its tag is literally "latest").

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"kpm/internal/config"
	"kpm/internal/forge"
	"kpm/internal/state"
)

// rollingRel is the release a rollingServer serves. Fields are read on every
// request, so a test mutates them to simulate an upstream rebuild.
type rollingRel struct {
	tag         string
	publishedAt string
	assetName   string // "" serves a release with no assets at all
	payload     []byte
}

// rollingServer stands up a fake Forgejo forge serving rel, answers the network
// probe on every other path, and wires its client into a. It returns the host.
func rollingServer(t *testing.T, a *App, rel *rollingRel) string {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/repos/"):
			assets := ""
			if rel.assetName != "" {
				assets = fmt.Sprintf(`{"name":%q,"size":%d,"browser_download_url":%q}`,
					rel.assetName, len(rel.payload), srv.URL+"/download/"+rel.assetName)
			}
			fmt.Fprintf(w, `{"tag_name":%q,"published_at":%q,"assets":[%s]}`,
				rel.tag, rel.publishedAt, assets)
		case strings.HasPrefix(r.URL.Path, "/download/"):
			w.Write(rel.payload)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	a.client = forge.NewClientWithHTTP(srv.Client())
	return strings.TrimPrefix(srv.URL, "https://")
}

// registerRollingPkg writes a packages.d def with the given update_check mode.
func registerRollingPkg(t *testing.T, a *App, id, host, mode, asset string) {
	t.Helper()
	if err := config.Save(a.paths.PackageFile(id), &config.Package{
		Name: id, Source: host + "/o/" + id, Forge: config.ForgeForgejo,
		Asset: asset, UpdateCheck: mode,
	}); err != nil {
		t.Fatal(err)
	}
}

// checkUpdateFlag runs `check --json` and returns the package's `update` bool.
func checkUpdateFlag(t *testing.T, a *App, id string) bool {
	t.Helper()
	var code int
	out := captureStdout(t, func() { code = a.cmdCheck([]string{"--json"}) })
	if code != exitOK {
		t.Fatalf("check --json exit = %d, want %d\n%s", code, exitOK, out)
	}
	var payload jsonCheckPayload
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &payload); err != nil {
		t.Fatal(err)
	}
	for _, p := range payload.Packages {
		if p.ID == id {
			return p.Update
		}
	}
	t.Fatalf("%s missing from the check payload: %s", id, out)
	return false
}

// THE KoboCloud CASE. Same permanent tag, a re-published release: check must
// report an update and update must stage it.
func TestRollingDetectsRebuiltRelease(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	payload := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/kobocloud/x": "v1"})
	rel := &rollingRel{tag: "latest", publishedAt: "2026-06-01T10:00:00Z", assetName: "KoboRoot.tgz", payload: payload}
	host := rollingServer(t, a, rel)
	registerRollingPkg(t, a, "kobocloud", host, config.UpdateCheckRolling, "KoboRoot.tgz")

	// Installed at "latest" with the OLD release's fingerprint recorded.
	ps := a.state.Get("kobocloud")
	ps.InstalledVersion = "latest"
	ps.InstalledFingerprint = "p=2026-06-01T10:00:00Z s=" + itoa(len(payload))
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	// Upstream re-publishes the same tag.
	rel.publishedAt = "2026-07-01T10:00:00Z"

	if !checkUpdateFlag(t, a, "kobocloud") {
		t.Fatal("a rebuilt rolling release must report update:true — this is the silent-forever bug")
	}
	// §6.3: the hook renders off `update`, which must be true even though
	// installed == latest. No new JSON field is added for this.
	out := captureStdout(t, func() { a.cmdCheck([]string{"--json"}) })
	if got := lastJSON(t, out); !strings.Contains(got, `"installed":"latest","latest":"latest","update":true`) {
		t.Errorf("check --json must carry installed == latest AND update:true: %s", got)
	}
	// The human summary must not read "latest -> latest" (§3.7).
	pkgs, _ := a.loadPackages()
	if status := a.buildStatus(pkgs); !strings.Contains(status, "UPDATE AVAILABLE (release rebuilt)") {
		t.Errorf("status should name the reason:\n%s", status)
	}

	var code int
	captureStdout(t, func() { code = a.cmdUpdate([]string{"kobocloud"}) })
	if code != exitOK {
		t.Fatalf("update exit = %d, want %d", code, exitOK)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err != nil {
		t.Errorf("a rebuilt rolling release must stage: %v", err)
	}
	if got := a.state.Get("kobocloud").StagedFingerprint; !strings.Contains(got, "p=2026-07-01T10:00:00Z") {
		t.Errorf("staged_fingerprint = %q, want the new release's", got)
	}
}

// The complement: an untouched rolling release is NOT an update.
func TestRollingIgnoresUnchangedRelease(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	payload := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/kobocloud/x": "v1"})
	rel := &rollingRel{tag: "latest", publishedAt: "2026-06-01T10:00:00Z", assetName: "KoboRoot.tgz", payload: payload}
	host := rollingServer(t, a, rel)
	registerRollingPkg(t, a, "kobocloud", host, config.UpdateCheckRolling, "KoboRoot.tgz")

	ps := a.state.Get("kobocloud")
	ps.InstalledVersion = "latest"
	ps.InstalledFingerprint = "p=2026-06-01T10:00:00Z s=" + itoa(len(payload))
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	if checkUpdateFlag(t, a, "kobocloud") {
		t.Error("an unchanged rolling release must not report an update")
	}
	captureStdout(t, func() { a.cmdUpdate([]string{"kobocloud"}) })
	if _, err := os.Stat(a.paths.StagedTgz()); err == nil {
		t.Error("an unchanged rolling release must stage nothing")
	}
}

// THE OPT-IN PROOF. A default (tag) package sees the same changed published_at
// and reports nothing — so the 11 non-rolling registry entries are untouched.
func TestNonRollingIgnoresChangedPublishedAt(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	payload := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/pkg/x": "v1"})
	rel := &rollingRel{tag: "latest", publishedAt: "2026-06-01T10:00:00Z", assetName: "KoboRoot.tgz", payload: payload}
	host := rollingServer(t, a, rel)
	registerRollingPkg(t, a, "pkg", host, "", "KoboRoot.tgz") // default: no update_check key

	ps := a.state.Get("pkg")
	ps.InstalledVersion = "latest"
	ps.InstalledFingerprint = "p=2026-06-01T10:00:00Z s=" + itoa(len(payload))
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	rel.publishedAt = "2026-07-01T10:00:00Z"

	if checkUpdateFlag(t, a, "pkg") {
		t.Error("rolling detection must be opt-in: a default package must ignore published_at")
	}
	captureStdout(t, func() { a.cmdUpdate([]string{"pkg"}) })
	if _, err := os.Stat(a.paths.StagedTgz()); err == nil {
		t.Error("a default package must stage nothing on a changed published_at")
	}
}

// §3.6.1 the migration rule: a rolling package installed before 0.10.0 has no
// recorded fingerprint. The first check adopts the current one as the baseline
// and reports NO update — otherwise every such package would stage a reboot for
// a payload the device already has.
func TestRollingBaselineAdoptedSilently(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	payload := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/kobocloud/x": "v1"})
	rel := &rollingRel{tag: "latest", publishedAt: "2026-07-01T10:00:00Z", assetName: "KoboRoot.tgz", payload: payload}
	host := rollingServer(t, a, rel)
	registerRollingPkg(t, a, "kobocloud", host, config.UpdateCheckRolling, "KoboRoot.tgz")

	ps := a.state.Get("kobocloud")
	ps.InstalledVersion = "latest" // installed, but no fingerprint: predates 0.10.0
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	if checkUpdateFlag(t, a, "kobocloud") {
		t.Error("an adopted baseline must not read as an update")
	}
	got := a.state.Get("kobocloud").InstalledFingerprint
	if !strings.Contains(got, "p=2026-07-01T10:00:00Z") {
		t.Errorf("installed_fingerprint = %q, want the adopted baseline", got)
	}
	if !logContains(t, a, "rolling baseline adopted") {
		t.Error("baseline adoption must leave an INFO trail in kpm.log")
	}
	// A never-installed package has no baseline to adopt.
	b := newTestApp(t)
	setVersion(t, "0.10.0")
	hostB := rollingServer(t, b, rel)
	registerRollingPkg(t, b, "kobocloud", hostB, config.UpdateCheckRolling, "KoboRoot.tgz")
	captureStdout(t, func() { b.cmdCheck([]string{"--json"}) })
	if got := b.state.Get("kobocloud").InstalledFingerprint; got != "" {
		t.Errorf("a never-installed package must not adopt a baseline, got %q", got)
	}
}

// m1: baseline adoption happens BEFORE resolveAndDownload's own up-to-date
// test, so a user who deliberately typed `kpm update <id>` got a silent no-op
// with no explanation. The behaviour stays (§1.2 item 1 justifies it) — but it
// must say what it did.
func TestRollingBaselineAdoptionIsExplainedByUpdate(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	payload := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/kobocloud/x": "v1"})
	rel := &rollingRel{tag: "latest", publishedAt: "2026-07-01T10:00:00Z", assetName: "KoboRoot.tgz", payload: payload}
	host := rollingServer(t, a, rel)
	registerRollingPkg(t, a, "kobocloud", host, config.UpdateCheckRolling, "KoboRoot.tgz")
	a.state.Get("kobocloud").InstalledVersion = "latest" // installed, no fingerprint
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"kobocloud"}) })
	if code != exitOK {
		t.Fatalf("update exit = %d, want %d\n%s", code, exitOK, out)
	}
	if !strings.Contains(out, "recording this release as the rolling baseline") {
		t.Errorf("the no-op must explain itself:\n%s", out)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err == nil {
		t.Error("baseline adoption must still stage nothing")
	}
	// Second run: the baseline exists, so there is nothing to explain.
	out = captureStdout(t, func() { a.cmdUpdate([]string{"kobocloud"}) })
	if strings.Contains(out, "rolling baseline") {
		t.Errorf("the note must fire only on the adopting run:\n%s", out)
	}
}

// m2: a hand-edited local def carrying BOTH rolling and checksums used to
// hard-fail every download forever (the rolling tag's asset moves, the pinned
// hash does not) with wording that reads like a security incident. It must
// degrade to unverified, with a WARN — §9.2's pattern.
func TestRollingWithChecksumsDegradesToUnverified(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	payload := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/kobocloud/x": "v2"})
	rel := &rollingRel{tag: "latest", publishedAt: "2026-07-01T10:00:00Z", assetName: "KoboRoot.tgz", payload: payload}
	host := rollingServer(t, a, rel)
	// ParseManifest would refuse this combination; packages.d is hand-editable.
	if err := config.Save(a.paths.PackageFile("kobocloud"), &config.Package{
		Name: "kobocloud", Source: host + "/o/kobocloud", Forge: config.ForgeForgejo,
		Asset: "KoboRoot.tgz", UpdateCheck: config.UpdateCheckRolling,
		Checksums: map[string]string{"latest": strings.Repeat("ab", 32)}, // stale by construction
	}); err != nil {
		t.Fatal(err)
	}
	ps := a.state.Get("kobocloud")
	ps.InstalledVersion = "latest"
	ps.InstalledFingerprint = "p=2026-06-01T10:00:00Z s=" + itoa(len(payload))
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"kobocloud"}) })
	if code != exitOK {
		t.Fatalf("a rolling def with a stale checksum must still update, exit = %d\n%s", code, out)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err != nil {
		t.Errorf("the rebuilt release should have staged: %v", err)
	}
	if !logContains(t, a, "cannot be combined with checksums") {
		t.Error("the degrade must be WARNed to kpm.log")
	}
	if logContains(t, a, "MISMATCH") {
		t.Error("a rolling def must never produce a checksum mismatch")
	}
}

// n1: §3.7 removed "latest -> latest UPDATE AVAILABLE"; the very next state
// rendered "latest -> latest STAGED (reboot)" — the same confusion, one step on.
func TestStagedRollingStatusNamesTheRebuild(t *testing.T) {
	a := newTestApp(t)
	registerRollingPkg(t, a, "kobocloud", "example.com", config.UpdateCheckRolling, "KoboRoot.tgz")
	ps := a.state.Get("kobocloud")
	ps.InstalledVersion = "latest"
	ps.StagedVersion = "latest"
	pkgs, _ := a.loadPackages()
	status := a.buildStatus(pkgs)
	if strings.Contains(status, "latest -> latest") {
		t.Errorf("a same-tag staging must not render latest -> latest:\n%s", status)
	}
	if !strings.Contains(status, "STAGED: release rebuilt (reboot)") {
		t.Errorf("status should name the reason:\n%s", status)
	}
	// A tag that really moved is unchanged.
	ps.InstalledVersion = "v1.0.0"
	ps.StagedVersion = "v2.0.0"
	if status := a.buildStatus(pkgs); !strings.Contains(status, "v1.0.0 -> v2.0.0\tSTAGED (reboot)") &&
		!strings.Contains(status, "v1.0.0 -> v2.0.0") {
		t.Errorf("a moved tag must still render the arrow:\n%s", status)
	}
}

// §3.2/§3.7: a forge that supplies neither signal degrades to tag comparison —
// visibly (WARN + a stdout note), never as an error. A package must not become
// un-updatable because a forge got quieter.
func TestRollingDegradesWhenForgeSuppliesNothing(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	// No published_at, and the release carries no asset the glob can match.
	rel := &rollingRel{tag: "latest", publishedAt: "", assetName: ""}
	host := rollingServer(t, a, rel)
	registerRollingPkg(t, a, "kobocloud", host, config.UpdateCheckRolling, "KoboRoot.tgz")
	a.state.Get("kobocloud").InstalledVersion = "latest"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStdout(t, func() { code = a.cmdCheck([]string{"--json"}) })
	if code != exitOK {
		t.Fatalf("check exit = %d, want %d (degrading is not an error)", code, exitOK)
	}
	if !strings.Contains(out, "rolling update detection unavailable from this forge") {
		t.Errorf("check should say it degraded:\n%s", out)
	}
	if !logContains(t, a, "falling back to tag comparison") {
		t.Error("the degrade must be WARNed to kpm.log")
	}
	// Degraded == today's behaviour: installed tag equals latest, so no update.
	var payload jsonCheckPayload
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Packages) != 1 || payload.Packages[0].Update {
		t.Errorf("degraded check must fall back to tag comparison: %+v", payload.Packages)
	}
}

// A rolling package already staged for the SAME payload must not be re-staged
// before the reboot that installs it.
func TestRollingStagedNotRestagedBeforeReboot(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	payload := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/kobocloud/x": "v1"})
	rel := &rollingRel{tag: "latest", publishedAt: "2026-07-01T10:00:00Z", assetName: "KoboRoot.tgz", payload: payload}
	host := rollingServer(t, a, rel)
	registerRollingPkg(t, a, "kobocloud", host, config.UpdateCheckRolling, "KoboRoot.tgz")

	fp := "p=2026-07-01T10:00:00Z s=" + itoa(len(payload))
	ps := a.state.Get("kobocloud")
	ps.InstalledVersion = "latest"
	ps.InstalledFingerprint = "p=2026-06-01T10:00:00Z s=" + itoa(len(payload))
	ps.StagedVersion = "latest"
	ps.StagedFingerprint = fp
	ps.LatestFingerprint = fp
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	if _, avail := updateTarget(pkgWithRolling(), ps, ""); avail {
		t.Error("a staged rolling package whose fingerprint matches must not report available")
	}
	out := captureStdout(t, func() { a.cmdUpdate([]string{"kobocloud"}) })
	if !strings.Contains(out, "nothing to stage") {
		t.Errorf("an already-staged rolling package must not re-stage:\n%s", out)
	}
	// But a FURTHER rebuild while staged does re-stage.
	rel.publishedAt = "2026-07-02T10:00:00Z"
	captureStdout(t, func() { a.cmdUpdate([]string{"kobocloud"}) })
	if _, err := os.Stat(a.paths.StagedTgz()); err != nil {
		t.Errorf("a rebuild after staging must re-stage: %v", err)
	}
}

// §3.5: the staged fingerprint describes what actually landed, so promotion
// moves it to installed and clears staged; a rollback discards it.
func TestRollingFingerprintPromotedOnReconcile(t *testing.T) {
	s := &state.State{Packages: map[string]*state.PackageState{
		"kobocloud": {InstalledVersion: "latest", StagedVersion: "latest", StagedFingerprint: "p=T2 s=9"},
		"plain":     {InstalledVersion: "v1", StagedVersion: "v2"}, // non-rolling: empty is promoted too
	}}
	s.PromoteStaged()
	ps := s.Packages["kobocloud"]
	if ps.InstalledFingerprint != "p=T2 s=9" || ps.StagedFingerprint != "" {
		t.Errorf("promote: installed=%q staged=%q", ps.InstalledFingerprint, ps.StagedFingerprint)
	}
	if got := s.Packages["plain"].InstalledFingerprint; got != "" {
		t.Errorf("a non-rolling promotion must carry the empty fingerprint, got %q", got)
	}

	s2 := &state.State{Packages: map[string]*state.PackageState{
		"kobocloud": {InstalledVersion: "latest", InstalledFingerprint: "p=T1 s=9",
			StagedVersion: "latest", StagedFingerprint: "p=T2 s=9"},
	}}
	s2.RollbackStaged()
	ps2 := s2.Packages["kobocloud"]
	if ps2.StagedFingerprint != "" {
		t.Errorf("rollback must clear staged_fingerprint, got %q", ps2.StagedFingerprint)
	}
	if ps2.InstalledFingerprint != "p=T1 s=9" {
		t.Errorf("rollback must not touch installed_fingerprint, got %q", ps2.InstalledFingerprint)
	}
}

// §3.6: hoisting MatchAsset ahead of the up-to-date check discriminates two
// failure classes that are NOT alike.
//
// A MALFORMED PATTERN is a property of the def: it can never match anything in
// any release, so it stays loud and early even when the package is up to date.
func TestUpdateMalformedAssetPatternFailsEvenWhenUpToDate(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	payload := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/pkg/x": "v1"})
	rel := &rollingRel{tag: "v1.0.0", publishedAt: "2026-07-01T10:00:00Z", assetName: "KoboRoot.tgz", payload: payload}
	host := rollingServer(t, a, rel)
	// Up to date by tag, and the glob is syntactically broken (unclosed class).
	registerRollingPkg(t, a, "pkg", host, "", "KoboRoot[.tgz")
	a.state.Get("pkg").InstalledVersion = "v1.0.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"pkg", "--json"}) })
	if code != exitError {
		t.Fatalf("a malformed asset pattern must fail, exit = %d\n%s", code, out)
	}
	var m jsonMutation
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Failed) != 1 || !strings.Contains(m.Failed[0].Error, "invalid asset pattern") {
		t.Errorf("failed[] should name the malformed pattern: %+v", m.Failed)
	}
}

// The other half: "no asset matching" is a property of the CURRENT RELEASE, not
// of the def. Reporting it as a def error made `update` exit 1 saying "update
// failed" while its own human output read "Everything up to date." — and made
// every KoboCloud publish (which recreates its release, so there is a window
// with no assets) flip `check` and `update` into disagreeing.
func TestUpdateUnmatchedGlobDoesNotFailAnUpToDatePackage(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	payload := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/pkg/x": "v1"})
	rel := &rollingRel{tag: "v1.0.0", publishedAt: "2026-07-01T10:00:00Z", assetName: "KoboRoot.tgz", payload: payload}
	host := rollingServer(t, a, rel)
	registerRollingPkg(t, a, "pkg", host, "", "NoSuchAsset-*.zip")
	a.state.Get("pkg").InstalledVersion = "v1.0.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"pkg", "--json"}) })
	if code != exitOK {
		t.Fatalf("an up-to-date package must not fail on an unmatched glob, exit = %d\n%s", code, out)
	}
	var m jsonMutation
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Failed) != 0 {
		t.Errorf("nothing failed here: %+v", m.Failed)
	}
	if strings.Contains(out, "Everything up to date.") && m.Error != "" {
		t.Errorf("human output and exit status must agree:\n%s", out)
	}

	// Already STAGED and one reboot from done: also must not fail.
	b := newTestApp(t)
	setVersion(t, "0.10.0")
	hostB := rollingServer(t, b, rel)
	registerRollingPkg(t, b, "pkg", hostB, "", "NoSuchAsset-*.zip")
	ps := b.state.Get("pkg")
	ps.InstalledVersion = "v0.9.0"
	ps.StagedVersion = "v1.0.0"
	if err := b.state.Save(); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { code = b.cmdUpdate([]string{"pkg", "--json"}) })
	if code != exitOK {
		t.Fatalf("an already-staged package must not fail on an unmatched glob, exit = %d\n%s", code, out)
	}

	// But a package that genuinely NEEDS the download still fails, at the point
	// the download would happen.
	c := newTestApp(t)
	setVersion(t, "0.10.0")
	hostC := rollingServer(t, c, rel)
	registerRollingPkg(t, c, "pkg", hostC, "", "NoSuchAsset-*.zip")
	c.state.Get("pkg").InstalledVersion = "v0.9.0"
	if err := c.state.Save(); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { code = c.cmdUpdate([]string{"pkg", "--json"}) })
	if code != exitError {
		t.Fatalf("a package that must download an unresolvable asset has to fail, exit = %d\n%s", code, out)
	}
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Failed) != 1 || !strings.Contains(m.Failed[0].Error, "no asset matching") {
		t.Errorf("failed[] should name the unresolvable glob: %+v", m.Failed)
	}
}

// §2.1: update_check is HashDef-INCLUDED precisely so a curator flipping a
// package to rolling reaches devices where it is ALREADY installed — via sync.
// If it did not propagate, an installed KoboCloud would never learn it is
// rolling and would stay silently stuck, reintroducing the bug one layer down.
// The complement matters just as much: a def that omits the key must read as up
// to date, not as drift (§2.2 hazard 1).
func TestSyncPropagatesUpdateCheckToInstalledPackage(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	// min_kpm is already present, so the ONLY thing the flip below changes is
	// update_check — otherwise a coincidental min_kpm bump would mask whether the
	// field is really part of the hash.
	const plain = `schema_version = 1
[packages.kobocloud]
name = "KoboCloud"
source = "github.com/fsantini/KoboCloud"
forge = "github"
asset = "KoboRoot.tgz"
min_kpm = "0.10.0"
`
	seedRegistry(t, a, "main", plain)
	if err := config.Save(a.paths.PackageFile("kobocloud"), &config.Package{
		Name: "KoboCloud", Source: "github.com/fsantini/KoboCloud", Forge: "github",
		Asset: "KoboRoot.tgz", Registry: "main", MinKpm: "0.10.0",
	}); err != nil {
		t.Fatal(err)
	}
	a.state.Get("kobocloud").InstalledVersion = "latest"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	// A def matching the registry byte for byte is up to date — NOT drifted.
	out := captureStdout(t, func() { a.cmdSync(nil) })
	if !strings.Contains(out, "0 applied, 1 up to date") {
		t.Fatalf("a def omitting update_check must read as up to date, not drift:\n%s", out)
	}

	// The curator flips it to rolling; sync must carry that to the device.
	if err := os.WriteFile(a.paths.RegistryCache("main"),
		[]byte(plain+"update_check = \"rolling\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { a.cmdSync(nil) })
	if !strings.Contains(out, "1 applied") {
		t.Fatalf("flipping a package to rolling must sync:\n%s", out)
	}
	local, err := config.Load(a.paths.PackageFile("kobocloud"), "kobocloud")
	if err != nil {
		t.Fatal(err)
	}
	if local.EffectiveUpdateCheck() != config.UpdateCheckRolling {
		t.Errorf("the installed def did not learn it is rolling: update_check = %q", local.UpdateCheck)
	}
	// The SECOND sync is the real assertion (see syncRoundTrip below): if
	// SaveReplace failed to re-emit the field it would have been erased from the
	// written def, so the local hash would no longer match the recorded synced
	// hash and every later sync would report permanent, silent drift.
	out = captureStdout(t, func() { a.cmdSync(nil) })
	if !strings.Contains(out, "0 applied, 1 up to date") {
		t.Fatalf("a just-synced def must read as up to date, not drifted:\n%s", out)
	}
}

// syncRoundTrip seeds a registry whose kobocloud entry carries `extra`, syncs it
// onto a def that lacks it, and asserts (a) the field landed in packages.d and
// (b) a SECOND sync reads up to date.
//
// (b) is the assertion that matters. SaveReplace clears every knownTopKey and
// re-emits only what its overlay writes, so a missing setOptional* line silently
// drops the field from the written file. The first sync still reports "1
// applied" — the write "succeeded" — and only the next one exposes it, as
// `local def has been edited since last sync — skipped`: permanent, silent
// drift, on the exact write path install and sync apply use. That is the 0.5.0
// bug verbatim, and deleting either overlay line left the whole suite green.
func syncRoundTrip(t *testing.T, extra string, check func(t *testing.T, p *config.Package)) {
	t.Helper()
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	const base = `schema_version = 1
[packages.kobocloud]
name = "KoboCloud"
source = "github.com/fsantini/KoboCloud"
forge = "github"
asset = "KoboRoot.tgz"
min_kpm = "0.10.0"
`
	seedRegistry(t, a, "main", base+extra)
	if err := config.Save(a.paths.PackageFile("kobocloud"), &config.Package{
		Name: "KoboCloud", Source: "github.com/fsantini/KoboCloud", Forge: "github",
		Asset: "KoboRoot.tgz", Registry: "main", MinKpm: "0.10.0",
	}); err != nil {
		t.Fatal(err)
	}
	a.state.Get("kobocloud").InstalledVersion = "latest"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() { a.cmdSync(nil) })
	if !strings.Contains(out, "1 applied") {
		t.Fatalf("the registry field must sync to the device:\n%s", out)
	}
	local, err := config.Load(a.paths.PackageFile("kobocloud"), "kobocloud")
	if err != nil {
		t.Fatal(err)
	}
	check(t, local)

	out = captureStdout(t, func() { a.cmdSync(nil) })
	if !strings.Contains(out, "0 applied, 1 up to date") {
		t.Fatalf("a just-synced def must read as up to date, not drifted:\n%s", out)
	}
}

// §2.2 hazard 4 for `conflicts`: it must survive the SaveReplace round trip, or
// the field that drives the whole install/update refusal is erased on write and
// the package drifts permanently.
func TestSyncPropagatesConflictsToInstalledPackage(t *testing.T) {
	syncRoundTrip(t, "conflicts = [\"nickelclock\"]\n", func(t *testing.T, p *config.Package) {
		if len(p.Conflicts) != 1 || p.Conflicts[0] != "nickelclock" {
			t.Errorf("the installed def did not learn its conflict: conflicts = %v", p.Conflicts)
		}
	})
}

// Same for `checksums`: a curator adding a hash for a new release must reach the
// download path, which reads the LOCAL def (§2.1).
func TestSyncPropagatesChecksumsToInstalledPackage(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	syncRoundTrip(t, "[packages.kobocloud.checksums]\n\"v1.0.0\" = \""+sum+"\"\n",
		func(t *testing.T, p *config.Package) {
			if got, ok := p.ChecksumFor("v1.0.0"); !ok || got != sum {
				t.Errorf("the installed def did not learn its checksum: (%q,%v)", got, ok)
			}
		})
}

// §3.6: A PIN MEANS FROZEN. Every updateTarget assertion above passes pin: "",
// so nothing exercised the two halves of rolling detection under a pin — and
// they disagreed. LatestFingerprint is pin-gated (check records it only when no
// pin is in force) while `update` read the live wire, so `check` reported a
// phantom "release rebuilt" from a value the pin froze and `update` downloaded
// and staged a release the user had explicitly frozen.
func TestPinFreezesRollingDetection(t *testing.T) {
	// Half 1 — check/status. The stale latest_fingerprint the pin left behind
	// must not manufacture an update that only `kpm unpin` can clear.
	ps := &state.PackageState{
		InstalledVersion:     "v1.0.0",
		InstalledFingerprint: "p=2026-06-01T10:00:00Z s=100",
		LatestSeen:           "v2.0.0",
		LatestFingerprint:    "p=2026-07-01T10:00:00Z s=200",
	}
	if _, avail := updateTarget(pkgWithRolling(), ps, "v1.0.0"); avail {
		t.Error("a pinned package installed at its pin must not report a rebuild")
	}
	// The same state without the pin is genuinely an update (proves the test
	// is not vacuous — the fingerprints really do differ).
	if _, avail := updateTarget(pkgWithRolling(), ps, ""); !avail {
		t.Error("unpinned, the changed fingerprint must still report an update")
	}
	// A staged pinned package must not re-report either.
	staged := &state.PackageState{
		InstalledVersion:  "v1.0.0",
		StagedVersion:     "v1.0.0",
		StagedFingerprint: "p=2026-06-01T10:00:00Z s=100",
		LatestFingerprint: "p=2026-07-01T10:00:00Z s=200",
	}
	if _, avail := updateTarget(pkgWithRolling(), staged, "v1.0.0"); avail {
		t.Error("a pinned, already-staged package must not report a rebuild")
	}

	// Half 2 — update. The wire says the pinned release was rebuilt; kpm must
	// stage NOTHING. On device the failing behaviour was "Update all" rebooting
	// the Kobo to install a package the user froze.
	a := newTestApp(t)
	setVersion(t, "0.10.0")
	payload := tgzBytes(t, map[string]string{"./mnt/onboard/.adds/kobocloud/x": "v1"})
	rel := &rollingRel{tag: "v1.0.0", publishedAt: "2026-06-01T10:00:00Z", assetName: "KoboRoot.tgz", payload: payload}
	host := rollingServer(t, a, rel)
	registerRollingPkg(t, a, "kobocloud", host, config.UpdateCheckRolling, "KoboRoot.tgz")
	p, err := config.Load(a.paths.PackageFile("kobocloud"), "kobocloud")
	if err != nil {
		t.Fatal(err)
	}
	p.Pin = "v1.0.0"
	if err := config.Save(a.paths.PackageFile("kobocloud"), p); err != nil {
		t.Fatal(err)
	}
	st := a.state.Get("kobocloud")
	st.InstalledVersion = "v1.0.0"
	st.InstalledFingerprint = "p=2026-06-01T10:00:00Z s=" + itoa(len(payload))
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	rel.publishedAt = "2026-07-01T10:00:00Z" // upstream rebuilds the pinned tag

	if checkUpdateFlag(t, a, "kobocloud") {
		t.Error("a pin must hold: check must not report an update for a frozen package")
	}
	var code int
	out := captureStdout(t, func() { code = a.cmdUpdate([]string{"kobocloud"}) })
	if code != exitOK {
		t.Fatalf("update exit = %d, want %d\n%s", code, exitOK, out)
	}
	if _, err := os.Stat(a.paths.StagedTgz()); err == nil {
		t.Error("a pinned package must never be staged from a rebuilt release")
	}
	if got := a.state.Get("kobocloud").StagedVersion; got != "" {
		t.Errorf("staged_version = %q, want empty (the pin froze it)", got)
	}
}

// pkgWithRolling is a minimal rolling def for the pure updateTarget assertions.
func pkgWithRolling() *config.Package {
	return &config.Package{ID: "kobocloud", UpdateCheck: config.UpdateCheckRolling}
}

// logContains reports whether kpm.log holds the given substring.
func logContains(t *testing.T, a *App, want string) bool {
	t.Helper()
	b, err := os.ReadFile(a.paths.LogFile())
	if err != nil {
		return false
	}
	return strings.Contains(string(b), want)
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
