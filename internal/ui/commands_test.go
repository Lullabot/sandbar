package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
)

// testProvider wraps a fake-runner-backed lima.Client in the local Lima
// provider (the same composition provider.NewDefault performs against a real
// limactl), so commands.go's provider-typed functions can be exercised
// against a fake without a real limactl — mirrors newTestModelWithCli in
// model_test.go.
func testProvider(cli *lima.Client) provider.Provider {
	return provider.NewLocalLima(cli, &provision.Provisioner{Lima: cli})
}

// secretsFakeRunner is a lima.Runner whose Output (backs cli.Start/cli.Stop)
// and Stream (backs cli.Shell, which provision.ApplySecrets uses) calls can
// each be made to fail independently, and which records every Stream
// invocation's argv and stdin so a test can assert exactly what ApplySecrets
// sent toward the guest.
type secretsFakeRunner struct {
	failOutput bool // makes Output (cli.Start/cli.Stop) fail
	failStream bool // makes Stream (cli.Shell / ApplySecrets) fail

	streamCalls [][]string
	streamStdin []string
}

func (f *secretsFakeRunner) Output(_ context.Context, _ ...string) ([]byte, error) {
	if f.failOutput {
		return nil, errors.New("boom: start/stop failed")
	}
	return nil, nil
}

func (f *secretsFakeRunner) Stream(_ context.Context, stdin io.Reader, _ io.Writer, args ...string) error {
	f.streamCalls = append(f.streamCalls, append([]string{}, args...))
	body := ""
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		body = string(b)
	}
	f.streamStdin = append(f.streamStdin, body)
	if f.failStream {
		return errors.New("boom: shell failed")
	}
	return nil
}

func (f *secretsFakeRunner) StreamOut(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	return f.Stream(ctx, stdin, out, args...)
}

// (a) A successful Start is followed by an ApplySecrets call (a lima Shell
// invocation targeting the VM) carrying the VM's stored pairs on stdin, run
// as the given guest user.
func TestStartAppliesSecretsAfterSuccessfulStart(t *testing.T) {
	fr := &secretsFakeRunner{}
	cli := lima.New(fr)

	msg := startCmd(testProvider(cli), "claude", "ada", map[string]map[string]string{"": {"GH_TOKEN": "ghp_x"}})()
	done, ok := msg.(actionDoneMsg)
	if !ok {
		t.Fatalf("startCmd's tea.Cmd returned %T, want actionDoneMsg", msg)
	}
	if done.err != nil {
		t.Fatalf("done.err = %v, want nil", done.err)
	}
	if done.warn != "" {
		t.Fatalf("done.warn = %q, want empty on a successful apply", done.warn)
	}
	// ApplySecrets makes three guest calls for a global-only apply: the write
	// itself, the idempotent ~/.profile + ~/.bashrc source-line ensure, and
	// the unconditional git-credential reconcile pass (task 04 — runs even
	// though a global-scope GH_TOKEN is not itself git-credential-wired; see
	// internal/provision/gitcred.go).
	if len(fr.streamCalls) != 3 {
		t.Fatalf("expected exactly three Shell (ApplySecrets) calls, got %d: %v", len(fr.streamCalls), fr.streamCalls)
	}
	call := fr.streamCalls[0]
	if call[0] != "shell" || call[1] != "claude" {
		t.Fatalf("Shell args = %v, want it to target claude", call)
	}
	// `sudo -H -u`, not `-iu`: -i runs a login shell that re-parses (and mangles)
	// the multi-line script. See TestApplySecrets_SudoDoesNotUseLoginShell.
	if joined := strings.Join(call, " "); !strings.Contains(joined, "sudo -H -u ada") {
		t.Fatalf("Shell args %v should run as the guest user ada", call)
	}
	if !strings.Contains(fr.streamStdin[0], "GH_TOKEN") {
		t.Fatalf("stdin %q should carry the rendered GH_TOKEN pair", fr.streamStdin[0])
	}
}

// (b) A failing Start must not be followed by an ApplySecrets call at all.
func TestStartFailureSkipsApplySecrets(t *testing.T) {
	fr := &secretsFakeRunner{failOutput: true}
	cli := lima.New(fr)

	msg := startCmd(testProvider(cli), "claude", "ada", map[string]map[string]string{"": {"A": "1"}})()
	done, ok := msg.(actionDoneMsg)
	if !ok {
		t.Fatalf("startCmd's tea.Cmd returned %T, want actionDoneMsg", msg)
	}
	if done.err == nil {
		t.Fatal("a failing Start should surface as done.err")
	}
	if len(fr.streamCalls) != 0 {
		t.Fatalf("a failing Start must not be followed by ApplySecrets, got %d Shell calls", len(fr.streamCalls))
	}
}

