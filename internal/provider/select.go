package provider

import (
	"fmt"

	"github.com/lullabot/sandbar/internal/registry"
)

// RemoteLimaProviderID is the registry.Scope.Provider tag a remote-owned
// entry carries (see TargetConfig.Scope) once a RemoteSSH profile's provider
// is constructed (BuildFleet, fleet.go) — defined here (not in
// internal/registry, which knows nothing about SSH) so the tag and the
// TargetConfig that produces it stay in one place.
const RemoteLimaProviderID = "lima-remote"

// TargetConfig is the provider layer's minimal, secret-free connection
// identity: which provider to construct and, for the remote provider, the
// connection identity needed to reach it. It is deliberately secret-free —
// IdentityPath is a path to a private key FILE, never key material — so
// every field is safe to derive a registry.Scope from (see Scope) and,
// transitively, safe to persist in the managed-VM index. A RemoteSSH
// profiles.Profile is converted into one of these by targetConfigFor
// (fleet.go); this package never persists a TargetConfig itself.
type TargetConfig struct {
	// Provider is "" (local Lima, the default) or RemoteLimaProviderID.
	Provider string
	Host     string
	User     string
	// Port is only meaningful for the remote provider.
	Port int
	// IdentityPath is a path to a private key file, or "" to fall back to the
	// ambient SSH agent/config. Never key material.
	IdentityPath string
	// RemoteLimaHome is the remote host's LIMA_HOME, or "" to fall back to
	// Lima's own default there.
	RemoteLimaHome string
}

// Scope derives the registry.Scope that owns entries created under cfg: the
// local scope for an unconfigured (or explicitly "lima") target, or a remote
// scope keyed by a stable, secret-free identity ("user@host:port") for a
// remote target. This is the one place a TargetConfig becomes the identity
// the registry persists — see registry.Scope's secret-free contract, which
// this satisfies because every field folded into RemoteTarget here already is.
func (cfg TargetConfig) Scope() registry.Scope {
	if cfg.Provider == "" || cfg.Provider == registry.LocalProviderID {
		return registry.LocalScope
	}
	return registry.Scope{
		Provider:     cfg.Provider,
		RemoteTarget: fmt.Sprintf("%s@%s:%d", cfg.User, cfg.Host, cfg.Port),
	}
}
