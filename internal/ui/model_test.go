package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/browse"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// fakeRunner is a no-op lima.Runner so the model can be constructed and driven
// in tests without ever spawning a real limactl.
type fakeRunner struct{}

func (fakeRunner) Output(context.Context, ...string) ([]byte, error)                { return nil, nil }
func (fakeRunner) Stream(context.Context, io.Reader, io.Writer, ...string) error    { return nil }
func (fakeRunner) StreamOut(context.Context, io.Reader, io.Writer, ...string) error { return nil }

func newTestModel(t *testing.T) model {
	t.Helper()
	// Isolate the managed-VM registry to a temp dir so tests never read or write
	// the developer's real index.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cli := lima.New(fakeRunner{})
	prov := &provision.Provisioner{Lima: cli}
	m, ok := New(cli, prov).(model)
	if !ok {
		t.Fatalf("New did not return a model")
	}
	return m
}

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// Pressing 'd' on the VM (detail) screen opens the confirm-delete overlay for
// the displayed VM (Delete must always confirm before destroying). Delete now
// lives on the detail screen, not the list.
func TestDeleteKeyEntersConfirm(t *testing.T) {
	m := newTestModel(t)

	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)

	if m.confirm != nil {
		t.Fatalf("model should not start with a pending confirmation")
	}

	m.view = viewDetail
	m.detail, _ = m.lookupVM("claude")
	next, _ := m.Update(runeKey('d'))
	m = next.(model)

	if m.confirm == nil {
		t.Fatalf("pressing 'd' should raise the confirm overlay")
	}
	if !strings.Contains(m.confirm.prompt, "claude") {
		t.Fatalf("confirm prompt = %q, want it to name %q", m.confirm.prompt, "claude")
	}
}

// The rendered delete prompt has shrunk to yes/cancel; recreate is now a
// separate, R-bound Reset action and must never reappear as an [r] branch
// here. Cheap insurance against that regression.
func TestConfirmDeletePromptHasNoRecreateBranch(t *testing.T) {
	m := newTestModel(t)

	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)

	m.view = viewDetail
	m.detail, _ = m.lookupVM("claude")
	next, _ := m.Update(runeKey('d'))
	m = next.(model)

	rendered := m.detailView()
	if !strings.Contains(rendered, `Delete "claude"?  [y] yes   [n] cancel`) {
		t.Fatalf("rendered prompt missing the expected text, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "[r]") {
		t.Fatalf("rendered prompt must not offer an [r] branch, got:\n%s", rendered)
	}
}

// An accidental double-tap of 'd' on the VM screen must not destroy the VM:
// only the bound Confirm key ('y') may dispatch the pending delete, and every
// other key (including a second 'd') is swallowed while confirming.
func TestDeleteNoRecreateDoubleTapIsSafe(t *testing.T) {
	m := newTestModel(t)
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)

	m.view = viewDetail
	m.detail, _ = m.lookupVM("claude")

	first, cmd1 := m.Update(runeKey('d'))
	m = first.(model)
	if m.confirm == nil {
		t.Fatal("first 'd' should raise the confirm overlay")
	}
	if cmd1 != nil {
		t.Fatal("raising the overlay must not itself dispatch a command")
	}

	second, cmd2 := m.Update(runeKey('d'))
	m = second.(model)
	if m.confirm == nil {
		t.Fatal("a second 'd' while confirming must not clear the overlay")
	}
	if cmd2 != nil {
		t.Fatal("a second 'd' while confirming must not dispatch anything (no double-tap delete)")
	}
}

// Reset ('R' on the VM screen) is gated to sand-managed VMs: pressing it on an
// UNMANAGED VM is a no-op that explains why via the status line (it must never
// replace an unrelated VM with a Claude sandbox).
func TestResetGateUnmanagedShowsStatus(t *testing.T) {
	m := newTestModel(t) // empty (temp) registry => nothing is managed

	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "default", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)

	m.view = viewDetail
	m.detail, _ = m.lookupVM("default")
	after, _ := m.Update(runeKey('R'))
	m = after.(model)

	if m.view != viewDetail {
		t.Fatalf("reset on an unmanaged VM must not leave the detail screen, view = %v", m.view)
	}
	if m.status == "" {
		t.Fatal("reset on an unmanaged VM should explain why via the status line")
	}
	if strings.Contains(strings.ToLower(m.status), "recreate") {
		t.Fatalf("status must not say 'recreate', got %q", m.status)
	}
}

// resetConfig is a fully-populated managed config used to seed reset-flow tests.
func resetConfig() vm.CreateConfig {
	return vm.CreateConfig{
		Name:     "claude",
		BaseName: "claude-base",
		Hostname: "claude-host",
		User:     "ada",
		GitName:  "Ada Lovelace",
		GitEmail: "ada@example.com",
		CPUs:     4,
		Memory:   "8GiB",
		Disk:     "100GiB",
		CloneURL: "https://github.com/org/repo",
	}
}

// openReset seeds the registry with cfg, loads it as a VM, and presses 'R' on
// the VM (detail) screen so the model lands on the pre-filled reset form. R
// opens the form directly — no confirmation step, and no hand-off to the
// list is needed.
func openReset(t *testing.T, cfg vm.CreateConfig) model {
	t.Helper()
	m := newTestModel(t)
	if err := m.reg.Add(cfg); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: cfg.Name, Status: "Stopped", CPUs: cfg.CPUs},
	}})
	m = loaded.(model)

	m.view = viewDetail
	m.detail, _ = m.lookupVM(cfg.Name)
	after, _ := m.Update(runeKey('R'))
	return after.(model)
}

