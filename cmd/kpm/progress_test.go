package main

// progress_test.go covers the UI progress lines (progress.go, protocol v1).

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Without KPM_PROGRESS=1 there is no writer, and a nil writer is a no-op.
func TestProgressOffWritesNothing(t *testing.T) {
	t.Setenv("KPM_PROGRESS", "")
	p := newProgress()
	if p != nil {
		t.Fatal("progress must be off without KPM_PROGRESS=1")
	}
	p.AddSteps(2)
	p.Step("x")
	p.Note("y")
	p.Bytes(1, 2)
	p.Items(1, 2)
	if p.BytesFunc(5) != nil {
		t.Error("BytesFunc must be nil when off")
	}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func parseProgress(t *testing.T, out string) []progressLine {
	t.Helper()
	var lines []progressLine
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, "PROGRESS ") {
			continue
		}
		var pl progressLine
		if err := json.Unmarshal([]byte(strings.TrimPrefix(l, "PROGRESS ")), &pl); err != nil {
			t.Fatalf("bad progress line %q: %v", l, err)
		}
		if pl.V != 1 {
			t.Fatalf("v = %d, want 1", pl.V)
		}
		lines = append(lines, pl)
	}
	return lines
}

// A step change always writes. In a step, a line goes out only when the shown
// value changes, at most once per second, except the last one.
func TestProgressRateLimit(t *testing.T) {
	var buf bytes.Buffer
	c := &fakeClock{t: time.Unix(1000, 0)}
	p := &progress{w: &buf, now: c.now, shown: -1}

	p.AddSteps(2)
	p.Step("Downloading a") // 1
	p.Bytes(0, 1000)        // 2: 0 %
	p.Bytes(30, 1000)       // same 5 % bucket: dropped
	c.t = c.t.Add(300 * time.Millisecond)
	p.Bytes(100, 1000) // 10 %, but too soon: dropped
	c.t = c.t.Add(time.Second)
	p.Bytes(200, 1000)  // 3: 20 %
	p.Bytes(1000, 1000) // 4: the end goes out at once
	p.Step("Staging")   // 5

	got := parseProgress(t, buf.String())
	if len(got) != 5 {
		t.Fatalf("got %d lines, want 5:\n%s", len(got), buf.String())
	}
	if got[0].Step != 1 || got[0].Steps != 2 || got[0].Text != "Downloading a" || got[0].Unit != "" {
		t.Errorf("first line = %+v", got[0])
	}
	if got[2].Done != 200 || got[2].Total != 1000 || got[2].Unit != "bytes" {
		t.Errorf("third line = %+v", got[2])
	}
	if got[3].Done != 1000 {
		t.Errorf("the 100 %% line is missing: %+v", got[3])
	}
	if got[4].Step != 2 || got[4].Steps != 2 || got[4].Done != 0 || got[4].Unit != "" {
		t.Errorf("a new step must reset the in-step progress: %+v", got[4])
	}
	for _, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if !strings.HasPrefix(l, "PROGRESS {") {
			t.Errorf("not a protocol line: %q", l)
		}
	}
}

// A step past the count raises the count: it never shows "3 of 2".
func TestProgressStepsNeverBelowStep(t *testing.T) {
	var buf bytes.Buffer
	p := &progress{w: &buf, now: time.Now, shown: -1}
	p.Note("Waiting for network")
	p.Step("a")
	p.Step("b")
	got := parseProgress(t, buf.String())
	if got[0].Step != 0 || got[0].Text != "Waiting for network" {
		t.Errorf("a wait before the first step is step 0: %+v", got[0])
	}
	if got[2].Step != 2 || got[2].Steps != 2 {
		t.Errorf("last = %+v, want step 2 of 2", got[2])
	}
}

// checkLines asserts the protocol order rules on a whole run: progress lines
// only before BEGIN_JSON, BEGIN_JSON last, steps never back, count >= step.
func checkLines(t *testing.T, out string) []progressLine {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.HasPrefix(lines[len(lines)-1], "BEGIN_JSON") {
		t.Fatalf("BEGIN_JSON must be the last line:\n%s", out)
	}
	got := parseProgress(t, out)
	if len(got) == 0 {
		t.Fatalf("no progress lines:\n%s", out)
	}
	steps := 0
	for i, l := range got {
		if l.Step > l.Steps {
			t.Errorf("line %d: step %d of %d", i, l.Step, l.Steps)
		}
		if l.Steps < steps {
			t.Errorf("line %d: the step count went down to %d from %d", i, l.Steps, steps)
		}
		if i > 0 && l.Step < got[i-1].Step {
			t.Errorf("line %d: step went back", i)
		}
		steps = l.Steps
	}
	return got
}

func texts(lines []progressLine) string {
	var s []string
	for _, l := range lines {
		s = append(s, l.Text)
	}
	return strings.Join(s, " | ")
}

