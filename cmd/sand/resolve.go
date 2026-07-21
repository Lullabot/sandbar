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
	switch p.Type {
	case profiles.TypeRemoteSSH:
		// A hand-edited profile with an empty host is only reachable here via
		// LoadFrom, which does not validate (see profiles.LoadFrom/validate's
		// doc comments) — surface a clear error rather than letting NewRemoteLima
		// construct something that later fails with a cryptic `ssh user@`
		// (finding 9); mirrors provider.BuildFleet's buildBinding.
		if p.Host == "" {
			return nil, registry.Scope{}, fmt.Errorf("profile %q has no host", p.Name)
		}
		cfg := targetConfigFor(p)
		prov, err := provider.NewRemoteLima(cfg)
		if err != nil {
			return nil, registry.Scope{}, fmt.Errorf("profile %q: %w", p.Name, err)
		}
		return prov, cfg.Scope(), nil
	case profiles.TypeProxmox:
		// Mirrors the TypeRemoteSSH case above and provider.BuildFleet's
		// buildBinding: an empty host is only reachable via a hand-edited
		// profiles.yaml (store.Add/Update refuse it — see profiles.validate),
		// so surface it as a clear error rather than letting NewProxmox
		// construct something that fails later on a missing host.
		if p.Host == "" {
			return nil, registry.Scope{}, fmt.Errorf("profile %q has no host", p.Name)
		}
		cfg := targetConfigFor(p)
		prov, err := provider.NewProxmox(cfg)
		if err != nil {
			return nil, registry.Scope{}, fmt.Errorf("profile %q: %w", p.Name, err)
		}
		return prov, cfg.Scope(), nil
	case profiles.TypeLocal:
		prov, err := provider.NewDefault()
		if err != nil {
			return nil, registry.Scope{}, fmt.Errorf("profile %q: %w", p.Name, err)
		}
		return prov, registry.LocalScope, nil
	default:
		// An unrecognised Type (e.g. a hand-edited "remote_ssh" typo) must be a
		// hard error, not silently treated as local (finding 3) — mirrors
		// provider.BuildFleet's buildBinding.
		return nil, registry.Scope{}, fmt.Errorf("profile %q: unknown profile type %q", p.Name, p.Type)
	}
}

// scopeForProfile derives the registry.Scope a profile's managed-VM entries
// are owned by, WITHOUT constructing its provider — used by `sand shell`'s
// cross-profile ownership lookup, which only needs each enabled profile's
// scope to query the registry, not a live connection to every remote.
func scopeForProfile(p profiles.Profile) registry.Scope {
	if p.Type == profiles.TypeRemoteSSH || p.Type == profiles.TypeProxmox {
		return targetConfigFor(p).Scope()
	}
	return registry.LocalScope
}

// targetConfigFor converts a RemoteSSH or Proxmox profile into the provider
// layer's TargetConfig — a direct field-for-field mapping, duplicating
// internal/provider/fleet.go's unexported targetConfigFor (which this
// package cannot call). Keep the two in agreement if either changes; see
// profiles.Profile.remoteTarget's doc comment for why this small duplication
// is preferred over an import-cycle-inducing export.
//
// Like its fleet.go counterpart, this stays total (no error return): the
// Proxmox token file is a PATH here too (TokenFile), never loaded eagerly —
// NewProxmox reads it at construction, so a bad token file surfaces from
// providerForProfile's call to NewProxmox, not from this conversion.
func targetConfigFor(p profiles.Profile) provider.TargetConfig {
	if p.Type == profiles.TypeProxmox {
		return provider.TargetConfig{
			Provider:     provider.ProxmoxProviderID,
			Host:         p.Host,
			User:         p.User,
			Node:         p.Node,
			Pool:         p.Pool,
			Storage:      p.Storage,
			ImageStorage: p.ImageStorage,
			Bridge:       p.Bridge,
			TokenFile:    p.TokenFile,
			// IdentityPath is REQUIRED for Proxmox (see the fleet.go twin): sand
			// installs <identity_path>.pub via cloud-init and connects with the
			// private key. Omitting it here is what left the profile's key unused.
			IdentityPath: p.IdentityPath,
			Insecure:     p.Insecure,
			CAFile:       p.CAFile,
		}
	}
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
