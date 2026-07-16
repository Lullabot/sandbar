package main

import (
	"fmt"
	"os"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
)

// resolveSingle picks the ONE provider/scope binding that drives each of
// sand's three current entrypoints (the TUI, `sand create`, `sand shell`):
// it loads the profiles store (internal/profiles), builds the fleet (plan 16
// task 4's provider.BuildFleet — one binding per ENABLED profile), and
// selects the store's last-used profile if it named one and still has a
// binding, falling back to the permanent Local profile otherwise. For an
// unconfigured (fresh) store the fleet has exactly one binding — the seeded
// Local profile — so this behaves exactly like the single always-local
// provider every entrypoint used before this task, unchanged.
//
// This is a deliberately temporary shim: task 5 finishes the CLI conversion
// (--profile flags, and disambiguating `sand shell` across more than one
// enabled profile). Until then, all three entrypoints keep working the same
// way they always have by construction — the same fallback-to-Local rule
// that made them work with no profiles.yaml at all.
func resolveSingle() (provider.Provider, registry.Scope, error) {
	store, err := profiles.Load()
	if err != nil {
		// profiles.Load already quarantines a corrupt file and reseeds a usable
		// (Local-only) store rather than failing outright — see LoadFrom's doc
		// comment — so this is reported, not fatal: the store returned alongside
		// err is still safe to build a fleet from.
		fmt.Fprintln(os.Stderr, err)
	}

	fleet := provider.BuildFleet(store)

	id := store.LastUsed()
	if id == "" {
		id = profiles.LocalProfileID
	}
	binding, ok := byProfileID(fleet, id)
	if !ok {
		binding, ok = byProfileID(fleet, profiles.LocalProfileID)
	}
	if !ok {
		return nil, registry.Scope{}, fmt.Errorf("no enabled connection profile found (not even %q)", profiles.LocalProfileID)
	}
	if binding.Err != nil {
		return nil, registry.Scope{}, fmt.Errorf("profile %q: %w", binding.Profile.Name, binding.Err)
	}
	return binding.Prov, binding.Scope, nil
}

// byProfileID returns the binding for the given profile ID, if the fleet has
// one.
func byProfileID(fleet provider.Fleet, id string) (provider.Binding, bool) {
	for _, b := range fleet {
		if b.Profile.ID == id {
			return b, true
		}
	}
	return provider.Binding{}, false
}
