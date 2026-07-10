package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
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
	// ApplySecrets makes two guest calls for a global-only apply: the write
	// itself, then the idempotent ~/.profile + ~/.bashrc source-line ensure.
	if len(fr.streamCalls) != 2 {
		t.Fatalf("expected exactly two Shell (ApplySecrets) calls, got %d: %v", len(fr.streamCalls), fr.streamCalls)
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
	if len(fr.streamCalls) != 2 {
		t.Fatalf("expected exactly two Shell (ApplySecrets) calls after restart, got %d", len(fr.streamCalls))
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
	if !strings.Contains(m.status, "ok") {
		t.Fatalf("status %q should still report the start as ok", m.status)
	}
	if !strings.Contains(m.status, done.warn) {
		t.Fatalf("status %q should include the warning %q", m.status, done.warn)
	}
}

// (d) Creating a VM with a non-empty CloneToken seeds it into the host
// secrets store as the VM's GH_TOKEN, while the registry's stored config keeps
// CloneToken empty — the token reaches secrets.json, never managed-vms.json.
func TestTokenSeedsStoreNotRegistry(t *testing.T) {
	m := newTestModel(t)
	m.view = viewProgress
	m.running = true
	m.provCfg = vm.CreateConfig{
		Name:       "claude",
		BaseName:   "claude-base",
		CloneToken: "ghp_x",
	}

	next, cmd := m.Update(provisionDoneMsg{})
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
	m.view = viewProgress
	m.running = true
	m.provCfg = vm.CreateConfig{Name: "claude", BaseName: "claude-base"}

	next, _ := m.Update(provisionDoneMsg{})
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
