package main

// progress.go writes the UI progress lines of protocol v1 (kpm-progress.md in
// the super-project, section "Progress protocol v1"). The NickelKPM hook sets
// KPM_PROGRESS=1 and shows each line while a command runs. Without the
// variable, kpm writes no progress lines and its stdout does not change.
//
// A line is `PROGRESS ` and one compact JSON object, before the BEGIN_JSON
// line. A nil *progress is valid and writes nothing, so every call site can
// call it without a check.

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"kpm/internal/device"
)

// progressInterval is the shortest time between two lines in one step. A step
// change and the last line of a step always go out.
const progressInterval = time.Second

type progress struct {
	mu    sync.Mutex // the check lookups report from goroutines
	w     io.Writer
	now   func() time.Time
	last  time.Time
	shown int64 // the shown value of the last line in this step, -1 for none

	line progressLine
}

type progressLine struct {
	V     int    `json:"v"`
	Step  int    `json:"step"`
	Steps int    `json:"steps"`
	Text  string `json:"text"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Unit  string `json:"unit"`
	// Next is the step count of the commands the caller runs after this one
	// (KPM_PROGRESS_NEXT), so a chained action shows one total from its first
	// line. Absent when the caller named no next command.
	Next int `json:"next,omitempty"`
}

// newProgress returns the progress writer of this run, or nil when the caller
// did not ask for progress lines.
func newProgress() *progress {
	if os.Getenv("KPM_PROGRESS") != "1" {
		return nil
	}
	return &progress{w: os.Stdout, now: time.Now, shown: -1}
}

// AddSteps adds n steps to the step count. The count never gets smaller. A
// command adds its whole plan before its first step, so the count it shows
// does not change while it runs (kpm-progress.md, "Steps per command").
func (p *progress) AddSteps(n int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.line.Steps += n
}

// Skip passes over a planned step that the run does not need, with no line.
func (p *progress) Skip() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.line.Step++
}

// Tail starts the last n steps of the plan: the next Step is number
// steps-n+1, so the planned steps that the run did not need are passed over.
// When the plan has fewer than n steps left (a command that did not plan
// them), the count grows to fit.
func (p *progress) Tail(n int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.line.Steps-p.line.Step >= n {
		p.line.Step = p.line.Steps - n
	} else {
		p.line.Steps = p.line.Step + n
	}
}

// SetNext sets the step count of the commands that come after this one.
func (p *progress) SetNext(n int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.line.Next = n
}

// Step starts the next step with its text.
func (p *progress) Step(text string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.line.Step++
	if p.line.Steps < p.line.Step {
		p.line.Steps = p.line.Step
	}
	p.setText(text)
}

// Note changes the text of the current step, for example from "Checking x"
// to "Downloading x v1". Before the first step it reports a wait (step 0).
func (p *progress) Note(text string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.setText(text)
}

func (p *progress) setText(text string) {
	p.line.Text = text
	p.line.Done, p.line.Total, p.line.Unit = 0, 0, ""
	p.shown = -1
	p.emit()
}

// Bytes reports a download. total 0 means the size is unknown.
func (p *progress) Bytes(done, total int64) {
	if p == nil {
		return
	}
	shown := done >> 20 // MiB when the size is unknown
	if total > 0 {
		shown = done * 20 / total // 5 % steps
	}
	p.report(done, total, "bytes", shown)
}

// Items reports the items done of a step, for example packages checked.
func (p *progress) Items(done, total int) {
	if p == nil {
		return
	}
	p.report(int64(done), int64(total), "items", int64(done))
}

// BytesFunc returns a download callback for forge.WithProgress, or nil when
// progress is off.
func (p *progress) BytesFunc(total int64) func(int64) {
	if p == nil {
		return nil
	}
	return func(done int64) { p.Bytes(done, total) }
}

func (p *progress) report(done, total int64, unit string, shown int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if shown == p.shown {
		return
	}
	end := total > 0 && done >= total
	if !end && p.shown >= 0 && p.now().Sub(p.last) < progressInterval {
		return
	}
	p.line.Done, p.line.Total, p.line.Unit = done, total, unit
	p.shown = shown
	p.emit()
}

// emit writes the line in one Write, so a line never mixes with other stdout.
// MUST hold p.mu.
func (p *progress) emit() {
	p.line.V = 1
	b, err := json.Marshal(p.line)
	if err != nil {
		return
	}
	p.w.Write(append(append([]byte("PROGRESS "), b...), '\n'))
	p.last = p.now()
}

// waitForNetwork is device.WaitForNetwork with a progress line: a run on a
// slow Wi-Fi start can wait here for up to 30 s. The line goes out only when
// the wait takes longer than networkNoteAfter: a connected device answers the
// first probe, and a text it shows for a moment is only an extra repaint.
func (a *App) waitForNetwork(host string) bool {
	if a.prog == nil {
		return device.WaitForNetwork(a.client.HTTP(), host)
	}
	var mu sync.Mutex
	finished := false
	t := time.AfterFunc(networkNoteAfter, func() {
		mu.Lock()
		defer mu.Unlock()
		if !finished {
			a.prog.Note("Waiting for network")
		}
	})
	ok := device.WaitForNetwork(a.client.HTTP(), host)
	mu.Lock()
	finished = true
	mu.Unlock()
	t.Stop()
	return ok
}

// checkSteps is the progress plan of check: its own step and the steps of the
// bundle refresh after it.
func (a *App) checkSteps() int {
	return 1 + a.carryoverSteps("check", "")
}

// progressNextSteps returns the step count of the command that the caller
// runs after this one, from KPM_PROGRESS_NEXT. The hook's Refresh runs
// `registry refresh` with KPM_PROGRESS_NEXT=check, and then check. 0 when the
// variable names no command that kpm knows.
func (a *App) progressNextSteps() int {
	if a.prog == nil {
		return 0
	}
	switch os.Getenv("KPM_PROGRESS_NEXT") {
	case "check":
		return a.checkSteps()
	}
	return 0
}

// networkNoteAfter is how long a network wait runs before kpm reports it.
var networkNoteAfter = time.Second
