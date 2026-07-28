package main

// check_cost_test.go pins what `kpm check` COSTS, which is a correctness
// property here rather than a nicety: GitHub allows 60 unauthenticated
// requests/hour/IP and check spends one per package, so a few UI Refresh taps
// used to run the device into a 403 — and getJSON maps that to errRateLimited
// and returns immediately, so a throttled check got *faster* while silently
// degrading every update badge.
//
// Two guards, one per half of the fix:
//   - revalidation: a repeat check rides the previous ETag and a 304 costs no
//     rate-limit budget, without moving a single badge.
//   - fan-out: the lookups run concurrently, bounded, and the run they produce
//     is indistinguishable from the sequential one.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"kpm/internal/config"
	"kpm/internal/forge"
)

// countingForge is a fake Forgejo that behaves like a real one for conditional
// requests (304 on a matching If-None-Match) and records what it was asked:
// how many 200s and 304s it served, and the high-water mark of simultaneously
// in-flight release lookups.
type countingForge struct {
	mu        sync.Mutex
	etag      string
	ok        int
	notMod    int
	inFlight  int
	maxFlight int
	fail      map[string]bool // repos that 404 their release lookup
	// hold, when non-zero, parks every release lookup until that many are in
	// flight AT ONCE — then waits holdGrace longer, so any FURTHER concurrent
	// lookups pile up and get counted before the barrier opens. Without the grace
	// an unbounded fan-out would be indistinguishable from a bounded one: the
	// barrier would open the instant the mark was reached and the rest would
	// trickle through one at a time. A sequential run never reaches the mark at
	// all and pays the fallback timer instead.
	hold   int
	arming bool
	parked chan struct{}
}

const (
	holdGrace    = 250 * time.Millisecond
	holdFallback = 5 * time.Second
)

const forgeRelJSON = `{"tag_name":"v2.0.0","published_at":"2026-07-01T10:00:00Z",` +
	`"assets":[{"name":"KoboRoot.tgz","size":4242,"browser_download_url":"https://%s/download/KoboRoot.tgz"}]}`

func newCountingForge(t *testing.T, a *App, etag string) (*countingForge, string) {
	t.Helper()
	f := &countingForge{etag: etag, fail: map[string]bool{}, parked: make(chan struct{})}
	srv := httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	a.client = forge.NewClientWithHTTP(srv.Client())
	return f, strings.TrimPrefix(srv.URL, "https://")
}

func (f *countingForge) handle(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, "/releases/") {
		w.WriteHeader(http.StatusOK) // network probe
		return
	}
	// path: /api/v1/repos/<owner>/<repo>/releases/latest
	parts := strings.Split(r.URL.Path, "/")
	repo := ""
	for i, p := range parts {
		if p == "repos" && i+2 < len(parts) {
			repo = parts[i+2]
		}
	}

	f.mu.Lock()
	f.inFlight++
	if f.inFlight > f.maxFlight {
		f.maxFlight = f.inFlight
	}
	given, etag, fails := r.Header.Get("If-None-Match"), f.etag, f.fail[repo]
	reached := f.hold > 0 && f.inFlight >= f.hold
	f.mu.Unlock()
	if reached {
		f.arm()
	}
	if f.hold > 0 {
		select {
		case <-f.parked:
		case <-time.After(holdFallback):
		}
	}
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()

	switch {
	case fails:
		http.NotFound(w, r)
	case given != "" && given == etag:
		f.mu.Lock()
		f.notMod++
		f.mu.Unlock()
		w.WriteHeader(http.StatusNotModified)
	default:
		f.mu.Lock()
		f.ok++
		f.mu.Unlock()
		w.Header().Set("ETag", etag)
		fmt.Fprintf(w, forgeRelJSON, r.Host)
	}
}

// arm opens the barrier once, holdGrace after the mark is first reached.
func (f *countingForge) arm() {
	f.mu.Lock()
	already := f.arming
	f.arming = true
	f.mu.Unlock()
	if already {
		return
	}
	go func() {
		time.Sleep(holdGrace)
		close(f.parked)
	}()
}

