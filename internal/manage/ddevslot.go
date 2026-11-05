package manage

import (
	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// AssignDDEVSlot gives cfg a ddev port slot if it needs one: a Lima VM (local
// or remote) with ddev selected and no slot recorded yet. Anything else is
// left untouched — Proxmox guests have their own address and keep ddev's
// default ports, and a recorded slot is kept as-is, without re-probing, even
// if another VM has since claimed it, because moving a VM's ports would break
// its bookmarks and tunnels.
//
// The slots already held are the union of the profile's provenance markers
// (the ownership authority, shared by every controller of the host) and its
// registry entries (which still cover a VM whose marker could not be read).
// Over-holding from the union only costs a probe step. Entries named cfg.Name
// are ignored so a reset never collides with its own previous record.
// Template records are in neither input and hold no slot.
//
// It performs no I/O: the TUI calls it on its submit path with the
// provenance it already has cached, and the CLI with one Provenance call.
func AssignDDEVSlot(cfg *vm.CreateConfig, kind profiles.Type, markers map[string]provider.Provenance, managed []registry.ManagedEntry) error {
	if kind != profiles.TypeLocal && kind != profiles.TypeRemoteSSH {
		return nil
	}
	if !cfg.WithDDEV || cfg.DDEVPortSlot != nil {
		return nil
	}
	held := map[int]bool{}
	hold := func(name string, c vm.CreateConfig) {
		if name != cfg.Name && c.DDEVPortSlot != nil {
			held[*c.DDEVPortSlot] = true
		}
	}
	for name, pv := range markers {
		hold(name, pv.Config)
	}
	for _, e := range managed {
		hold(e.Name, e.Config)
	}
	s, err := vm.AllocateDDEVSlot(cfg.Name, held)
	if err != nil {
		return err
	}
	// A fresh pointer: cfg may be a copy whose fields alias a cached record.
	cfg.DDEVPortSlot = &s
	return nil
}
