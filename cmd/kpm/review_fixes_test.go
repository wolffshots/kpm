package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kpm/internal/config"
	"kpm/internal/forge"
	"kpm/internal/state"
)

// twoEndpointForge serves distinct tags for the "latest" and "by-tag" release
// endpoints so a test can tell which path resolveRelease took.
func twoEndpointForge(t *testing.T, latestTag string) (*App, *config.Package) {
	t.Helper()
	a := newTestApp(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/releases/latest"):
			fmt.Fprintf(w, `{"tag_name":%q,"assets":[]}`, latestTag)
		case strings.Contains(r.URL.Path, "/releases/tags/"):
			// Echo the requested tag, so reusing a stale latest_seen is visible.
			parts := strings.Split(r.URL.Path, "/")
			fmt.Fprintf(w, `{"tag_name":%q,"assets":[]}`, parts[len(parts)-1])
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	a.client = forge.NewClientWithHTTP(srv.Client())
	host := strings.TrimPrefix(srv.URL, "https://")
	p := &config.Package{ID: "pkg", Name: "Pkg", Source: host + "/o/pkg", Forge: "forgejo", Asset: "KoboRoot.tgz"}
	return a, p
}

// M2: update's freshness window must key on the package's OWN last_checked, not
// the global last_check. A package that failed (or was skipped by) the last
// check keeps a stale latest_seen; update must re-resolve it rather than reuse
// the stale tag just because some OTHER package refreshed the global timestamp.
func TestUpdateFreshnessIsPerPackage(t *testing.T) {
	a, p := twoEndpointForge(t, "v2")
	ps := a.state.Get("pkg")
	ps.InstalledVersion = "v1"
	ps.LatestSeen = "v1"                    // stale, from an old successful check
	ps.LastChecked = "2000-01-01T00:00:00Z" // THIS package not checked recently
	// Global last_check is fresh (other packages were just checked).
	a.state.LastCheck = time.Now().UTC().Format(state.TimeFormat)

	rel, err := a.resolveRelease(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v2" {
		t.Errorf("resolved %q, want v2 — a stale per-package latest_seen must not be reused", rel.Tag)
	}
}

// The complementary case: a package checked within the window reuses its cached
// latest_seen (the optimization the freshness window exists for).
func TestUpdateFreshnessReusesRecentPerPackage(t *testing.T) {
	a, p := twoEndpointForge(t, "v2")
	ps := a.state.Get("pkg")
	ps.InstalledVersion = "v1"
	ps.LatestSeen = "v1"
	ps.LastChecked = time.Now().UTC().Format(state.TimeFormat) // freshly checked
	a.state.LastCheck = ps.LastChecked

	rel, err := a.resolveRelease(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v1" {
		t.Errorf("resolved %q, want the cached v1 (fresh per-package check)", rel.Tag)
	}
}

// §11: a forge response that decodes to an empty tag must be a loud error, not a
// silent "up to date" via tagsEqual("","") for a never-installed package.
func TestResolveEmptyTagIsError(t *testing.T) {
	a, p := twoEndpointForge(t, "") // latest returns tag_name:""
	if _, err := a.resolveTag(p); err == nil {
		t.Error("resolveTag should error on an empty tag")
	}
	if _, _, err := a.resolveAndDownload(p); err == nil {
		t.Error("resolveAndDownload should error on an empty tag")
	}
}
