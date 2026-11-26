package main

// progress_plan_test.go covers the step plan of each command (kpm-progress.md,
// "Steps per command"): a run shows one step count from its first step line
// to its last. Finding 5 of the 0.16.0-rc2 device test: one Refresh showed
// "of 2", then "of 3", then "of 5".

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"kpm/internal/config"
	"kpm/internal/forge"
	"kpm/internal/registry"
)

// fixedTotal asserts that every step line of a run has the step count of the
// first one, and that the run ends on the last step. It returns the count.
func fixedTotal(t *testing.T, lines []progressLine) int {
	t.Helper()
	total := 0
	for i, l := range lines {
		if l.Step == 0 {
			continue // a wait before the first step shows no count
		}
		if total == 0 {
			total = l.Steps
		}
		if l.Steps != total {
			t.Errorf("line %d (%q): step count %d, the run started with %d", i, l.Text, l.Steps, total)
		}
	}
	if last := lines[len(lines)-1]; last.Step != total {
		t.Errorf("last line %+v: want step %d of %d", last, total, total)
	}
	return total
}

// progressCarrySetup is carryoverSetup with a 4.x KoboRoot.tgz in the release
// too, so update --all can stage each package.
func progressCarrySetup(t *testing.T) *App {
	t.Helper()
	a, _ := carryoverSetup(t)
	root := tgzBytes(t, map[string]string{"./usr/local/Kobo/imageformats/libpkg.so": "qt5"})
	host := assetsServer(t, a, "v2.0.0", map[string][]byte{
		"KoboRoot.tgz": root,
		"Kobo.tgz":     tgzBytes(t, map[string]string{"imageformats/libpkg-qt6.so": "qt6"}),
		"kpm-Kobo.tgz": tgzBytes(t, map[string]string{"imageformats/libnkpm-qt6.so": "hook", "kpm-seed/VERSION": "v2.0.0"}),
	})
	pkgs, err := a.loadPackages()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		p.Source = host + "/o/" + p.ID
		if err := config.Save(a.paths.PackageFile(p.ID), p); err != nil {
			t.Fatal(err)
		}
	}
	a.locked = true // carryoverAfter and the plan need the lock, as in run()
	return a
}

// runWithProgress runs one command as run() does: progress on, the payload
// held until the bundle refresh after the command is done.
func runWithProgress(t *testing.T, a *App, cmd string, args ...string) (int, []progressLine) {
	t.Helper()
	heldJSON = &[]string{}
	t.Cleanup(func() { heldJSON = nil })
	var code int
	out := captureStdout(t, func() {
		a.prog = &progress{w: os.Stdout, now: time.Now, shown: -1}
		code = a.dispatch(cmd, args)
		a.carryoverAfter(cmd)
		writeHeldJSON()
	})
	return code, checkLines(t, out)
}

// check counts the carry-over downloads in its plan before its first step:
// "of 4" from the first line, not "of 1" and then "of 4".
func TestCheckPlanWithCarryover(t *testing.T) {
	a := progressCarrySetup(t)
	code, lines := runWithProgress(t, a, "check", "--json")
	if code != exitOK {
		t.Fatalf("check = %d", code)
	}
	if n := fixedTotal(t, lines); n != 4 {
		t.Errorf("total %d, want 4 (check, kpm and pkg downloads, bundle): %s", n, texts(lines))
	}
}

// With the bundle up to date, check is one step.
func TestCheckPlanNoCarryover(t *testing.T) {
	a := progressCarrySetup(t)
	if err := a.refreshCarryover(true); err != nil {
		t.Fatal(err)
	}
	code, lines := runWithProgress(t, a, "check", "--json")
	if code != exitOK {
		t.Fatalf("check = %d", code)
	}
	if n := fixedTotal(t, lines); n != 1 {
		t.Errorf("total %d, want 1: %s", n, texts(lines))
	}
}

// update --all counts its packages, the staging step and the carry-over
// downloads before its first step.
func TestUpdateAllPlanWithCarryover(t *testing.T) {
	a := progressCarrySetup(t)
	code, lines := runWithProgress(t, a, "update", "--all", "--json")
	if code != exitOK {
		t.Fatalf("update = %d: %s", code, texts(lines))
	}
	// 4 packages, staging, 2 downloads, bundle.
	if n := fixedTotal(t, lines); n != 8 {
		t.Errorf("total %d, want 8: %s", n, texts(lines))
	}
	if !strings.Contains(texts(lines), "Building the firmware 6 bundle") {
		t.Errorf("no bundle step: %s", texts(lines))
	}
}

func TestUpdateAllPlanNoCarryover(t *testing.T) {
	a := progressCarrySetup(t)
	if err := a.refreshCarryover(true); err != nil {
		t.Fatal(err)
	}
	code, lines := runWithProgress(t, a, "update", "--all", "--json")
	if code != exitOK {
		t.Fatalf("update = %d: %s", code, texts(lines))
	}
	if n := fixedTotal(t, lines); n != 5 {
		t.Errorf("total %d, want 5 (4 packages, staging): %s", n, texts(lines))
	}
}

