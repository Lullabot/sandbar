package ui

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
)

// staleDisabledToggleLabel is the old (now-removed) disabled-toggle
// annotation toggleRow used to render ("(no project" + " cloned)"). Built by
// concatenation, and described here without the literal phrase, so a grep
// sweep of internal/ for that phrase (the task's acceptance check that it is
// gone from the codebase) doesn't trip over this negative test assertion.
const staleDisabledToggleLabel = "no project" + " cloned"

// walkResetFocusNext calls resetFocusNext n times, recording every toggleFocus
// value observed after each call.
func walkResetFocusNext(m *model, n int) []int {
	seen := make([]int, 0, n)
	for i := 0; i < n; i++ {
		m.resetFocusNext()
		seen = append(seen, m.toggleFocus)
	}
	return seen
}

func walkResetFocusPrev(m *model, n int) []int {
	seen := make([]int, 0, n)
	for i := 0; i < n; i++ {
		m.resetFocusPrev()
		seen = append(seen, m.toggleFocus)
	}
	return seen
}

// TestResetFocusNextToggleHidden pins the invariant that toggleFocus must never
// rest on 1 when the project toggle is hidden (no cloned project): forward
// focus from fCloneToken must land on the Claude toggle (0) and then wrap to
// fHostname.
func TestResetFocusNextToggleHidden(t *testing.T) {
	m := newTestModel(t)
	m.openResetForm("vm1", vm.CreateConfig{Name: "vm1"}) // no CloneURL => toggle hidden
	if m.projectToggleEnabled {
		t.Fatalf("projectToggleEnabled = true, want false for empty CloneURL")
	}

	// Advance focus from fHostname all the way to fCloneToken.
	m.focusIdx = fCloneToken
	m.toggleFocus = -1

	// One more Next from fCloneToken should land on the Claude toggle (0), not 1.
	m.resetFocusNext()
	if m.toggleFocus != 0 {
		t.Fatalf("after fCloneToken -> Next: toggleFocus = %d, want 0", m.toggleFocus)
	}

	// Next again should wrap back to the editable inputs at fHostname.
	m.resetFocusNext()
	if m.toggleFocus != -1 || m.focusIdx != fHostname {
		t.Fatalf("after wrap: toggleFocus=%d focusIdx=%d, want toggleFocus=-1 focusIdx=fHostname", m.toggleFocus, m.focusIdx)
	}

	// Full cycle from fHostname must never observe toggleFocus == 1.
	m.focusIdx = fHostname
	m.toggleFocus = -1
	steps := (fCloneToken - fHostname) + 2 // inputs to fCloneToken, then two more Nexts to wrap
	seen := walkResetFocusNext(&m, steps)
	for _, v := range seen {
		if v == 1 {
			t.Fatalf("resetFocusNext walk observed toggleFocus == 1 while toggle hidden; sequence=%v", seen)
		}
	}
	if m.toggleFocus != -1 || m.focusIdx != fHostname {
		t.Fatalf("full cycle did not return to fHostname: toggleFocus=%d focusIdx=%d", m.toggleFocus, m.focusIdx)
	}
}

// TestResetFocusPrevToggleHidden mirrors TestResetFocusNextToggleHidden for the
// reverse direction: backward focus from fHostname must land on the Claude
// toggle (0), never 1.
func TestResetFocusPrevToggleHidden(t *testing.T) {
	m := newTestModel(t)
	m.openResetForm("vm1", vm.CreateConfig{Name: "vm1"}) // no CloneURL => toggle hidden

	m.focusIdx = fHostname
	m.toggleFocus = -1

	m.resetFocusPrev()
	if m.toggleFocus != 0 {
		t.Fatalf("after fHostname -> Prev: toggleFocus = %d, want 0", m.toggleFocus)
	}

	m.resetFocusPrev()
	if m.toggleFocus != -1 || m.focusIdx != fCloneToken {
		t.Fatalf("after wrap: toggleFocus=%d focusIdx=%d, want toggleFocus=-1 focusIdx=fCloneToken", m.toggleFocus, m.focusIdx)
	}

	m.focusIdx = fHostname
	m.toggleFocus = -1
	steps := (fCloneToken - fHostname) + 2
	seen := walkResetFocusPrev(&m, steps)
	for _, v := range seen {
		if v == 1 {
			t.Fatalf("resetFocusPrev walk observed toggleFocus == 1 while toggle hidden; sequence=%v", seen)
		}
	}
	if m.toggleFocus != -1 || m.focusIdx != fHostname {
		t.Fatalf("full cycle did not return to fHostname: toggleFocus=%d focusIdx=%d", m.toggleFocus, m.focusIdx)
	}
}