// For a managed VM, Reset opens the pre-filled reset form (instead of
// provisioning immediately): Name is locked and the editable fields hold the
// VM's recorded settings.
func TestResetGateManagedOpensForm(t *testing.T) {
	cfg := resetConfig()
	m := openReset(t, cfg)

	if m.view != viewForm {
		t.Fatalf("recreate should open the form, view = %v", m.view)
	}
	if !m.resetMode {
		t.Fatalf("the form should be in reset mode")
	}
	if m.running || m.view == viewProgress {
		t.Fatalf("recreate must no longer provision immediately")
	}
	if m.resetName != cfg.Name {
		t.Fatalf("resetName = %q, want %q", m.resetName, cfg.Name)
	}
	if got := m.inputs[fHostname].Value(); got != cfg.Hostname {
		t.Fatalf("Hostname input = %q, want %q", got, cfg.Hostname)
	}
	if got := m.inputs[fDisk].Value(); got != cfg.Disk {
		t.Fatalf("Disk input = %q, want %q", got, cfg.Disk)
	}
	if !m.projectToggleEnabled {
		t.Fatalf("project toggle should be enabled when the config has a CloneURL")
	}
	// Focus must start on the first editable field, never the locked Name.
	if m.focusIdx == fName {
		t.Fatalf("focus must not start on the locked Name field")
	}
}

// Navigating onto a preserve toggle and pressing space flips its bool and shows
// the compromise warning.
func TestResetToggleFlipsAndWarns(t *testing.T) {
	m := openReset(t, resetConfig())

	// Tab through the inputs until focus lands on the first toggle.
	for i := 0; i < 20 && m.toggleFocus != 0; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = next.(model)
	}
	if m.toggleFocus != 0 {
		t.Fatalf("could not navigate to the first toggle (toggleFocus=%d)", m.toggleFocus)
	}
	if m.preserveClaude {
		t.Fatalf("preserveClaude should start false")
	}
	if strings.Contains(m.formView(), "compromised") {
		t.Fatalf("the warning must not show before any toggle is on")
	}

	sp, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m = sp.(model)
	if !m.preserveClaude {
		t.Fatalf("space on the first toggle should enable preserveClaude")
	}
	if !strings.Contains(m.formView(), "compromised") {
		t.Fatalf("the compromise warning should appear once a toggle is on")
	}
}

// The project toggle is hidden entirely when the seeded config cloned no repo
// (it can never be selected, so it should never render).
func TestResetProjectToggleDisabledWithoutCloneURL(t *testing.T) {
	cfg := resetConfig()
	cfg.CloneURL = ""
	m := openReset(t, cfg)

	if m.projectToggleEnabled {
		t.Fatalf("project toggle should be disabled when CloneURL is empty")
	}
	if strings.Contains(m.formView(), staleDisabledToggleLabel) {
		t.Fatalf("the disabled project toggle should no longer render at all")
	}
	if strings.Contains(m.formView(), "Preserve ~/") {
		t.Fatalf("no project-preserve line should render when the toggle is hidden")
	}
}

// A reset whose disk is below the base floor is rejected (the form stays put with
// an error); a valid reset dispatches and switches to the progress view.
func TestResetDiskFloorAndDispatch(t *testing.T) {
	m := openReset(t, resetConfig())

	// Below the floor: keep the form and surface an error, do not provision.
	m.inputs[fDisk].SetValue("10GiB")
	rejected, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = rejected.(model)
	if m.view == viewProgress || m.running {
		t.Fatalf("a sub-floor disk must not provision (view=%v running=%v)", m.view, m.running)
	}
	if m.formErr == nil {
		t.Fatalf("a sub-floor disk should surface a validation error")
	}

	// At/above the floor: dispatch the reset and switch to the progress view.
	m.inputs[fDisk].SetValue("100GiB")
	accepted, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = accepted.(model)
	if m.view != viewProgress || !m.running {
		t.Fatalf("a valid reset should provision (view=%v running=%v)", m.view, m.running)
	}
}

// A detail-screen lifecycle action shows a live spinner: starting a VM marks
// an action in flight (so the spinner animates and renders beside the
// status), a spinner tick keeps animating while it runs, and the matching
// actionDoneMsg clears it.
func TestListActionShowsSpinnerUntilDone(t *testing.T) {
	m := newTestModel(t)
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Stopped", CPUs: 2},
	}})
	m = loaded.(model)
	m.view = viewDetail
	m.detail, _ = m.lookupVM("claude")

	started, cmd := m.Update(runeKey('s'))
	m = started.(model)
	if !m.acting {
		t.Fatal("starting a VM should mark an action in flight")
	}
	if cmd == nil {
		t.Fatal("starting a VM should dispatch a command (action + spinner tick)")
	}
	if !strings.Contains(m.detailView(), "starting") {
		t.Fatalf("the detail view should show the in-flight status, got:\n%s", m.detailView())
	}

	// While acting, a spinner tick keeps the animation going (returns a next tick).
	_, tick := m.Update(spinner.TickMsg{})
	if tick == nil {
		t.Fatal("the spinner should keep ticking while an action is in flight")
	}

	// The action's completion clears the in-flight flag and stops the spinner.
	done, _ := m.Update(actionDoneMsg{action: "start", name: "claude"})
	m = done.(model)
	if m.acting {
		t.Fatal("actionDoneMsg should clear the in-flight flag")
	}
	if _, tick := m.Update(spinner.TickMsg{}); tick != nil {
		t.Fatal("the spinner must stop ticking once the action is done")
	}
}

