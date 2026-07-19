package forge

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// D1: the github forge must refuse any host other than github.com.
func TestGitHubRefusesNonGitHubHost(t *testing.T) {
	g := &GitHub{c: &Client{hc: http.DefaultClient}}
	if _, err := g.LatestRelease(context.Background(), "example.com", "o", "r"); err == nil {
		t.Error("github forge must reject a non-github.com host")
	}
	if _, err := g.ReleaseByTag(context.Background(), "example.com", "o", "r", "v1"); err == nil {
		t.Error("github forge must reject a non-github.com host (by tag)")
	}
}

// D9: For returns an error for unknown forge identifiers.
func TestForUnknownForge(t *testing.T) {
	c := &Client{hc: http.DefaultClient}
	if _, err := For("gitlab", c); err == nil {
		t.Error("unknown forge should error")
	}
	if f, err := For("github", c); err != nil || f == nil {
		t.Errorf("github should resolve: %v", err)
	}
	if f, err := For("forgejo", c); err != nil || f == nil {
		t.Errorf("forgejo should resolve: %v", err)
	}
}

// D4: a >=400 status is permanent — Download must not retry it.
func TestDownloadDoesNotRetryHTTPError(t *testing.T) {
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := &Client{hc: srv.Client()}
	f, err := os.CreateTemp(t.TempDir(), "dl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := c.Download(context.Background(), srv.URL, f); err == nil {
		t.Error("expected download error on 500")
	}
	if hits != 1 {
		t.Errorf("500 must not be retried: %d hits", hits)
	}
}

// D5: Probe requires a JSON body with a version field.
func TestProbeRejectsNonJSON(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>hello</html>"))
	}))
	defer srv.Close()
	c := &Client{hc: srv.Client()}
	host := strings.TrimPrefix(srv.URL, "https://")
	if Probe(context.Background(), c, host) {
		t.Error("a non-JSON 200 response must not be treated as a forge")
	}
}

// D6: buildCertPool errors when the embedded bundle is garbage and there is no
// system pool to fall back on.
func TestBuildCertPoolGuards(t *testing.T) {
	if _, err := buildCertPool(nil, []byte("not a pem")); err == nil {
		t.Error("garbage PEM with no system pool must error")
	}
	if _, err := buildCertPool(nil, caPEM); err != nil {
		t.Errorf("real bundle should build: %v", err)
	}
	// With a (non-nil) system pool present, a bad embedded bundle is tolerated.
	if _, err := buildCertPool(x509.NewCertPool(), []byte("garbage")); err != nil {
		t.Errorf("system pool present should tolerate a bad embed: %v", err)
	}
}

// D7: 404 maps to a repo-oriented message (no raw API URL); a rate-limit 403
// maps to a clear message.
func TestReleaseErrorMessages(t *testing.T) {
	srv404 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv404.Close()
	f := &Forgejo{c: &Client{hc: srv404.Client()}}
	host := strings.TrimPrefix(srv404.URL, "https://")
	_, err := f.LatestRelease(context.Background(), host, "owner", "repo")
	if err == nil || !strings.Contains(err.Error(), "repository not found") {
		t.Errorf("404 message = %v", err)
	}
	if strings.Contains(err.Error(), "/api/v1/") {
		t.Errorf("404 message leaks the API URL: %v", err)
	}
	if !strings.Contains(err.Error(), "owner/repo") {
		t.Errorf("404 message should name the repo: %v", err)
	}

	srv403 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"API rate limit exceeded for x"}`))
	}))
	defer srv403.Close()
	g := &GitHub{c: &Client{hc: srv403.Client()}, base: srv403.URL}
	_, err = g.LatestRelease(context.Background(), "github.com", "o", "r")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "rate limit") {
		t.Errorf("rate-limit message = %v", err)
	}
}

// ensure the embedded bundle file exists (guards the go:embed path).
func TestCacertEmbedPresent(t *testing.T) {
	if _, err := os.Stat(filepath.Join(".", "cacert.pem")); err != nil {
		t.Fatalf("cacert.pem missing: %v", err)
	}
}
