package ui

// profilesview_golden_test.go pins the profile management screen's rendered
// look (regenerate with `go test ./internal/ui/ -run TestTUIProfiles -update`
// — see teatest_test.go's own note on the harness) and drives the real 'p'
// key path end-to-end, the same coverage TestTUINewFormAcceptsTyping already
// gives the VM create form.

import (
	"testing"

	"github.com/charmbracelet/x/exp/teatest/v2"
)

// 'p' from the board opens the profile management screen, listing the
// zero-config seeded Local profile.
func TestTUIProfilesScreen(t *testing.T) {
	tm := newTeaProgram(t)
	waitForText(t, tm, "claude")
	tm.Send(runeKey('p'))
	waitForText(t, tm, "Connection Profiles")
	teatest.RequireEqualOutput(t, finalScreen(t, tm))
}

// 'n' on the profile list opens the (blank) create form, and it accepts
// typing — the behavioural counterpart to the golden above, catching a form
// that opens unfocused and silently drops input.
func TestTUIProfilesNewFormAcceptsTyping(t *testing.T) {
	tm := newTeaProgram(t)
	waitForText(t, tm, "claude")
	tm.Send(runeKey('p'))
	waitForText(t, tm, "Connection Profiles")
	tm.Send(runeKey('n'))
	waitForText(t, tm, "New Connection Profile")
	tm.Type("build-host")
	waitForTypedText(t, tm, "build-host")

	fm := finalModel(t, tm)
	if got := fm.profileInputs[pfName].Value(); got != "build-host" {
		t.Fatalf("typed name did not reach the focused field: Name input = %q, want %q", got, "build-host")
	}
}