// Upload from a Running VM opens the source browser with a host lister and
// records the transfer direction/VM.
func TestUploadOpensBrowserForRunningVM(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDetail
	m.detail = vm.VM{Name: "claude", Status: "Running"}

	after, cmd := m.Update(runeKey('u'))
	m = after.(model)

	if m.view != viewBrowse {
		t.Fatalf("upload on a running VM should open the browser, view=%v", m.view)
	}
	if cmd == nil {
		t.Fatal("upload should issue the browser's Open command")
	}
	if !m.transferUpload {
		t.Fatal("upload should set transferUpload=true")
	}
	if m.transferVM != "claude" {
		t.Fatalf("transferVM = %q, want claude", m.transferVM)
	}
}

// Download from a Running VM opens the browser as a guest-side (download)
// transfer.
func TestDownloadOpensBrowserForRunningVM(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDetail
	m.detail = vm.VM{Name: "claude", Status: "Running"}

	// Download rebound from 'd' to 'g': 'd' is delete everywhere now.
	after, _ := m.Update(runeKey('g'))
	m = after.(model)

	if m.view != viewBrowse {
		t.Fatalf("download on a running VM should open the browser, view=%v", m.view)
	}
	if m.transferUpload {
		t.Fatal("download should set transferUpload=false")
	}
}

// Upload/Download are guarded to running VMs: pressing 'u' on a Stopped VM stays
// on the detail view and explains why, issuing no command.
func TestTransferRequiresRunningVM(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDetail
	m.detail = vm.VM{Name: "claude", Status: "Stopped"}

	after, cmd := m.Update(runeKey('u'))
	m = after.(model)

	if m.view != viewDetail {
		t.Fatalf("upload on a stopped VM must stay on the detail view, view=%v", m.view)
	}
	if cmd != nil {
		t.Fatal("upload on a stopped VM should not issue a command")
	}
	if !strings.Contains(m.status, "must be running") {
		t.Fatalf("status should explain the running requirement, got %q", m.status)
	}
}

// Confirming the destination (ctrl+s) switches to the reused progress view and
// starts the streamed transfer (state transition only — the fake lima client
// makes the underlying copy a no-op).
func TestDestConfirmTransitionsToProgress(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDest
	m.transferVM = "claude"
	m.transferSrc = "/home/u/file.txt"
	m.transferUpload = false // download: guest source → host destination
	m.dest, _ = browse.NewDestInput("Destination dir: ", "/tmp/host-dst", nil)

	accepted, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = accepted.(model)

	if m.view != viewProgress {
		t.Fatalf("confirming the destination should switch to progress, view=%v", m.view)
	}
	if !m.running {
		t.Fatal("confirming should start the transfer (running=true)")
	}
	if cmd == nil {
		t.Fatal("confirming should return the streaming commands")
	}
	// A transfer must not be recorded as a managed VM.
	if m.provCfg.Name != "" {
		t.Fatalf("a transfer must clear provCfg so it is not recorded managed, got %q", m.provCfg.Name)
	}
}

// A completed transfer (empty provCfg) must not be added to the managed registry
// by the shared provisionDoneMsg handler.
func TestTransferNotRecordedManaged(t *testing.T) {
	m := newTestModel(t)
	m.view = viewProgress
	m.running = true
	m.provCfg = vm.CreateConfig{} // a transfer leaves this empty

	done, _ := m.Update(provisionDoneMsg{})
	m = done.(model)

	if m.reg.IsManaged("claude") {
		t.Fatal("a transfer must never be recorded as a managed VM")
	}
}