func (f *countingForge) stats() (ok, notMod, maxFlight int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ok, f.notMod, f.maxFlight
}

// checkPayload runs `check --json` and returns the decoded payload.
func checkPayload(t *testing.T, a *App) jsonCheckPayload {
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
	return payload
}

// A repeat check revalidates instead of re-downloading: the second run's lookup
// carries the previous ETag, the forge answers 304, and every badge — latest,
// update, and the rolling fingerprint the badge for a permanent-tag package is
// computed from — is byte-identical to the run that fetched a body.
func TestRepeatCheckRevalidatesWithoutMovingABadge(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.11.0")
	f, host := newCountingForge(t, a, `"rel-1"`)
	a.client.SetCacheDir(a.paths.CacheDir())
	registerRollingPkg(t, a, "kobocloud", host, config.UpdateCheckRolling, "KoboRoot.tgz")

	first := checkPayload(t, a)
	firstFP := a.state.Get("kobocloud").LatestFingerprint
	if firstFP == "" {
		t.Fatal("the first check must record a rolling fingerprint")
	}
	second := checkPayload(t, a)
	secondFP := a.state.Get("kobocloud").LatestFingerprint

	ok, notMod, _ := f.stats()
	if ok != 1 || notMod != 1 {
		t.Errorf("forge served %d x 200 and %d x 304; want 1 and 1 — the repeat check must revalidate", ok, notMod)
	}
	if !sameJSON(t, first.Packages, second.Packages) {
		t.Errorf("a 304 moved a badge:\n 200: %+v\n 304: %+v", first.Packages, second.Packages)
	}
	if secondFP != firstFP {
		t.Errorf("a 304 changed the rolling fingerprint: %q -> %q", firstFP, secondFP)
	}
}

// sameJSON compares two check payloads by their JSON rendering,
// which is the surface the hook actually consumes (JSON-OUTPUT.md §2.2) and
// dodges pointer identity in jsonCheckPkg.
func sameJSON(t *testing.T, a, b any) bool {
	t.Helper()
	x, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(x) == string(y)
}

// The fan-out is real and it is bounded. The barrier makes both directions
// decidable without a stopwatch: a sequential loop never gets a second lookup in
// flight, and an unbounded one parks every package at once.
func TestCheckFansOutBoundedByCheckConcurrency(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.11.0")
	f, host := newCountingForge(t, a, `"rel-1"`)
	f.hold = checkConcurrency
	n := checkConcurrency*2 + 1
	for i := 0; i < n; i++ {
		fakeForgePkg(t, a, host, fmt.Sprintf("pkg-%02d", i), "v1.0.0")
	}

	captureStdout(t, func() {
		if code := a.cmdCheck(nil); code != exitOK {
			t.Errorf("check exit = %d, want %d", code, exitOK)
		}
	})

	_, _, maxFlight := f.stats()
	if maxFlight < checkConcurrency {
		t.Errorf("only %d lookup(s) were ever in flight at once, want %d — the lookups are not fanning out",
			maxFlight, checkConcurrency)
	}
	if maxFlight > checkConcurrency {
		t.Errorf("%d lookups in flight at once, want at most %d — the fan-out is unbounded",
			maxFlight, checkConcurrency)
	}
}

