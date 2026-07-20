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

// ProxmoxProviderID is the registry.Scope.Provider tag a Proxmox-owned entry
// carries (see TargetConfig.Scope), the sibling of RemoteLimaProviderID and
// defined here for the same reason: the tag and the TargetConfig that produces
// it stay in one place.
const ProxmoxProviderID = "proxmox"

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

	// --- Proxmox-only fields; zero for the other providers. ---

	// Node is the PVE node name (the identifier PVE uses in its own path
	// segments, e.g. /nodes/{node}/status) — distinct from Host, which is where
	// the API answers.
	Node string
	// Pool is the PVE resource pool every VM this target creates belongs to. It
	// is the whole isolation boundary: the API token is scoped to /pool/{Pool},
	// so a VM outside it is structurally unreachable rather than merely
	// unlisted.
	Pool string
	// Storage backs VM disks and the cloud-init drive; Bridge is the Linux
	// bridge net0 attaches to.
	Storage string
	Bridge  string
	// TokenFile is a PATH to a file holding the PVE API token, never the token
	// itself — the same contract IdentityPath keeps, and non-negotiable here:
	// Scope() folds this struct's fields into an identity the registry
	// PERSISTS, so a secret in this struct would be a secret on disk. The
	// constructor reads the file (profiles.LoadToken, which refuses one
	// readable by group or other); nothing else may carry the value.
	TokenFile string
	// Insecure disables TLS verification and CAFile pins a CA instead. PVE
	// ships a self-signed certificate, so these are a real per-profile choice
	// rather than a footgun left in by omission.
	Insecure bool
	CAFile   string
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
	if cfg.Provider == ProxmoxProviderID {
		// "host:node/pool" — the stable, secret-free identity of ONE pool on one
		// node. TokenFile is deliberately excluded: two profiles pointing at the
		// same pool through different token files are the same target and must
		// resolve to the same scope, or the registry would hand each its own
		// private view of the same VMs.
		//
		// This intentionally duplicates the trivial formatting in
		// profiles.Profile.proxmoxTarget (which cannot import this package
		// without a cycle). Keep the two formats in agreement if either changes,
		// exactly as the "user@host:port" pair below already requires.
		return registry.Scope{
			Provider:     cfg.Provider,
			RemoteTarget: fmt.Sprintf("%s:%s/%s", cfg.Host, cfg.Node, cfg.Pool),
		}
	}
	return registry.Scope{
		Provider:     cfg.Provider,
		RemoteTarget: fmt.Sprintf("%s@%s:%d", cfg.User, cfg.Host, cfg.Port),
	}
}