// parseLimaSize parses binary Lima sizes and bare-byte numbers, rejecting blanks
// and garbage.
func TestParseLimaSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"20GiB", 20 << 30, true},
		{"512MiB", 512 << 20, true},
		{"2TiB", 2 << 40, true},
		{"1024", 1024, true}, // bare number = bytes
		{"", 0, false},
		{"abc", 0, false},
	}
	for _, c := range cases {
		got, ok := parseLimaSize(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseLimaSize(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
	// A 100GiB reset disk must compare as larger than the 20GiB floor.
	floor, _ := parseLimaSize(vm.BaseDiskFloor)
	big, _ := parseLimaSize("100GiB")
	if !(big > floor) {
		t.Errorf("100GiB (%d) should exceed the floor %s (%d)", big, vm.BaseDiskFloor, floor)
	}
}

// cappedMemoryGiB caps the 8GiB default at half the host's RAM, rounds to a
// whole GiB, and floors at 1GiB — falling back to the full cap on an unknown host.
func TestCappedMemoryGiB(t *testing.T) {
	const gib = int64(1) << 30
	cases := []struct {
		name  string
		total int64
		want  string
	}{
		{"unknown host falls back to the cap", 0, "8GiB"},
		{"32GiB host keeps the 8GiB cap", 32 * gib, "8GiB"},
		{"16GiB host: half equals the cap", 16 * gib, "8GiB"},
		{"~15.6GiB host (reserved RAM) rounds back to 8", 15*gib + gib*6/10, "8GiB"},
		{"8GiB host gets half", 8 * gib, "4GiB"},
		{"4GiB host gets half", 4 * gib, "2GiB"},
		{"tiny host floors at 1GiB", 512 * (1 << 20), "1GiB"},
	}
	for _, c := range cases {
		if got := cappedMemoryGiB(c.total, memCapBytes); got != c.want {
			t.Errorf("%s: cappedMemoryGiB(%d) = %q, want %q", c.name, c.total, got, c.want)
		}
	}
}

// A requested disk larger than the free space on the Lima volume surfaces a
// (non-blocking) warning; a disk that fits, or an unknown free space, does not.
func TestDiskOverflowWarning(t *testing.T) {
	m := newTestModel(t)
	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)

	m.hostDiskFree = 50 << 30 // pretend the Lima volume has 50GiB free

	m.inputs[fDisk].SetValue("20GiB")
	if w := m.diskOverflowWarning(); w != "" {
		t.Errorf("20GiB under 50GiB free should not warn, got %q", w)
	}
	if strings.Contains(m.formView(), "exceeds") {
		t.Errorf("the form should not warn when the disk fits in free space")
	}

	m.inputs[fDisk].SetValue("100GiB")
	if m.diskOverflowWarning() == "" {
		t.Errorf("100GiB over 50GiB free should warn")
	}
	if !strings.Contains(m.formView(), "exceeds") {
		t.Errorf("the form should surface the disk-exceeds-free-space warning")
	}

	m.hostDiskFree = 0 // unprobed host: never warn
	if w := m.diskOverflowWarning(); w != "" {
		t.Errorf("unknown free space must not warn, got %q", w)
	}
}

// Tab navigation in the reset form skips the locked Name, walks every editable
// field then both toggles, and wraps back to Hostname — never focusing Name.
func TestResetFocusSkipsLockedNameAndWrapsToggles(t *testing.T) {
	m := openReset(t, resetConfig())

	sawToggle0, sawToggle1, wrapped := false, false, false
	for i := 0; i < 40; i++ {
		if m.toggleFocus == -1 && m.focusIdx == fName {
			t.Fatalf("focus landed on the locked Name field")
		}
		switch m.toggleFocus {
		case 0:
			sawToggle0 = true
		case 1:
			sawToggle1 = true
		}
		// A full cycle is complete once we return to Hostname after seeing a toggle.
		if sawToggle1 && m.toggleFocus == -1 && m.focusIdx == fHostname {
			wrapped = true
			break
		}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = next.(model)
	}
	if !sawToggle0 || !sawToggle1 {
		t.Fatalf("tab cycle missed a toggle (claude=%v project=%v)", sawToggle0, sawToggle1)
	}
	if !wrapped {
		t.Fatalf("tab navigation never wrapped back to the first editable field")
	}

	// Shift+tab from the first editable field wraps up to the last toggle.
	for m.focusIdx != fHostname || m.toggleFocus != -1 {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = next.(model)
	}
	prev, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = prev.(model)
	if m.toggleFocus != 1 {
		t.Fatalf("shift+tab from Hostname should wrap to the last toggle, got toggleFocus=%d", m.toggleFocus)
	}
}

// When the project toggle is disabled (no CloneURL), tab navigation reaches the
// Claude toggle but never the disabled project toggle, and the Claude toggle still
// flips.
func TestResetDisabledProjectToggleSkippedInNav(t *testing.T) {
	cfg := resetConfig()
	cfg.CloneURL = ""
	m := openReset(t, cfg)

	sawClaude := false
	for i := 0; i < 40; i++ {
		if m.toggleFocus == 1 {
			t.Fatalf("navigation must skip the disabled project toggle")
		}
		if m.toggleFocus == 0 {
			sawClaude = true
			break
		}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = next.(model)
	}
	if !sawClaude {
		t.Fatalf("navigation never reached the Claude toggle")
	}
	// Space still flips the Claude toggle even with the project toggle disabled.
	sp, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m = sp.(model)
	if !m.preserveClaude {
		t.Fatalf("space on the Claude toggle should enable preserveClaude")
	}
}

// Leaving the reset form via esc returns to the list and clears reset state, so a
// later create form ('n') is a clean, non-reset form with no preserve toggles.
func TestResetEscClearsResetMode(t *testing.T) {
	m := openReset(t, resetConfig())
	if !m.resetMode {
		t.Fatalf("precondition: form should be in reset mode")
	}

	back, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = back.(model)
	if m.view != viewList {
		t.Fatalf("esc should return to the list, got view %v", m.view)
	}
	if m.resetMode {
		t.Fatalf("esc must clear reset mode")
	}

	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)
	if m.resetMode {
		t.Fatalf("a fresh create form must not inherit reset mode")
	}
	if strings.Contains(m.formView(), "Preserve Claude") {
		t.Fatalf("a create form must not render preserve toggles")
	}
}