// The fan-out must be invisible. checkPkgs order is a contract (JSON-OUTPUT.md
// §2.2: per-package latest, in pkgs order) and the hook renders the list as
// given, so a concurrent run must produce the same order, the same counters, the
// same exit code and the same log-line order as the sequential loop it replaced
// — including the mixed run where some packages fail.
func TestCheckFanOutIsIndistinguishableFromSequential(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.11.0")
	f, host := newCountingForge(t, a, `"rel-1"`)
	var ids []string
	for i := 0; i < checkConcurrency*2+1; i++ {
		id := fmt.Sprintf("pkg-%02d", i)
		ids = append(ids, id)
		fakeForgePkg(t, a, host, id, "v1.0.0")
	}
	// Every third package 404s its release lookup: a mixed run, so the partial
	// exit accounting is exercised and not just the happy path.
	var wantFailed, wantAvail []string
	for i, id := range ids {
		if i%3 == 0 {
			f.fail[id] = true
			wantFailed = append(wantFailed, id)
		} else {
			wantAvail = append(wantAvail, id)
		}
	}

	var code int
	var stderr string
	out := captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = a.cmdCheck([]string{"--json"}) })
	})
	if code != exitPartial {
		t.Fatalf("a mixed run must exit %d (partial), got %d\n%s\n%s", exitPartial, code, out, stderr)
	}

	var payload jsonCheckPayload
	if err := json.Unmarshal([]byte(lastJSON(t, out)), &payload); err != nil {
		t.Fatal(err)
	}
	var gotIDs []string
	failedIDs := map[string]bool{}
	for _, p := range payload.Packages {
		gotIDs = append(gotIDs, p.ID)
		if p.Error != nil {
			failedIDs[p.ID] = true
			if p.Latest != nil || p.Update {
				t.Errorf("%s failed but carries latest=%v update=%v", p.ID, p.Latest, p.Update)
			}
		} else if p.Latest == nil || *p.Latest != "v2.0.0" || !p.Update {
			t.Errorf("%s: latest=%v update=%v, want v2.0.0/true", p.ID, p.Latest, p.Update)
		}
	}
	if !sameJSON(t, gotIDs, ids) {
		t.Errorf("checkPkgs order = %v, want pkgs order %v", gotIDs, ids)
	}
	for _, id := range wantFailed {
		if !failedIDs[id] {
			t.Errorf("%s should have failed", id)
		}
		if !strings.Contains(stderr, "kpm check: "+id+": ") {
			t.Errorf("stderr must name the failure of %s:\n%s", id, stderr)
		}
	}
	if len(failedIDs) != len(wantFailed) {
		t.Errorf("%d packages failed, want %d", len(failedIDs), len(wantFailed))
	}
	// Log lines are written from the ordered apply pass, never from a worker, so
	// they arrive in package order too.
	logBytes, err := os.ReadFile(a.paths.LogFile())
	if err != nil {
		t.Fatal(err)
	}
	var logged []string
	for _, line := range strings.Split(string(logBytes), "\n") {
		for _, id := range ids {
			if strings.Contains(line, "CHECK") && strings.Contains(line, id) {
				logged = append(logged, id)
			}
		}
	}
	if !sameJSON(t, logged, ids) {
		t.Errorf("kpm.log CHECK lines are out of order:\n got %v\nwant %v", logged, ids)
	}
	// Every package still got exactly one lookup — a fan-out that dropped or
	// duplicated one would still satisfy the ordering assertions above.
	ok, _, _ := f.stats()
	if ok != len(wantAvail) {
		t.Errorf("forge served %d successful lookups, want %d", ok, len(wantAvail))
	}
}

// B1 and the wiring in one: a mutating command gets a response cache, a
// read-only one does not — a cache entry is a write, and a read-only command
// writes nothing.
func TestForgeCacheDirIsWiredForMutatingCommandsOnly(t *testing.T) {
	t.Setenv("KPM_ROOT", t.TempDir())

	ro, err := newApp(false)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.releaseLock()
	if got := ro.client.CacheDir(); got != "" {
		t.Errorf("a read-only command must write no cache entries (B1), cache dir = %q", got)
	}

	mu, err := newApp(true)
	if err != nil {
		t.Fatal(err)
	}
	defer mu.releaseLock()
	if got, want := mu.client.CacheDir(), mu.paths.CacheDir(); got != want {
		t.Errorf("mutating command cache dir = %q, want %q", got, want)
	}
}
