package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"kpm/internal/config"
	"kpm/internal/forge"
)

// shippedSelfToml is the sourceless kpm.toml the release tarball ships (build/
// package.go selfToml). Writing it over an adopted def reproduces exactly what
// Nickel does when it unpacks a kpm self-update (SELF-SOURCE motivation).
const shippedSelfToml = `name = "kpm"
asset = "KoboRoot.tgz"
pin = ""
`

// clobberSelfToml overwrites kpm.toml with the shipped, sourceless template,
// simulating a self-update tarball unpack.
func clobberSelfToml(t *testing.T, a *App) {
	t.Helper()
	if err := os.WriteFile(a.paths.PackageFile(selfID), []byte(shippedSelfToml), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stubForge is a fake Forgejo instance that answers the "latest release"
// endpoint and records the owner/repo it was contacted with, so a test can
// prove which source the resolve actually used.
//
// The spec's example uses github.com/o/r, but the GitHub client pins its API
// base to api.github.com and rejects any host except "github.com", so it cannot
// be driven against a local stub through forgeFor. Forgejo takes the host from
// the source string, which is exactly the value under test here; the owner/repo
// assertions ("o"/"r") are unchanged.
type stubForge struct {
	host   string
	client *forge.Client
	owner  string
	repo   string
	hits   int
}

func newStubForge(t *testing.T, tag string) *stubForge {
	t.Helper()
	s := &stubForge{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /api/v1/repos/<owner>/<repo>/releases/latest
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 5 && parts[2] == "repos" {
			s.owner, s.repo = parts[3], parts[4]
		}
		s.hits++
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[]}`, tag)
	}))
	t.Cleanup(srv.Close)
	s.host = strings.TrimPrefix(srv.URL, "https://")
	s.client = forge.NewClientWithHTTP(srv.Client())
	return s
}

// adoptInState records an adoption identity in state.json the way install
// --adopt now does (source/forge durable, kpm.toml left sourceless).
func adoptInState(t *testing.T, a *App, source, forgeKind string) {
	t.Helper()
	ps := a.state.Get(selfID)
	ps.Source = source
	ps.Forge = forgeKind
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
}

// rawTOML decodes a written def so a test can assert on key presence/absence.
func rawTOML(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if _, err := toml.Decode(string(b), &m); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return m
}

// §5.1: a sourceless kpm.toml plus a state-resident source resolves through the
// overlay — configured, effective* read from state, and the forge is contacted
// with the state source's host/owner/repo.
func TestSelfSourceOverlayResolves(t *testing.T) {
	a := newTestApp(t)
	registerSelf(t, a) // ships with source = ""
	stub := newStubForge(t, "v9.9.9")
	a.client = stub.client
	adoptInState(t, a, stub.host+"/o/r", "forgejo")

	p, err := a.loadPackage(selfID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != "" {
		t.Fatalf("kpm.toml should stay sourceless, got %q", p.Source)
	}
	if !a.configured(p) {
		t.Error("adopted kpm (source in state) must be configured")
	}
	if got, want := a.effectiveSource(p), stub.host+"/o/r"; got != want {
		t.Errorf("effectiveSource = %q, want %q", got, want)
	}
	if got := a.effectiveForge(p); got != "forgejo" {
		t.Errorf("effectiveForge = %q, want forgejo", got)
	}

	tag, err := a.resolveTag(p)
	if err != nil {
		t.Fatalf("resolveTag: %v", err)
	}
	if tag != "v9.9.9" {
		t.Errorf("tag = %q, want v9.9.9", tag)
	}
	if stub.owner != "o" || stub.repo != "r" {
		t.Errorf("forge contacted with owner/repo %q/%q, want o/r", stub.owner, stub.repo)
	}
}

// §5.2: THE REGRESSION. An adopted kpm whose kpm.toml is then overwritten by a
// self-update tarball (sourceless template) must stay configured and resolvable,
// because the adoption identity lives in state.json, not the clobbered TOML.
func TestSelfSourceSurvivesTarballClobber(t *testing.T) {
	a := newTestApp(t)
	registerSelf(t, a)
	stub := newStubForge(t, "v0.6.0")
	a.client = stub.client

	// Adopt: identity goes to state, kpm.toml stays sourceless.
	adoptInState(t, a, stub.host+"/o/r", "forgejo")
	a.state.Get(selfID).InstalledVersion = "v0.5.0"

	// A self-update unpacks and overwrites kpm.toml with the shipped template.
	clobberSelfToml(t, a)

	pkgs, err := a.loadPackages()
	if err != nil {
		t.Fatal(err)
	}
	var kpmPkg *config.Package
	for _, p := range pkgs {
		if p.ID == selfID {
			kpmPkg = p
		}
	}
	if kpmPkg == nil {
		t.Fatal("kpm should still be registered after the clobber")
	}
	if kpmPkg.Source != "" || kpmPkg.Configured() {
		t.Fatalf("the clobbered TOML must be sourceless: %+v", kpmPkg)
	}

	// status must NOT regress to "self-update not configured" (the reported bug).
	status := a.buildStatus(pkgs)
	if strings.Contains(status, "self-update not configured") {
		t.Errorf("clobbered kpm must stay configured via state:\n%s", status)
	}
	if !a.configured(kpmPkg) {
		t.Error("configured() must read the state source after a clobber")
	}

	// check must still resolve it against the adopted source.
	tag, err := a.resolveTag(kpmPkg)
	if err != nil {
		t.Fatalf("resolveTag after clobber: %v", err)
	}
	if tag != "v0.6.0" {
		t.Errorf("tag = %q, want v0.6.0", tag)
	}
	if stub.owner != "o" || stub.repo != "r" {
		t.Errorf("forge contacted with %q/%q, want o/r", stub.owner, stub.repo)
	}

	// update must not skip it as unconfigured either.
	if _, skip, err := a.resolveAndDownload(kpmPkg); err != nil || skip {
		// A no-asset release makes MatchAsset fail; what matters is that it was
		// NOT silently skipped as unconfigured (F7).
		if skip {
			t.Error("update must not skip an adopted kpm as unconfigured")
		}
	}
}

// §5.3: install --adopt routes kpm's source/forge into state.json and leaves the
// written kpm.toml with no source/forge keys, so the next tarball clobber is a
// no-op (SELF-SOURCE §5).
func TestSelfAdoptWritesStateNotToml(t *testing.T) {
	a := newTestApp(t)
	registerSelf(t, a)
	seedRegistry(t, a, "main", `schema_version = 1
[packages.kpm]
name = "kpm"
source = "github.com/wolffshots/kpm"
forge = "github"
asset = "KoboRoot.tgz"
`)

	var code int
	captureStdout(t, func() { code = a.cmdInstall([]string{"kpm", "--adopt", "--yes"}) })
	if code != exitOK {
		t.Fatalf("install kpm --adopt --yes exit %d", code)
	}

	// The adoption identity is durable in state.
	ps := a.state.Get(selfID)
	if ps.Source != "github.com/wolffshots/kpm" {
		t.Errorf("state source = %q, want github.com/wolffshots/kpm", ps.Source)
	}
	if ps.Forge != "github" {
		t.Errorf("state forge = %q, want github", ps.Forge)
	}

	// ...and absent from the tarball-clobbered TOML.
	m := rawTOML(t, a.paths.PackageFile(selfID))
	if v, ok := m["source"]; ok {
		t.Errorf("kpm.toml must carry no source key, got %v", v)
	}
	if v, ok := m["forge"]; ok {
		t.Errorf("kpm.toml must carry no forge key, got %v", v)
	}
	// Release-invariant fields still ship in the TOML.
	if m["asset"] != "KoboRoot.tgz" {
		t.Errorf("asset should stay in kpm.toml: %v", m["asset"])
	}

	// The overlay makes it configured despite the sourceless TOML.
	p, err := a.loadPackage(selfID)
	if err != nil {
		t.Fatal(err)
	}
	if !a.configured(p) {
		t.Error("adopted kpm must be configured through the state overlay")
	}
	if got := a.effectiveSource(p); got != "github.com/wolffshots/kpm" {
		t.Errorf("effectiveSource = %q", got)
	}
}

// §5.4 (§2.1): a device adopted under <=0.4.1 still carries the source in
// kpm.toml. seedSelf migrates it into state before the next self-update wipes
// it, logs INFO, and never overwrites a source already in state.
func TestSeedSelfMigratesSourceToState(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.5.0") // migration never runs under a "dev" binary
	adopted := &config.Package{
		Name: "kpm", Source: "github.com/wolffshots/kpm", Forge: "github", Asset: "KoboRoot.tgz",
	}
	if err := config.Save(a.paths.PackageFile(selfID), adopted); err != nil {
		t.Fatal(err)
	}
	if err := a.seedSelf(); err != nil {
		t.Fatal(err)
	}
	ps := a.state.Get(selfID)
	if ps.Source != "github.com/wolffshots/kpm" || ps.Forge != "github" {
		t.Errorf("seedSelf should migrate source/forge to state: %+v", ps)
	}
	log, _ := os.ReadFile(a.paths.LogFile())
	if !strings.Contains(string(log), "self source migrated to state (github.com/wolffshots/kpm)") {
		t.Errorf("missing migration INFO line; log = %q", log)
	}

	// An existing state source is authoritative and must not be overwritten.
	ps.Source = "codeberg.org/other/kpm"
	ps.Forge = "forgejo"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	if err := a.seedSelf(); err != nil {
		t.Fatal(err)
	}
	if got := a.state.Get(selfID).Source; got != "codeberg.org/other/kpm" {
		t.Errorf("state source must not be overwritten, got %q", got)
	}
}

// §5.4 (§2.1): the migration is a no-op under a "dev" binary, so a host sandbox
// running against a copied device tree cannot pollute state.
func TestSeedSelfMigrationSkippedForDevBinary(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "dev")
	adopted := &config.Package{
		Name: "kpm", Source: "github.com/wolffshots/kpm", Forge: "github", Asset: "KoboRoot.tgz",
	}
	if err := config.Save(a.paths.PackageFile(selfID), adopted); err != nil {
		t.Fatal(err)
	}
	if err := a.seedSelf(); err != nil {
		t.Fatal(err)
	}
	if got := a.state.Get(selfID).Source; got != "" {
		t.Errorf("dev binary must not migrate the self source, got %q", got)
	}
}

// §5.5: a never-adopted kpm (both TOML and state empty) stays unconfigured —
// status says so, check/update skip it silently, and nothing churns in state.
func TestSelfNeverAdoptedStaysUnconfigured(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.5.0")
	registerSelf(t, a) // source = "", and no state source

	// Pre-seed the version so seedSelf's version branch is a no-op too, isolating
	// the source migration for the churn assertion.
	a.state.Get(selfID).InstalledVersion = "0.5.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(a.paths.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.seedSelf(); err != nil {
		t.Fatalf("never-adopted kpm must not error: %v", err)
	}
	after, err := os.ReadFile(a.paths.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("never-adopted kpm must not churn state:\nbefore=%s\nafter=%s", before, after)
	}

	p, err := a.loadPackage(selfID)
	if err != nil {
		t.Fatal(err)
	}
	if a.configured(p) {
		t.Error("never-adopted kpm must be unconfigured")
	}
	status := a.buildStatus([]*config.Package{p})
	if !strings.Contains(status, "self-update not configured") {
		t.Errorf("status should report the unconfigured self-update:\n%s", status)
	}
	// update skips it silently, no error (F7).
	if _, skip, err := a.resolveAndDownload(p); err != nil || !skip {
		t.Errorf("unconfigured kpm should be skipped without error (skip=%v err=%v)", skip, err)
	}
}

// §5.6: the selfID special-casing must not leak. A normal package resolves its
// source/forge from its own TOML exactly as before, and state source/forge
// fields (which it never has in practice) are ignored for it.
func TestNonSelfPackageResolvesFromToml(t *testing.T) {
	a := newTestApp(t)
	p := &config.Package{
		ID: "nickelmenu", Name: "NickelMenu",
		Source: "github.com/pgaskin/NickelMenu", Forge: "github", Asset: "KoboRoot.tgz",
	}
	if got := a.effectiveSource(p); got != "github.com/pgaskin/NickelMenu" {
		t.Errorf("effectiveSource = %q, want the TOML source", got)
	}
	if got := a.effectiveForge(p); got != "github" {
		t.Errorf("effectiveForge = %q, want github", got)
	}
	if !a.configured(p) {
		t.Error("a package with a TOML source must be configured")
	}

	// Even if its state somehow carried source/forge, the TOML still wins: the
	// override is scoped to selfID only.
	ps := a.state.Get("nickelmenu")
	ps.Source = "evil.example/attacker/pkg"
	ps.Forge = "forgejo"
	if got := a.effectiveSource(p); got != "github.com/pgaskin/NickelMenu" {
		t.Errorf("state must not override a non-kpm source, got %q", got)
	}
	if got := a.effectiveForge(p); got != "github" {
		t.Errorf("state must not override a non-kpm forge, got %q", got)
	}

	// An empty-source non-kpm package stays unconfigured regardless of kpm state.
	a.state.Get(selfID).Source = "github.com/wolffshots/kpm"
	empty := &config.Package{ID: "other", Source: ""}
	if a.configured(empty) {
		t.Error("kpm's state source must not configure another package")
	}
}

// searchSelfRow runs search --json and returns the one kpm self row, failing if
// it is absent — a small helper for the self_configured assertions below.
func searchSelfRow(t *testing.T, a *App) map[string]any {
	t.Helper()
	out := captureStdout(t, func() { a.cmdSearch([]string{"--json"}) })
	var payload struct {
		Packages []map[string]any `json:"packages"`
	}
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &payload); err != nil {
		t.Fatal(err)
	}
	for _, p := range payload.Packages {
		if p["id"] == selfID {
			return p
		}
	}
	t.Fatalf("no kpm self row in payload: %s", out)
	return nil
}

// kpm-self-enrol-plan §1: search's self_configured tracks the state overlay —
// false for a never-adopted kpm, true once adopted, and it SURVIVES a tarball
// clobber (the sourceless kpm.toml rewrite) because the source lives in state.
func TestSearchSelfConfiguredReflectsAdoption(t *testing.T) {
	a := newTestApp(t)
	registerSelf(t, a) // sourceless kpm.toml, no state source

	// Never adopted → is_self true, self_configured false.
	row := searchSelfRow(t, a)
	if row["is_self"] != true || row["self_configured"] != false {
		t.Errorf("never-adopted: is_self/self_configured = %v/%v, want true/false", row["is_self"], row["self_configured"])
	}

	// Adopt (identity → state) → self_configured true.
	adoptInState(t, a, "github.com/wolffshots/kpm", "github")
	row = searchSelfRow(t, a)
	if row["self_configured"] != true {
		t.Errorf("after adopt: self_configured = %v, want true", row["self_configured"])
	}

	// A self-update clobbers kpm.toml back to the sourceless template; the state
	// source survives, so self_configured stays true.
	clobberSelfToml(t, a)
	row = searchSelfRow(t, a)
	if row["self_configured"] != true {
		t.Errorf("after tarball clobber: self_configured = %v, want true (state source survives)", row["self_configured"])
	}
}

// kpm-self-enrol-plan §1: a non-kpm row never carries is_self/self_configured
// true — the self signal is scoped to the kpm id only.
func TestSearchSelfConfiguredFalseForNonKpm(t *testing.T) {
	a := newTestApp(t)
	if err := config.Save(a.paths.PackageFile("nickelmenu"), &config.Package{
		Name: "NickelMenu", Source: "github.com/pgaskin/NickelMenu", Forge: "github", Asset: "KoboRoot.tgz",
	}); err != nil {
		t.Fatal(err)
	}
	// Even if kpm's state carries a source, it must not leak onto another row.
	a.state.Get(selfID).Source = "github.com/wolffshots/kpm"
	out := captureStdout(t, func() { a.cmdSearch([]string{"--json"}) })
	var payload struct {
		Packages []map[string]any `json:"packages"`
	}
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &payload); err != nil {
		t.Fatal(err)
	}
	for _, p := range payload.Packages {
		if p["id"] == "nickelmenu" {
			if p["is_self"] != false || p["self_configured"] != false {
				t.Errorf("non-kpm row is_self/self_configured = %v/%v, want false/false", p["is_self"], p["self_configured"])
			}
		}
	}
}

// SELF-SOURCE §6 follow-up: sync compares the LOCAL def's hash against the
// registry's. kpm's local def is deliberately sourceless (the adoption lives in
// state), so a naive DefFromPackage would hash a blank source/forge, never match
// the registry def, and report an adopted kpm as permanently drifted — skipping
// it from every sync. The local def must be compared using the effective
// (state-resident) source/forge.
func TestSyncSelfNotFalselyDrifted(t *testing.T) {
	a := newTestApp(t)
	registerSelf(t, a)
	seedRegistry(t, a, "main", `schema_version = 1
[packages.kpm]
name = "kpm"
source = "github.com/wolffshots/kpm"
forge = "github"
asset = "KoboRoot.tgz"
`)
	var code int
	captureStdout(t, func() { code = a.cmdInstall([]string{"kpm", "--adopt", "--yes"}) })
	if code != exitOK {
		t.Fatalf("adopt exit %d", code)
	}

	out := captureStdout(t, func() { code = a.cmdSync(nil) })
	if code != exitOK {
		t.Fatalf("sync exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "drift") {
		t.Errorf("adopted kpm must not read as drifted:\n%s", out)
	}
	if !strings.Contains(out, "1 up to date") {
		t.Errorf("adopted kpm should be up to date:\n%s", out)
	}
}
