package main

import (
	"context"
	"fmt"
	"io"

	"github.com/lullabot/sandbar/internal/manage"
	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// assignDDEVSlot gives cfg a ddev port slot if it needs one (see
// manage.AssignDDEVSlot). Callers run it before the provider sees cfg, so the
// in-flight marker written at clone time already carries the slot and any
// other controller allocating on the same host meanwhile sees it as held.
//
// The profile's markers are read with one Provenance call, and only when an
// allocation can actually happen — a Proxmox profile, a VM without ddev, or a
// VM whose slot is already recorded never needs them. A failed read is not
// fatal: the registry still holds every VM this controller manages, and
// missing another controller's VM costs at worst the port clash this exists
// to make rare, which is no reason to refuse the create.
func assignDDEVSlot(ctx context.Context, cfg *vm.CreateConfig, kind profiles.Type, pv provider.Provenancer, reg *registry.Registry, scope registry.Scope) error {
	if !manage.NeedsDDEVSlot(*cfg, kind) {
		return nil
	}
	var markers map[string]provider.Provenance
	if pv != nil {
		if m, err := pv.Provenance(ctx); err == nil {
			markers = m
		}
	}
	return manage.AssignDDEVSlot(cfg, kind, markers, reg.ManagedInScope(scope))
}

// printDDEVPorts reports the router ports of a VM that has a slot, right after
// it is built — the moment the user needs a URL. The project name is not
// sandbar's to know, so it stays a placeholder.
func printDDEVPorts(out io.Writer, cfg vm.CreateConfig) {
	if !cfg.WithDDEV || cfg.DDEVPortSlot == nil {
		return
	}
	p := vm.DDEVPortsForSlot(*cfg.DDEVPortSlot)
	fmt.Fprintf(out, "ddev: http://<project>.ddev.site:%d  https://<project>.ddev.site:%d\n", p.RouterHTTP, p.RouterHTTPS)
}
