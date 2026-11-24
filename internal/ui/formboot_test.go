package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// focusStartAtBoot puts the focus ring on the Proxmox start-at-boot row. It is
// found by label because reset mode's toggle count varies.
func focusStartAtBoot(t *testing.T, m *model) {
	t.Helper()
	for i, toggle := range m.toggles() {
		if toggle.label == "Start at boot" {
			m.toggleFocus = i
			return
		}
	}
	t.Fatal("no Start at boot row")
}

// pressBoot sends one key through the model's real Update, so the form's own key
// routing is what is tested.
func pressBoot(t *testing.T, m *model, code rune) {
	t.Helper()
	next, _ := m.Update(tea.KeyPressMsg{Code: code})
	*m = next.(model)
}

func proxmoxFormModel(t *testing.T) model {
	t.Helper()
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	fillCreateForm(t, &m, "web")
	m.members[0].profile.Type = profiles.TypeProxmox
	return m
}

func TestFormStartAtBootSelector(t *testing.T) {
	m := proxmoxFormModel(t)
	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.StartAtBoot(); got != vm.StartAtBootAlways {
		t.Fatalf("default mode = %q, want always", got)
	}
	// The form scrolls to keep the focused row on screen, so focus it first.
	focusStartAtBoot(t, &m)
	if !strings.Contains(ansi.Strip(m.formView()), "Start at boot: < Always >") {
		t.Fatalf("selector row not rendered:\n%s", ansi.Strip(m.formView()))
	}

	// Each mode reached by keys must reach the create config, and a reset form
	// opened from that config must show it again.
	for _, tc := range []struct {
		key  rune
		want string
		row  string
	}{
		{tea.KeyRight, vm.StartAtBootNever, "< Never >"},
		{tea.KeySpace, vm.StartAtBootIfRunning, "< If running >"},
		{tea.KeyEnter, vm.StartAtBootAlways, "< Always >"},
		{tea.KeyLeft, vm.StartAtBootIfRunning, "< If running >"},
	} {
		focusStartAtBoot(t, &m)
		pressBoot(t, &m, tc.key)
		if !strings.Contains(ansi.Strip(m.formView()), "Start at boot: "+tc.row) {
			t.Fatalf("after %q the row does not read %s:\n%s", tc.key, tc.row, ansi.Strip(m.formView()))
		}
		cfg, err := m.buildConfig()
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.StartAtBoot(); got != tc.want {
			t.Fatalf("after %q mode = %q, want %q", tc.key, got, tc.want)
		}

		reset := m
		reset.openResetForm(reset.formScope, cfg.Name, cfg)
		focusStartAtBoot(t, &reset)
		rcfg, err := reset.buildConfig()
		if err != nil {
			t.Fatal(err)
		}
		if got := rcfg.StartAtBoot(); got != tc.want {
			t.Fatalf("reset form lost the mode: %q, want %q", got, tc.want)
		}
		// The reset form has its own key routing; it must step the row too.
		pressBoot(t, &reset, tea.KeyLeft)
		if rcfg, _ = reset.buildConfig(); rcfg.StartAtBoot() == tc.want {
			t.Fatalf("left on the reset form's row did not change the mode from %q", tc.want)
		}
	}

	// A new create form starts from the default, not the last VM's choice.
	m.openForm()
	if cfg, _ := m.buildConfig(); cfg.StartAtBoot() != vm.StartAtBootAlways {
		t.Fatal("create form carried the previous choice forward")
	}
}

// Keys on the selector must not leak into navigation or the text inputs.
func TestFormStartAtBootKeysStayOnRow(t *testing.T) {
	m := proxmoxFormModel(t)
	focusStartAtBoot(t, &m)
	focus := m.toggleFocus
	before := m.inputs[fName].Value()
	pressBoot(t, &m, tea.KeyEnter)
	pressBoot(t, &m, tea.KeyRight)
	if m.toggleFocus != focus {
		t.Fatalf("focus moved from %d to %d", focus, m.toggleFocus)
	}
	if m.inputs[fName].Value() != before {
		t.Fatal("a selector key reached a text input")
	}
}

func TestFormStartAtBootLimaHasNoRow(t *testing.T) {
	m := proxmoxFormModel(t)
	m.members[0].profile.Type = profiles.TypeLocal
	for _, toggle := range m.toggles() {
		if toggle.label == "Start at boot" {
			t.Fatal("Lima exposes the Proxmox setting")
		}
	}
}
