package config

import (
	"fmt"
	"net/url"
	"strings"
)

// AddSpec is the result of parsing a "kpm add" URL. Forge may be empty when
// the host is not github.com, meaning the caller must probe /api/v1/version
// to decide (or the user must pass --forge).
type AddSpec struct {
	Host  string
	Owner string
	Repo  string
	Pin   string // set when the URL was a /releases/tag/<tag> link
	Forge string // "github", or "" when a probe is required
	ID    string // default package id derived from the repo name
}

// Source returns the host/owner/repo string stored in the TOML.
func (s AddSpec) Source() string {
	return s.Host + "/" + s.Owner + "/" + s.Repo
}

// ParseAddURL parses a forge repository URL per §3. It accepts an optional
// scheme, and trailing /releases, /releases/latest, or /releases/tag/<tag>.
func ParseAddURL(raw string) (*AddSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty url")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	host := u.Host
	if host == "" {
		return nil, fmt.Errorf("url has no host: %q", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("url must be host/owner/repo, got %q", raw)
	}
	// Strip a single trailing ".git" from the repo segment (clone URLs) (D8).
	repo := strings.TrimSuffix(parts[1], ".git")
	if repo == "" {
		return nil, fmt.Errorf("url must be host/owner/repo, got %q", raw)
	}
	spec := &AddSpec{Host: host, Owner: parts[0], Repo: repo}

	// Optional trailing /releases[/latest | /tag/<tag>].
	rest := parts[2:]
	if len(rest) > 0 {
		if rest[0] != "releases" {
			return nil, fmt.Errorf("unexpected path segment %q in %q", rest[0], raw)
		}
		switch {
		case len(rest) == 1, len(rest) == 2 && rest[1] == "latest":
			// track latest
		case len(rest) >= 3 && rest[1] == "tag":
			spec.Pin = strings.Join(rest[2:], "/")
		default:
			return nil, fmt.Errorf("unrecognized releases path in %q", raw)
		}
	}

	if host == "github.com" {
		spec.Forge = ForgeGitHub
	}
	spec.ID = deriveID(spec.Repo)
	return spec, nil
}

// deriveID lowercases the repo name and keeps only [a-z0-9-].
func deriveID(repo string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(repo) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}
