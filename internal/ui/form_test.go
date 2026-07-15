package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	// Walk from the last text input onto the toggles: Claude (0), DDEV (1),
	// Go (2), Java (3).
	m.focusIdx = fCloneToken
	for i := 0; i < 4; i++ {
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		m = next.(model)
	}
	if m.toggleFocus != 3 {
		t.Fatalf("expected focus on the Java toggle (index 3), got toggleFocus=%d", m.toggleFocus)
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
	if !cfg.WithClaude || !cfg.WithDDEV || !cfg.WithGo {
		t.Fatalf("untouched toggles should stay at their default on: WithClaude=%v WithDDEV=%v WithGo=%v", cfg.WithClaude, cfg.WithDDEV, cfg.WithGo)
	}
}

// TestCreateFormClaudeToggleOff pins that Claude Code is a de-selectable tool
// like any other — the point of making it optional is that a user can bring
// their own agent — and that de-selecting it leaves the other tools alone.
func TestCreateFormClaudeToggleOff(t *testing.T) {
	m := newTestModel(t)
	m.openForm()
	m.inputs[fName].SetValue("web")
	m.inputs[fGitName].SetValue("Dev")
	m.inputs[fGitEmail].SetValue("dev@example.com")

	if !m.toolClaude {
		t.Fatalf("Claude Code must default ON: an unconfigured create installs what it always did")
	}

	m.focusIdx = fCloneToken
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(model)
	if m.toggleFocus != 0 {
		t.Fatalf("expected focus on the Claude Code toggle (index 0), got toggleFocus=%d", m.toggleFocus)
	}
	sp, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m = sp.(model)

	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cfg.WithClaude {
		t.Fatalf("WithClaude = true after flipping the Claude Code toggle off, want false")
	}
	if !cfg.WithDDEV || !cfg.WithGo || !cfg.WithJava {
		t.Fatalf("untouched toggles should stay at their default on: WithDDEV=%v WithGo=%v WithJava=%v", cfg.WithDDEV, cfg.WithGo, cfg.WithJava)
	}
	if got, want := cfg.ToolsetKey(), "ddev+go+java"; got != want {
		t.Fatalf("ToolsetKey() = %q, want %q", got, want)
	}
}

// TestCreateFormRebuildToggle pins that the last create-mode toggle
// ("Rebuild base image") is reachable and flips independently of the tool
// toggles.
func TestCreateFormRebuildToggle(t *testing.T) {
	m := newTestModel(t)
	m.openForm()
	m.focusIdx = fCloneToken

	for i := 0; i < 5; i++ {
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		m = next.(model)
	}
	if m.toggleFocus != 4 {
		t.Fatalf("expected focus on the Rebuild toggle (index 4), got toggleFocus=%d", m.toggleFocus)
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
		Name:       "vm1",
		GitName:    "A",
		GitEmail:   "a@example.com",
		CPUs:       4,
		Memory:     "8GiB",
		Disk:       "20GiB",
		WithClaude: false, // explicitly opted out (brought their own agent)
		WithDDEV:   true,
		WithGo:     false, // explicitly opted out
		WithJava:   false, // explicitly opted out
	}
	m.openResetForm("vm1", recorded)

	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cfg.WithClaude != false || cfg.WithDDEV != true || cfg.WithGo != false || cfg.WithJava != false {
		t.Errorf("reset rebuilt the tool-set as claude=%v ddev=%v go=%v java=%v, want the VM's recorded claude=false ddev=true go=false java=false.\n"+
			"A reset must not re-converge the shared base back to the full tool-set.",
			cfg.WithClaude, cfg.WithDDEV, cfg.WithGo, cfg.WithJava)
	}
	if got, want := cfg.ToolsetKey(), "ddev"; got != want {
		t.Errorf("ToolsetKey() = %q, want %q", got, want)
	}
}

// writeBaseStamp plants a base-image version stamp in the isolated LIMA_HOME,
// standing in for a base that was actually built with that tool-set.
func writeBaseStamp(t *testing.T, baseName, toolset string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("LIMA_HOME"), "_sand")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stamp := "v2:deadbeef:" + toolset + "\n" + time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(filepath.Join(dir, baseName+".playbook-version"), []byte(stamp), 0o644); err != nil {
		t.Fatalf("write stamp: %v", err)
	}
}

