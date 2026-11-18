package tgz

import (
	"archive/tar"
	"path/filepath"
	"strings"
	"testing"
)

// CheckKoboTgz (QT6-FIRMWARE6.md section 3b): members under imageformats/ only,
// kpm-seed/ for kpm only, a KoboRoot.tgz refused, the manifest prefixed.
func TestCheckKoboTgz(t *testing.T) {
	cases := []struct {
		name    string
		members []string
		links   []string
		self    bool
		wantErr string
	}{
		{"plugin", []string{"imageformats", "imageformats/libx.so"}, nil, false, ""},
		{"self seed", []string{"imageformats/libnkpm.so", "kpm-seed", "kpm-seed/bin/kpm"}, nil, true, ""},
		{"seed for other", []string{"imageformats/libx.so", "kpm-seed/bin/kpm"}, nil, false, `"kpm-seed/bin/kpm" is not under imageformats/`},
		{"koboroot dir", []string{"usr", "usr/local/Kobo/imageformats/libx.so"}, nil, false, "the asset is a KoboRoot.tgz for firmware 4.x"},
		{"koboroot onboard", []string{"mnt/onboard/.adds/x"}, nil, false, "the asset is a KoboRoot.tgz for firmware 4.x"},
		{"nickel file", []string{"imageformats/libx.so", "nickel"}, nil, false, `"nickel" is not under imageformats/`},
		{"link out", []string{"imageformats/l"}, []string{"."}, false, "link points outside imageformats/"},
		{"link in", []string{"imageformats/l"}, []string{"imageformats/libx.so.1"}, false, ""},
	}
	for _, c := range cases {
		got, err := CheckKoboTgz("x", &VerifyResult{Manifest: c.members, Links: c.links}, c.self)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		for i, m := range got {
			if m != "usr/local/Kobo/"+c.members[i] {
				t.Errorf("%s: manifest[%d] = %q", c.name, i, m)
			}
		}
	}
}

// Verify records the resolved targets of symlinks and hardlinks.
func TestVerifyRecordsLinks(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k.tgz")
	writeTestTgzHeaders(t, p, []*tar.Header{
		{Name: "imageformats/libx.so.1", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
		{Name: "imageformats/libx.so", Typeflag: tar.TypeSymlink, Linkname: "libx.so.1", Mode: 0o777},
		{Name: "imageformats/up", Typeflag: tar.TypeSymlink, Linkname: "..", Mode: 0o777},
		{Name: "imageformats/hard", Typeflag: tar.TypeLink, Linkname: "imageformats/libx.so.1", Mode: 0o644},
	})
	res, err := Verify(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"imageformats/libx.so.1", ".", "imageformats/libx.so.1"}
	if strings.Join(res.Links, ",") != strings.Join(want, ",") {
		t.Errorf("links = %v, want %v", res.Links, want)
	}
	if _, err := CheckKoboTgz("x", res, false); err == nil {
		t.Error("the link to the archive root must be refused")
	}
}