// Backspace inside the create form must edit the focused field, not navigate
// back to the list. (The shared Back binding also matches backspace, so the form
// has to special-case it.)
func TestBackspaceEditsFieldInForm(t *testing.T) {
	m := newTestModel(t)

	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)
	if m.view != viewForm {
		t.Fatalf("'n' should open the form, view = %v", m.view)
	}

	// Put a known value in the focused field (cursor lands at the end).
	m.inputs[m.focusIdx].SetValue("claude")

	after, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = after.(model)

	if m.view != viewForm {
		t.Fatalf("backspace must stay on the form, got view %v", m.view)
	}
	if got := m.inputs[m.focusIdx].Value(); got != "claud" {
		t.Fatalf("backspace should delete the last char: got %q, want %q", got, "claud")
	}
}

// Esc inside the create form returns to the list.
func TestEscLeavesForm(t *testing.T) {
	m := newTestModel(t)

	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)
	if m.view != viewForm {
		t.Fatalf("'n' should open the form, view = %v", m.view)
	}

	after, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = after.(model)
	if m.view != viewList {
		t.Fatalf("esc should return to the list, got view %v", m.view)
	}
}

// Shell is guarded to running VMs: pressing 'S' on the detail screen for a
// stopped VM explains why and issues no command, so the user never sees a raw
// limactl error. Shell now lives on the detail screen, not the list.
func TestShellRequiresRunningVM(t *testing.T) {
	m := newTestModel(t)
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Stopped", CPUs: 2},
	}})
	m = loaded.(model)
	m.view = viewDetail
	m.detail, _ = m.lookupVM("claude")

	after, cmd := m.Update(runeKey('S'))
	m = after.(model)
	if cmd != nil {
		t.Fatal("shell on a stopped VM should not issue a command")
	}
	if m.view == viewProgress || m.running {
		t.Fatal("shell on a stopped VM must not start anything")
	}
	if m.status == "" {
		t.Fatal("shell on a stopped VM should explain why it can't open")
	}
}

// On a running VM, 'S' on the detail screen issues the (TTY-handover) shell
// command.
func TestShellRunsForRunningVM(t *testing.T) {
	m := newTestModel(t)
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)
	m.view = viewDetail
	m.detail, _ = m.lookupVM("claude")

	if _, cmd := m.Update(runeKey('S')); cmd == nil {
		t.Fatal("shell on a running VM should issue a command")
	}
}

// Base images are distinguished from ordinary VMs: the default base name is
// recognised even with an empty registry, and any recorded clone source counts.
func TestBaseImageDetection(t *testing.T) {
	m := newTestModel(t)

	if !m.isBaseImage(vm.DefaultCreateConfig().BaseName) {
		t.Errorf("the default base name should be detected as a base image")
	}
	if m.isBaseImage("some-unrelated-vm") {
		t.Errorf("an unrelated VM must not be flagged as a base image")
	}

	if err := m.reg.Add(vm.CreateConfig{Name: "claude", BaseName: "my-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	if !m.isBaseImage("my-base") {
		t.Errorf("a recorded clone source should be detected as a base image")
	}
	if m.isBaseImage("claude") {
		t.Errorf("a managed clone is not itself a base image")
	}
}

// The create form opens with the script's defaults pre-filled for hostname-less
// fields (user, CPUs, memory, disk), but Name is intentionally left blank so it
// does not silently default to "claude".
func TestNewInputsSeedsDefaults(t *testing.T) {
	in := newInputs()
	for _, f := range []struct {
		idx  int
		name string
	}{
		{fUser, "user"}, {fCPUs, "cpus"}, {fMemory, "memory"}, {fDisk, "disk"},
	} {
		if in[f.idx].Value() == "" {
			t.Errorf("%s field should be seeded with a default, got empty", f.name)
		}
	}
	if in[fName].Value() != "" {
		t.Errorf("name field must start empty (no claude default), got %q", in[fName].Value())
	}
}

// A blank optional field must fall back to its default (mirroring the
// original bash provisioner): clearing hostname/user/memory/disk on a named VM
// yields a valid, fully populated config. Name has no default and is covered
// separately.
func TestBlankFieldsFallBackToDefaults(t *testing.T) {
	m := newTestModel(t)
	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)

	m.inputs[fName].SetValue("myvm")
	m.inputs[fHostname].SetValue("")
	m.inputs[fUser].SetValue("   ") // whitespace-only counts as blank
	m.inputs[fMemory].SetValue("")
	m.inputs[fDisk].SetValue("")
	m.inputs[fGitName].SetValue("Ada Lovelace")
	m.inputs[fGitEmail].SetValue("ada@example.com")

	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cfg.Name != "myvm" {
		t.Errorf("Name = %q, want %q", cfg.Name, "myvm")
	}
	if cfg.Hostname != "myvm" {
		t.Errorf("Hostname = %q, want it to default to the name", cfg.Hostname)
	}
	if cfg.User == "" {
		t.Errorf("User should default to the host user, got empty")
	}
	if cfg.Memory != defaultMemory() {
		t.Errorf("Memory = %q, want the host-capped default %q", cfg.Memory, defaultMemory())
	}
	if cfg.Disk != "100GiB" {
		t.Errorf("Disk = %q, want %q", cfg.Disk, "100GiB")
	}
	if cfg.CPUs < 1 {
		t.Errorf("CPUs = %d, want a positive default", cfg.CPUs)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("a fully-defaulted config should validate, got %v", err)
	}
}