// restartCmd is not redundant with startCmd (it drives cli.Stop/cli.Start
// directly), so it needs its own coverage of the apply-after-start step.
func TestRestartAppliesSecretsAfterSuccessfulStart(t *testing.T) {
	fr := &secretsFakeRunner{}
	cli := lima.New(fr)

	msg := restartCmd(testProvider(cli), "claude", "ada", map[string]map[string]string{"": {"GH_TOKEN": "ghp_x"}})()
	done, ok := msg.(actionDoneMsg)
	if !ok {
		t.Fatalf("restartCmd's tea.Cmd returned %T, want actionDoneMsg", msg)
	}
	if done.err != nil {
		t.Fatalf("done.err = %v, want nil", done.err)
	}
	// Same three-call shape as TestStartAppliesSecretsAfterSuccessfulStart
	// (write, ensure-profile, git-credential reconcile — task 04).
	if len(fr.streamCalls) != 3 {
		t.Fatalf("expected exactly three Shell (ApplySecrets) calls after restart, got %d", len(fr.streamCalls))
	}
}

// (c) A failing ApplySecrets after a successful Start must not fail the
// action: actionDoneMsg.err stays nil and the failure is carried as warn
// instead. Routed through Update, the status line shows both "ok" and the
// warning text — never one without the other.
func TestSecretsWarnNotFailOnApplyFailure(t *testing.T) {
	fr := &secretsFakeRunner{failStream: true}
	cli := lima.New(fr)

	msg := startCmd(testProvider(cli), "claude", "ada", map[string]map[string]string{"": {"A": "1"}})()
	done, ok := msg.(actionDoneMsg)
	if !ok {
		t.Fatalf("startCmd's tea.Cmd returned %T, want actionDoneMsg", msg)
	}
	if done.err != nil {
		t.Fatalf("a failed ApplySecrets must not fail the action, done.err = %v", done.err)
	}
	if done.warn == "" {
		t.Fatal("a failed ApplySecrets should be reported as a warning")
	}

	m := newTestModel(t)
	next, _ := m.Update(done)
	m = next.(model)
	if !strings.Contains(m.lastMessage(), "ok") {
		t.Fatalf("status %q should still report the start as ok", m.lastMessage())
	}
	if !strings.Contains(m.lastMessage(), done.warn) {
		t.Fatalf("status %q should include the warning %q", m.lastMessage(), done.warn)
	}
}

// (d) Creating a VM with a non-empty CloneToken seeds it into the host
// secrets store as the VM's GH_TOKEN, while the registry's stored config keeps
// CloneToken empty — the token reaches secrets.json, never managed-vms.json.
func TestTokenSeedsStoreNotRegistry(t *testing.T) {
	m := newTestModel(t)
	seedJob(t, &m, "claude", vm.CreateConfig{
		Name:       "claude",
		BaseName:   "sandbar-base",
		CloneToken: "ghp_x",
	})

	next, cmd := m.Update(provisionDoneMsg{job: provisionKey(registry.LocalScope, "claude")})
	m = next.(model)

	if got := m.sec.Get("claude", registry.LocalScope)["GH_TOKEN"]; got != "ghp_x" {
		t.Fatalf(`sec.Get("claude")["GH_TOKEN"] = %q, want "ghp_x"`, got)
	}
	cfg, ok := m.reg.Config("claude")
	if !ok {
		t.Fatal("a successful create should record claude as managed")
	}
	if cfg.CloneToken != "" {
		t.Fatalf("registry config retained the token: %q, want empty", cfg.CloneToken)
	}
	if cmd == nil {
		t.Fatal("a successful create with a token should dispatch a follow-up command (list refresh + apply)")
	}
}

// A create with no CloneToken must not disturb any secrets already stored for
// the VM (e.g. from a prior edit) — merge semantics, not wholesale overwrite.
func TestNoTokenLeavesExistingSecretsAlone(t *testing.T) {
	m := newTestModel(t)
	if err := m.sec.Set("claude", registry.LocalScope, map[string]string{"OTHER": "kept"}); err != nil {
		t.Fatalf("seed secrets: %v", err)
	}
	seedJob(t, &m, "claude", vm.CreateConfig{Name: "claude", BaseName: "sandbar-base"})

	next, _ := m.Update(provisionDoneMsg{job: provisionKey(registry.LocalScope, "claude")})
	m = next.(model)

	got := m.sec.Get("claude", registry.LocalScope)
	if got["OTHER"] != "kept" {
		t.Fatalf("existing secret should survive a tokenless create, got %v", got)
	}
	if _, ok := got["GH_TOKEN"]; ok {
		t.Fatalf("no GH_TOKEN should be written when CloneToken is empty, got %v", got)
	}
}

