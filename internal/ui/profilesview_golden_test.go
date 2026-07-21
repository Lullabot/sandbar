package ui

// profilesview_golden_test.go pins the profile management screen's rendered
// look (regenerate with `go test ./internal/ui/ -run TestTUIProfiles -update`
// — see teatest_test.go's own note on the harness) and drives the real 'p'
// key path end-to-end, the same coverage TestTUINewFormAcceptsTyping already
// gives the VM create form.

import (
	"testing"

	tea "charm.land/bubbletea/v2"
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

// 'n' on the profile list opens the pre-form type picker (task 2); selecting
// its default (Remote SSH) entry opens the blank create form, and it accepts
// typing — the behavioural counterpart to the golden above, catching a form
// that opens unfocused and silently drops input.
func TestTUIProfilesNewFormAcceptsTyping(t *testing.T) {
	tm := newTeaProgram(t)
	waitForText(t, tm, "claude")
	tm.Send(runeKey('p'))
	waitForText(t, tm, "Connection Profiles")
	tm.Send(runeKey('n'))
	waitForText(t, tm, "New Connection Profile")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter}) // select the picker's default (Remote SSH) entry
	waitForText(t, tm, "Name:")
	tm.Type("build-host")
	waitForTypedText(t, tm, "build-host")

	fm := finalModel(t, tm)
	if got := fm.profileInputs[pfName].Value(); got != "build-host" {
		t.Fatalf("typed name did not reach the focused field: Name input = %q, want %q", got, "build-host")
	}
}

// GOLDEN: the pre-form type picker (task 2) that 'n' now opens instead of
// jumping straight into a blank RemoteSSH form — pins the two-entry list
// ("Remote SSH", "Proxmox") and its cursor styling, layout regression
// insurance the same way TestTUIProfilesScreen pins the list screen above.
func TestTUIProfileTypePickerGolden(t *testing.T) {
	tm := newTeaProgram(t)
	waitForText(t, tm, "claude")
	tm.Send(runeKey('p'))
	waitForText(t, tm, "Connection Profiles")
	tm.Send(runeKey('n'))
	waitForText(t, tm, "New Connection Profile")
	teatest.RequireEqualOutput(t, finalScreen(t, tm))
}

// GOLDEN: the Proxmox field form (task 1) — all eight fields plus the
// insecure checkbox row, unchecked in its initial state. Reaching it means
// walking the picker's cursor down onto Proxmox (index 1 of
// creatableProfileTypes) before selecting it; RemoteSSH's own form is
// already covered by TestTUIProfilesNewFormAcceptsTyping's behavioural walk,
// so this is the layout counterpart for the type the earlier goldens never
// saw.
func TestTUIProxmoxFormGolden(t *testing.T) {
	tm := newTeaProgram(t)
	waitForText(t, tm, "claude")
	tm.Send(runeKey('p'))
	waitForText(t, tm, "Connection Profiles")
	tm.Send(runeKey('n'))
	waitForText(t, tm, "New Connection Profile")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown}) // move the picker cursor onto Proxmox
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, tm, "Insecure")
	teatest.RequireEqualOutput(t, finalScreen(t, tm))
}

// GOLDEN: the same Proxmox form with the insecure checkbox TOGGLED ON,
// proving "[x] Insecure" renders distinctly from the "[ ] Insecure" the
// golden above pins — the pair is what actually proves the checkbox glyph
// flips rather than just existing. Tab from Name (focus 0) walks the
// remaining seven text fields (Host, Node, Pool, Storage, Bridge, Token
// file) onto the checkbox row (profileFormSlots — see profilesview.go),
// where space toggles it per updateProfileForm's onCheckbox branch.
func TestTUIProxmoxFormGoldenInsecureChecked(t *testing.T) {
	tm := newTeaProgram(t)
	waitForText(t, tm, "claude")
	tm.Send(runeKey('p'))
	waitForText(t, tm, "Connection Profiles")
	tm.Send(runeKey('n'))
	waitForText(t, tm, "New Connection Profile")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, tm, "Insecure")
	for i := 0; i < 7; i++ {
		tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	teatest.RequireEqualOutput(t, finalScreen(t, tm))
}
