package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// GitHub implements Forge for github.com via api.github.com.
type GitHub struct {
	c    *Client
	base string // API base, overridable in tests; defaults to api.github.com
}

const githubAccept = "application/vnd.github+json"
const defaultGitHubBase = "https://api.github.com"

func (g *GitHub) apiBase() string {
	if g.base != "" {
		return g.base
	}
	return defaultGitHubBase
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// ErrGitHubHost is returned when a github source names a host other than
// github.com — GitHub Enterprise is out of scope (D1).
func gitHubHostErr(host string) error {
	return fmt.Errorf("github forge only supports github.com, not %q; use --forge forgejo for self-hosted", host)
}

func (g *GitHub) LatestRelease(ctx context.Context, host, owner, repo string) (Release, error) {
	if host != "github.com" {
		return Release{}, gitHubHostErr(host)
	}
	u := fmt.Sprintf("%s/repos/%s/%s/releases/latest",
		g.apiBase(), url.PathEscape(owner), url.PathEscape(repo))
	rel, err := g.fetch(ctx, u)
	if err != nil {
		return Release{}, wrapReleaseErr(err, host, owner, repo)
	}
	return rel, nil
}

func (g *GitHub) ReleaseByTag(ctx context.Context, host, owner, repo, tag string) (Release, error) {
	if host != "github.com" {
		return Release{}, gitHubHostErr(host)
	}
	u := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s",
		g.apiBase(), url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(tag))
	rel, err := g.fetch(ctx, u)
	if err != nil {
		return Release{}, wrapReleaseErr(err, host, owner, repo)
	}
	return rel, nil
}

func (g *GitHub) fetch(ctx context.Context, u string) (Release, error) {
	body, err := g.c.getJSON(ctx, u, githubAccept)
	if err != nil {
		return Release{}, err
	}
	var r githubRelease
	if err := json.Unmarshal(body, &r); err != nil {
		return Release{}, fmt.Errorf("decode github release: %w", err)
	}
	rel := Release{Tag: r.TagName}
	for _, a := range r.Assets {
		rel.Assets = append(rel.Assets, Asset{
			Name:        a.Name,
			Size:        a.Size,
			DownloadURL: a.BrowserDownloadURL,
		})
	}
	return rel, nil
}
