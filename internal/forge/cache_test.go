package forge

// cache_test.go pins the conditional-request path of cache.go: kpm replays the
// previous ETag so a forge that answers 304 costs no rate-limit budget, and a
// cache that is missing, stale, corrupt or simply absent falls back to exactly
// the unconditional GET kpm made before any of this existed.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// githubRollingJSON carries both halves of the rolling-release fingerprint
// (INTEGRITY.md §3.2): published_at and a matched asset size.
const githubRollingJSON = `{
  "tag_name": "nightly",
  "published_at": "2026-07-01T10:00:00Z",
  "assets": [
    {"name": "KoboRoot.tgz", "size": 48213,
     "browser_download_url": "https://github.com/o/r/releases/download/nightly/KoboRoot.tgz"}
  ]
}`

// etagServer answers the way GitHub does: 304 when the client's If-None-Match
// matches the current validator, 200 + ETag otherwise. etag/body are swappable
// mid-test so a release can genuinely change under the cache.
type etagServer struct {
	mu       sync.Mutex
	etag     string
	body     string
	inm      []string // If-None-Match seen per request, in arrival order
	ok       int      // 200s served
	notMod   int      // 304s served
	omitEtag bool     // answer without an ETag (some Forgejo deployments)
	status   int      // override the 200 (404, 403, ...)
}

func newEtagServer(t *testing.T, etag, body string) (*etagServer, *httptest.Server) {
	t.Helper()
	s := &etagServer{etag: etag, body: body}
	srv := httptest.NewTLSServer(http.HandlerFunc(s.handle))
	t.Cleanup(srv.Close)
	return s, srv
}

// noINM is what sent reports for a request that carried no If-None-Match header
// AT ALL — which is what "unconditional" has to mean. An empty If-None-Match is
// still a conditional request on the wire, so distinguishing the two is what
// makes "fall back to a plain GET" a testable claim.
const noINM = "<no If-None-Match>"

