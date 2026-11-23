package ui

import (
	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"testing"
)

// bootToggle finds a Proxmox boot row by label. The rows are looked up by
// label rather than position because reset mode's toggle count varies.
func bootToggle(t *testing.T, m *model, label string) formToggle {
	t.Helper()
	for _, toggle := range m.toggles() {
		if toggle.label == label {
			return toggle
		}
	}
	t.Fatalf("no %q toggle", label)
	return formToggle{}
}

func TestFormStartAtBoot(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	fillCreateForm(t, &m, "web")
	m.members[0].profile.Type = profiles.TypeProxmox
	for _, reset := range []bool{false, true} {
		cfg, err := m.buildConfig()
		if err != nil {
			t.Fatal(err)
		}
		if reset {
			m.openResetForm(m.formScope, cfg.Name, cfg)
		}
		boot := bootToggle(t, &m, "Start at boot")
		if !boot.get(&m) {
			t.Fatal("start at boot must default on")
		}
		boot.set(&m, false)
		cfg, err = m.buildConfig()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.DisableStartAtBoot {
			t.Fatal("checkbox did not reach create config")
		}
		m.openResetForm(m.formScope, cfg.Name, cfg)
		if bootToggle(t, &m, "Start at boot").get(&m) {
			t.Fatal("reset lost opt-out")
		}
		boot.set(&m, true)
	}
	m.members[0].profile.Type = profiles.TypeLocal
	for _, toggle := range m.toggles() {
		if toggle.label == "Start at boot" || toggle.label == "Only if left running" {
			t.Fatal("Lima exposes Proxmox setting")
		}
	}
}

func TestFormBootFollowsPower(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	fillCreateForm(t, &m, "web")
	m.members[0].profile.Type = profiles.TypeProxmox

	follow := bootToggle(t, &m, "Only if left running")
	if follow.get(&m) {
		t.Fatal("following the power state must default off")
	}
	follow.set(&m, true)
	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.BootFollowsPower {
		t.Fatal("checkbox did not reach create config")
	}
	m.openResetForm(m.formScope, cfg.Name, cfg)
	if !bootToggle(t, &m, "Only if left running").get(&m) {
		t.Fatal("reset lost the recorded setting")
	}

	// A new create form starts from the default, not the last VM's choice.
	m.openForm()
	if bootToggle(t, &m, "Only if left running").get(&m) {
		t.Fatal("create form carried the previous choice forward")
	}
}