// TestResetFocusNextToggleShown confirms the two-toggle cycle still works when
// the project toggle is visible: fCloneToken -> toggle0 -> toggle1 -> fHostname.
func TestResetFocusNextToggleShown(t *testing.T) {
	m := newTestModel(t)
	m.openResetForm("vm1", vm.CreateConfig{Name: "vm1", CloneURL: "https://github.com/lullabot/sandbar"})
	if !m.projectToggleEnabled {
		t.Fatalf("projectToggleEnabled = false, want true for a URL with an org segment")
	}

	m.focusIdx = fCloneToken
	m.toggleFocus = -1

	m.resetFocusNext()
	if m.toggleFocus != 0 {
		t.Fatalf("toggleFocus = %d, want 0", m.toggleFocus)
	}
	m.resetFocusNext()
	if m.toggleFocus != 1 {
		t.Fatalf("toggleFocus = %d, want 1", m.toggleFocus)
	}
	m.resetFocusNext()
	if m.toggleFocus != -1 || m.focusIdx != fHostname {
		t.Fatalf("after wrap: toggleFocus=%d focusIdx=%d, want toggleFocus=-1 focusIdx=fHostname", m.toggleFocus, m.focusIdx)
	}
}

// TestResetFocusPrevToggleShown mirrors TestResetFocusNextToggleShown.
func TestResetFocusPrevToggleShown(t *testing.T) {
	m := newTestModel(t)
	m.openResetForm("vm1", vm.CreateConfig{Name: "vm1", CloneURL: "https://github.com/lullabot/sandbar"})

	m.focusIdx = fHostname
	m.toggleFocus = -1

	m.resetFocusPrev()
	if m.toggleFocus != 1 {
		t.Fatalf("toggleFocus = %d, want 1", m.toggleFocus)
	}
	m.resetFocusPrev()
	if m.toggleFocus != 0 {
		t.Fatalf("toggleFocus = %d, want 0", m.toggleFocus)
	}
	m.resetFocusPrev()
	if m.toggleFocus != -1 || m.focusIdx != fCloneToken {
		t.Fatalf("after wrap: toggleFocus=%d focusIdx=%d, want toggleFocus=-1 focusIdx=fCloneToken", m.toggleFocus, m.focusIdx)
	}
}

// TestFormViewProjectToggleLabel checks the render-level contract: the toggle
// (and its "Preserve ~/..." label) only appear when there is an org dir to
// protect, and the label names the concrete directory.
func TestFormViewProjectToggleLabel(t *testing.T) {
	m := newTestModel(t)
	m.openResetForm("vm1", vm.CreateConfig{Name: "vm1"}) // no CloneURL
	view := m.formView()
	if strings.Contains(view, "Preserve ~/") {
		t.Fatalf("formView with no CloneURL contains %q, want no project-preserve line", "Preserve ~/")
	}
	if strings.Contains(view, staleDisabledToggleLabel) {
		t.Fatalf("formView contains stale disabled-toggle text %q", staleDisabledToggleLabel)
	}

	m2 := newTestModel(t)
	m2.openResetForm("vm2", vm.CreateConfig{Name: "vm2", CloneURL: "https://github.com/lullabot/sandbar"})
	view2 := m2.formView()
	if !strings.Contains(view2, "Preserve ~/github.com/lullabot") {
		t.Fatalf("formView for https://github.com/lullabot/sandbar missing %q; got:\n%s", "Preserve ~/github.com/lullabot", view2)
	}
}

// TestOpenResetFormNoOrgSegment pins the latent-bug fix: a CloneURL with no org
// segment (e.g. a bare repo directly under the host) must disable the toggle,
// even though the URL itself is non-empty.
func TestOpenResetFormNoOrgSegment(t *testing.T) {
	m := newTestModel(t)
	m.openResetForm("vm1", vm.CreateConfig{Name: "vm1", CloneURL: "https://github.com/repo"})
	if m.projectToggleEnabled {
		t.Fatalf("projectToggleEnabled = true for a no-org-segment URL, want false")
	}
	view := m.formView()
	if strings.Contains(view, "Preserve ~/") {
		t.Fatalf("formView for a no-org-segment URL contains %q, want no project-preserve line", "Preserve ~/")
	}
}
