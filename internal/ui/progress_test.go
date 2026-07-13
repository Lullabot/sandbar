package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
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
		{"a provision returns to the board", provisionKey("claude")},
		{"a transfer returns to the board too", transferKey("claude")},
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
	m := model{view: viewProgress, progressJob: provisionKey("gone"), jobs: newJobRegistry(), keys: newKeyMap(), help: help.New()}
	if out := m.progressView(); !strings.Contains(out, "esc") {
		t.Fatalf("a vanished run should still offer a way out, got:\n%s", out)
	}
	got, _ := m.updateProgress(tea.KeyPressMsg{Code: tea.KeyEsc})
	if v := got.(model).view; v != viewBoard {
		t.Fatalf("esc on a vanished run should return to the list, got %v", v)
	}
}