// An empty Name must fail validation (no silent claude default): ctrl+s on a
// nameless form keeps the form and surfaces the error.
func TestEmptyNameFailsValidation(t *testing.T) {
	m := newTestModel(t)
	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)

	m.inputs[fName].SetValue("")
	m.inputs[fGitName].SetValue("Ada")
	m.inputs[fGitEmail].SetValue("ada@example.com")

	submitted, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = submitted.(model)

	if m.view != viewForm {
		t.Fatalf("nameless submit should keep the form, view = %v", m.view)
	}
	if m.formErr == nil {
		t.Fatalf("nameless submit should surface a validation error")
	}
}

// Enter advances to the next field rather than creating; ctrl+s creates.
func TestEnterAdvancesFieldNotSubmit(t *testing.T) {
	m := newTestModel(t)
	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)
	if m.focusIdx != 0 {
		t.Fatalf("form should open focused on the first field, got %d", m.focusIdx)
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.focusIdx != 1 {
		t.Fatalf("enter should advance to the next field, got focus %d", m.focusIdx)
	}
	if m.view != viewForm || m.running {
		t.Fatalf("enter must not start provisioning (view=%v running=%v)", m.view, m.running)
	}
}

// isQuitCmd reports whether cmd is tea.Quit (it produces a tea.QuitMsg). The
// fake runner makes any incidental command this triggers a harmless no-op.
func isQuitCmd(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// Streamed output longer than the viewport width must wrap, not be truncated:
// the bubbles viewport hard-truncates over-wide lines, which silently hid the
// tail of long Ansible error lines (e.g. clone destination paths). After the
// fix, a single long line reflows so its tail stays visible and no rendered
// line exceeds the viewport width.
func TestLongOutputLineWraps(t *testing.T) {
	m := newTestModel(t)
	m.view = viewProgress
	m.running = true

	// Width 28 -> viewport width max(20, 28-8) = 20; height leaves room for the
	// wrapped lines so GotoBottom keeps the tail on screen.
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 28, Height: 24})
	m = sized.(model)

	// A 69-char unbroken token: the first 20 chars fill one wrapped line, so a
	// truncating viewport would drop the trailing marker entirely.
	line := strings.Repeat("A", 60) + "ENDMARKER"
	out, _ := m.Update(provisionOutputMsg(line))
	m = out.(model)

	view := m.viewport.View()
	if !strings.Contains(view, "ENDMARKER") {
		t.Fatalf("wrapped output should keep the line's tail visible; got:\n%s", view)
	}
	for _, l := range strings.Split(view, "\n") {
		if w := ansi.StringWidth(l); w > m.viewport.Width {
			t.Fatalf("rendered line width %d exceeds viewport width %d: %q", w, m.viewport.Width, l)
		}
	}
}

// Idle (on the list), ctrl+c quits the whole TUI as usual.
func TestCtrlCQuitsWhenIdle(t *testing.T) {
	m := newTestModel(t)
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); !isQuitCmd(cmd) {
		t.Fatal("ctrl+c on the list should quit the TUI")
	}
}

// While a build is in flight, ctrl+c cancels the provisioner (cancels its
// context) and stays on the progress view to show the result — it must NOT quit
// the whole TUI and orphan a half-built VM.
func TestCtrlCCancelsRunningProvision(t *testing.T) {
	m := newTestModel(t)
	called := false
	m.view = viewProgress
	m.running = true
	m.cancel = func() { called = true }

	after, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = after.(model)

	if !called {
		t.Fatal("ctrl+c during a build should cancel the provisioner context")
	}
	if !m.canceled {
		t.Fatal("ctrl+c during a build should mark the run canceled")
	}
	if m.view != viewProgress {
		t.Fatalf("ctrl+c during a build should stay on the progress view, got %v", m.view)
	}
	if isQuitCmd(cmd) {
		t.Fatal("ctrl+c during a build must not quit the whole TUI")
	}
}

// q must not quit (nor navigate away) while a build runs — only ctrl+c cancels.
// This guards the footgun where q (the global quit) abandoned a running build.
func TestQDoesNotQuitDuringProvision(t *testing.T) {
	m := newTestModel(t)
	m.view = viewProgress
	m.running = true

	if _, cmd := m.Update(runeKey('q')); isQuitCmd(cmd) {
		t.Fatal("q during a build must not quit; only ctrl+c cancels")
	}
}

// A canceled run leaves partial state, so its done message must neither record
// the VM as managed nor surface the (kill-induced) error as a failure.
func TestCanceledRunIsNotRecordedManaged(t *testing.T) {
	m := newTestModel(t)
	m.view = viewProgress
	m.running = true
	m.canceled = true
	m.provCfg = vm.CreateConfig{Name: "myvm", BaseName: "claude-base"}

	done, _ := m.Update(provisionDoneMsg{err: context.Canceled})
	m = done.(model)

	if m.running {
		t.Fatal("a done message should clear running")
	}
	if m.doneErr != nil {
		t.Fatalf("a canceled run should not surface a failure, got %v", m.doneErr)
	}
	if m.reg.IsManaged("myvm") {
		t.Fatal("a canceled run must not be recorded as managed")
	}
}

