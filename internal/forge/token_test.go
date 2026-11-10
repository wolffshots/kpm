package forge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// tokenServer answers every request with status and records the last request's
// Authorization and Accept headers.
func tokenServer(t *testing.T, status int) (srv *httptest.Server, auth, accept *string) {
	t.Helper()
	auth, accept = new(string), new(string)
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*auth, *accept = r.Header.Get("Authorization"), r.Header.Get("Accept")
		w.WriteHeader(status)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, auth, accept
}

// trustHost makes host the only GitHub host for one test.
func trustHost(t *testing.T, host string) {
	t.Helper()
	old := githubHosts
	githubHosts = map[string]bool{host: true}
	t.Cleanup(func() { githubHosts = old })
}

func TestTokenGoesOnlyToGitHubHosts(t *testing.T) {
	ctx := context.Background()
	srv, auth, _ := tokenServer(t, http.StatusOK)
	c := &Client{hc: srv.Client(), githubToken: "tok"}
	if _, err := c.getJSON(ctx, srv.URL, ""); err != nil {
		t.Fatal(err)
	}
	if *auth != "" {
		t.Errorf("a host that is not GitHub received %q", *auth)
	}
	trustHost(t, "127.0.0.1")
	if _, err := c.getJSON(ctx, srv.URL, ""); err != nil {
		t.Fatal(err)
	}
	if *auth != "token tok" {
		t.Errorf("GitHub got Authorization %q", *auth)
	}
	if _, err := (&Client{hc: srv.Client()}).getJSON(ctx, srv.URL, ""); err != nil {
		t.Fatal(err)
	}
	if *auth != "" {
		t.Errorf("no token configured, but %q was sent", *auth)
	}
}

// An expired token fails every GitHub request, public ones too, so each request
// kind must say what to fix.
func TestRejectedTokenNamesTheFile(t *testing.T) {
	ctx := context.Background()
	srv, _, _ := tokenServer(t, http.StatusUnauthorized)
	trustHost(t, "127.0.0.1")
	c := &Client{hc: srv.Client(), githubToken: "expired"}
	if _, err := c.getJSON(ctx, srv.URL, ""); !errors.Is(err, errBadToken) {
		t.Errorf("getJSON: %v", err)
	}
	if _, err := c.FetchRaw(ctx, srv.URL, ""); !errors.Is(err, errBadToken) {
		t.Errorf("FetchRaw: %v", err)
	}
	f, err := os.CreateTemp(t.TempDir(), "dl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := c.Download(ctx, srv.URL, f); !errors.Is(err, errBadToken) {
		t.Errorf("Download: %v", err)
	}
}

func TestDownloadAsksForBytesWithToken(t *testing.T) {
	srv, auth, accept := tokenServer(t, http.StatusOK)
	trustHost(t, "127.0.0.1")
	f, err := os.CreateTemp(t.TempDir(), "dl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c := &Client{hc: srv.Client(), githubToken: "tok"}
	if _, err := c.Download(context.Background(), srv.URL, f); err != nil {
		t.Fatal(err)
	}
	if *accept != "application/octet-stream" || *auth != "token tok" {
		t.Errorf("Accept %q, Authorization %q", *accept, *auth)
	}
}

// A private repository's browser URL needs a web session, so a token switches
// the download to the API asset URL.
func TestGitHubUsesAPIAssetURLWithToken(t *testing.T) {
	const browser = "https://github.com/o/r/releases/download/v1/a.zip"
	const api = "https://api.github.com/repos/o/r/releases/assets/1"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v1","assets":[{"name":"a.zip","size":1,` +
			`"browser_download_url":"` + browser + `","url":"` + api + `"}]}`))
	}))
	defer srv.Close()
	for token, want := range map[string]string{"": browser, "tok": api} {
		g := &GitHub{c: &Client{hc: srv.Client(), githubToken: token}, base: srv.URL}
		rel, err := g.ReleaseByTag(context.Background(), "github.com", "o", "r", "v1")
		if err != nil {
			t.Fatal(err)
		}
		if got := rel.Assets[0].DownloadURL; got != want {
			t.Errorf("token %q: download URL %q, want %q", token, got, want)
		}
	}
}
