package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/browse"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
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
	return newTestModelWithCli(t, lima.New(fakeRunner{}))
}

// isolateHostState redirects every host location the app writes to into temp dirs,
// so a unit test can never mutate the developer's machine.
//
// XDG_DATA_HOME covers the managed-VM index and the secrets store. LIMA_HOME covers
// the base image's playbook-version stamp, and that one is not hypothetical: the TUI
// tests build a REAL provision.Provisioner over a fake lima.Runner, so driving a
// create walks ensureBaseStopped → writeBaseVersion, which wrote the CURRENT git
// version into the real ~/.lima/_sand/sandbar-base.playbook-version — stamping the
// developer's base image as freshly built from a playbook it had never seen, without
// building anything at all. A stamp is only as good as the rebuild it records, and a
// false one is worse than none: baseStale then reports the base as current and sand
// SKIPS the rebuild the user actually needs, silently cloning from a stale image.
//
// A fake Runner stops a test from RUNNING limactl. It does nothing about the files
// the code around it writes. Isolate the environment, not just the subprocess.
func isolateHostState(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	// XDG_CONFIG_HOME covers the connection-profiles store (internal/profiles):
	// New (model.go) loads it exactly like the registry/secrets stores above,
	// so without this every test process would read and seed-write the
	// developer's REAL ~/.config/sandbar/profiles.yaml the first time it built
	// a model.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// newTestModelWithCli is newTestModel's parametrized form, for tests that need
// to observe or control the lima.Client's underlying calls (e.g. a
// secretsFakeRunner asserting exactly what ApplySecrets sent toward the
// guest) rather than the no-op fakeRunner. It wraps cli in the local Lima
// provider (provider.NewLocalLima) — the same composition
// provider.NewDefault performs for a real limactl — so the model is built
// exactly the way every real entrypoint builds it, just over a fake runner.
func newTestModelWithCli(t *testing.T, cli *lima.Client) model {
	t.Helper()
	isolateHostState(t)
	prov := &provision.Provisioner{Lima: cli}
	m, ok := New(singleFleet(provider.NewLocalLima(cli, prov), registry.LocalScope)).(model)
	if !ok {
		t.Fatalf("New did not return a model")
	}
	return m
}

// singleFleet wraps one provider/scope in a one-member fleet — the zero-config
// shape (a single enabled Local profile) every unit test drives, so New builds a
// board bit-identical to the pre-fleet single-provider model.
func singleFleet(p provider.Provider, scope registry.Scope) provider.Fleet {
	prof := profiles.Profile{ID: profiles.LocalProfileID, Name: profiles.DefaultLocalName, Type: profiles.TypeLocal, Enabled: true}
	return provider.Fleet{{Profile: prof, Prov: p, Scope: scope}}
}

// putOnBoard places a VM on the board — managed AND reported by Lima, which is
// what earns it a tile — and puts the focus ring on it. Every per-VM verb fires on
// the tile under the ring now that the VM screen is gone, so this is what a test
// that used to say `m.view = viewDetail; m.detail = v` says instead.
//
// It edits m.vms in place rather than going through loadManaged, because a
// vmsLoadedMsg RECONCILES the managed index against the list it is given — passing
// one VM would quietly unmanage every other VM the test had already set up.
func putOnBoard(t *testing.T, m model, v vm.VM) model {
	t.Helper()
	if !m.reg.IsManaged(v.Name) {
		if err := m.reg.Add(vm.CreateConfig{Name: v.Name, BaseName: "sandbar-base"}); err != nil {
			t.Fatalf("seed %s as managed: %v", v.Name, err)
		}
	}
	// The single-member (local) fleet holds the loaded list; mark it connected so
	// the board is READY (boardReady) rather than treating this as a pre-connect
	// empty board.
	m.members[0].state = connConnected
	found := false
	for i := range m.members[0].vms {
		if m.members[0].vms[i].Name == v.Name {
			m.members[0].vms[i], found = v, true
		}
	}
	if !found {
		m.members[0].vms = append(m.members[0].vms, v)
	}
	m.view = viewBoard
	m.focusVM.Name = v.Name
	return m
}

// pressDispatch drives one key through the ACTIVE VIEW's dispatcher and returns the
// command THAT VIEW produced — without the heartbeat and refresh ticks Update
// batches onto every single message (see Update).
//
// A test asserting "this key dispatched nothing" cannot look at Update's command on
// the board, because those ticks are always in it. That was safe to ignore while
// these tests ran on the VM screen, which ticks nothing; it is not safe now that
// every verb fires from the board.
func pressDispatch(t *testing.T, m model, msg tea.Msg) (model, tea.Cmd) {
	t.Helper()
	next, cmd := m.dispatch(msg)
	return next.(model), cmd
}

// resized drives a real tea.WindowSizeMsg through Update, exactly as a
// terminal resize would, rather than poking m.width/m.height directly — the
// layout budgets every screen sizes itself from (m.layout) are only
// recomputed by that single WindowSizeMsg path (see applySize in model.go),
// so a test that wants a specific size must go through it too.
func resized(m model, w, h int) model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(model)
}