// (e) Deleting a VM prunes its secrets alongside the managed registry entry.
func TestDeleteSecretsPruned(t *testing.T) {
	m := newTestModel(t)
	if err := m.sec.Set("claude", registry.LocalScope, map[string]string{"A": "1"}); err != nil {
		t.Fatalf("seed secrets: %v", err)
	}
	if got := m.sec.Get("claude", registry.LocalScope); len(got) == 0 {
		t.Fatal("precondition: secrets should be seeded")
	}

	next, _ := m.Update(actionDoneMsg{action: "delete", name: "claude"})
	m = next.(model)

	if got := m.sec.Get("claude", registry.LocalScope); len(got) != 0 {
		t.Fatalf("a successful delete should prune secrets, sec.Get returned %v", got)
	}
}

// (f) Saving the secrets editor (ctrl+s) on a RUNNING VM must not stop at the
// host store: it must also push the new value into the guest, the same way
// startCmd/restartCmd do after a boot. This is the in-process half of the
// "ctrl+s never reaches the guest" fix — necessary but not sufficient; the
// far-side proof (a real guest actually receiving the new value, without a
// restart) lives in the limae2e test.
func TestSecretsSaveOnRunningVMPushesToGuest(t *testing.T) {
	fr := &secretsFakeRunner{}
	cli := lima.New(fr)
	m := newTestModelWithCli(t, cli)
	m = resized(m, 100, 30)
	m.vms = []vm.VM{{Name: "claude", Status: "Running"}}
	m = openSecretsViaKey(t, m, "claude", "Running")
	m = typeInto(m, "GH_TOKEN=ghp_new")

	m, cmd := pressDispatch(t, m, ctrlKey('s'))

	if m.view != viewBoard {
		t.Fatalf("a valid save should return to the board, got %v", m.view)
	}
	if got := m.sec.Get("claude", registry.LocalScope); got["GH_TOKEN"] != "ghp_new" {
		t.Fatalf("the host store should still persist immediately, got %v", got)
	}
	if cmd == nil {
		t.Fatal("saving on a RUNNING VM must dispatch a command that pushes the change to the guest, got nil")
	}

	// Drive whatever cmd() returns to a concrete actionDoneMsg — beginAction
	// batches the apply with the spinner tick, so unwrap a possible BatchMsg.
	var done actionDoneMsg
	found := false
	msg := cmd()
	cmds := []tea.Cmd{func() tea.Msg { return msg }}
	if batch, ok := msg.(tea.BatchMsg); ok {
		cmds = batch
	}
	for _, c := range cmds {
		if c == nil {
			continue
		}
		if d, ok := c().(actionDoneMsg); ok {
			done = d
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an actionDoneMsg from the save-on-running-VM command, got %v", msg)
	}
	if done.err != nil {
		t.Fatalf("the guest apply should have succeeded, got err = %v", done.err)
	}
	if len(fr.streamCalls) == 0 {
		t.Fatal("saving on a running VM should have shelled into the guest to apply the secret")
	}
	if !strings.Contains(fr.streamStdin[0], "GH_TOKEN") {
		t.Fatalf("guest apply stdin %q should carry the new GH_TOKEN pair", fr.streamStdin[0])
	}
}

// (g) The same save on a STOPPED VM must NOT reach the guest — there is
// nothing running to apply to — and the status must say so honestly (not
// claim the value already applied).
func TestSecretsSaveOnStoppedVMDoesNotTouchGuest(t *testing.T) {
	fr := &secretsFakeRunner{}
	cli := lima.New(fr)
	m := newTestModelWithCli(t, cli)
	m = resized(m, 100, 30)
	m.vms = []vm.VM{{Name: "claude", Status: "Stopped"}}
	m = openSecretsViaKey(t, m, "claude", "Stopped")
	m = typeInto(m, "GH_TOKEN=ghp_new")

	m, cmd := pressDispatch(t, m, ctrlKey('s'))

	if cmd != nil {
		t.Fatal("saving on a STOPPED VM must not dispatch a guest-apply command")
	}
	if len(fr.streamCalls) != 0 {
		t.Fatalf("saving on a stopped VM must not shell into the guest, got %d calls", len(fr.streamCalls))
	}
	if !strings.Contains(m.lastMessage(), "claude") || !strings.Contains(m.lastMessage(), "next start") {
		t.Fatalf("status %q should name the VM and honestly say it applies on next start", m.lastMessage())
	}
}

// A VM that vanished outside the TUI is pruned from the secrets store too,
// exactly like the managed registry — manage.Reconcile is the single shared
// place both the TUI and the headless path agree on reconciliation.
func TestReconcileDropPrunesSecrets(t *testing.T) {
	m := newTestModel(t)
	if err := m.reg.Add(vm.CreateConfig{Name: "gone", BaseName: "sandbar-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	if err := m.sec.Set("gone", registry.LocalScope, map[string]string{"A": "1"}); err != nil {
		t.Fatalf("seed secrets: %v", err)
	}

	next, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{{Name: "other", Status: "Running"}}})
	m = next.(model)

	if got := m.sec.Get("gone", registry.LocalScope); len(got) != 0 {
		t.Fatalf("a VM dropped by Reconcile should have its secrets pruned, got %v", got)
	}
}

