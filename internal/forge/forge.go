// Package forge talks to git forges (GitHub and Forgejo/Gitea) to resolve
// releases and download assets, using a shared HTTP client with an embedded
// Mozilla CA bundle so it works on Kobo firmware with no usable system TLS.
package forge

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Asset is one downloadable release artifact.
type Asset struct {
	Name        string
	Size        int64
	DownloadURL string
}

// Release is a resolved forge release. PublishedAt is the release's publish
// timestamp (RFC3339) as both forges report it, "" when absent; it is one half
// of the rolling-release change signal (INTEGRITY.md §3.2).
type Release struct {
	Tag         string
	PublishedAt string
	Assets      []Asset
}

// Fingerprint components. p is the release's published_at, s the matched
// asset's size in decimal. Keys are emitted in ascending order.
const (
	fpPublished = "p"
	fpSize      = "s"
)

// Fingerprint renders the rolling-release change signal as space-separated
// key=value components, deliberately human-readable in state.json:
//
//	"p=2026-07-01T10:00:00Z s=48213"
//
// a is the asset the def's glob resolved to, or nil when it did not resolve. A
// component is emitted only when its source value is known, so the result is ""
// when nothing is known — a forge that supplies neither signal degrades to plain
// tag comparison instead of erroring (INTEGRITY.md §3.2/§3.4).
func (r Release) Fingerprint(a *Asset) string {
	var parts []string
	if r.PublishedAt != "" {
		parts = append(parts, fpPublished+"="+r.PublishedAt)
	}
	// Size 0 is "not reported" (or an empty asset, which is not a real payload):
	// emitting s=0 would make a later real size read as a change.
	if a != nil && a.Size > 0 {
		parts = append(parts, fpSize+"="+strconv.FormatInt(a.Size, 10))
	}
	return strings.Join(parts, " ")
}

// FingerprintChanged reports whether two fingerprints PROVE a different release
// payload: true iff some component key present in BOTH strings has a different
// value. A key present in only one side is ignored, so a transiently
// unresolvable asset (or an empty fingerprint on either side) never fabricates
// an update — which is what makes both the degrade-to-tag-compare path and the
// baseline adoption of INTEGRITY.md §3.6.1 work.
func FingerprintChanged(old, cur string) bool {
	o := fingerprintParts(old)
	c := fingerprintParts(cur)
	for k, ov := range o {
		if cv, ok := c[k]; ok && cv != ov {
			return true
		}
	}
	return false
}

// fingerprintParts splits a fingerprint into its key=value components. Tokens
// without a "=" are ignored rather than treated as a key with an empty value.
func fingerprintParts(s string) map[string]string {
	if s == "" {
		return nil
	}
	out := map[string]string{}
	for _, tok := range strings.Fields(s) {
		if k, v, ok := strings.Cut(tok, "="); ok && k != "" {
			out[k] = v
		}
	}
	return out
}

// Forge resolves releases for one host/owner/repo.
type Forge interface {
	LatestRelease(ctx context.Context, host, owner, repo string) (Release, error)
	ReleaseByTag(ctx context.Context, host, owner, repo, tag string) (Release, error)
}

// MatchAsset picks the release asset whose name matches pattern (a glob, e.g.
// "KoboRoot*.tgz"). Exact matches win over glob matches. Errors if none or,
// for globs, if ambiguous.
func (r Release) MatchAsset(pattern string) (Asset, error) {
	for _, a := range r.Assets {
		if a.Name == pattern {
			return a, nil
		}
	}
	var matches []Asset
	for _, a := range r.Assets {
		ok, err := path.Match(pattern, a.Name)
		if err != nil {
			// path.ErrBadPattern: a malformed user glob (e.g. "KoboRoot[.tgz")
			// must surface as an invalid-pattern error, not a silent no-match.
			return Asset{}, fmt.Errorf("invalid asset pattern %q: %w", pattern, err)
		}
		if ok {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 0:
		return Asset{}, fmt.Errorf("no asset matching %q in release %s", pattern, r.Tag)
	case 1:
		return matches[0], nil
	default:
		return Asset{}, fmt.Errorf("%d assets match %q in release %s; narrow the pattern", len(matches), pattern, r.Tag)
	}
}

// For returns the Forge implementation for the given identifier. Unknown
// identifiers are an error rather than silently defaulting to Forgejo (D9).
func For(kind string, c *Client) (Forge, error) {
	switch kind {
	case "github":
		return &GitHub{c: c}, nil
	case "forgejo":
		return &Forgejo{c: c}, nil
	default:
		return nil, fmt.Errorf("unknown forge %q (want %q or %q)", kind, "github", "forgejo")
	}
}