// runeKey builds a tea.KeyPressMsg for a single printable character, mirroring
// how a real keypress is delivered in v2 (Code and Text both carry the rune).
func runeKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// ctrlKey builds a tea.KeyPressMsg for ctrl+<r> (e.g. ctrlKey('s') is
// ctrl+s) — v2 dropped the named tea.KeyCtrlS/tea.KeyCtrlC constants in
// favor of Code+Mod.
func ctrlKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
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

	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Running", CPUs: 2})
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

	m = resized(m, 120, 40)
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Running", CPUs: 2})
	next, _ := m.Update(runeKey('d'))
	m = next.(model)

	rendered := ansi.Strip(m.boardView())
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
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Running", CPUs: 2})

	m, cmd1 := pressDispatch(t, m, runeKey('d'))
	if m.confirm == nil {
		t.Fatal("first 'd' should raise the confirm overlay")
	}
	if cmd1 != nil {
		t.Fatal("raising the overlay must not itself dispatch a command")
	}

	m, cmd2 := pressDispatch(t, m, runeKey('d'))
	if m.confirm == nil {
		t.Fatal("a second 'd' while confirming must not clear the overlay")
	}
	if cmd2 != nil {
		t.Fatal("a second 'd' while confirming must not dispatch anything (no double-tap delete)")
	}
}

// Reset ('R' on the VM screen) is gated to sand-managed VMs: it must never
// replace an unrelated VM with a Claude sandbox. Pressing it on an UNMANAGED
// VM used to be a no-op that explained why via the status line — but an
// advertised verb that fires and only prints an explanation is the same
// lying-footer bug Shell was fixed for (see TestShellOfferedOnlyWhenRunning),
// so the gate now lives in enabledFor: the help bar omits Reset entirely
// (the reason is already visible in the VM record's Managed field) and
// pressing 'R' is a silent no-op — no command, no status change.
func TestResetGateUnmanagedIsSilentNoOp(t *testing.T) {
	// An unmanaged VM gets no tile at all, so the ONLY way one is on the board is
	// boardVMs' single exception: a VM whose provision FAILED. It never became
	// managed (only a successful build records it), yet it must keep a tile — that is
	// where its failure is reported, and where it is deleted from. Reset is exactly
	// what must not be offered on it: there is no recorded base to clone from.
	m := newTestModel(t) // empty (temp) registry => nothing is managed
	m = resized(m, 120, 40)
	l := newTeaLoop(t, m)

	build := newFakeJob()
	l.exec(l.m.beginProvision("Creating orphan", build.run, vm.CreateConfig{Name: "orphan", BaseName: "sandbar-base"}))
	build.done <- errAnsibleBoom
	l.pump("the build to fail", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "orphan") })
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "orphan", Status: "Running", CPUs: 2}}})

	m = l.m
	m.focusVM.Name = "orphan"
	if m.reg.IsManaged("orphan") {
		t.Fatal("precondition: a failed build must not be recorded managed")
	}
	if _, ok := m.focusedVM(); !ok {
		t.Fatal("precondition: a failed build must still have a tile under the ring")
	}

	if strings.Contains(boardVerbs(m), "R reset") {
		t.Fatalf("an unmanaged VM's help bar must not offer reset, got:\n%s", boardVerbs(m))
	}

	after, cmd := m.Update(runeKey('R'))
	m = after.(model)

	if m.view != viewBoard {
		t.Fatalf("reset on an unmanaged VM must not navigate anywhere, view = %v", m.view)
	}
	if cmd != nil {
		t.Fatal("reset on an unmanaged VM should dispatch no command")
	}
	if m.lastMessage() != "" {
		t.Fatalf("reset on an unmanaged VM should be a silent no-op (help bar already omits it), got status %q", m.lastMessage())
	}
}

