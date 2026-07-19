package forge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchRawSuccessAndEtag: a 2xx returns the body and the server's ETag.
func TestFetchRawSuccessAndEtag(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc123"`)
		w.Write([]byte("schema_version = 1\n"))
	}))
	defer srv.Close()

	c := &Client{hc: srv.Client()}
	res, err := c.FetchRaw(context.Background(), srv.URL+"/registry.toml", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.NotModified {
		t.Error("fresh 200 should not be NotModified")
	}
	if string(res.Body) != "schema_version = 1\n" {
		t.Errorf("body = %q", res.Body)
	}
	if res.Etag != `"abc123"` {
		t.Errorf("etag = %q", res.Etag)
	}
}

// TestFetchRawNotModified: an If-None-Match match returns 304/NotModified, no body.
func TestFetchRawNotModified(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"abc123"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		t.Errorf("missing If-None-Match header: %q", r.Header.Get("If-None-Match"))
		w.Write([]byte("new"))
	}))
	defer srv.Close()

	c := &Client{hc: srv.Client()}
	res, err := c.FetchRaw(context.Background(), srv.URL+"/registry.toml", `"abc123"`)
	if err != nil {
		t.Fatal(err)
	}
	if !res.NotModified {
		t.Error("304 should map to NotModified")
	}
	if len(res.Body) != 0 {
		t.Errorf("304 should carry no body: %q", res.Body)
	}
	if res.Etag != `"abc123"` {
		t.Errorf("304 should retain the prior etag: %q", res.Etag)
	}
}

// B3: a manifest larger than 4 MiB is rejected instead of read unbounded.
func TestFetchRawTooLarge(t *testing.T) {
	big := make([]byte, maxManifestBytes+100)
	for i := range big {
		big[i] = 'a'
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(big)
	}))
	defer srv.Close()

	c := &Client{hc: srv.Client()}
	_, err := c.FetchRaw(context.Background(), srv.URL+"/registry.toml", "")
	if err == nil || !strings.Contains(err.Error(), "exceeds 4 MiB") {
		t.Errorf("oversized manifest should be rejected, got %v", err)
	}
}

// B3: a manifest right at the 4 MiB cap is still accepted.
func TestFetchRawAtLimitOK(t *testing.T) {
	body := make([]byte, maxManifestBytes)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	c := &Client{hc: srv.Client()}
	res, err := c.FetchRaw(context.Background(), srv.URL+"/registry.toml", "")
	if err != nil {
		t.Fatalf("a manifest at the cap should be accepted: %v", err)
	}
	if len(res.Body) != maxManifestBytes {
		t.Errorf("body length = %d, want %d", len(res.Body), maxManifestBytes)
	}
}

// TestFetchRawNotFound: a 404 maps to the exported ErrNotFound sentinel.
func TestFetchRawNotFound(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := &Client{hc: srv.Client()}
	_, err := c.FetchRaw(context.Background(), srv.URL+"/missing.toml", "")
	if err != ErrNotFound {
		t.Errorf("404 should map to ErrNotFound, got %v", err)
	}
}