// Submitting a fully valid form with ctrl+s starts provisioning.
func TestSubmitFormValidationKeepsForm(t *testing.T) {
	m := newTestModel(t)

	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)
	if m.view != viewForm {
		t.Fatalf("'n' should open the form, view = %v", m.view)
	}

	// Valid name, but force a git-name validation failure deterministically (the
	// host git config may otherwise seed a non-empty name).
	m.inputs[fName].SetValue("myvm")
	m.inputs[fGitName].SetValue("")

	submitted, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = submitted.(model)

	if m.view != viewForm {
		t.Fatalf("invalid submit should keep the form, view = %v", m.view)
	}
	if m.formErr == nil {
		t.Fatalf("invalid submit should surface a validation error")
	}
}

// The list only selects a VM now; it no longer acts on one. Former list-only
// lifecycle keys (start/stop/restart/shell/delete) are not bubbles/table
// bindings either, so on the list they simply do nothing.
func TestListKeysNoLongerDispatchLifecycleActions(t *testing.T) {
	m := newTestModel(t)
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Stopped", CPUs: 2},
	}})
	m = loaded.(model)

	after, cmd := m.Update(runeKey('s'))
	m = after.(model)

	if cmd != nil {
		t.Fatal("pressing 's' on the list should dispatch no command")
	}
	if m.acting {
		t.Fatal("pressing 's' on the list should not mark an action in flight")
	}
	if m.view != viewList {
		t.Fatalf("pressing 's' on the list should leave the view on viewList, got %v", m.view)
	}
}

// The VM screen now owns per-VM lifecycle actions: pressing 's' there marks an
// action in flight and dispatches a command against the displayed VM.
func TestDetailActionsDispatchLifecycleCommands(t *testing.T) {
	m := newTestModel(t)
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Stopped", CPUs: 2},
	}})
	m = loaded.(model)
	m.view = viewDetail
	m.detail, _ = m.lookupVM("claude")

	after, cmd := m.Update(runeKey('s'))
	m = after.(model)

	if !m.acting {
		t.Fatal("pressing 's' on the detail view should mark an action in flight")
	}
	if cmd == nil {
		t.Fatal("pressing 's' on the detail view should dispatch a command")
	}
}

// The VM screen's snapshot goes stale after a lifecycle action; a reload must
// re-seed m.detail (by name) from the fresh list so Status reflects reality
// without kicking the user back to the list — unless the VM is gone, in which
// case there is nothing left to display and the model falls back to the list.
func TestDetailRefreshReseedsFromReloadedList(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDetail
	m.detail = vm.VM{Name: "claude", Status: "Stopped", CPUs: 2}

	updated, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Running", CPUs: 2},
	}})
	m = updated.(model)

	if m.view != viewDetail {
		t.Fatalf("a reload must not change the view while the VM still exists, got %v", m.view)
	}
	if m.detail.Status != "Running" {
		t.Fatalf("m.detail.Status = %q, want %q", m.detail.Status, "Running")
	}

	gone, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "someone-else", Status: "Running"},
	}})
	m = gone.(model)
	if m.view != viewList {
		t.Fatalf("a reload where the displayed VM is gone should fall back to the list, got %v", m.view)
	}
}

// stopAllTargets must include only VMs that are sand-managed AND currently
// Running AND not a base image — never an unmanaged VM, a stopped managed VM,
// or a (possibly running, mid-build) base image.
func TestStopAllTargetsFiltersToManagedRunning(t *testing.T) {
	m := newTestModel(t)
	if err := m.reg.Add(vm.CreateConfig{Name: "managed-running", BaseName: "claude-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	if err := m.reg.Add(vm.CreateConfig{Name: "managed-stopped", BaseName: "claude-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "managed-running", Status: "Running"},
		{Name: "managed-stopped", Status: "Stopped"},
		{Name: "unmanaged-running", Status: "Running"},
		{Name: "claude-base", Status: "Running"}, // the default base image name
	}})
	m = loaded.(model)

	got := m.stopAllTargets()
	if len(got) != 1 || got[0] != "managed-running" {
		t.Fatalf("stopAllTargets() = %v, want [managed-running]", got)
	}
}

// Confirming a stop-all marks an action in flight and shows the live spinner
// beside a stop-all status — the run must not spin against a blank status line
// (the PR feedback: "There's no progress spinner when running stop all").
func TestStopAllConfirmShowsSpinner(t *testing.T) {
	m := newTestModel(t)
	if err := m.reg.Add(vm.CreateConfig{Name: "managed-running", BaseName: "claude-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "managed-running", Status: "Running"},
	}})
	m = loaded.(model)
	m.width, m.height = 100, 30 // a reported size so the view renders realistically

	// X raises the confirm overlay; y confirms it.
	raised, _ := m.Update(runeKey('X'))
	m = raised.(model)
	if m.confirm == nil {
		t.Fatal("X with a running managed VM should raise the confirm overlay")
	}
	confirmed, cmd := m.Update(runeKey('y'))
	m = confirmed.(model)

	if !m.acting {
		t.Fatal("confirming stop-all should mark an action in flight")
	}
	if cmd == nil {
		t.Fatal("confirming stop-all should dispatch a command (action + spinner tick)")
	}
	if !strings.Contains(m.status, "stopping") {
		t.Fatalf("confirming stop-all should seed a status, got %q", m.status)
	}
	if !strings.Contains(m.listView(), m.spinner.View()) {
		t.Fatalf("the list view should render the spinner while stopping, got:\n%s", m.listView())
	}
}