// resetConfig is a fully-populated managed config used to seed reset-flow tests.
func resetConfig() vm.CreateConfig {
	return vm.CreateConfig{
		Name:     "claude",
		BaseName: "sandbar-base",
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

	m.view = viewBoard
	m.focusVM.Name = cfg.Name
	after, _ := m.Update(runeKey('R'))
	return after.(model)
}

// The mirror of TestResetGateUnmanagedIsSilentNoOp: a sand-managed VM's help
// bar DOES offer Reset.
func TestResetGateManagedOffersReset(t *testing.T) {
	cfg := resetConfig()
	m := newTestModel(t)
	if err := m.reg.Add(cfg); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: cfg.Name, Status: "Stopped", CPUs: cfg.CPUs},
	}})
	m = loaded.(model)
	m.view = viewBoard
	m.focusVM.Name = cfg.Name

	if !strings.Contains(boardVerbs(m), "R reset") {
		t.Fatalf("a sand-managed VM's help bar should offer reset, got:\n%s", boardVerbs(m))
	}
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
	if m.jobs.anyRunning() || m.view == viewProgress {
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
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
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

	sp, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
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
// an error); a valid reset dispatches and returns to the board, where the VM being
// rebuilt shows a building tile. A reset is a build, and a build does not take the
// screen — see TestSubmittingTheCreateFormLandsOnTheBoardNotTheLog.
func TestResetDiskFloorAndDispatch(t *testing.T) {
	m := openReset(t, resetConfig())

	// Below the floor: keep the form and surface an error, do not provision.
	m.inputs[fDisk].SetValue("10GiB")
	rejected, _ := m.Update(ctrlKey('s'))
	m = rejected.(model)
	if m.view != viewForm || m.jobs.anyRunning() {
		t.Fatalf("a sub-floor disk must not provision, and must keep the form on screen (view=%v)", m.view)
	}
	if m.formErr == nil {
		t.Fatalf("a sub-floor disk should surface a validation error")
	}

	// At/above the floor: dispatch the reset and drop back to the live board.
	m.inputs[fDisk].SetValue("100GiB")
	accepted, _ := m.Update(ctrlKey('s'))
	m = accepted.(model)
	if m.view != viewBoard || !m.jobs.isRunning(registry.LocalScope, "claude") {
		t.Fatalf("a valid reset should provision and land on the board (view=%v)", m.view)
	}
	// A reset deletes and re-clones its own VM, so it must be exempt from the
	// disappeared-VM reaper — see TestResetJobSurvivesItsOwnDelete.
	m.jobs.mu.Lock()
	recreates := m.jobs.jobs[provisionKey(registry.LocalScope, "claude")].recreates
	m.jobs.mu.Unlock()
	if !recreates {
		t.Fatal("a reset's job must be flagged as one that recreates its own VM")
	}
}

// A lifecycle action shows a live spinner: starting a VM marks an action in
// flight (so the spinner animates and renders beside the status), a spinner tick
// keeps animating while it runs, and the matching actionDoneMsg clears it.
func TestLifecycleActionShowsSpinnerUntilDone(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Stopped", CPUs: 2})

	started, cmd := m.Update(runeKey('s'))
	m = started.(model)
	if !m.acting {
		t.Fatal("starting a VM should mark an action in flight")
	}
	if cmd == nil {
		t.Fatal("starting a VM should dispatch a command (action + spinner tick)")
	}
	if !strings.Contains(ansi.Strip(m.boardView()), "starting") {
		t.Fatalf("the board should show the in-flight status, got:\n%s", ansi.Strip(m.boardView()))
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
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Running"})

	if !strings.Contains(boardVerbs(m), "u upload") {
		t.Fatalf("a running VM's help bar should offer upload, got:\n%s", boardVerbs(m))
	}

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
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Running"})

	if !strings.Contains(boardVerbs(m), "g download") {
		t.Fatalf("a running VM's help bar should offer download, got:\n%s", boardVerbs(m))
	}

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

// Upload/Download are guarded to running VMs via the command registry's
// enabledFor (commandreg.go), the same way Shell is: a stopped VM's help bar
// omits both, and pressing 'u' or 'g' is a silent no-op — no command, no
// status change. This used to surface a "must be running" status message
// instead; that is the exact lying-footer pattern (advertise the verb, fire
// it, print an explanation instead of doing the thing) the registry exists
// to eliminate — see TestShellRequiresRunningVM for the precedent this
// mirrors. The running-VM mirror (help bar offers both, and pressing the key
// fires it) is covered by TestUploadOpensBrowserForRunningVM and
// TestDownloadOpensBrowserForRunningVM below.
func TestTransferRequiresRunningVM(t *testing.T) {
	m := newTestModel(t)
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Stopped"})

	rendered := boardVerbs(m)
	if strings.Contains(rendered, "u upload") {
		t.Fatalf("a stopped VM's help bar must not offer upload, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "g download") {
		t.Fatalf("a stopped VM's help bar must not offer download, got:\n%s", rendered)
	}

	for _, k := range []rune{'u', 'g'} {
		m2, cmd := pressDispatch(t, m, runeKey(k))
		if m2.view != viewBoard {
			t.Fatalf("%q on a stopped VM must stay on the board, view=%v", k, m2.view)
		}
		if cmd != nil {
			t.Fatalf("%q on a stopped VM should not issue a command", k)
		}
		if m2.lastMessage() != "" {
			t.Fatalf("%q on a stopped VM should be a silent no-op (help bar already omits it), got status %q", k, m2.lastMessage())
		}
	}
}

// Confirming the destination (ctrl+s) switches to the reused progress view and
// starts the streamed transfer (state transition only — the fake lima client
// makes the underlying copy a no-op).
func TestDestConfirmTransitionsToProgress(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDest
	m.transferVM = "claude"
	m.transferScope = registry.LocalScope
	m.transferSrc = "/home/u/file.txt"
	m.transferUpload = false // download: guest source → host destination
	m.dest, _ = browse.NewDestInput("Destination dir: ", "/tmp/host-dst", nil)

	accepted, cmd := m.Update(ctrlKey('s'))
	m = accepted.(model)

	if m.view != viewProgress {
		t.Fatalf("confirming the destination should switch to progress, view=%v", m.view)
	}
	if !m.jobs.isRunning(registry.LocalScope, "claude") {
		t.Fatal("confirming should start the transfer as a job on its VM")
	}
	if cmd == nil {
		t.Fatal("confirming should return the streaming commands")
	}
	// A transfer's job carries no provision config, so it is neither recorded as a
	// managed VM nor rendered as a Building tile.
	s, _ := m.jobs.snapshot(transferKey(registry.LocalScope, "claude"))
	if s.Provision {
		t.Fatal("a transfer must not be marked a provision")
	}
	if got := m.statusOf(registry.LocalScope, vm.VM{Name: "claude", Status: "Running"}); got != statusRunning {
		t.Fatalf("a running transfer must not make its VM read as Building, got %v", got)
	}
}

// A completed transfer (empty provCfg) must not be added to the managed registry
// by the shared provisionDoneMsg handler.
func TestTransferNotRecordedManaged(t *testing.T) {
	m := newTestModel(t)
	seedJob(t, &m, "claude", vm.CreateConfig{}) // a transfer's job carries no config

	done, _ := m.Update(provisionDoneMsg{job: transferKey(registry.LocalScope, "claude")})
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
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
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
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		m = next.(model)
	}
	prev, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
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
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		m = next.(model)
	}
	if !sawClaude {
		t.Fatalf("navigation never reached the Claude toggle")
	}
	// Space still flips the Claude toggle even with the project toggle disabled.
	sp, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
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

	back, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = back.(model)
	if m.view != viewBoard {
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

	after, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
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

	after, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = after.(model)
	if m.view != viewBoard {
		t.Fatalf("esc should return to the list, got view %v", m.view)
	}
}

// Shell is guarded to running VMs: pressing 'S' on the detail screen for a
// stopped VM is a silent no-op — no command, no state change. The guard used
// to live inline in updateDetail (surfacing a "must be running" status
// message); it now lives in the command registry's enabledFor (commandreg.go),
// so the key doesn't fire at all rather than firing and explaining itself,
// mirroring Start/Stop's fixed behaviour (see TestShellOfferedOnlyWhenRunning
// in commandreg_test.go for the matching help-bar assertion). Shell now lives
// on the detail screen, not the list.
func TestShellRequiresRunningVM(t *testing.T) {
	m := newTestModel(t)
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Stopped", CPUs: 2})

	m, cmd := pressDispatch(t, m, runeKey('S'))
	if cmd != nil {
		t.Fatal("shell on a stopped VM should not issue a command")
	}
	if m.view == viewProgress || m.jobs.anyRunning() {
		t.Fatal("shell on a stopped VM must not start anything")
	}
	if m.lastMessage() != "" {
		t.Fatalf("shell on a stopped VM should be a silent no-op (help bar already omits it), got status %q", m.lastMessage())
	}
}

// On a running VM, 'S' on the detail screen issues the (TTY-handover) shell
// command.
func TestShellRunsForRunningVM(t *testing.T) {
	m := newTestModel(t)
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Running", CPUs: 2})

	if _, cmd := pressDispatch(t, m, runeKey('S')); cmd == nil {
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
	in := newInputs(0, 0, "")
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
	if cfg.Memory != defaultMemory(m.formHostSample().mem) {
		t.Errorf("Memory = %q, want the host-capped default %q", cfg.Memory, defaultMemory(m.formHostSample().mem))
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

	submitted, _ := m.Update(ctrlKey('s'))
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

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if m.focusIdx != 1 {
		t.Fatalf("enter should advance to the next field, got focus %d", m.focusIdx)
	}
	if m.view != viewForm || m.jobs.anyRunning() {
		t.Fatalf("enter must not start provisioning (view=%v)", m.view)
	}
}

// isQuitCmd reports whether cmd is tea.Quit, or a tea.Batch containing it —
// Update (model.go) always batches whatever a dispatch returns with
// syncHeartbeats/tickRefresh, and both are routinely non-nil
// on a fresh idle model (shouldTick starts true), so a plain tea.Quit from
// ctrl+c or 'q' now regularly arrives wrapped in a tea.BatchMsg rather than
// bare. The fake runner makes any incidental command this triggers a
// harmless no-op.
func isQuitCmd(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.QuitMsg:
		return true
	case tea.BatchMsg:
		for _, c := range msg {
			if isQuitCmd(c) {
				return true
			}
		}
	}
	return false
}

// Streamed output longer than the viewport width must wrap, not be truncated:
// the bubbles viewport hard-truncates over-wide lines, which silently hid the
// tail of long Ansible error lines (e.g. clone destination paths). After the
// fix, a single long line reflows so its tail stays visible and no rendered
// line exceeds the viewport width.
func TestLongOutputLineWraps(t *testing.T) {
	m := newTestModel(t)
	seedJob(t, &m, "claude", vm.CreateConfig{Name: "claude", BaseName: "sandbar-base"})

	// The viewport's width/height come from classify's layout budget (see
	// layout.go); height leaves room for the wrapped lines so GotoBottom keeps
	// the tail on screen.
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 28, Height: 24})
	m = sized.(model)

	// A 69-char unbroken token: the first 20 chars fill one wrapped line, so a
	// truncating viewport would drop the trailing marker entirely.
	line := strings.Repeat("A", 60) + "ENDMARKER"
	out, _ := m.Update(provisionOutputMsg{job: provisionKey(registry.LocalScope, "claude"), chunk: line})
	m = out.(model)

	view := m.viewport.View()
	if !strings.Contains(view, "ENDMARKER") {
		t.Fatalf("wrapped output should keep the line's tail visible; got:\n%s", view)
	}
	for _, l := range strings.Split(view, "\n") {
		if w := ansi.StringWidth(l); w > m.viewport.Width() {
			t.Fatalf("rendered line width %d exceeds viewport width %d: %q", w, m.viewport.Width(), l)
		}
	}
}

// Idle (on the list), ctrl+c quits the whole TUI as usual.
func TestCtrlCQuitsWhenIdle(t *testing.T) {
	m := newTestModel(t)
	if _, cmd := m.Update(ctrlKey('c')); !isQuitCmd(cmd) {
		t.Fatal("ctrl+c on the list should quit the TUI")
	}
}

// While a build is in flight, ctrl+c cancels the provisioner (cancels its
// context) and stays on the progress view to show the result — it must NOT quit
// the whole TUI and orphan a half-built VM. Cancellation targets the job the
// screen is SHOWING; the per-job isolation is covered by TestTwoJobsInFlight.
func TestCtrlCCancelsRunningProvision(t *testing.T) {
	m := newTestModel(t)
	seedJob(t, &m, "claude", vm.CreateConfig{Name: "claude", BaseName: "sandbar-base"})
	called := make(chan struct{})
	m.jobs.mu.Lock()
	m.jobs.jobs[provisionKey(registry.LocalScope, "claude")].cancel = func() { close(called) }
	m.jobs.mu.Unlock()

	after, cmd := m.Update(ctrlKey('c'))
	m = after.(model)

	select {
	case <-called:
	default:
		t.Fatal("ctrl+c during a build should cancel the provisioner context")
	}
	if s, _ := m.jobs.snapshot(provisionKey(registry.LocalScope, "claude")); !s.Canceled {
		t.Fatal("ctrl+c during a build should mark the run canceled")
	}
	if m.view != viewProgress {
		t.Fatalf("ctrl+c during a build should stay on the progress view, got %v", m.view)
	}
	if isQuitCmd(cmd) {
		t.Fatal("ctrl+c during a build must not quit the whole TUI")
	}
}

// q must not quit while the build you are watching is still running — the reflex
// that ends a session must not orphan a half-built VM. esc is how you leave (and
// it leaves the build running); ctrl+c is how you cancel.
func TestQDoesNotQuitDuringProvision(t *testing.T) {
	m := newTestModel(t)
	seedJob(t, &m, "claude", vm.CreateConfig{Name: "claude", BaseName: "sandbar-base"})

	if _, cmd := m.Update(runeKey('q')); isQuitCmd(cmd) {
		t.Fatal("q during a build must not quit; esc backgrounds it and ctrl+c cancels")
	}
}

// A canceled run leaves partial state, so its done message must neither record
// the VM as managed nor surface the (kill-induced) error as a failure.
func TestCanceledRunIsNotRecordedManaged(t *testing.T) {
	m := newTestModel(t)
	seedJob(t, &m, "myvm", vm.CreateConfig{Name: "myvm", BaseName: "sandbar-base"})
	if !m.jobs.cancelJob(provisionKey(registry.LocalScope, "myvm")) {
		t.Fatal("precondition: the running job should be cancellable")
	}

	done, _ := m.Update(provisionDoneMsg{job: provisionKey(registry.LocalScope, "myvm"), err: context.Canceled})
	m = done.(model)

	s, ok := m.jobs.snapshot(provisionKey(registry.LocalScope, "myvm"))
	if !ok || s.Running() {
		t.Fatal("a done message should end the run")
	}
	if s.Err != nil {
		t.Fatalf("a canceled run should not surface a failure, got %v", s.Err)
	}
	if s.Failed() {
		t.Fatal("a canceled run is not a failure — its VM must not render as Failed")
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

	submitted, _ := m.Update(ctrlKey('s'))
	m = submitted.(model)

	if m.view != viewForm {
		t.Fatalf("invalid submit should keep the form, view = %v", m.view)
	}
	if m.formErr == nil {
		t.Fatalf("invalid submit should surface a validation error")
	}
}

// The VM screen now owns per-VM lifecycle actions: pressing 's' there marks an
// action in flight and dispatches a command against the displayed VM.
func TestDetailActionsDispatchLifecycleCommands(t *testing.T) {
	m := newTestModel(t)
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Stopped", CPUs: 2})

	after, cmd := m.Update(runeKey('s'))
	m = after.(model)

	if !m.acting {
		t.Fatal("pressing 's' on the detail view should mark an action in flight")
	}
	if cmd == nil {
		t.Fatal("pressing 's' on the detail view should dispatch a command")
	}
}

// stopAllTargets must include only VMs that are sand-managed AND currently
// Running AND not a base image — never an unmanaged VM, a stopped managed VM,
// or a (possibly running, mid-build) base image.
func TestStopAllTargetsFiltersToManagedRunning(t *testing.T) {
	m := newTestModel(t)
	if err := m.reg.Add(vm.CreateConfig{Name: "managed-running", BaseName: "sandbar-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	if err := m.reg.Add(vm.CreateConfig{Name: "managed-stopped", BaseName: "sandbar-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "managed-running", Status: "Running"},
		{Name: "managed-stopped", Status: "Stopped"},
		{Name: "unmanaged-running", Status: "Running"},
		{Name: "sandbar-base", Status: "Running"}, // the default base image name
	}})
	m = loaded.(model)

	got := m.stopAllTargets()
	if len(got) != 1 || got[0].Name != "managed-running" {
		t.Fatalf("stopAllTargets() = %v, want [managed-running]", got)
	}
}

// Confirming a stop-all marks an action in flight and shows the live spinner
// beside a stop-all status — the run must not spin against a blank status line
// (the PR feedback: "There's no progress spinner when running stop all").
func TestStopAllConfirmShowsSpinner(t *testing.T) {
	m := newTestModel(t)
	if err := m.reg.Add(vm.CreateConfig{Name: "managed-running", BaseName: "sandbar-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "managed-running", Status: "Running"},
	}})
	m = loaded.(model)
	m = resized(m, 100, 30) // a reported size so the view renders realistically

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
	if !strings.Contains(m.lastMessage(), "stopping") {
		t.Fatalf("confirming stop-all should seed a status, got %q", m.lastMessage())
	}
	if !strings.Contains(m.boardView(), m.spinner.View()) {
		t.Fatalf("the board should render the spinner while stopping, got:\n%s", m.boardView())
	}
}

