package ui

import (
	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"testing"
)

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
		toggles := m.toggles()
		boot := toggles[len(toggles)-1]
		if boot.label != "Start at boot" || !boot.get(&m) {
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
		toggles = m.toggles()
		if toggles[len(toggles)-1].get(&m) {
			t.Fatal("reset lost opt-out")
		}
		boot.set(&m, true)
	}
	m.members[0].profile.Type = profiles.TypeLocal
	for _, toggle := range m.toggles() {
		if toggle.label == "Start at boot" {
			t.Fatal("Lima exposes Proxmox setting")
		}
	}
}
