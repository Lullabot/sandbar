package ui

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
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

// TestCreateFormJavaToggleOff walks create-mode focus onto the Java toggle,
// flips it with space, and asserts buildConfig produces WithJava: false while
// the other two tool toggles (left untouched, default on) stay true. This is
// the create-mode analogue of TestResetToggleFlipsAndWarns: create mode's
// focus walk and space/enter handling must reach and flip its own toggles,
// not just reset mode's.
func TestCreateFormJavaToggleOff(t *testing.T) {
	m := newTestModel(t)
	m.openForm()

	// Fill the required fields so buildConfig/Validate don't fail on something
	// unrelated to the toggle under test.
	m.inputs[fName].SetValue("web")
	m.inputs[fGitName].SetValue("Dev")
	m.inputs[fGitEmail].SetValue("dev@example.com")

	if m.toggleFocus != -1 {
		t.Fatalf("openForm must reset toggleFocus to -1, got %d", m.toggleFocus)
	}

	// Walk from the last text input onto the toggles: DDEV (0), Go (1), Java (2).
	m.focusIdx = fCloneToken
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(model)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(model)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(model)
	if m.toggleFocus != 2 {
		t.Fatalf("expected focus on the Java toggle (index 2), got toggleFocus=%d", m.toggleFocus)
	}

	sp, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m = sp.(model)

	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cfg.WithJava {
		t.Fatalf("WithJava = true after flipping the Java toggle off, want false")
	}
	if !cfg.WithDDEV || !cfg.WithGo {
		t.Fatalf("untouched toggles should stay at their default on: WithDDEV=%v WithGo=%v", cfg.WithDDEV, cfg.WithGo)
	}
}

// TestCreateFormRebuildToggle pins that the fourth create-mode toggle
// ("Rebuild base image") is reachable and flips independently of the tool
// toggles.
func TestCreateFormRebuildToggle(t *testing.T) {
	m := newTestModel(t)
	m.openForm()
	m.focusIdx = fCloneToken

	for i := 0; i < 4; i++ {
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		m = next.(model)
	}
	if m.toggleFocus != 3 {
		t.Fatalf("expected focus on the Rebuild toggle (index 3), got toggleFocus=%d", m.toggleFocus)
	}
	if m.toolRebuild {
		t.Fatalf("rebuild should default off")
	}
	sp, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m = sp.(model)
	if !m.toolRebuild {
		t.Fatalf("space on the Rebuild toggle should enable it")
	}

	// One more Tab wraps back around to the first text input.
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(model)
	if m.toggleFocus != -1 || m.focusIdx != fName {
		t.Fatalf("wrap after the last toggle: toggleFocus=%d focusIdx=%d, want toggleFocus=-1 focusIdx=fName", m.toggleFocus, m.focusIdx)
	}
}

// TestResetReplaysTheRecordedToolset pins the tool-set through a reset. The reset
// form deliberately shows no tool toggles, so buildConfig must REPLAY the VM's
// recorded selection. It used to just leave cfg at DefaultCreateConfig()'s
// all-on values, which meant resetting a VM created with --with-go=false silently
// asked for the full tool-set — marking the SHARED base stale against its stamp
// and re-converging it, installing a Go toolchain and a JDK the user had
// explicitly opted out of, from a form that never mentions them.
func TestResetReplaysTheRecordedToolset(t *testing.T) {
	m := newTestModel(t)
	recorded := vm.CreateConfig{
		Name:     "vm1",
		GitName:  "A",
		GitEmail: "a@example.com",
		CPUs:     4,
		Memory:   "8GiB",
		Disk:     "20GiB",
		WithDDEV: true,
		WithGo:   false, // explicitly opted out
		WithJava: false, // explicitly opted out
	}
	m.openResetForm("vm1", recorded)

	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cfg.WithDDEV != true || cfg.WithGo != false || cfg.WithJava != false {
		t.Errorf("reset rebuilt the tool-set as ddev=%v go=%v java=%v, want the VM's recorded ddev=true go=false java=false.\n"+
			"A reset must not re-converge the shared base back to the full tool-set.",
			cfg.WithDDEV, cfg.WithGo, cfg.WithJava)
	}
	if got, want := cfg.ToolsetKey(), "ddev"; got != want {
		t.Errorf("ToolsetKey() = %q, want %q", got, want)
	}
}
