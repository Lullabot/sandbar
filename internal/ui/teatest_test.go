package ui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"
)

// These are integration tests that drive the whole Bubble Tea program the way a
// user does — a real event loop, real key events, real renders — rather than
// calling Update/View in isolation. teatest boots the model in a simulated
// terminal; we navigate with keystrokes, wait for the target screen to render,
// then snapshot it against a golden file (regenerate with `go test -update`).
//
// Snapshots are of ansi.Strip(FinalModel().View()): the plain text grid of a
// single deterministic final render. Stripping the ANSI keeps the goldens
// portable (immune to the terminal colour profile CI happens to expose) and
// human-readable in review, while still pinning the layout — column widths,
// wrapping, overflow, and any stray editor chrome. Colours are deliberately not
// asserted. Screens whose defaults are host-derived (the create form seeds
// CPUs/RAM/git identity from the host) get a behavioural assertion instead of a
// golden, since a pixel-stable golden is impossible across machines.

// listFakeRunner returns a canned `limactl list --format json` payload so the
// program's real List() path populates a deterministic VM list, and no-ops
// everything else. Two VMs, first is "claude" (so enter selects it).
type listFakeRunner struct{}

const listJSON = `{"name":"claude","status":"Running","cpus":4,"memory":8589934592,"disk":107374182400,"dir":"/nonexistent/claude","arch":"x86_64"}
{"name":"web","status":"Stopped","cpus":2,"memory":4294967296,"disk":53687091200,"dir":"/nonexistent/web","arch":"aarch64"}`

func (listFakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "list" {
		return []byte(listJSON), nil
	}
	return nil, nil
}
func (listFakeRunner) Stream(context.Context, io.Reader, io.Writer, ...string) error    { return nil }
func (listFakeRunner) StreamOut(context.Context, io.Reader, io.Writer, ...string) error { return nil }

// newTeaProgram builds the real tea.Model over the canned lima client, with the
// managed-index/secrets store isolated to a temp dir, and boots it in an
// 100x30 simulated terminal.
func newTeaProgram(t *testing.T) *teatest.TestModel {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cli := lima.New(listFakeRunner{})
	prov := &provision.Provisioner{Lima: cli}
	return teatest.NewTestModel(t, New(cli, prov), teatest.WithInitialTermSize(100, 30))
}

// waitForText blocks until the program's output contains want (ANSI stripped),
// so a test only snapshots once the target screen has actually rendered.
func waitForText(t *testing.T, tm *teatest.TestModel, want string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(ansi.Strip(string(b)), want)
	}, teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(20*time.Millisecond))
}

// waitForTypedText is waitForText's counterpart for freshly-typed input. v2's
// cell-diffing renderer redraws only the cell under the blinking virtual
// cursor (bubbles/v2 textinput resets and restarts the blink on every
// keystroke) — so the raw output stream can catch a bare cursor-blink space
// wedged between two characters that were, from the model's perspective,
// typed back to back (e.g. "my vm" instead of "myvm"). Collapsing spaces
// before matching absorbs that renderer artifact while still failing if a
// character is actually dropped, garbled, or reordered.
func waitForTypedText(t *testing.T, tm *teatest.TestModel, want string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		got := strings.ReplaceAll(ansi.Strip(string(b)), " ", "")
		return strings.Contains(got, want)
	}, teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(20*time.Millisecond))
}

// finalScreen quits the program and returns the final model's rendered view
// with ANSI stripped — the deterministic snapshot payload.
func finalScreen(t *testing.T, tm *teatest.TestModel) []byte {
	t.Helper()
	if err := tm.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	return []byte(ansi.Strip(fm.View().Content) + "\n")
}

// The VM list renders with the canned instances, their sizes humanized and the
// action help bar.
func TestTUIListView(t *testing.T) {
	tm := newTeaProgram(t)
	waitForText(t, tm, "claude")
	teatest.RequireEqualOutput(t, finalScreen(t, tm))
}

// Enter on the list opens the detail screen for the highlighted VM.
func TestTUIDetailView(t *testing.T) {
	tm := newTeaProgram(t)
	waitForText(t, tm, "claude")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, tm, "VM: claude")
	teatest.RequireEqualOutput(t, finalScreen(t, tm))
}

// 'd' on the VM screen raises the delete-confirmation overlay for that VM.
func TestTUIDeleteConfirm(t *testing.T) {
	tm := newTeaProgram(t)
	waitForText(t, tm, "claude")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, tm, "VM: claude")
	tm.Send(runeKey('d'))
	waitForText(t, tm, `Delete "claude"?`)
	teatest.RequireEqualOutput(t, finalScreen(t, tm))
}

// VM screen -> 'e' opens the (empty) secrets editor for the VM.
func TestTUISecretsPanelEmpty(t *testing.T) {
	tm := newTeaProgram(t)
	waitForText(t, tm, "claude")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, tm, "VM: claude")
	tm.Send(runeKey('e'))
	waitForText(t, tm, "Secrets: claude")
	teatest.RequireEqualOutput(t, finalScreen(t, tm))
}

