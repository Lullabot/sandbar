package main

import (
	"fmt"
	"os"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
)

// loadStore loads the profiles store, reporting (but not failing on) a
// quarantined-and-reseeded corrupt file — see profiles.LoadFrom's doc
// comment: the store it returns alongside an error is still safe to build
// from.
func loadStore() *profiles.Store {
	store, err := profiles.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return store
}

// resolveProfileName picks the ONE profile a headless CLI command
// (`sand create`, `sand shell`) should act on: an explicit name takes
// precedence and is a hard error if it does not name an enabled profile (no
// fallback — the user asked for THIS profile). With no explicit name, it
// falls back to the store's last-used profile, and finally to the permanent
// Local profile — the same default order every entrypoint used before this
// task (see the now-removed resolveSingle), made explicit and
// name-addressable. Unlike the explicit-name path, a stale/disabled
// last-used pointer (e.g. that profile was later removed or disabled)
// silently falls back to Local rather than erroring, since the user did not
// ask for that profile by name here.
func resolveProfileName(store *profiles.Store, name string) (profiles.Profile, error) {
	if name != "" {
		p, ok := store.GetByName(name)
		if !ok {
			return profiles.Profile{}, fmt.Errorf("unknown connection profile %q", name)
		}
		if !p.Enabled {
			return profiles.Profile{}, fmt.Errorf("profile %q is disabled", p.Name)
		}
		return p, nil
	}

	id := store.LastUsed()
	if id == "" {
		id = profiles.LocalProfileID
	}
	p, ok := store.Get(id)
	if !ok || !p.Enabled {
		p, ok = store.Get(profiles.LocalProfileID)
	}
	if !ok || !p.Enabled {
		return profiles.Profile{}, fmt.Errorf("no enabled connection profile found (not even %q)", profiles.LocalProfileID)
	}
	return p, nil
}

// providerForProfile constructs the single provider/scope pair for one
// profile — the same conversion provider.BuildFleet applies per-profile
// (fleet.go's buildBinding), reimplemented here so a one-shot CLI command
// need not build (and risk failing on) every OTHER enabled profile's remote
// just to act on this one. Construction never round-trips the network (see
// BuildFleet's doc comment), so this is cheap and safe to call for a profile
// that may turn out to be unreachable — that failure surfaces later, from
// Preflight.
func providerForProfile(p profiles.Profile) (provider.Provider, registry.Scope, error) {
	if p.Type == profiles.TypeRemoteSSH {
		cfg := targetConfigFor(p)
		prov, err := provider.NewRemoteLima(cfg)
		if err != nil {
			return nil, registry.Scope{}, fmt.Errorf("profile %q: %w", p.Name, err)
		}
		return prov, cfg.Scope(), nil
	}

	prov, err := provider.NewDefault()
	if err != nil {
		return nil, registry.Scope{}, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	return prov, registry.LocalScope, nil
}

// scopeForProfile derives the registry.Scope a profile's managed-VM entries
// are owned by, WITHOUT constructing its provider — used by `sand shell`'s
// cross-profile ownership lookup, which only needs each enabled profile's
// scope to query the registry, not a live connection to every remote.
func scopeForProfile(p profiles.Profile) registry.Scope {
	if p.Type == profiles.TypeRemoteSSH {
		return targetConfigFor(p).Scope()
	}
	return registry.LocalScope
}

// targetConfigFor converts a RemoteSSH profile into the provider layer's
// TargetConfig — a direct field-for-field mapping, duplicating
// internal/provider/fleet.go's unexported targetConfigFor (which this
// package cannot call). Keep the two in agreement if either changes; see
// profiles.Profile.remoteTarget's doc comment for why this small duplication
// is preferred over an import-cycle-inducing export.
func targetConfigFor(p profiles.Profile) provider.TargetConfig {
	return provider.TargetConfig{
		Provider:       provider.RemoteLimaProviderID,
		Host:           p.Host,
		User:           p.User,
		Port:           p.Port,
		IdentityPath:   p.IdentityPath,
		RemoteLimaHome: p.LimaHome,
	}
}

// bindingForProfileName resolves name to a profile (see resolveProfileName)
// and constructs its provider/scope, returning the resolved profile too so
// the caller can record it as last-used or report it by name/ID.
func bindingForProfileName(store *profiles.Store, name string) (provider.Provider, registry.Scope, profiles.Profile, error) {
	p, err := resolveProfileName(store, name)
	if err != nil {
		return nil, registry.Scope{}, profiles.Profile{}, err
	}
	prov, scope, err := providerForProfile(p)
	if err != nil {
		return nil, registry.Scope{}, profiles.Profile{}, err
	}
	return prov, scope, p, nil
}