// The reset form flags an already-saved GH_TOKEN with a placeholder so a blank
// field is not misread as "no token" (the PR feedback: "The blank github token
// is confusing"). A VM with no saved token leaves the placeholder empty.
func TestResetFormPlaceholdersSavedToken(t *testing.T) {
	m := newTestModel(t)
	if err := m.sec.Set("has-token", map[string]string{"GH_TOKEN": "ghp_secret"}); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	m.openResetForm("has-token", vm.CreateConfig{Name: "has-token", BaseName: "claude-base"})
	if ph := m.inputs[fCloneToken].Placeholder; !strings.Contains(ph, "***") {
		t.Fatalf("token placeholder for a VM with a saved token = %q, want it to signal a saved token", ph)
	}

	m2 := newTestModel(t)
	m2.openResetForm("no-token", vm.CreateConfig{Name: "no-token", BaseName: "claude-base"})
	if ph := m2.inputs[fCloneToken].Placeholder; ph != "" {
		t.Fatalf("token placeholder for a VM with no saved token = %q, want empty", ph)
	}
}

// With no managed-running VMs, pressing X raises no confirm overlay and just
// sets an explanatory status.
func TestStopAllNoTargetsSetsStatusNoOverlay(t *testing.T) {
	m := newTestModel(t)
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "stopped-vm", Status: "Stopped"},
	}})
	m = loaded.(model)

	after, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	m = after.(model)

	if m.confirm != nil {
		t.Fatal("with no running managed VMs, X must not raise the confirm overlay")
	}
	if cmd != nil {
		t.Fatal("with no running managed VMs, X must dispatch nothing")
	}
	if m.status == "" {
		t.Fatal("X with no targets should explain via the status line")
	}
}

// stopAllFakeRunner lets Stop fail for specific instance names while
// succeeding for others, so stopAllCmd's partial-failure accumulation can be
// exercised without a real limactl.
type stopAllFakeRunner struct{ failNames map[string]bool }

func (f stopAllFakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	if len(args) >= 2 && args[0] == "stop" && f.failNames[args[1]] {
		return nil, errors.New("boom")
	}
	return nil, nil
}
func (stopAllFakeRunner) Stream(context.Context, io.Reader, io.Writer, ...string) error { return nil }
func (stopAllFakeRunner) StreamOut(context.Context, io.Reader, io.Writer, ...string) error {
	return nil
}

// stopAllCmd stops every target sequentially, accumulating failures: a fake
// client that fails for "bad" and succeeds for "good" must report an error
// naming "bad" but not "good", while still calling Stop for both (the good VM
// stays stopped even though a later one fails).
func TestStopAllCmdReportsPartialFailureByName(t *testing.T) {
	cli := lima.New(stopAllFakeRunner{failNames: map[string]bool{"bad": true}})

	msg := stopAllCmd(cli, []string{"good", "bad"})()
	done, ok := msg.(actionDoneMsg)
	if !ok {
		t.Fatalf("stopAllCmd's tea.Cmd returned %T, want actionDoneMsg", msg)
	}
	if done.err == nil {
		t.Fatal("stopAllCmd should report an error when a target fails to stop")
	}
	if !strings.Contains(done.err.Error(), "bad") {
		t.Fatalf("error %q should name the failed VM %q", done.err.Error(), "bad")
	}
	if strings.Contains(done.err.Error(), "good") {
		t.Fatalf("error %q must not name the VM that stopped successfully", done.err.Error())
	}
}

// summarizeNames truncates the display list once the running length would
// exceed the width budget, but every target is still stopped regardless of
// display — this test only checks the truncation boundary.
func TestSummarizeNamesTruncatesForNarrowWidth(t *testing.T) {
	names := []string{"web", "api", "db", "cache", "worker", "queue"}
	got := summarizeNames(names, 20) // narrow width forces truncation
	if !strings.Contains(got, "more") {
		t.Fatalf("summarizeNames with a narrow width should truncate with '...and N more', got %q", got)
	}
	full := summarizeNames(names, 500) // ample width: no truncation needed
	for _, n := range names {
		if !strings.Contains(full, n) {
			t.Fatalf("summarizeNames with ample width should include %q, got %q", n, full)
		}
	}
}

// A successful delete makes the VM screen's record disappear, so the
// completion message returns the user to the list rather than leaving them
// stranded on a VM that no longer exists. A failed delete leaves the VM in
// place, so the user stays on its screen to see the error.
func TestDeleteReturnsToListFromDetail(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDetail
	m.detail = vm.VM{Name: "claude", Status: "Running"}

	done, _ := m.Update(actionDoneMsg{action: "delete", name: "claude"})
	m = done.(model)
	if m.view != viewList {
		t.Fatalf("a successful delete should return to the list, view = %v", m.view)
	}

	m2 := newTestModel(t)
	m2.view = viewDetail
	m2.detail = vm.VM{Name: "claude", Status: "Running"}

	failed, _ := m2.Update(actionDoneMsg{action: "delete", name: "claude", err: errors.New("boom")})
	m2 = failed.(model)
	if m2.view != viewDetail {
		t.Fatalf("a failed delete must keep the user on the detail view, view = %v", m2.view)
	}
}
