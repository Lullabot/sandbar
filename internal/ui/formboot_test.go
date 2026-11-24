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

// pressBoot sends one key through the model's real Update, so the form's own key
// routing is what is tested.
func pressBoot(t *testing.T, m *model, code rune) {
	t.Helper()
	next, _ := m.Update(tea.KeyPressMsg{Code: code})
	*m = next.(model)
}

// focusField walks the real focus ring (down) from wherever it is to field i,
// failing if the ring never reaches it.
func focusField(t *testing.T, m *model, i int) {
	t.Helper()
	for range 64 {
		if m.toggleFocus == -1 && m.focusIdx == i {
			return
		}
		pressBoot(t, m, tea.KeyDown)
	}
	t.Fatalf("focus never reached field %d", i)
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

	// Reached by walking the ring from Disk, it is the very next stop.
	focusField(t, &m, fDisk)
	pressBoot(t, &m, tea.KeyDown)
	if m.toggleFocus != -1 || m.focusIdx != fStartAtBoot {
		t.Fatalf("the field after Disk is %d, want Start at boot", m.focusIdx)
	}
	view := ansi.Strip(m.formView())
	disk := strings.Index(view, "Disk:")
	boot := strings.Index(view, "Start at boot:")
	if disk < 0 || boot < disk || !strings.Contains(view, "< Always >") {
		t.Fatalf("Start at boot is not drawn after Disk:\n%s", view)
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
		{tea.KeyLeft, vm.StartAtBootNever, "< Never >"},
		{tea.KeyRight, vm.StartAtBootIfRunning, "< If running >"},
	} {
		focusField(t, &m, fStartAtBoot)
		pressBoot(t, &m, tc.key)
		if !strings.Contains(ansi.Strip(m.formView()), "Start at boot:") || !strings.Contains(ansi.Strip(m.formView()), tc.row) {
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
		rcfg, err := reset.buildConfig()
		if err != nil {
			t.Fatal(err)
		}
		if got := rcfg.StartAtBoot(); got != tc.want {
			t.Fatalf("reset form lost the mode: %q, want %q", got, tc.want)
		}
		// The reset form has its own key routing; it must step the row too.
		focusField(t, &reset, fStartAtBoot)
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

// Enter moves on, as on every other field; the selector keys change the value
// without moving focus or typing into a text input.
func TestFormStartAtBootKeys(t *testing.T) {
	m := proxmoxFormModel(t)
	focusField(t, &m, fStartAtBoot)
	before := m.inputs[fName].Value()
	pressBoot(t, &m, tea.KeySpace)
	pressBoot(t, &m, tea.KeyRight)
	if m.focusIdx != fStartAtBoot {
		t.Fatalf("a selector key moved focus to %d", m.focusIdx)
	}
	if m.inputs[fName].Value() != before || m.inputs[fStartAtBoot].Value() != "" {
		t.Fatal("a selector key reached a text input")
	}
	pressBoot(t, &m, tea.KeyEnter)
	if m.focusIdx == fStartAtBoot {
		t.Fatal("enter did not move to the next field")
	}
}

// The row is a Proxmox setting: a Lima form neither draws it nor stops on it.
func TestFormStartAtBootHiddenOnLima(t *testing.T) {
	m := proxmoxFormModel(t)
	m.members[0].profile.Type = profiles.TypeLocal
	focusField(t, &m, fDisk)
	pressBoot(t, &m, tea.KeyDown)
	if m.focusIdx == fStartAtBoot {
		t.Fatal("focus landed on the hidden Start at boot field")
	}
	pressBoot(t, &m, tea.KeyUp)
	if m.focusIdx != fDisk {
		t.Fatalf("going back from the field after Disk landed on %d, want Disk", m.focusIdx)
	}
	if strings.Contains(ansi.Strip(m.formView()), "Start at boot") {
		t.Fatal("a Lima form draws the Proxmox setting")
	}
}