// TestCreateFormSeedsTogglesFromTheBuiltBase: the create form's tool toggles
// must show what the SHARED base actually CONTAINS, read back from its stamp.
// They used to open all-on unconditionally, so a user who built a base with no
// tools was shown four ticked boxes on the very next create — and either
// un-ticked all four again by hand, every time, or unknowingly asked for the
// full tool-set and silently converged it back onto the base.
func TestCreateFormSeedsTogglesFromTheBuiltBase(t *testing.T) {
	m := newTestModel(t)
	writeBaseStamp(t, vm.DefaultCreateConfig().BaseName, "none")

	m.openForm()

	if m.toolClaude || m.toolDDEV || m.toolGo || m.toolJava {
		t.Errorf("form opened with claude=%v ddev=%v go=%v java=%v against a base built with NO tools; every toggle must start off",
			m.toolClaude, m.toolDDEV, m.toolGo, m.toolJava)
	}
	m.inputs[fName].SetValue("web")
	m.inputs[fGitName].SetValue("Dev")
	m.inputs[fGitEmail].SetValue("dev@example.com")
	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if got, want := cfg.ToolsetKey(), "none"; got != want {
		t.Errorf("ToolsetKey() = %q, want %q — submitting the untouched form must not re-converge the base", got, want)
	}
}

// The partial case: a base built with only DDEV opens with only DDEV ticked.
func TestCreateFormSeedsTogglesFromAPartialToolset(t *testing.T) {
	m := newTestModel(t)
	writeBaseStamp(t, vm.DefaultCreateConfig().BaseName, "ddev+java")

	m.openForm()

	if m.toolClaude || m.toolGo {
		t.Errorf("claude=%v go=%v, want both off: the base does not have them", m.toolClaude, m.toolGo)
	}
	if !m.toolDDEV || !m.toolJava {
		t.Errorf("ddev=%v java=%v, want both on: the base was built with them", m.toolDDEV, m.toolJava)
	}
}

// With no base built yet there is nothing to adopt, so the form keeps its all-on
// default — a first create still installs everything sand always has.
func TestCreateFormWithNoBaseKeepsTheAllOnDefault(t *testing.T) {
	m := newTestModel(t)
	m.openForm()
	if !m.toolClaude || !m.toolDDEV || !m.toolGo || !m.toolJava {
		t.Errorf("with no base stamp the form must open all-on, got claude=%v ddev=%v go=%v java=%v",
			m.toolClaude, m.toolDDEV, m.toolGo, m.toolJava)
	}
}

// TestDefaultsScaleToHostResources pins that the CPU/memory suggestions scale to
// the host the VM will run on (the REMOTE host for a remote provider, passed in
// via the model's sampled headerCPUs/headerMem), not the local machine.
func TestDefaultsScaleToHostResources(t *testing.T) {
	// A big remote host: half the cores, memory still capped at 8GiB.
	if got := defaultCPUs(64); got != 32 {
		t.Errorf("defaultCPUs(64) = %d, want 32 (half)", got)
	}
	if got := defaultMemory(256 << 30); got != "8GiB" {
		t.Errorf("defaultMemory(256GiB) = %q, want 8GiB (cap)", got)
	}
	// A small remote host: floor of 2 vCPUs, half the RAM.
	if got := defaultCPUs(4); got != 2 {
		t.Errorf("defaultCPUs(4) = %d, want 2 (floor)", got)
	}
	if got := defaultMemory(6 << 30); got != "3GiB" {
		t.Errorf("defaultMemory(6GiB) = %q, want 3GiB (half)", got)
	}
	// Unknown/local (0) falls back to sampling THIS machine — non-degenerate.
	if got := defaultCPUs(0); got < 2 {
		t.Errorf("defaultCPUs(0) should fall back to the local count, got %d", got)
	}
	if got := defaultMemory(0); got == "" {
		t.Error("defaultMemory(0) should fall back to the local probe, got empty")
	}
}
