package forge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"kpm/internal/version"
)

//go:embed cacert.pem
var caPEM []byte

// apiTimeout bounds each forge API call.
const apiTimeout = 30 * time.Second

// Client is the shared HTTP client: embedded CA pool (plus the system pool if
// available), 2 retries with backoff, and a kpm User-Agent.
type Client struct {
	hc *http.Client
}

// NewClient builds the shared client with the embedded CA bundle. It panics if
// no usable CA pool can be assembled (a build-time invariant: the embedded
// bundle must parse) — D6.
func NewClient() *Client {
	system, _ := x509.SystemCertPool()
	pool, err := buildCertPool(system, caPEM)
	if err != nil {
		panic("kpm: " + err.Error())
	}
	tr := &http.Transport{
		TLSClientConfig:       &tls.Config{RootCAs: pool},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	return &Client{hc: &http.Client{Transport: tr}}
}

// buildCertPool assembles the CA pool from the system pool (if any) plus the
// embedded PEM. It errors only when the embedded bundle fails to parse AND there
// is no system pool to fall back on — otherwise there are no usable roots (D6).
func buildCertPool(system *x509.CertPool, pem []byte) (*x509.CertPool, error) {
	pool := system
	if pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) && system == nil {
		return nil, errors.New("no usable CA certificates: embedded bundle failed to parse")
	}
	return pool, nil
}

// HTTP exposes the underlying *http.Client (for connectivity probes).
func (c *Client) HTTP() *http.Client { return c.hc }

// NewClientWithHTTP wraps a preconfigured *http.Client instead of building the
// embedded-CA transport. Production always uses NewClient; this exists so tests
// (and any future custom-transport caller) can drive the network paths against
// an httptest server whose self-signed cert the default pool would reject.
func NewClientWithHTTP(hc *http.Client) *Client { return &Client{hc: hc} }

// maxManifestBytes caps a fetched registry manifest so a hostile or misbehaving
// server cannot exhaust memory (B3).
const maxManifestBytes = 4 << 20 // 4 MiB

// userAgent identifies kpm to forges (GitHub requires a UA).
func userAgent() string { return "kpm/" + version.Version }

// errNotFound / errRateLimited are sentinel errors the forge layer translates
// into user-facing messages via wrapReleaseErr (D7).
var (
	errNotFound    = errors.New("not found")
	errRateLimited = errors.New("github rate limited")
)

// wrapReleaseErr turns getJSON's sentinel errors into user-facing messages that
// name the repo instead of the raw API URL (D7).
func wrapReleaseErr(err error, host, owner, repo string) error {
	switch {
	case errors.Is(err, errNotFound):
		return fmt.Errorf("repository not found, or it has no releases yet (checked %s/%s/%s)", host, owner, repo)
	case errors.Is(err, errRateLimited):
		return errors.New("GitHub API rate limit exceeded; try again later")
	default:
		return err
	}
}

// getJSON fetches url with the given Accept header and returns the body,
// retrying twice on transient network errors. Non-2xx is a permanent error (no
// retry); 404 and GitHub rate-limit responses map to sentinel errors (D7).
func (c *Client) getJSON(ctx context.Context, url, accept string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		cctx, cancel := context.WithTimeout(ctx, apiTimeout)
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent())
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue // network error: retry
		}
		body, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if rerr != nil {
			lastErr = rerr
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, errNotFound
		}
		if resp.StatusCode == http.StatusForbidden && mentionsRateLimit(body) {
			return nil, errRateLimited
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("http %d from %s: %s", resp.StatusCode, url, snippet(body))
		}
		return body, nil
	}
	return nil, fmt.Errorf("request failed after retries: %w", lastErr)
}

// RawResult is the outcome of a conditional raw-file fetch (REGISTRY.md §9.3).
type RawResult struct {
	Body        []byte
	Etag        string // the server's ETag, if any (recorded for next If-None-Match)
	NotModified bool   // true when the server answered 304 (cache is still current)
}

