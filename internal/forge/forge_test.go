package forge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// representative Forgejo/Gitea release JSON (trimmed from a real Codeberg response).
const forgejoLatestJSON = `{
  "tag_name": "v0.5.1",
  "draft": false,
  "prerelease": false,
  "assets": [
    {"name": "KoboRoot.tgz", "size": 123456,
     "browser_download_url": "https://codeberg.org/StrayRose/NickelHardcover/releases/download/v0.5.1/KoboRoot.tgz"}
  ]
}`

// representative GitHub release JSON (trimmed).
const githubTagJSON = `{
  "tag_name": "v1.2.3",
  "draft": false,
  "prerelease": false,
  "assets": [
    {"name": "KoboRoot.tgz", "size": 999,
     "browser_download_url": "https://github.com/o/r/releases/download/v1.2.3/KoboRoot.tgz"}
  ]
}`

func TestForgejoLatestRelease(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/StrayRose/NickelHardcover/releases/latest" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(forgejoLatestJSON))
	}))
	defer srv.Close()

	c := &Client{hc: srv.Client()}
	f := &Forgejo{c: c}
	host := strings.TrimPrefix(srv.URL, "https://")
	rel, err := f.LatestRelease(context.Background(), host, "StrayRose", "NickelHardcover")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v0.5.1" {
		t.Errorf("tag = %q", rel.Tag)
	}
	a, err := rel.MatchAsset("KoboRoot.tgz")
	if err != nil {
		t.Fatal(err)
	}
	if a.Size != 123456 || !strings.HasSuffix(a.DownloadURL, "/v0.5.1/KoboRoot.tgz") {
		t.Errorf("asset = %+v", a)
	}
}

func TestGitHubReleaseByTag(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != githubAccept {
			t.Errorf("missing github Accept header: %q", r.Header.Get("Accept"))
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "kpm/") {
			t.Errorf("missing kpm User-Agent: %q", r.Header.Get("User-Agent"))
		}
		if r.URL.Path != "/repos/o/r/releases/tags/v1.2.3" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(githubTagJSON))
	}))
	defer srv.Close()

	c := &Client{hc: srv.Client()}
	g := &GitHub{c: c, base: srv.URL}
	rel, err := g.ReleaseByTag(context.Background(), "github.com", "o", "r", "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v1.2.3" {
		t.Errorf("tag = %q", rel.Tag)
	}
}

func TestForgejoNotFound(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := &Client{hc: srv.Client()}
	f := &Forgejo{c: c}
	host := strings.TrimPrefix(srv.URL, "https://")
	if _, err := f.LatestRelease(context.Background(), host, "o", "r"); err == nil {
		t.Error("expected error on 404")
	}
}

