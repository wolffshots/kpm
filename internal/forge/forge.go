// Package forge talks to git forges (GitHub and Forgejo/Gitea) to resolve
// releases and download assets, using a shared HTTP client with an embedded
// Mozilla CA bundle so it works on Kobo firmware with no usable system TLS.
package forge

import (
	"context"
	"fmt"
	"path"
)

// Asset is one downloadable release artifact.
type Asset struct {
	Name        string
	Size        int64
	DownloadURL string
}

// Release is a resolved forge release.
type Release struct {
	Tag    string
	Assets []Asset
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