// The B case: check, then the carry-over refresh downloads the 6.x builds.
// Its progress lines come after the check and before the held payload, so the
// hook sees the download and BEGIN_JSON stays last.
func TestCheckCarryoverProgressBeforePayload(t *testing.T) {
	a, _ := carryoverSetup(t)
	a.locked = true // carryoverAfter runs only under the lock, as in run()
	heldJSON = &[]string{}
	t.Cleanup(func() { heldJSON = nil })

	var code int
	out := captureStdout(t, func() {
		a.prog = &progress{w: os.Stdout, now: time.Now, shown: -1}
		code = a.cmdCheck([]string{"--json"})
		a.carryoverAfter("check")
		writeHeldJSON()
	})
	if code != exitOK {
		t.Fatalf("check = %d\n%s", code, out)
	}
	got := checkLines(t, out)
	all := texts(got)
	for _, want := range []string{
		"Checking for updates",
		"Getting kpm for firmware 6", "Downloading kpm v2.0.0 for firmware 6", "Verifying kpm",
		"Getting pkg for firmware 6", "Building the firmware 6 bundle",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in %s", want, all)
		}
	}
	last := got[len(got)-1]
	if last.Text != "Building the firmware 6 bundle" || last.Step != last.Steps || last.Steps != 4 {
		t.Errorf("last = %+v, want step 4 of 4 (check, 2 downloads, bundle)", last)
	}
	items := false
	for _, l := range got {
		if l.Text == "Checking for updates" && l.Unit == "items" && l.Done == l.Total && l.Total == 4 {
			items = true
		}
	}
	if !items {
		t.Errorf("check must end its step on 4 of 4 packages: %s", out)
	}
}

// update --all reports a step per package, the download in bytes up to the
// asset size, and the staging step.
func TestUpdateProgress(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.13.0")
	writeFirmware(t, a, fw4)
	dualRelease(t, a, false, nil)
	var code int
	out := captureStdout(t, func() {
		a.prog = &progress{w: os.Stdout, now: time.Now, shown: -1}
		code = a.cmdUpdate([]string{"--all", "--json"})
	})
	if code != exitOK {
		t.Fatalf("update = %d\n%s", code, out)
	}
	got := checkLines(t, out)
	all := texts(got)
	for _, want := range []string{"Checking pkg", "Downloading pkg v2.0.0", "Verifying pkg", "Staging KoboRoot.tgz"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in %s", want, all)
		}
	}
	end := false
	for _, l := range got {
		if l.Unit == "bytes" && l.Total > 0 && l.Done == l.Total {
			end = true
		}
	}
	if !end {
		t.Errorf("no 100 %% download line:\n%s", out)
	}
	if last := got[len(got)-1]; last.Step != 2 || last.Steps != 2 {
		t.Errorf("last = %+v, want step 2 of 2", last)
	}
}

// With progress off, stdout has no protocol line: the CLI and the old hook see
// the same output as before.
func TestUpdateNoProgressWhenOff(t *testing.T) {
	a := newTestApp(t)
	setVersion(t, "0.13.0")
	writeFirmware(t, a, fw4)
	dualRelease(t, a, false, nil)
	out := captureStdout(t, func() { a.cmdUpdate([]string{"--all", "--json"}) })
	if strings.Contains(out, "PROGRESS") {
		t.Errorf("progress lines without KPM_PROGRESS:\n%s", out)
	}
}

// A network wait that ends at once writes no line. A long one writes
// "Waiting for network" as step 0.
func TestWaitForNetworkNote(t *testing.T) {
	a := newTestApp(t)
	host := assetsServer(t, a, "v1", nil)
	var buf bytes.Buffer
	a.prog = &progress{w: &buf, now: time.Now, shown: -1}
	if !a.waitForNetwork(host) {
		t.Fatal("the fake forge must answer")
	}
	if buf.Len() != 0 {
		t.Errorf("a quick wait wrote %q", buf.String())
	}
	old := networkNoteAfter
	networkNoteAfter = 0
	t.Cleanup(func() { networkNoteAfter = old })
	time.Sleep(10 * time.Millisecond)
	a.waitForNetwork(host)
	// The note may race the end of a 0 s wait. Either it is absent, or it is
	// a step 0 line with the wait text.
	for _, l := range parseProgress(t, buf.String()) {
		if l.Step != 0 || l.Text != "Waiting for network" {
			t.Errorf("unexpected line %+v", l)
		}
	}
}

// check <id> (the Check button of the detail page) runs the same carry-over
// refresh, so its download shows as a step too.
func TestCheckOneCarryoverProgress(t *testing.T) {
	a, _ := carryoverSetup(t)
	a.locked = true
	heldJSON = &[]string{}
	t.Cleanup(func() { heldJSON = nil })
	var code int
	out := captureStdout(t, func() {
		a.prog = &progress{w: os.Stdout, now: time.Now, shown: -1}
		code = a.cmdCheck([]string{"pkg", "--json"})
		a.carryoverAfter("check")
		writeHeldJSON()
	})
	if code != exitOK {
		t.Fatalf("check pkg = %d\n%s", code, out)
	}
	got := checkLines(t, out)
	all := texts(got)
	for _, want := range []string{"Checking for updates", "Getting kpm for firmware 6", "Building the firmware 6 bundle"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in %s", want, all)
		}
	}
}
