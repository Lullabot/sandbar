package manage

import (
	"strconv"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

func slot(n int) *int { return &n }

func showSlot(p *int) string {
	if p == nil {
		return "none"
	}
	return strconv.Itoa(*p)
}

func TestAssignDDEVSlot(t *testing.T) {
	// "claude" prefers slot 743 (FNV-1a 32 mod 1000, pinned in internal/vm).
	const pref = 743
	withSlot := func(name string, s *int) vm.CreateConfig {
		return vm.CreateConfig{Name: name, WithDDEV: true, DDEVPortSlot: s}
	}
	// 743 is held by a marker and 744 only by the registry, so a fresh
	// allocation for "claude" must probe to 745 — which is claude's OWN
	// previous record and must therefore not count as held.
	markers := map[string]provider.Provenance{
		"other":  {Config: withSlot("other", slot(pref))},
		"claude": {Config: withSlot("claude", slot(pref+2))},
		// An unrecorded VM holds nothing — in particular not slot 0.
		"legacy": {Config: withSlot("legacy", nil)},
	}
	managed := []registry.ManagedEntry{
		{Name: "claude", Config: withSlot("claude", slot(pref+2))},
		// Present only in the registry: a marker-less VM still holds its slot.
		{Name: "cached", Config: withSlot("cached", slot(pref+1))},
	}

	tests := []struct {
		name    string
		kind    profiles.Type
		cfg     vm.CreateConfig
		markers map[string]provider.Provenance
		managed []registry.ManagedEntry
		want    *int
	}{
		{"fresh local VM takes its preferred slot", profiles.TypeLocal, withSlot("claude", nil), nil, nil, slot(pref)},
		{"held slots are probed past and the VM's own record is excluded", profiles.TypeLocal, withSlot("claude", nil), markers, managed, slot(pref + 2)},
		{"remote Lima allocates too", profiles.TypeRemoteSSH, withSlot("claude", nil), markers, managed, slot(pref + 2)},
		{"a recorded slot is kept even if another VM now holds it", profiles.TypeLocal, withSlot("claude", slot(pref)), markers, managed, slot(pref)},
		{"recorded slot 0 is kept, not reallocated", profiles.TypeLocal, withSlot("claude", slot(0)), markers, managed, slot(0)},
		{"Proxmox never gets a slot", profiles.TypeProxmox, withSlot("claude", nil), markers, managed, nil},
		{"a VM without ddev gets no slot", profiles.TypeLocal, vm.CreateConfig{Name: "claude"}, markers, managed, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			if err := AssignDDEVSlot(&cfg, tt.kind, tt.markers, tt.managed); err != nil {
				t.Fatalf("AssignDDEVSlot: %v", err)
			}
			if showSlot(cfg.DDEVPortSlot) != showSlot(tt.want) {
				t.Fatalf("slot = %s, want %s", showSlot(cfg.DDEVPortSlot), showSlot(tt.want))
			}
		})
	}

	t.Run("a full table is an error and leaves the config unset", func(t *testing.T) {
		var all []registry.ManagedEntry
		for s := range vm.DDEVPortSlots {
			all = append(all, registry.ManagedEntry{Name: "vm" + strconv.Itoa(s), Config: withSlot("vm", slot(s))})
		}
		cfg := withSlot("claude", nil)
		err := AssignDDEVSlot(&cfg, profiles.TypeLocal, nil, all)
		if err == nil || !strings.Contains(err.Error(), "no free ddev port slot") {
			t.Fatalf("AssignDDEVSlot on a full table = %v, want a no-free-slot error", err)
		}
		if cfg.DDEVPortSlot != nil {
			t.Fatalf("slot = %d after a failed allocation, want none", *cfg.DDEVPortSlot)
		}
	})
}