// shellCmd's two tests below drive its suspend branch (which calls
// AttachArgv to build the exec'd argv) and its host-tmux fast path (which
// never touches the provider at all), so a providerfake.Provider with only
// AttachArgvFunc set is enough — the fake's zero-value default answers every
// other method inertly instead of panicking, unlike the nil-embedded-Provider
// trick this used to reach for.

// shellCmd's suspend branch (host $TMUX unset, the common case) must return
// tea.ExecProcess's own message kind, not actionDoneMsg directly — calling
// the tea.Cmd merely hands tea's runtime an execMsg to act on later; it does
// not itself run anything. This is the branch a plain terminal takes: a
// tmux CLIENT needs the real TTY only tea.ExecProcess can hand it.
func TestShellCmdSuspendsWhenHostTMUXUnset(t *testing.T) {
	t.Setenv("TMUX", "")

	p := &providerfake.Provider{AttachArgvFunc: func(vm.VM) []string { return []string{"limactl", "shell", "claude"} }}
	msg := shellCmd(p, vm.VM{Name: "claude"})()

	if _, ok := msg.(actionDoneMsg); ok {
		t.Fatal("suspend branch must not resolve to actionDoneMsg directly — that is the fast path's shape, not tea.ExecProcess's")
	}
	if got := fmt.Sprintf("%T", msg); got != "tea.execMsg" {
		t.Fatalf("suspend branch should yield tea.ExecProcess's internal execMsg, got %s", got)
	}
}

// shellCmd's fast path (host $TMUX set: the TUI is itself running inside
// host tmux) must be an ORDINARY tea.Cmd, not tea.ExecProcess — wrapping a
// `tmux new-window` that returns in milliseconds would suspend the TUI
// (tearing down the alt-screen, releasing input) for no reason, defeating
// the entire point of the branch: keeping the live board and its job
// progress bars on screen. Calling the returned tea.Cmd must resolve
// directly to actionDoneMsg with no host process actually run (the real
// `tmux new-window` invocation is stubbed via runHostTmuxNewWindow so this
// test never touches a real tmux server — see AGENTS.md on not requiring
// real external state in unit tests).
func TestShellCmdFastPathDoesNotSuspend(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,4242,0")

	var gotShellCommand string
	orig := runHostTmuxNewWindow
	runHostTmuxNewWindow = func(shellCommand string) error {
		gotShellCommand = shellCommand
		return nil
	}
	t.Cleanup(func() { runHostTmuxNewWindow = orig })

	msg := shellCmd(&providerfake.Provider{}, vm.VM{Name: "claude"})()

	done, ok := msg.(actionDoneMsg)
	if !ok {
		t.Fatalf("fast path should resolve directly to actionDoneMsg (an ordinary tea.Cmd), got %T", msg)
	}
	if done.action != "shell" || done.name != "claude" || done.err != nil {
		t.Fatalf("actionDoneMsg = %+v, want {action: shell, name: claude, err: nil}", done)
	}
	if !strings.Contains(gotShellCommand, "shell") || !strings.Contains(gotShellCommand, "claude") {
		t.Fatalf("host tmux new-window shell-command = %q, want it to re-enter via `sand shell claude`", gotShellCommand)
	}
}

// hostTmuxShellCommand is the pure argv-shaped builder behind the fast
// path's `tmux new-window` call: tmux hands its shell-command string to
// $SHELL -c, so both the resolved sand path and the VM name are quoted as
// single POSIX shell words rather than joined with a bare space — a
// resolved binary path is not guaranteed to be space-free.
func TestHostTmuxShellCommandQuotesArguments(t *testing.T) {
	got := hostTmuxShellCommand("/path with spaces/sand", "claude")
	if want := "'/path with spaces/sand' shell 'claude'"; !strings.HasPrefix(got, want) {
		t.Fatalf("hostTmuxShellCommand = %q, want it to start with %q", got, want)
	}
	// tmux closes a window the instant its command exits, so a failure would flash
	// past unread ("a window opened and immediately closed"). The command holds the
	// window open on a NON-ZERO exit only — a clean detach must still close it.
	if !strings.Contains(got, "||") || !strings.Contains(got, "read -r _") {
		t.Fatalf("hostTmuxShellCommand = %q, want it to hold the window open on failure", got)
	}
}
