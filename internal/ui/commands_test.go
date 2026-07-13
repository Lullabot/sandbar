package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
)

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

	msg := startCmd(cli, "claude", "ada", map[string]map[string]string{"": {"GH_TOKEN": "ghp_x"}})()
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

	msg := startCmd(cli, "claude", "ada", map[string]map[string]string{"": {"A": "1"}})()
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

	msg := restartCmd(cli, "claude", "ada", map[string]map[string]string{"": {"GH_TOKEN": "ghp_x"}})()
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

	msg := startCmd(cli, "claude", "ada", map[string]map[string]string{"": {"A": "1"}})()
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
		BaseName:   "claude-base",
		CloneToken: "ghp_x",
	})

	next, cmd := m.Update(provisionDoneMsg{vm: "claude"})
	m = next.(model)

	if got := m.sec.Get("claude")["GH_TOKEN"]; got != "ghp_x" {
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
	if err := m.sec.Set("claude", map[string]string{"OTHER": "kept"}); err != nil {
		t.Fatalf("seed secrets: %v", err)
	}
	seedJob(t, &m, "claude", vm.CreateConfig{Name: "claude", BaseName: "claude-base"})

	next, _ := m.Update(provisionDoneMsg{vm: "claude"})
	m = next.(model)

	got := m.sec.Get("claude")
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
	if err := m.sec.Set("claude", map[string]string{"A": "1"}); err != nil {
		t.Fatalf("seed secrets: %v", err)
	}
	if got := m.sec.Get("claude"); len(got) == 0 {
		t.Fatal("precondition: secrets should be seeded")
	}

	next, _ := m.Update(actionDoneMsg{action: "delete", name: "claude"})
	m = next.(model)

	if got := m.sec.Get("claude"); len(got) != 0 {
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
	m = openSecretsViaKey(m, "claude", "Running")
	m = typeInto(m, "GH_TOKEN=ghp_new")

	after, cmd := m.Update(ctrlKey('s'))
	m = after.(model)

	if m.view != viewDetail {
		t.Fatalf("a valid save should return to the detail view, got %v", m.view)
	}
	if got := m.sec.Get("claude"); got["GH_TOKEN"] != "ghp_new" {
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
	m = openSecretsViaKey(m, "claude", "Stopped")
	m = typeInto(m, "GH_TOKEN=ghp_new")

	after, cmd := m.Update(ctrlKey('s'))
	m = after.(model)

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
	if err := m.reg.Add(vm.CreateConfig{Name: "gone", BaseName: "claude-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	if err := m.sec.Set("gone", map[string]string{"A": "1"}); err != nil {
		t.Fatalf("seed secrets: %v", err)
	}

	next, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{{Name: "other", Status: "Running"}}})
	m = next.(model)

	if got := m.sec.Get("gone"); len(got) != 0 {
		t.Fatalf("a VM dropped by Reconcile should have its secrets pruned, got %v", got)
	}
}