// The reset form flags an already-saved GH_TOKEN with a placeholder so a blank
// field is not misread as "no token" (the PR feedback: "The blank github token
// is confusing"). A VM with no saved token leaves the placeholder empty.
func TestResetFormPlaceholdersSavedToken(t *testing.T) {
	m := newTestModel(t)
	if err := m.sec.Set("has-token", registry.LocalScope, map[string]string{"GH_TOKEN": "ghp_secret"}); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	m.openResetForm(registry.LocalScope, "has-token", vm.CreateConfig{Name: "has-token", BaseName: "sandbar-base"})
	if ph := m.inputs[fCloneToken].Placeholder; !strings.Contains(ph, "***") {
		t.Fatalf("token placeholder for a VM with a saved token = %q, want it to signal a saved token", ph)
	}

	m2 := newTestModel(t)
	m2.openResetForm(registry.LocalScope, "no-token", vm.CreateConfig{Name: "no-token", BaseName: "sandbar-base"})
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

	after, cmd := m.Update(runeKey('X'))
	m = after.(model)

	if m.confirm != nil {
		t.Fatal("with no running managed VMs, X must not raise the confirm overlay")
	}
	if cmd != nil {
		t.Fatal("with no running managed VMs, X must dispatch nothing")
	}
	if m.lastMessage() == "" {
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
	p := provider.NewLocalLima(cli, &provision.Provisioner{Lima: cli})

	msg := stopAllCmd(p, registry.LocalScope, []string{"good", "bad"})()
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

// TestFleetConnectingHint: while every member is still connecting/errored and no
// tiles have landed, the board shows the "connecting to N profiles…" hint (with
// the first member's error) instead of the empty-slot create invitation. The
// board stays interactive throughout — a fleet never blocks on one member's
// handshake — and the hint gives way to the ghost the moment a member connects.
func TestFleetConnectingHint(t *testing.T) {
	isolateHostState(t)
	remote := registry.Scope{Provider: "lima-remote", RemoteTarget: "andrew@host:22"}
	m, ok := New(singleFleet(&providerfake.Provider{}, remote)).(model)
	if !ok {
		t.Fatal("New did not return a model")
	}
	m = resized(m, 100, 30)

	// Nothing has connected: the grid shows the connecting hint, NOT the ghost.
	if m.boardReady() {
		t.Fatal("a fleet with no successful list yet must not be boardReady")
	}
	if m.showsGhost() {
		t.Fatal("the create-invitation ghost must be withheld until a member connects")
	}
	if got := m.gridView(); !strings.Contains(got, "Connecting to 1 profile") {
		t.Fatalf("gridView should show the fleet-connecting hint, got:\n%s", got)
	}

	// A failed first connect marks the member errored and surfaces its error in
	// the hint — but the board is still interactive (a key opens a real screen).
	failed, _ := m.Update(vmsLoadedMsg{scope: remote, err: errors.New("ssh: connection refused")})
	fm := failed.(model)
	if fm.members[0].state != connErrored {
		t.Fatalf("a failed first list should mark the member errored, got %v", fm.members[0].state)
	}
	if got := fm.gridView(); !strings.Contains(got, "ssh: connection refused") {
		t.Fatalf("the connecting hint should surface the member's error, got:\n%s", got)
	}
	opened, _ := fm.Update(runeKey('n'))
	if opened.(model).view != viewForm {
		t.Fatal("the board must stay interactive while the fleet connects: 'n' should open the create form")
	}

	// The first successful list connects the member and dismisses the hint.
	done, _ := fm.Update(vmsLoadedMsg{scope: remote})
	dm := done.(model)
	if !dm.boardReady() {
		t.Fatal("a successful list should make the board ready")
	}
	if !dm.showsGhost() {
		t.Fatal("once a member connects, the empty board should offer the create ghost")
	}
}

// TestLocalMemberConnectsFast: a local member starts connecting and reaches
// connected as soon as its (instant) first list lands — there is never a
// user-facing blocking interstitial for local Lima.
func TestLocalMemberConnectsFast(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	if m.members[0].state != connConnecting {
		t.Fatalf("a fresh member starts connecting, got %v", m.members[0].state)
	}
	done, _ := m.Update(vmsLoadedMsg{scope: registry.LocalScope})
	if done.(model).members[0].state != connConnected {
		t.Fatal("the first successful list should connect the local member")
	}
}
