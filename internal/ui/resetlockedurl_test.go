package ui

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
)

// tabKey is the "next field" keypress the form's own navigation matches on.
func tabKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyTab} }

// resetVMConfig is the recorded config of a VM that cloned a repo — the case
// every test here is about.
func resetVMConfig() vm.CreateConfig {
	return vm.CreateConfig{
		Name:     "vm1",
		BaseName: "sandbar-base",
		CPUs:     2,
		Memory:   "8GiB",
		Disk:     "100GiB",
		GitName:  "Dev",
		GitEmail: "dev@example.com",
		CloneURL: "https://github.com/octocat/hello",
	}
}

// TestResetFormNeverFocusesTheRepoURL is the keyboard half of "a reset keeps
// the project it has": the repo URL is displayed but unreachable, so there is
// no sequence of tabs that puts a cursor in it.
//
// This is the fix for a form that quietly meant two different things. The
// preserve toggle is labelled from the VM's RECORDED repo ("Preserve
// ~/github.com/octocat") while the reset acted on whatever the URL field said,
// so pointing it at another org meant asking to keep a tree and having the old
// one discarded — and a reset that genuinely wanted a different project had no
// way to say what should happen to the existing checkout. `n` creates a VM for
// a different repo; `R` gives this one back.
func TestResetFormNeverFocusesTheRepoURL(t *testing.T) {
	m := newTestModel(t)
	m.openResetForm(registry.LocalScope, "vm1", resetVMConfig())

	// Walk the whole ring twice, forward then backward, and assert the URL row
	// never takes focus.
	steps := 2 * (len(m.inputs) + len(m.toggles()) + 2)
	for i := 0; i < steps; i++ {
		m.resetFocusNext()
		if m.toggleFocus == -1 && m.focusIdx == fCloneURL {
			t.Fatalf("forward focus landed on the locked repo URL after %d steps", i+1)
		}
	}
	for i := 0; i < steps; i++ {
		m.resetFocusPrev()
		if m.toggleFocus == -1 && m.focusIdx == fCloneURL {
			t.Fatalf("backward focus landed on the locked repo URL after %d steps", i+1)
		}
	}
}

// TestResetFormRepoURLIsRenderedLocked: the URL is still SHOWN — it is what
// identifies the project the preserve toggle is talking about — but as a
// dimmed, locked line rather than an editable box.
func TestResetFormRepoURLIsRenderedLocked(t *testing.T) {
	m := newTestModel(t)
	m.openResetForm(registry.LocalScope, "vm1", resetVMConfig())
	view := m.formView()

	if !strings.Contains(view, "https://github.com/octocat/hello") {
		t.Fatalf("reset form should still show the VM's repo; got:\n%s", view)
	}
	for _, want := range []string{"Name: vm1 (locked)", "GitHub repo URL: https://github.com/octocat/hello (locked)"} {
		if !strings.Contains(view, want) {
			t.Errorf("reset form missing %q; got:\n%s", want, view)
		}
	}

	// A VM that cloned nothing says so rather than showing a bare label.
	m2 := newTestModel(t)
	m2.openResetForm(registry.LocalScope, "vm2", vm.CreateConfig{Name: "vm2", BaseName: "sandbar-base"})
	if !strings.Contains(m2.formView(), "GitHub repo URL: (none) (locked)") {
		t.Errorf("a VM with no repo should render an explicit (none); got:\n%s", m2.formView())
	}
}

// TestResetFormTypingCannotChangeTheRepoURL drives the real key path
// (updateResetForm, which is what a keypress reaches) rather than the focus
// helpers, because that is where the previous version of this form accepted
// the edit: keystrokes are forwarded to every input, and only focus decides
// which one consumes them.
func TestResetFormTypingCannotChangeTheRepoURL(t *testing.T) {
	m := newTestModel(t)
	m.openResetForm(registry.LocalScope, "vm1", resetVMConfig())

	// Tab through every field, typing into whatever has focus.
	for i := 0; i < len(m.inputs)+len(m.toggles()); i++ {
		next, _ := m.updateResetForm(runeKey('X'))
		m = next.(model)
		next, _ = m.updateResetForm(tabKey())
		m = next.(model)
	}

	if got := m.inputs[fCloneURL].Value(); got != resetVMConfig().CloneURL {
		t.Fatalf("the locked repo URL was edited: %q", got)
	}
	if got := m.inputs[fName].Value(); got != "vm1" {
		t.Fatalf("the locked Name was edited: %q", got)
	}
}

// TestSubmitResetUsesTheRecordedRepo checks the value that actually reaches the
// provisioner. buildConfig reads the URL out of the form field; submitReset
// overrides it from the locked target, so the two can never disagree — and the
// preserve-project decision (projectToggleEnabled, computed from the same
// recorded URL) is about the same repo the reset will clone.
func TestSubmitResetUsesTheRecordedRepo(t *testing.T) {
	m := newTestModel(t)
	m.openResetForm(registry.LocalScope, "vm1", resetVMConfig())

	// Simulate the field having been changed by any means at all.
	m.inputs[fCloneURL].SetValue("https://github.com/someone-else/other")

	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	got := m.resetConfig(cfg)
	if got.CloneURL != resetVMConfig().CloneURL {
		t.Errorf("reset clone URL = %q, want the VM's recorded %q", got.CloneURL, resetVMConfig().CloneURL)
	}
	if got.Name != "vm1" || got.BaseName != "sandbar-base" {
		t.Errorf("reset target = %q on base %q, want vm1 on sandbar-base", got.Name, got.BaseName)
	}
	if !m.projectToggleEnabled || !strings.Contains(m.projectToggleLabel, "github.com/octocat") {
		t.Errorf("the preserve toggle should name the recorded repo's org, got enabled=%v label=%q", m.projectToggleEnabled, m.projectToggleLabel)
	}
}
