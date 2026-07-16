package ui

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// After a finished run, esc/back returns to the view the JOB recorded: the VM
// EVERY RUN RETURNS TO THE BOARD. A job used to carry its own `back` view, because
// a transfer's log returned to the VM screen and a build's to the board; the VM
// screen is deleted, so there is one destination and the field went with it.
func TestProgressReturnsToTheBoard(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  jobKey
	}{
		{"a provision returns to the board", provisionKey(registry.LocalScope, "claude")},
		{"a transfer returns to the board too", transferKey(registry.LocalScope, "claude")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newJobRegistry()
			reg.begin(&job{key: tc.key, state: jobSucceeded})
			m := model{view: viewProgress, progressJob: tc.key, jobs: reg, keys: newKeyMap()}
			got, _ := m.updateProgress(tea.KeyPressMsg{Code: tea.KeyEsc})
			if v := got.(model).view; v != viewBoard {
				t.Fatalf("esc after done: view = %v, want the board", v)
			}
		})
	}
}

// A run whose VM disappeared out from under it (reaped by the registry) leaves
// the progress screen with nothing to show. It must still render, and esc must
// still work — the alternative is a user stranded on a screen about a VM that no
// longer exists.
func TestProgressSurvivesAReapedJob(t *testing.T) {
	m := model{view: viewProgress, progressJob: provisionKey(registry.LocalScope, "gone"), jobs: newJobRegistry(), keys: newKeyMap(), help: help.New()}
	if out := m.progressView(); !strings.Contains(out, "esc") {
		t.Fatalf("a vanished run should still offer a way out, got:\n%s", out)
	}
	got, _ := m.updateProgress(tea.KeyPressMsg{Code: tea.KeyEsc})
	if v := got.(model).view; v != viewBoard {
		t.Fatalf("esc on a vanished run should return to the list, got %v", v)
	}
}

// The log box must FIT. boxStyle draws a border and padding AROUND whatever it
// wraps, so a viewport sized to the full content width made a box of
// ContentWidth+4 — the terminal clipped the overrun and the box lost its
// right-hand border, which is what a user sees as "the log view is missing its
// right-hand line".
func TestProgressLogBoxFitsTheTerminal(t *testing.T) {
	for _, size := range []struct{ w, h int }{{80, 24}, {120, 40}, {200, 50}} {
		m := newTestModel(t)
		m = resized(m, size.w, size.h)
		l := newTeaLoop(t, m)

		job := newFakeJob()
		l.exec(l.m.beginProvision("Creating web", job.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))
		job.write(l, provisionKey(registry.LocalScope, "web"), "TASK [base : Install every base-phase package in a single transaction]\n")

		// Open the run's log — the view under test (what `l`, and now enter, show).
		l.exec(l.m.showJobLog("web"))
		if l.m.view != viewProgress {
			t.Fatalf("%dx%d: precondition: showJobLog must open the progress view, got %v", size.w, size.h, l.m.view)
		}

		view := ansi.Strip(l.m.progressView())
		for _, line := range strings.Split(view, "\n") {
			if got := ansi.StringWidth(line); got > size.w {
				t.Errorf("%dx%d: a line is %d cells wide — wider than the terminal, so it is clipped:\n%s",
					size.w, size.h, got, line)
				break
			}
		}
		// Both right-hand corners survive the render: the box is closed, not clipped.
		if !strings.Contains(view, "╮") || !strings.Contains(view, "╯") {
			t.Errorf("%dx%d: the log box lost its right-hand border:\n%s", size.w, size.h, view)
		}
	}
}
