package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
)

// After a finished run, esc/back returns to the view the JOB recorded: the VM
// detail view for a file transfer, the list for a provision (create/recreate).
// That "back" used to be a single field on the model, which is exactly why only
// one run could exist at a time; it now travels with the job.
func TestProgressReturnsToBackView(t *testing.T) {
	for _, tc := range []struct {
		name string
		back view
	}{
		{"transfer returns to the detail view", viewDetail},
		{"provision returns to the board", viewBoard},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newJobRegistry()
			reg.begin(&job{key: provisionKey("claude"), back: tc.back, state: jobSucceeded})
			m := model{view: viewProgress, progressJob: provisionKey("claude"), jobs: reg, keys: newKeyMap()}
			got, _ := m.updateProgress(tea.KeyPressMsg{Code: tea.KeyEsc})
			if v := got.(model).view; v != tc.back {
				t.Fatalf("esc after done: view = %v, want %v", v, tc.back)
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
