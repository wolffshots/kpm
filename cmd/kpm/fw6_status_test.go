package main

import (
	"strings"
	"testing"

	"kpm/internal/config"
)

// After the upgrade to 6.x, a 4.x-only package keeps the latest_seen of the
// 4.x line. update skips it on 6.x, so status and check must not count it as
// an update. A package with a 6.x build still counts.
func TestStatusSkipsPackageWithNoBuildForLine(t *testing.T) {
	a := newTestApp(t)
	writeFirmware(t, a, fw6)
	if err := config.Save(a.paths.PackageFile("oldmod"), &config.Package{
		Name: "Old mod", Source: "github.com/o/oldmod", Forge: "github", Asset: "KoboRoot.tgz",
	}); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(a.paths.PackageFile("newmod"), &config.Package{
		Name: "New mod", Source: "github.com/o/newmod", Forge: "github", Asset: "KoboRoot.tgz",
		Fw6: &config.Fw6{Asset: "Kobo.tgz"},
	}); err != nil {
		t.Fatal(err)
	}
	// The firmware removed oldmod, so it is not installed. newmod is behind.
	a.state.Get("oldmod").LatestSeen = "v1.0.0"
	a.state.Get("newmod").InstalledVersion = "v1.0.0"
	a.state.Get("newmod").LatestSeen = "v2.0.0"
	if err := a.state.Save(); err != nil {
		t.Fatal(err)
	}
	pkgs, err := a.loadPackages()
	if err != nil {
		t.Fatal(err)
	}

	got := a.buildStatus(pkgs)
	if strings.Contains(got, "oldmod  -  -> v1.0.0") || !strings.Contains(got, config.Unavailable6) {
		t.Errorf("oldmod must show the line reason, not an update:\n%s", got)
	}
	if !strings.Contains(got, "1 update available") {
		t.Errorf("only newmod is an update:\n%s", got)
	}

	out := lastJSON(t, captureStdout(t, func() { a.cmdStatus([]string{"--json"}) }))
	if !strings.Contains(out, `{"id":"oldmod","installed":null,"staged":null,"latest":"v1.0.0","pinned":null,"state":"unavailable"`) {
		t.Errorf("status --json: oldmod must be unavailable:\n%s", out)
	}
	if !strings.Contains(out, `"id":"newmod","installed":"v1.0.0","staged":null,"latest":"v2.0.0","pinned":null,"state":"update-available"`) {
		t.Errorf("status --json: newmod must be update-available:\n%s", out)
	}

	// On 4.x the same state is an update, as before.
	writeFirmware(t, a, fw4)
	if got := a.buildStatus(pkgs); !strings.Contains(got, "2 update available") {
		t.Errorf("on 4.x both packages are updates:\n%s", got)
	}
}
