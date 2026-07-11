package ui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"
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
func (listFakeRunner) Stream(context.Context, io.Reader, io.Writer, ...string) error { return nil }

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

// finalScreen quits the program and returns the final model's rendered view
// with ANSI stripped — the deterministic snapshot payload.
func finalScreen(t *testing.T, tm *teatest.TestModel) []byte {
	t.Helper()
	if err := tm.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	return []byte(ansi.Strip(fm.View()) + "\n")
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
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	waitForText(t, tm, "VM: claude")
	teatest.RequireEqualOutput(t, finalScreen(t, tm))
}

// 'd' on the list raises the delete-confirmation overlay for the highlighted VM.
func TestTUIDeleteConfirm(t *testing.T) {
	tm := newTeaProgram(t)
	waitForText(t, tm, "claude")
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	waitForText(t, tm, `Delete "claude"?`)
	teatest.RequireEqualOutput(t, finalScreen(t, tm))
}

// Detail -> 's' opens the (empty) secrets panel for the VM.
func TestTUISecretsPanelEmpty(t *testing.T) {
	tm := newTeaProgram(t)
	waitForText(t, tm, "claude")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	waitForText(t, tm, "VM: claude")
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
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
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	waitForText(t, tm, "New VM")
	tm.Type("myvm")
	waitForText(t, tm, "myvm") // the field echoes the typed characters

	fm := finalModel(t, tm)
	if got := fm.inputs[fName].Value(); got != "myvm" {
		t.Fatalf("typed name did not reach the focused field: Name input = %q, want %q", got, "myvm")
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
