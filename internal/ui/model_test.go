package ui

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/browse"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// fakeRunner is a no-op lima.Runner so the model can be constructed and driven
// in tests without ever spawning a real limactl.
type fakeRunner struct{}

func (fakeRunner) Output(context.Context, ...string) ([]byte, error)             { return nil, nil }
func (fakeRunner) Stream(context.Context, io.Reader, io.Writer, ...string) error { return nil }

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

// Pressing 'd' on a populated list opens the confirm-delete overlay for the
// highlighted VM (Delete must always confirm before destroying).
func TestDeleteKeyEntersConfirm(t *testing.T) {
	m := newTestModel(t)

	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)

	if m.confirming {
		t.Fatalf("model should not start in confirming state")
	}

	next, _ := m.Update(runeKey('d'))
	m = next.(model)

	if !m.confirming {
		t.Fatalf("pressing 'd' should enter confirm state")
	}
	if m.confirmName != "claude" {
		t.Fatalf("confirmName = %q, want %q", m.confirmName, "claude")
	}
}

// Recreate must be gated to sand-managed VMs: pressing 'r' in the confirm
// overlay on an UNMANAGED VM is a no-op (it must never replace an unrelated VM
// with a Claude sandbox).
func TestRecreateGatedForUnmanagedVM(t *testing.T) {
	m := newTestModel(t) // empty (temp) registry => nothing is managed

	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "default", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)

	confirm, _ := m.Update(runeKey('d'))
	m = confirm.(model)
	if !m.confirming {
		t.Fatal("'d' should enter confirm state")
	}
	if m.confirmBase != "" {
		t.Fatalf("unmanaged VM must have no recreate base, got %q", m.confirmBase)
	}

	after, _ := m.Update(runeKey('r'))
	m = after.(model)
	if m.view == viewProgress || m.running {
		t.Fatal("recreate on an unmanaged VM must not start provisioning")
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

// openReset seeds the registry with cfg, loads it as a VM, and drives `d` then
// `r` so the model lands on the pre-filled reset form.
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

	confirm, _ := m.Update(runeKey('d'))
	m = confirm.(model)
	if m.confirmBase != cfg.BaseName {
		t.Fatalf("managed VM should carry its recreate base, got %q", m.confirmBase)
	}

	after, _ := m.Update(runeKey('r'))
	return after.(model)
}

// For a managed VM, recreate now opens the pre-filled reset form (instead of
// provisioning immediately): Name is locked and the editable fields hold the
// VM's recorded settings.
func TestRecreateOpensResetForm(t *testing.T) {
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

// The project toggle is disabled when the seeded config cloned no repo.
func TestResetProjectToggleDisabledWithoutCloneURL(t *testing.T) {
	cfg := resetConfig()
	cfg.CloneURL = ""
	m := openReset(t, cfg)

	if m.projectToggleEnabled {
		t.Fatalf("project toggle should be disabled when CloneURL is empty")
	}
	if !strings.Contains(m.formView(), "no project cloned") {
		t.Fatalf("the disabled project toggle should be annotated")
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

// A list lifecycle action shows a live spinner: starting a VM marks an action in
// flight (so the spinner animates and renders beside the status), a spinner tick
// keeps animating while it runs, and the matching actionDoneMsg clears it.
func TestListActionShowsSpinnerUntilDone(t *testing.T) {
	m := newTestModel(t)
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Stopped", CPUs: 2},
	}})
	m = loaded.(model)

	started, cmd := m.Update(runeKey('s'))
	m = started.(model)
	if !m.acting {
		t.Fatal("starting a VM should mark an action in flight")
	}
	if cmd == nil {
		t.Fatal("starting a VM should dispatch a command (action + spinner tick)")
	}
	if !strings.Contains(m.listView(), "starting") {
		t.Fatalf("the list should show the in-flight status, got:\n%s", m.listView())
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

	after, _ := m.Update(runeKey('d'))
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
// Claude toggle but never the inert project toggle, and the Claude toggle still
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

// Shell is guarded to running VMs: pressing 'S' on a stopped VM explains why and
// issues no command, so the user never sees a raw limactl error.
func TestShellRequiresRunningVM(t *testing.T) {
	m := newTestModel(t)
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Stopped", CPUs: 2},
	}})
	m = loaded.(model)

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

// On a running VM, 'S' issues the (TTY-handover) shell command.
func TestShellRunsForRunningVM(t *testing.T) {
	m := newTestModel(t)
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)

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

// A create-form submission with a GitHub clone URL + token must record the
// clone token as a github-scoped secret in the host store BEFORE CreateVM
// runs (provision.RecordCloneTokenSecret — the same call cmd/sand/create.go's
// doHeadlessCreate makes), so a TUI-created VM's private clone can
// authenticate. Regression: the TUI create path previously called
// prov.CreateVM directly, bypassing that call entirely. RecordCloneTokenSecret
// runs synchronously inside submitForm, before beginProvision spawns the
// provisioning goroutine, so the recorded secret is observable immediately
// after the ctrl+s keystroke (the goroutine itself needs no time to run).
func TestSubmitFormRecordsCloneTokenSecretBeforeCreate(t *testing.T) {
	m := newTestModel(t) // newTestModel sets XDG_DATA_HOME to an isolated temp dir

	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)

	m.inputs[fName].SetValue("myvm")
	m.inputs[fGitName].SetValue("Ada Lovelace")
	m.inputs[fGitEmail].SetValue("ada@example.com")
	m.inputs[fCloneURL].SetValue("https://github.com/acme/repo")
	m.inputs[fCloneToken].SetValue("ghp_supersecrettoken")

	submitted, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = submitted.(model)

	if m.view != viewProgress || !m.running {
		t.Fatalf("a valid create should start provisioning, view=%v running=%v", m.view, m.running)
	}
	if m.formErr != nil {
		t.Fatalf("a valid create must not surface a form error, got %v", m.formErr)
	}

	store, err := secrets.Load("myvm")
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	if len(store.GitHub) != 1 {
		t.Fatalf("expected the clone token recorded as one github secret before CreateVM ran, got %d", len(store.GitHub))
	}
	if got := store.GitHub[0].Scope; got != "github.com/acme" {
		t.Fatalf("recorded scope = %q, want %q", got, "github.com/acme")
	}
	if got := store.GitHub[0].Token; got != "ghp_supersecrettoken" {
		t.Fatalf("recorded token = %q, want the entered clone token", got)
	}
}