// uninstall counts the bundle rebuild that follows the removal of a carried
// package.
func TestUninstallPlanWithCarryover(t *testing.T) {
	a := progressCarrySetup(t)
	if err := a.refreshCarryover(true); err != nil {
		t.Fatal(err)
	}
	a.state.Get("pkg").Manifest = []string{"usr/local/Kobo/imageformats/libpkg.so"}
	code, lines := runWithProgress(t, a, "uninstall", "pkg", "--yes", "--json")
	if code != exitOK {
		t.Fatalf("uninstall = %d: %s", code, texts(lines))
	}
	if n := fixedTotal(t, lines); n != 2 {
		t.Errorf("total %d, want 2 (remove, bundle): %s", n, texts(lines))
	}
}

// An install step that the run does not need is passed over: the staging
// step keeps its number and the run ends on the last step.
func TestUpdatePlanSkipsUnusedInstallStep(t *testing.T) {
	var buf bytes.Buffer
	p := &progress{w: &buf, now: time.Now, shown: -1}
	p.AddSteps(1 + 1 + 1 + 2) // a package, install, staging, a download and the bundle
	p.Step("Checking pkg")
	p.Skip() // nothing installs live
	p.Step("Staging KoboRoot.tgz")
	p.Tail(2)
	p.Step("Getting kpm for firmware 6")
	p.Step("Building the firmware 6 bundle")
	got := parseProgress(t, buf.String())
	var steps []int
	for _, l := range got {
		steps = append(steps, l.Step)
	}
	if fmt.Sprint(steps) != "[1 3 4 5]" {
		t.Errorf("steps = %v, want [1 3 4 5]", steps)
	}
	fixedTotal(t, got)
}

// Tail passes over the planned steps that the run did not need, and grows
// the count for a command that did not plan the refresh (kpm carryover).
func TestProgressTail(t *testing.T) {
	var buf bytes.Buffer
	p := &progress{w: &buf, now: time.Now, shown: -1}
	p.AddSteps(5)
	p.Step("a")
	p.Tail(2) // steps 2 and 3 not needed
	p.Step("b")
	p.Step("c")
	got := parseProgress(t, buf.String())
	if got[1].Step != 4 || got[2].Step != 5 || got[2].Steps != 5 {
		t.Errorf("got %+v, want steps 4 and 5 of 5", got)
	}
	buf.Reset()
	q := &progress{w: &buf, now: time.Now, shown: -1}
	q.Tail(2)
	q.Step("x")
	if l := parseProgress(t, buf.String()); l[0].Step != 1 || l[0].Steps != 2 {
		t.Errorf("unplanned tail = %+v, want step 1 of 2", l[0])
	}
}

// Refresh is registry refresh and then check, two kpm runs. With
// KPM_PROGRESS_NEXT=check, registry refresh reports the plan of that check
// in "next", so the hook shows one total for the whole action from the first
// line. Without the variable, no line has the field.
func TestRegistryRefreshNext(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    string
		bundle bool
		want   int
	}{
		{"carry-over download", "check", false, 4},
		{"bundle up to date", "check", true, 1},
		{"no next command", "", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := progressCarrySetup(t)
			if tc.bundle {
				if err := a.refreshCarryover(true); err != nil {
					t.Fatal(err)
				}
			}
			forgeClient := a.client
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(nmManifest))
			}))
			t.Cleanup(srv.Close)
			a.client = forge.NewClientWithHTTP(srv.Client())
			cfg, err := a.loadRegistryConfig()
			if err != nil {
				t.Fatal(err)
			}
			host := strings.TrimPrefix(srv.URL, "https://")
			cfg.Registries = []registry.Registry{
				{Name: "one", URL: host + "/o/r", Ref: "main", Path: "registry.toml", Forge: config.ForgeForgejo},
				{Name: "two", URL: host + "/o/s", Ref: "main", Path: "registry.toml", Forge: config.ForgeForgejo},
			}
			if err := a.saveRegistryConfig(cfg); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KPM_PROGRESS_NEXT", tc.env)
			code, lines := runWithProgress(t, a, "registry", "refresh", "--json")
			if code != exitOK {
				t.Fatalf("registry refresh = %d", code)
			}
			if n := fixedTotal(t, lines); n != 2 {
				t.Errorf("own total %d, want 2 registries", n)
			}
			for _, l := range lines {
				if l.Next != tc.want {
					t.Errorf("line %q: next %d, want %d", l.Text, l.Next, tc.want)
				}
			}
			if tc.want == 0 {
				return
			}
			// The check that follows shows the count that next promised.
			t.Setenv("KPM_PROGRESS_NEXT", "")
			a.client = forgeClient
			code, lines = runWithProgress(t, a, "check", "--json")
			if code != exitOK {
				t.Fatalf("check = %d", code)
			}
			if n := fixedTotal(t, lines); n != tc.want {
				t.Errorf("check total %d, registry refresh promised %d", n, tc.want)
			}
		})
	}
}
