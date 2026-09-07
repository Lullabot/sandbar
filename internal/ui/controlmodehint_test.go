package ui

import (
	"strings"
	"testing"
)

// stubHostTmux forces hostHasTmux's answer for one test, restoring it after, so
// no assertion here depends on whether the machine running the suite happens to
// have tmux installed.
func stubHostTmux(t *testing.T, installed bool) {
	t.Helper()
	orig := hostHasTmux
	hostHasTmux = func() bool { return installed }
	t.Cleanup(func() { hostHasTmux = orig })
}

// TestControlModeHintLogsOncePerRun is the whole point of the feature: a user
// who shells into several VMs in one session must read the tip once. Logging it
// on every `S` would turn a helpful line into the noise that pushes the messages
// that matter out of a 50-entry ring.
func TestControlModeHintLogsOncePerRun(t *testing.T) {
	t.Setenv("TMUX", "")
	stubHostTmux(t, true)

	var m model
	m.controlModeHint("web")
	m.controlModeHint("db")
	m.controlModeHint("web")

	if len(m.messages) != 1 {
		t.Fatalf("controlModeHint logged %d times, want exactly 1:\n%v", len(m.messages), m.messages)
	}
	got := m.messages[0].text
	if !strings.Contains(got, "web") {
		t.Errorf("the hint does not name the VM the user just shelled into, so the suggested command is not copy-pasteable: %s", got)
	}
	for _, want := range []string{"--cc", "tmux -CC"} {
		if !strings.Contains(got, want) {
			t.Errorf("the hint never mentions %q, so it does not tell the user the thing it exists to tell them: %s", want, got)
		}
	}
}

// TestControlModeHintSilentInsideTmux: a user whose $TMUX is set is already on
// the branch that keeps the board live, so half the advice is redundant — and
// `sand shell --cc` would refuse from inside tmux anyway (a tmux pane strips the
// control-mode handshake), so the other half is advice to run a command that
// errors.
func TestControlModeHintSilentInsideTmux(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,4242,0")
	stubHostTmux(t, true)

	var m model
	m.controlModeHint("web")

	if len(m.messages) != 0 {
		t.Fatalf("controlModeHint spoke inside a host tmux, where its advice is redundant or wrong:\n%v", m.messages)
	}
}

// TestControlModeHintSilentWithoutTmux: both halves of the tip need tmux on the
// host. Telling a user who has not got it to run `tmux -CC` is a detour into
// installing a package, not a tip.
func TestControlModeHintSilentWithoutTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	stubHostTmux(t, false)

	var m model
	m.controlModeHint("web")

	if len(m.messages) != 0 {
		t.Fatalf("controlModeHint suggested tmux commands on a host with no tmux:\n%v", m.messages)
	}
}

// TestControlModeHintMutatesThroughAPointer guards the mechanism the
// once-per-run promise rests on. The model travels BY VALUE through Update, and
// the command registry's actions take a *model precisely so their mutations
// stick (board.go: `cmd := c.action(&m, v)`). If ccHintShown were ever set on a
// copy, the tip would come back on every `S` — so the flag is asserted on the
// caller's own model, not on a return value.
func TestControlModeHintMutatesThroughAPointer(t *testing.T) {
	t.Setenv("TMUX", "")
	stubHostTmux(t, true)

	var m model
	hint := func(mp *model) { mp.controlModeHint("web") }
	hint(&m)

	if !m.ccHintShown {
		t.Fatal("ccHintShown did not survive back to the caller's model, so the tip would repeat on every S")
	}
	hint(&m)
	if len(m.messages) != 1 {
		t.Fatalf("the tip was logged %d times, want 1:\n%v", len(m.messages), m.messages)
	}
}
