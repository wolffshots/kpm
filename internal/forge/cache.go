package forge

// cache.go holds the on-disk response cache that makes forge API requests
// CONDITIONAL: kpm replays the previous ETag as If-None-Match, and a forge that
// answers 304 costs no rate-limit budget (GitHub allows 60 unauthenticated
// requests/hour/IP, and `kpm check` spends one per package).
//
// The design hinge, and the one thing that differs from the registry.toml ETag
// path in FetchRaw: a 304 carries NO BODY. FetchRaw's caller already had the
// file it downloaded; here the body IS the thing being revalidated, so the body
// must be cached alongside its validator, in one file so there is no cross-file
// atomicity problem.
//
// The cache may only ever make kpm faster. It must never change an answer, so
// every failure path — caching disabled, missing entry, corrupt file, wrong
// schema, no ETag from the server — falls back to an ordinary unconditional GET.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// cacheSchema versions the envelope. Bump it whenever what kpm PARSES out of a
// cached body changes, so entries written by an older binary are ignored rather
// than silently misread into the new shape.
const cacheSchema = 1

// maxCacheBytes bounds a cache entry read. The envelope is JSON with the body
// base64-encoded (~4/3 of it) plus a small header, so twice maxAPIBytes is
// generous headroom while still bounding a corrupt or hostile file on a device
// with ~256 MB of RAM (B4).
const maxCacheBytes = 2 * maxAPIBytes

// cacheEnvelope is one cached forge API response: the validator the server gave
// us and the exact bytes it validated. URL and Accept are recorded so an entry
// can never be served for a request it was not written for, whatever the file
// name says.
type cacheEnvelope struct {
	Schema int    `json:"schema"`
	URL    string `json:"url"`
	Accept string `json:"accept"`
	Etag   string `json:"etag"`
	Body   []byte `json:"body"`
}

// cachePath is the entry file for one (url, accept) pair, or "" when response
// caching is off. The name is a hash of both: hashing keeps an arbitrarily long
// URL — or any "/" or ".." inside one — from escaping or overflowing the cache
// dir, and including Accept keeps two representations of the same URL from
// sharing an entry.
//
// The entry set is naturally bounded: one file per (source, pin) a package
// resolves against, and those are stable. A repin or a renamed repo leaves a few
// KB behind, which is why there is no pruner here.
func (c *Client) cachePath(url, accept string) string {
	if c.cacheDir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(url + "\n" + accept))
	return filepath.Join(c.cacheDir, "api-"+hex.EncodeToString(sum[:])+".json")
}

// loadCache returns the usable cached envelope for a request, or nil. EVERY
// failure returns nil — caching off, missing file, oversized file, unparseable
// JSON, a schema from another kpm, an entry recorded for a different request, or
// a missing half of the etag/body pair — and nil means the caller issues a plain
// unconditional GET, exactly as it did before this file existed.
//
// The body is read and validated BEFORE the request goes out. That is what lets
// getJSON promise it can honour a 304: If-None-Match is only ever sent when the
// body it would fall back to is already in memory, so "304 arrived but the
// cached body turned out to be unreadable" is unreachable rather than handled.
func (c *Client) loadCache(url, accept string) *cacheEnvelope {
	path := c.cachePath(url, accept)
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	// Bound the read (B4). Unlike getJSON there is no "too large" error to raise:
	// an entry past the cap simply arrives truncated, and truncated JSON cannot
	// parse — which lands on the fail-open path this whole function is built on.
	data, err := io.ReadAll(io.LimitReader(f, maxCacheBytes))
	if err != nil {
		return nil
	}
	var e cacheEnvelope
	if err := json.Unmarshal(data, &e); err != nil {
		return nil
	}
	if e.Schema != cacheSchema || e.URL != url || e.Accept != accept ||
		e.Etag == "" || len(e.Body) == 0 {
		return nil
	}
	return &e
}

// storeCache records a 2xx body against the ETag that validated it.
//
// A response with NO ETag is not cacheable, and any previous entry is removed
// rather than kept: holding a validator the server has stopped issuing would let
// a later If-None-Match serve a body kpm can no longer prove current. A forge
// that never sends ETags (some Forgejo deployments) therefore degrades silently
// and exactly to today's unconditional fetches.
//
// Every failure is silent. A cache write that fails must never fail the request
// it exists to make cheaper.
func (c *Client) storeCache(url, accept, etag string, body []byte) {
	path := c.cachePath(url, accept)
	if path == "" {
		return
	}
	if etag == "" {
		os.Remove(path)
		return
	}
	data, err := json.Marshal(cacheEnvelope{
		Schema: cacheSchema, URL: url, Accept: accept, Etag: etag, Body: body,
	})
	if err != nil {
		return
	}
	writeCacheAtomic(path, data)
}

// writeCacheAtomic writes an entry via temp file + fsync + rename, so neither a
// power loss nor a concurrent writer for the same URL can leave a half-written
// envelope for the next run to misparse.
//
// Unlike device.WriteFileAtomic it does not fsync the parent directory: this is
// a cache, and an entry whose directory update FAT32 lost is simply a cache
// miss — which is already the fail-open path. Best-effort throughout.
func writeCacheAtomic(path string, data []byte) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".kpm-api-*.tmp")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
	}
}
