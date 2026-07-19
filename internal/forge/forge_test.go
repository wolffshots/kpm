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