func TestProbe(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/version" {
			w.Write([]byte(`{"version":"1.21.0"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := &Client{hc: srv.Client()}
	host := strings.TrimPrefix(srv.URL, "https://")
	if !Probe(context.Background(), c, host) {
		t.Error("probe should succeed for a version-answering host")
	}
}

func TestNewClientEmbedsCA(t *testing.T) {
	if len(caPEM) < 1000 {
		t.Fatalf("embedded CA bundle looks empty: %d bytes", len(caPEM))
	}
	if c := NewClient(); c.HTTP() == nil {
		t.Fatal("NewClient produced nil http client")
	}
}

func TestMatchAssetGlob(t *testing.T) {
	rel := Release{Tag: "v1", Assets: []Asset{
		{Name: "KoboRoot.tgz"}, {Name: "SOURCE.tar.gz"},
	}}
	if _, err := rel.MatchAsset("KoboRoot*.tgz"); err != nil {
		t.Errorf("glob should match: %v", err)
	}
	if _, err := rel.MatchAsset("nope*"); err == nil {
		t.Error("expected no-match error")
	}
	rel.Assets = append(rel.Assets, Asset{Name: "KoboRoot-arm.tgz"})
	if _, err := rel.MatchAsset("KoboRoot*.tgz"); err == nil {
		t.Error("expected ambiguous-match error")
	}
}

// ---- INTEGRITY.md §3: the rolling-release fingerprint --------------------

// §3.4: components are emitted only when known, always in ascending key order.
func TestFingerprintComponents(t *testing.T) {
	asset := &Asset{Name: "KoboRoot.tgz", Size: 48213}
	cases := []struct {
		name string
		rel  Release
		a    *Asset
		want string
	}{
		{"both", Release{PublishedAt: "2026-07-01T10:00:00Z"}, asset, "p=2026-07-01T10:00:00Z s=48213"},
		{"asset nil", Release{PublishedAt: "2026-07-01T10:00:00Z"}, nil, "p=2026-07-01T10:00:00Z"},
		{"no published_at", Release{}, asset, "s=48213"},
		{"neither", Release{}, nil, ""},
		{"zero size is unknown", Release{}, &Asset{Name: "K.tgz"}, ""},
	}
	for _, c := range cases {
		if got := c.rel.Fingerprint(c.a); got != c.want {
			t.Errorf("%s: Fingerprint = %q, want %q", c.name, got, c.want)
		}
	}
}

// §3.4: only a key present on BOTH sides with a different value proves a change.
func TestFingerprintChangedIgnoresMissingComponents(t *testing.T) {
	cases := []struct {
		old, cur string
		want     bool
	}{
		{"", "p=A s=1", false},    // no baseline: never an update (baseline adoption)
		{"p=A s=1", "", false},    // forge went quiet: degrade, never fabricate
		{"", "", false},           //
		{"p=A", "p=A s=1", false}, // asset newly resolvable: p still agrees
		{"p=A s=1", "p=A", false}, // asset no longer resolvable: p still agrees
		{"p=A s=1", "p=A s=2", true},
		{"p=A", "p=B", true},
		{"p=A s=1", "p=B s=1", true},
		{"s=1", "p=A", false}, // no shared key at all
	}
	for _, c := range cases {
		if got := FingerprintChanged(c.old, c.cur); got != c.want {
			t.Errorf("FingerprintChanged(%q, %q) = %v, want %v", c.old, c.cur, got, c.want)
		}
	}
}

// §3.2: both decoders populate PublishedAt, and a response omitting it (or
// sending null) yields "" rather than an error.
func TestGitHubDecodesPublishedAt(t *testing.T) {
	const withTS = `{"tag_name":"latest","published_at":"2026-07-01T10:00:00Z",
	  "assets":[{"name":"KoboRoot.tgz","size":48213,"browser_download_url":"https://x/KoboRoot.tgz"}]}`
	const withNull = `{"tag_name":"latest","published_at":null,"assets":[]}`
	const without = `{"tag_name":"latest","assets":[]}`
	for _, c := range []struct{ body, want string }{{withTS, "2026-07-01T10:00:00Z"}, {withNull, ""}, {without, ""}} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(c.body))
		}))
		g := &GitHub{c: &Client{hc: srv.Client()}, base: srv.URL}
		rel, err := g.LatestRelease(context.Background(), "github.com", "o", "r")
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", c.body, err)
		}
		if rel.PublishedAt != c.want {
			t.Errorf("PublishedAt = %q, want %q (body %s)", rel.PublishedAt, c.want, c.body)
		}
	}
}

func TestForgejoDecodesPublishedAt(t *testing.T) {
	const withTS = `{"tag_name":"latest","published_at":"2026-07-01T10:00:00Z",
	  "assets":[{"name":"KoboRoot.tgz","size":48213,"browser_download_url":"https://x/KoboRoot.tgz"}]}`
	const without = `{"tag_name":"latest","assets":[]}`
	for _, c := range []struct{ body, want string }{{withTS, "2026-07-01T10:00:00Z"}, {without, ""}} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(c.body))
		}))
		f := &Forgejo{c: &Client{hc: srv.Client()}}
		rel, err := f.LatestRelease(context.Background(), strings.TrimPrefix(srv.URL, "https://"), "o", "r")
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", c.body, err)
		}
		if rel.PublishedAt != c.want {
			t.Errorf("PublishedAt = %q, want %q (body %s)", rel.PublishedAt, c.want, c.body)
		}
	}
}