// FetchRaw GETs a raw file URL, sending If-None-Match when etag is non-empty.
// A 304 returns NotModified with no body (the caller keeps its cache); a 2xx
// returns the body and any new ETag. Non-2xx (other than 304) is a permanent
// error. Only used by "kpm registry refresh" — the sole network path for
// registries (§9.2). Retries twice on transient network errors like getJSON.
func (c *Client) FetchRaw(ctx context.Context, url, etag string) (RawResult, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return RawResult{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		cctx, cancel := context.WithTimeout(ctx, apiTimeout)
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			return RawResult{}, err
		}
		req.Header.Set("User-Agent", userAgent())
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue // network error: retry
		}
		if resp.StatusCode == http.StatusNotModified {
			resp.Body.Close()
			cancel()
			return RawResult{Etag: etag, NotModified: true}, nil
		}
		// Bound the read: one byte past the cap tells us it overflowed (B3).
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
		newEtag := resp.Header.Get("ETag")
		resp.Body.Close()
		cancel()
		if rerr != nil {
			lastErr = rerr
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			return RawResult{}, errNotFound
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return RawResult{}, fmt.Errorf("http %d from %s: %s", resp.StatusCode, url, snippet(body))
		}
		if len(body) > maxManifestBytes {
			return RawResult{}, fmt.Errorf("registry manifest exceeds 4 MiB")
		}
		return RawResult{Body: body, Etag: newEtag}, nil
	}
	return RawResult{}, fmt.Errorf("request failed after retries: %w", lastErr)
}

// ErrNotFound is the exported sentinel for a 404, so callers (registry refresh)
// can turn it into a registry-specific message.
var ErrNotFound = errNotFound

// mentionsRateLimit reports whether a 403 body indicates GitHub API throttling.
func mentionsRateLimit(body []byte) bool {
	return strings.Contains(strings.ToLower(string(body)), "rate limit")
}

// Download streams url into dst (a *os.File). An inactivity watchdog aborts if
// no bytes arrive for 60s; there is no overall timeout so large assets on slow
// links still complete. Only transport/network/read errors retry; an HTTP >=400
// status or a local write error (ENOSPC etc.) is permanent — no retry (D4).
func (c *Client) Download(ctx context.Context, url string, dst *os.File) (int64, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if _, err := dst.Seek(0, io.SeekStart); err != nil {
				return 0, err
			}
			if err := dst.Truncate(0); err != nil {
				return 0, err
			}
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		n, permanent, err := c.downloadOnce(ctx, url, dst)
		if err == nil {
			return n, nil
		}
		lastErr = err
		if permanent {
			return 0, err
		}
	}
	return 0, fmt.Errorf("download failed after retries: %w", lastErr)
}

// downloadOnce performs one download attempt. permanent reports whether the
// error must not be retried (HTTP >=400, or a local write failure).
func (c *Client) downloadOnce(ctx context.Context, url string, dst *os.File) (n int64, permanent bool, err error) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, true, err
	}
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, false, err // transport error: retryable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, true, fmt.Errorf("http %d downloading %s", resp.StatusCode, url)
	}

	// Inactivity watchdog: cancel the request if a read stalls past 60s.
	w := &watchdogReader{
		r:      resp.Body,
		reset:  make(chan struct{}, 1),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go w.run()
	ew := &writeErrTracker{w: dst}
	n, err = io.Copy(ew, w)
	w.stop()
	if err != nil {
		// A write failure (disk full etc.) is permanent; a read/transport
		// failure is retryable (D4).
		return n, ew.err != nil, err
	}
	return n, false, nil
}

// writeErrTracker records the last error returned by the underlying writer so
// Download can tell a local write failure from a read/transport failure (D4).
type writeErrTracker struct {
	w   io.Writer
	err error
}

func (t *writeErrTracker) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if err != nil {
		t.err = err
	}
	return n, err
}

type watchdogReader struct {
	r      io.Reader
	reset  chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

func (w *watchdogReader) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if n > 0 {
		select {
		case w.reset <- struct{}{}:
		default:
		}
	}
	return n, err
}

func (w *watchdogReader) run() {
	t := time.NewTimer(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-w.reset:
			if !t.Stop() {
				<-t.C
			}
			t.Reset(60 * time.Second)
		case <-t.C:
			w.cancel()
			return
		case <-w.done:
			return
		}
	}
}

func (w *watchdogReader) stop() {
	close(w.done)
}

func snippet(b []byte) string {
	if len(b) > 200 {
		return string(b[:200])
	}
	return string(b)
}
