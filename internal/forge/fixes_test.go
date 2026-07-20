package forge

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
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

// redirectClient wraps a TLS test server's transport with kpm's redirect
// policy so the network paths exercise checkRedirect against httptest.
func redirectClient(srv *httptest.Server) *Client {
	return &Client{hc: &http.Client{
		Transport:     srv.Client().Transport,
		CheckRedirect: checkRedirect,
	}}
}

// B1: a redirect whose target scheme is not https is refused (a MITM must not
// be able to downgrade the payload fetch to plaintext).
func TestRedirectToHTTPRefused(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "http://downgrade.example/evil", http.StatusFound)
			return
		}
		w.Write([]byte("SHOULD NOT REACH"))
	}))
	defer srv.Close()

	c := redirectClient(srv)
	_, err := c.getJSON(context.Background(), srv.URL+"/start", "application/json")
	if err == nil {
		t.Fatal("expected error on http downgrade redirect")
	}
	if !strings.Contains(err.Error(), "non-https") {
		t.Errorf("error should name the non-https redirect: %v", err)
	}
}

// B1: a redirect to another https target is followed (scheme policy allows it).
func TestRedirectToHTTPSFollowed(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := redirectClient(srv)
	body, err := c.getJSON(context.Background(), srv.URL+"/start", "application/json")
	if err != nil {
		t.Fatalf("https redirect should be followed: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body after redirect = %q", body)
	}
}

// B1: checkRedirect allows a cross-host https target and caps the chain length.
func TestCheckRedirectPolicy(t *testing.T) {
	httpsTarget, _ := url.Parse("https://other.example/asset")
	if err := checkRedirect(&http.Request{URL: httpsTarget}, nil); err != nil {
		t.Errorf("cross-host https redirect must be allowed for scheme: %v", err)
	}
	httpTarget, _ := url.Parse("http://other.example/asset")
	if err := checkRedirect(&http.Request{URL: httpTarget}, nil); err == nil {
		t.Error("http redirect target must be refused")
	}
	via := make([]*http.Request, maxRedirects)
	if err := checkRedirect(&http.Request{URL: httpsTarget}, via); err == nil {
		t.Errorf("chain of %d hops must be refused", maxRedirects)
	}
}

// B4: an API JSON body larger than the cap is rejected instead of read unbounded.
func TestAPIBodyTooLarge(t *testing.T) {
	big := make([]byte, maxAPIBytes+100)
	for i := range big {
		big[i] = 'a'
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(big)
	}))
	defer srv.Close()

	c := &Client{hc: srv.Client()}
	_, err := c.getJSON(context.Background(), srv.URL+"/api", "application/json")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("oversized API body should be rejected, got %v", err)
	}
}

// B2: Download refuses a non-https asset URL before dialing.
func TestDownloadRejectsHTTPURL(t *testing.T) {
	c := &Client{hc: http.DefaultClient}
	f, err := os.CreateTemp(t.TempDir(), "dl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := c.Download(context.Background(), "http://example.com/KoboRoot.tgz", f); err == nil {
		t.Error("Download must reject an http:// URL")
	} else if !strings.Contains(err.Error(), "non-https") {
		t.Errorf("error should name the non-https scheme: %v", err)
	}
	if _, err := c.Download(context.Background(), "/relative/path", f); err == nil {
		t.Error("Download must reject a relative URL")
	}
}

// B6: MatchAsset surfaces a malformed glob as path.ErrBadPattern, not no-match.
func TestMatchAssetBadPattern(t *testing.T) {
	rel := Release{Tag: "v1", Assets: []Asset{{Name: "KoboRoot.tgz"}}}
	_, err := rel.MatchAsset("KoboRoot[.tgz")
	if err == nil {
		t.Fatal("malformed pattern must error")
	}
	if !errors.Is(err, path.ErrBadPattern) {
		t.Errorf("error should wrap path.ErrBadPattern: %v", err)
	}
}

// B5: HTTP 429 maps to the friendly rate-limit message.
func TestRateLimit429(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("slow down"))
	}))
	defer srv.Close()
	g := &GitHub{c: &Client{hc: srv.Client()}, base: srv.URL}
	_, err := g.LatestRelease(context.Background(), "github.com", "o", "r")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "rate limit") {
		t.Errorf("429 should map to a rate-limit message: %v", err)
	}
}

// B7: the snippet sanitizer strips control characters (e.g. an ANSI escape) so
// a hostile server body cannot inject them into logs/terminals.
func TestSnippetStripsControlChars(t *testing.T) {
	s := snippet([]byte("ab\x1b[31mcd\x00ef\x7fgh"))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("snippet retained a control char %#x in %q", r, s)
		}
	}
	if !strings.Contains(s, "ab") || !strings.Contains(s, "gh") {
		t.Errorf("snippet dropped printable content: %q", s)
	}
}