func (s *etagServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	given := r.Header.Get("If-None-Match")
	if _, ok := r.Header["If-None-Match"]; ok {
		s.inm = append(s.inm, given)
	} else {
		s.inm = append(s.inm, noINM)
	}
	if s.status != 0 {
		w.WriteHeader(s.status)
		w.Write([]byte(s.body))
		return
	}
	if given != "" && given == s.etag && !s.omitEtag {
		s.notMod++
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if !s.omitEtag {
		w.Header().Set("ETag", s.etag)
	}
	s.ok++
	w.Write([]byte(s.body))
}

// sent returns the If-None-Match header of the nth request (noINM when the
// request carried none), or a sentinel when there was no nth request.
func (s *etagServer) sent(n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n >= len(s.inm) {
		return "<no such request>"
	}
	return s.inm[n]
}

func (s *etagServer) counts() (ok, notMod, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ok, s.notMod, len(s.inm)
}

func (s *etagServer) set(etag, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.etag, s.body = etag, body
}

// The core of the change: the second lookup rides the previous ETag, the forge
// answers 304 (which GitHub does not charge against the 60/hour limit), and kpm
// parses the SAME release out of the cached body — a 304 has no body of its own,
// so this only works because the body is cached alongside its validator.
func TestSecondLookupIsConditionalAndServesTheCachedBody(t *testing.T) {
	s, srv := newEtagServer(t, `"v1"`, githubTagJSON)
	c := &Client{hc: srv.Client(), cacheDir: t.TempDir()}
	g := &GitHub{c: c, base: srv.URL}

	first, err := g.ReleaseByTag(context.Background(), "github.com", "o", "r", "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	second, err := g.ReleaseByTag(context.Background(), "github.com", "o", "r", "v1.2.3")
	if err != nil {
		t.Fatalf("a 304 is not an error: %v", err)
	}
	if got := s.sent(0); got != noINM {
		t.Errorf("the first request must be unconditional, sent If-None-Match %q", got)
	}
	if got := s.sent(1); got != `"v1"` {
		t.Errorf("the second request must carry If-None-Match %q, sent %q", `"v1"`, got)
	}
	if ok, notMod, total := s.counts(); ok != 1 || notMod != 1 || total != 2 {
		t.Errorf("server saw %d x 200, %d x 304, %d total; want 1/1/2", ok, notMod, total)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("the 304 must yield an identical release:\n 200: %+v\n 304: %+v", first, second)
	}
	if first.Tag != "v1.2.3" {
		t.Errorf("tag = %q", first.Tag)
	}
}

// Forgejo goes through the same getJSON, so it gets the same conditional
// request — and its distinct Accept keeps its entries separate from GitHub's.
func TestForgejoLookupIsConditionalToo(t *testing.T) {
	s, srv := newEtagServer(t, `"f1"`, forgejoLatestJSON)
	c := &Client{hc: srv.Client(), cacheDir: t.TempDir()}
	f := &Forgejo{c: c}
	host := strings.TrimPrefix(srv.URL, "https://")

	first, err := f.LatestRelease(context.Background(), host, "o", "r")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.LatestRelease(context.Background(), host, "o", "r")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.sent(1); got != `"f1"` {
		t.Errorf("second request sent If-None-Match %q, want %q", got, `"f1"`)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("304 release differs:\n 200: %+v\n 304: %+v", first, second)
	}
}

// An entry is keyed by URL *and* Accept, so two representations of one URL can
// never be served for each other — the same class of misparse the schema marker
// guards against, one dimension over.
func TestCacheEntryIsKeyedByAcceptAsWellAsURL(t *testing.T) {
	s, srv := newEtagServer(t, `"v1"`, githubTagJSON)
	c := &Client{hc: srv.Client(), cacheDir: t.TempDir()}
	u := srv.URL + "/repos/o/r/releases/latest"

	if _, err := c.getJSON(context.Background(), u, githubAccept); err != nil {
		t.Fatal(err)
	}
	if _, err := c.getJSON(context.Background(), u, "application/json"); err != nil {
		t.Fatal(err)
	}
	if got := s.sent(1); got != noINM {
		t.Errorf("a different Accept must not reuse the entry; sent If-None-Match %q", got)
	}
	if _, err := c.getJSON(context.Background(), u, githubAccept); err != nil {
		t.Fatal(err)
	}
	if got := s.sent(2); got != `"v1"` {
		t.Errorf("the original Accept must still hit its own entry; sent %q", got)
	}
}

// Every way an entry can be unusable falls back to a plain unconditional GET.
// The cache is allowed to make kpm faster; it is never allowed to change an
// answer, so a corrupt or foreign entry must be indistinguishable from no cache
// at all — never a silently stale release and never an empty body.
func TestUnusableCacheEntryFallsBackToUnconditionalGET(t *testing.T) {
	rewrite := func(t *testing.T, path string, f func(*cacheEnvelope)) {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var e cacheEnvelope
		if err := json.Unmarshal(b, &e); err != nil {
			t.Fatal(err)
		}
		f(&e)
		out, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw := func(t *testing.T, path string, b []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name   string
		damage func(t *testing.T, path string)
	}{
		{"entry missing", func(t *testing.T, p string) {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}},
		{"not json at all", func(t *testing.T, p string) { raw(t, p, []byte("{ truncated")) }},
		// A PERFECTLY VALID envelope, just past the read cap: the bound has to hold
		// on its own, not because the file happened to be junk.
		{"oversized", func(t *testing.T, p string) {
			rewrite(t, p, func(e *cacheEnvelope) {
				e.Body = append(e.Body, make([]byte, maxCacheBytes)...)
			})
			if fi, err := os.Stat(p); err != nil || fi.Size() <= maxCacheBytes {
				t.Fatalf("fixture must exceed the cap: size %v, err %v", fi.Size(), err)
			}
		}},
		{"schema from another kpm", func(t *testing.T, p string) {
			rewrite(t, p, func(e *cacheEnvelope) { e.Schema = cacheSchema + 1 })
		}},
		{"recorded for another url", func(t *testing.T, p string) {
			rewrite(t, p, func(e *cacheEnvelope) { e.URL += "-elsewhere" })
		}},
		{"recorded for another accept", func(t *testing.T, p string) {
			rewrite(t, p, func(e *cacheEnvelope) { e.Accept = "text/plain" })
		}},
		{"validator lost", func(t *testing.T, p string) {
			rewrite(t, p, func(e *cacheEnvelope) { e.Etag = "" })
		}},
		{"body lost", func(t *testing.T, p string) {
			rewrite(t, p, func(e *cacheEnvelope) { e.Body = nil })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newEtagServer(t, `"v1"`, githubTagJSON)
			c := &Client{hc: srv.Client(), cacheDir: t.TempDir()}
			u := srv.URL + "/repos/o/r/releases/latest"

			if _, err := c.getJSON(context.Background(), u, githubAccept); err != nil {
				t.Fatal(err)
			}
			tc.damage(t, c.cachePath(u, githubAccept))

			body, err := c.getJSON(context.Background(), u, githubAccept)
			if err != nil {
				t.Fatalf("a damaged entry must degrade to a plain GET, not fail: %v", err)
			}
			if got := s.sent(1); got != noINM {
				t.Errorf("sent If-None-Match %q on a validator it cannot honour", got)
			}
			if string(body) != githubTagJSON {
				t.Errorf("body = %q, want the freshly fetched release", body)
			}
		})
	}
}

// With no cache dir — every non-device caller, and every read-only command (B1)
// — nothing is read, nothing is written, and every request is unconditional.
func TestNoCacheDirIsExactlyTheOldBehaviour(t *testing.T) {
	s, srv := newEtagServer(t, `"v1"`, githubTagJSON)
	dir := t.TempDir()
	c := &Client{hc: srv.Client()} // deliberately no cacheDir
	u := srv.URL + "/repos/o/r/releases/latest"

	for i := 0; i < 2; i++ {
		if _, err := c.getJSON(context.Background(), u, githubAccept); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.sent(1); got != noINM {
		t.Errorf("an uncached client must never send If-None-Match, sent %q", got)
	}
	if ok, notMod, _ := s.counts(); ok != 2 || notMod != 0 {
		t.Errorf("server saw %d x 200, %d x 304; want 2/0", ok, notMod)
	}
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) != 0 {
		t.Errorf("an uncached client must write nothing (%d entries, err %v)", len(ents), err)
	}
	if p := c.cachePath(u, githubAccept); p != "" {
		t.Errorf("cachePath with no dir = %q, want empty", p)
	}
}

// A forge that sends no ETag (some Forgejo deployments) is not cacheable, and
// any entry from when it DID send one is removed rather than kept: replaying a
// validator the server has stopped issuing is how a cache goes stale.
func TestServerWithoutEtagStoresNothingAndDropsTheOldEntry(t *testing.T) {
	s, srv := newEtagServer(t, `"v1"`, githubTagJSON)
	dir := t.TempDir()
	c := &Client{hc: srv.Client(), cacheDir: dir}
	u := srv.URL + "/repos/o/r/releases/latest"

	if _, err := c.getJSON(context.Background(), u, githubAccept); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.cachePath(u, githubAccept)); err != nil {
		t.Fatalf("a 200 with an ETag must be cached: %v", err)
	}

	s.mu.Lock()
	s.omitEtag = true
	s.mu.Unlock()
	if _, err := c.getJSON(context.Background(), u, githubAccept); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.cachePath(u, githubAccept)); !os.IsNotExist(err) {
		t.Errorf("an ETag-less 200 must leave no entry behind, stat err = %v", err)
	}
	// And the run after that is unconditional again — today's behaviour exactly.
	if _, err := c.getJSON(context.Background(), u, githubAccept); err != nil {
		t.Fatal(err)
	}
	if got := s.sent(2); got != noINM {
		t.Errorf("sent If-None-Match %q against a forge that issues none", got)
	}
}

// Caching must not swallow any of getJSON's existing outcomes: 404 and the
// throttling responses still map to their sentinels, and an UNSOLICITED 304 (a
// forge answering a request kpm never made conditional) is still the permanent
// error it always was, never an empty body.
func TestCachingPreservesTheExistingErrorSemantics(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
		substr string
	}{
		{name: "not found", status: http.StatusNotFound, want: errNotFound},
		{name: "rate limited", status: http.StatusForbidden, body: "API rate limit exceeded", want: errRateLimited},
		{name: "too many requests", status: http.StatusTooManyRequests, want: errRateLimited},
		{name: "unsolicited 304", status: http.StatusNotModified, substr: "http 304"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newEtagServer(t, `"v1"`, tc.body)
			s.status = tc.status
			c := &Client{hc: srv.Client(), cacheDir: t.TempDir()}
			_, err := c.getJSON(context.Background(), srv.URL+"/x", githubAccept)
			if tc.want != nil && err != tc.want {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.substr != "" && (err == nil || !strings.Contains(err.Error(), tc.substr)) {
				t.Fatalf("err = %v, want one containing %q", err, tc.substr)
			}
			if _, serr := os.Stat(c.cachePath(srv.URL+"/x", githubAccept)); !os.IsNotExist(serr) {
				t.Errorf("a failed request must cache nothing, stat err = %v", serr)
			}
		})
	}
}

