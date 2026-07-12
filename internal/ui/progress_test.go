package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// After a finished run, esc/back returns to progressBack: the VM detail view for
// a file transfer, the list for a provision (create/recreate).
func TestProgressReturnsToBackView(t *testing.T) {
	for _, tc := range []struct {
		name string
		back view
	}{
		{"transfer returns to the detail view", viewDetail},
		{"provision returns to the list", viewList},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := model{view: viewProgress, running: false, progressBack: tc.back, keys: defaultKeys()}
			got, _ := m.updateProgress(tea.KeyPressMsg{Code: tea.KeyEsc})
			if v := got.(model).view; v != tc.back {
				t.Fatalf("esc after done: view = %v, want %v", v, tc.back)
			}
		})
	}
}
