package config

import "testing"

func TestParseAddURL(t *testing.T) {
	cases := []struct {
		in                        string
		wantHost, wantOwner, repo string
		wantPin, wantForge, wantID string
		wantErr                   bool
	}{
		{
			in: "https://github.com/owner/Repo", wantHost: "github.com",
			wantOwner: "owner", repo: "Repo", wantForge: "github", wantID: "repo",
		},
		{
			in: "https://codeberg.org/StrayRose/NickelHardcover",
			wantHost: "codeberg.org", wantOwner: "StrayRose", repo: "NickelHardcover",
			wantForge: "", wantID: "nickelhardcover",
		},
		{
			in: "codeberg.org/StrayRose/NickelHardcover/releases",
			wantHost: "codeberg.org", wantOwner: "StrayRose", repo: "NickelHardcover",
			wantID: "nickelhardcover",
		},
		{
			in: "https://codeberg.org/o/r/releases/latest",
			wantHost: "codeberg.org", wantOwner: "o", repo: "r", wantID: "r",
		},
		{
			in: "https://codeberg.org/o/r/releases/tag/v0.5.0",
			wantHost: "codeberg.org", wantOwner: "o", repo: "r", wantPin: "v0.5.0", wantID: "r",
		},
		{
			in: "https://github.com/owner/repo/releases/tag/v1.2.3-rc1",
			wantHost: "github.com", wantOwner: "owner", repo: "repo",
			wantPin: "v1.2.3-rc1", wantForge: "github", wantID: "repo",
		},
		{in: "https://github.com/owner", wantErr: true},
		{in: "", wantErr: true},
		{in: "https://github.com/o/r/issues", wantErr: true},
		{in: "https://github.com/o/r/releases/bogus", wantErr: true},
	}
	for _, c := range cases {
		got, err := ParseAddURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error, got %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.in, err)
			continue
		}
		if got.Host != c.wantHost || got.Owner != c.wantOwner || got.Repo != c.repo {
			t.Errorf("%q: got %s/%s/%s", c.in, got.Host, got.Owner, got.Repo)
		}
		if got.Pin != c.wantPin {
			t.Errorf("%q: pin = %q, want %q", c.in, got.Pin, c.wantPin)
		}
		if got.Forge != c.wantForge {
			t.Errorf("%q: forge = %q, want %q", c.in, got.Forge, c.wantForge)
		}
		if got.ID != c.wantID {
			t.Errorf("%q: id = %q, want %q", c.in, got.ID, c.wantID)
		}
	}
}

func TestParseAddURLStripsGitSuffix(t *testing.T) {
	got, err := ParseAddURL("github.com/o/r.git")
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "r" || got.ID != "r" {
		t.Errorf("expected repo/id 'r', got repo=%q id=%q", got.Repo, got.ID)
	}
	// Only one ".git" is stripped.
	got, err = ParseAddURL("github.com/o/r.git.git")
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "r.git" {
		t.Errorf("expected 'r.git', got %q", got.Repo)
	}
}

func TestValidID(t *testing.T) {
	for _, id := range []string{"kpm", "nickel-hardcover", "abc123"} {
		if !ValidID(id) {
			t.Errorf("%q should be valid", id)
		}
	}
	for _, id := range []string{"Kobo", "with_underscore", "with space", ""} {
		if ValidID(id) {
			t.Errorf("%q should be invalid", id)
		}
	}
}
