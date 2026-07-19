package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// Forgejo implements Forge for Forgejo/Gitea instances (incl. Codeberg).
type Forgejo struct{ c *Client }

type forgejoRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func (f *Forgejo) LatestRelease(ctx context.Context, host, owner, repo string) (Release, error) {
	u := fmt.Sprintf("https://%s/api/v1/repos/%s/%s/releases/latest",
		host, url.PathEscape(owner), url.PathEscape(repo))
	rel, err := f.fetch(ctx, u)
	if err != nil {
		return Release{}, wrapReleaseErr(err, host, owner, repo)
	}
	return rel, nil
}

func (f *Forgejo) ReleaseByTag(ctx context.Context, host, owner, repo, tag string) (Release, error) {
	u := fmt.Sprintf("https://%s/api/v1/repos/%s/%s/releases/tags/%s",
		host, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(tag))
	rel, err := f.fetch(ctx, u)
	if err != nil {
		return Release{}, wrapReleaseErr(err, host, owner, repo)
	}
	return rel, nil
}

func (f *Forgejo) fetch(ctx context.Context, u string) (Release, error) {
	body, err := f.c.getJSON(ctx, u, "application/json")
	if err != nil {
		return Release{}, err
	}
	var r forgejoRelease
	if err := json.Unmarshal(body, &r); err != nil {
		return Release{}, fmt.Errorf("decode forgejo release: %w", err)
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

// Probe checks whether host answers the Forgejo/Gitea version endpoint with a
// JSON body carrying a "version" field. Requiring the field avoids treating any
// site that returns HTTP 200 as a forge (D5).
func Probe(ctx context.Context, c *Client, host string) bool {
	u := fmt.Sprintf("https://%s/api/v1/version", host)
	body, err := c.getJSON(ctx, u, "application/json")
	if err != nil {
		return false
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return false
	}
	return v.Version != ""
}