// The highest-risk interaction (INTEGRITY.md §3): update_check = "rolling"
// compares published_at + asset size, so a body served from cache must produce a
// BYTE-IDENTICAL fingerprint — and, just as important, a cache hit must not be
// able to hide a rebuilt release. The forge's ETag is the thing that changes when
// the release does, which is what makes both halves hold.
func TestRollingFingerprintSurvivesA304AndStillSeesARebuild(t *testing.T) {
	s, srv := newEtagServer(t, `"r1"`, githubRollingJSON)
	c := &Client{hc: srv.Client(), cacheDir: t.TempDir()}
	g := &GitHub{c: c, base: srv.URL}

	fp := func(t *testing.T) string {
		t.Helper()
		rel, err := g.LatestRelease(context.Background(), "github.com", "o", "r")
		if err != nil {
			t.Fatal(err)
		}
		a, err := rel.MatchAsset("KoboRoot.tgz")
		if err != nil {
			t.Fatal(err)
		}
		return rel.Fingerprint(&a)
	}

	live := fp(t)
	if live != "p=2026-07-01T10:00:00Z s=48213" {
		t.Fatalf("fingerprint from the live fetch = %q", live)
	}
	cachedFP := fp(t)
	if _, notMod, _ := s.counts(); notMod != 1 {
		t.Fatalf("the second lookup should have been a 304; server saw %d", notMod)
	}
	if cachedFP != live {
		t.Errorf("a 304 changed the rolling fingerprint: %q -> %q", live, cachedFP)
	}

	// The release is rebuilt: same tag, new published_at and asset size, new
	// validator. The cache must not mask it.
	rebuilt := strings.NewReplacer(
		"2026-07-01T10:00:00Z", "2026-07-20T08:00:00Z",
		"48213", "51999",
	).Replace(githubRollingJSON)
	s.set(`"r2"`, rebuilt)

	after := fp(t)
	if after == live {
		t.Fatalf("a cache hit masked a real rolling update (fingerprint still %q)", after)
	}
	if after != "p=2026-07-20T08:00:00Z s=51999" {
		t.Errorf("fingerprint after the rebuild = %q", after)
	}
	if !FingerprintChanged(live, after) {
		t.Errorf("FingerprintChanged(%q, %q) must be true", live, after)
	}
	// ...and the new validator is what the next run replays.
	if _, err := g.LatestRelease(context.Background(), "github.com", "o", "r"); err != nil {
		t.Fatal(err)
	}
	if got := s.sent(3); got != `"r2"` {
		t.Errorf("the rebuilt release's ETag must replace the old one, sent %q", got)
	}
}