// The create form accepts typing into its focused field: 'n' opens it, and the
// typed name reaches the Name input. This is the behavioural counterpart to the
// goldens — it drives the real key path end-to-end, the exact coverage that
// catches an editor/form that opens unfocused and silently drops input.
func TestTUINewFormAcceptsTyping(t *testing.T) {
	tm := newTeaProgram(t)
	waitForText(t, tm, "claude")
	tm.Send(runeKey('n'))
	waitForText(t, tm, "New VM")
	tm.Type("myvm")
	waitForTypedText(t, tm, "myvm") // the field echoes the typed characters

	fm := finalModel(t, tm)
	if got := fm.inputs[fName].Value(); got != "myvm" {
		t.Fatalf("typed name did not reach the focused field: Name input = %q, want %q", got, "myvm")
	}
}

// buildingRunner is a lima.Runner whose streaming calls BLOCK, dribbling
// Ansible-shaped output until the test releases them — a stand-in for the real
// provisioner, which blocks for minutes. `limactl list` still answers, so the
// program's normal refreshes keep working underneath the build.
type buildingRunner struct {
	listFakeRunner
	started chan struct{} // closed once the provisioner is actually streaming
	release chan struct{} // closed to let the build finish
	once    sync.Once
}

// Output answers the fleet listing from the canned JSON, but reports a per-name
// status lookup (`limactl list <name> --format {{.Status}}`) as EMPTY: the VM
// being created does not exist yet, which is exactly the state the provisioner's
// already-exists guard is checking for.
func (r *buildingRunner) Output(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) >= 2 && args[0] == "list" && !strings.HasPrefix(args[1], "--") {
		return nil, nil
	}
	return r.listFakeRunner.Output(ctx, args...)
}

func (r *buildingRunner) Stream(ctx context.Context, _ io.Reader, out io.Writer, _ ...string) error {
	r.once.Do(func() { close(r.started) })
	io.WriteString(out, "SAND_ANSIBLE_TASK_TOTAL=19\nPLAY [Provision]\nTASK [dev-tools : Install Docker] ***\n")
	select {
	case <-r.release:
		return nil
	case <-ctx.Done(): // a real cancel (ctrl+c) kills the limactl subprocess here
		return ctx.Err()
	}
}

// THE SIGNATURE BEHAVIOUR OF THIS PLAN, driven through the REAL Bubble Tea
// runtime rather than a hand-rolled update loop: a user starts a VM, and instead
// of the screen going dark with a full-screen Ansible dump for minutes, they can
// walk away from the build — which keeps running — and start a SECOND VM. The
// old model froze every key here for the entire provision, so this test is the
// difference between the feature working and the feature being claimed.
func TestTUIKeyboardStaysLiveWhileAVMBuilds(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	// The create form seeds its git identity from the host's git config, so pin one:
	// an unconfigured host would fail validation and never reach the provisioner.
	gitconfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(gitconfig, []byte("[user]\n\tname = Ada Lovelace\n\temail = ada@example.com\n"), 0o600); err != nil {
		t.Fatalf("write gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)

	runner := &buildingRunner{started: make(chan struct{}), release: make(chan struct{})}
	cli := lima.New(runner)
	prov := &provision.Provisioner{Lima: cli, PlaybookDir: t.TempDir()}
	tm := teatest.NewTestModel(t, New(cli, prov), teatest.WithInitialTermSize(100, 30))

	// n → the create form → name it → ctrl+s → the provisioner starts streaming.
	waitForText(t, tm, "claude")
	tm.Send(runeKey('n'))
	waitForText(t, tm, "New VM")
	tm.Type("newvm")
	waitForTypedText(t, tm, "newvm")
	tm.Send(ctrlKey('s'))

	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("submitting the form should have started the provisioner")
	}
	waitForText(t, tm, "Install Docker") // its output is streaming into the progress pane

	// ESC — the key that used to do nothing at all here. The build must keep going.
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEsc})
	waitForText(t, tm, "stop all") // the list's help bar: we are back on a live list

	// And the whole UI is live: a SECOND VM can be started while the first builds.
	tm.Send(runeKey('n'))
	waitForText(t, tm, "New VM")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEsc})

	// The build is still running, and its log kept filling while the user was away.
	close(runner.release)
	fm := finalModel(t, tm)
	if !fm.vmHasRetainedRun("newvm") {
		t.Fatal("the build should have been retained as newvm's run")
	}
	s, _ := fm.jobs.snapshot("newvm")
	if !strings.Contains(s.Output, "Install Docker") {
		t.Fatalf("the job's log should hold the output streamed while the user was elsewhere, got:\n%s", s.Output)
	}
	if s.Progress.Task != "Install Docker" || s.Progress.Total != 19 {
		t.Fatalf("the job should have parsed its Ansible progress, got %+v", s.Progress)
	}
}

// finalModel quits the program and returns the concrete *model for state
// assertions.
func finalModel(t *testing.T, tm *teatest.TestModel) model {
	t.Helper()
	if err := tm.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
	m, ok := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(model)
	if !ok {
		t.Fatal("FinalModel was not a ui.model")
	}
	return m
}
