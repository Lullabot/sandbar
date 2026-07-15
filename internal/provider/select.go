package provider

import (
	"fmt"
	"os"
	"strconv"

	"github.com/lullabot/sandbar/internal/registry"
)

// Environment variables that make up sand's opt-in provider-selection
// surface (plan 15 task 4/7). Kept deliberately minimal: one variable picks
// the provider, and the rest are only consulted for a remote target. None of
// them may ever carry a secret — SandRemoteIdentityEnv is a PATH to a private
// key file on disk, never key material, which is what makes it safe to also
// persist as part of a registry.Scope (see TargetConfig.Scope).
const (
	// SandProviderEnv selects the backend: unset or "lima" (registry.LocalProviderID)
	// is the default, local Lima; RemoteLimaProviderID selects the remote-Lima-
	// over-SSH provider that plan 15 task 5 implements.
	SandProviderEnv = "SAND_PROVIDER"
	// SandRemoteHostEnv is the remote host to SSH to. Required when
	// SandProviderEnv selects the remote provider.
	SandRemoteHostEnv = "SAND_REMOTE_HOST"
	// SandRemoteUserEnv is the SSH user on the remote host. Optional; the
	// remote provider (task 5) falls back to its own default when empty.
	SandRemoteUserEnv = "SAND_REMOTE_USER"
	// SandRemotePortEnv is the SSH port on the remote host. Optional; defaults
	// to 22 when unset or empty.
	SandRemotePortEnv = "SAND_REMOTE_PORT"
	// SandRemoteIdentityEnv is a PATH to the SSH private key file to use — never
	// the key material itself. Optional; the remote provider (task 5) falls back
	// to the ambient SSH agent/config when empty.
	SandRemoteIdentityEnv = "SAND_REMOTE_IDENTITY"
	// SandRemoteLimaHomeEnv is the value of LIMA_HOME on the remote host, so the
	// remote provider (task 5) knows where that host keeps its Lima instance
	// files. Optional; the remote provider falls back to Lima's own default
	// (~/.lima) when empty.
	SandRemoteLimaHomeEnv = "SAND_REMOTE_LIMA_HOME"
)

// RemoteLimaProviderID is the SandProviderEnv value that selects the
// remote-Lima-over-SSH provider. It is also the registry.Scope.Provider tag a
// remote-owned entry carries once task 5 implements the provider — defined
// here (not in internal/registry, which knows nothing about SSH or task 5) so
// the tag and the selection value that produces it stay in one place.
const RemoteLimaProviderID = "lima-remote"

// defaultRemotePort is used when SandRemotePortEnv is unset or empty.
const defaultRemotePort = 22

// TargetConfig is sand's minimal provider-selection surface: which provider to
// construct and, for the remote provider, the connection identity needed to
// reach it. It is deliberately secret-free — IdentityPath is a path to a
// private key FILE, never key material — so every field is safe to derive a
// registry.Scope from (see Scope) and, transitively, safe to persist in the
// managed-VM index.
type TargetConfig struct {
	// Provider is "" (local Lima, the default) or RemoteLimaProviderID.
	Provider string
	Host     string
	User     string
	// Port is only meaningful for the remote provider; resolveTargetConfig
	// fills it with defaultRemotePort when unconfigured.
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

// resolveTargetConfig reads the provider-selection environment (the Sand*Env
// constants above) into a TargetConfig. An unset SandProviderEnv (or a value
// of registry.LocalProviderID) means "local Lima, unconfigured" — the
// zero-config default this whole seam must not disturb.
func resolveTargetConfig() (TargetConfig, error) {
	cfg := TargetConfig{
		Provider:       os.Getenv(SandProviderEnv),
		Host:           os.Getenv(SandRemoteHostEnv),
		User:           os.Getenv(SandRemoteUserEnv),
		IdentityPath:   os.Getenv(SandRemoteIdentityEnv),
		RemoteLimaHome: os.Getenv(SandRemoteLimaHomeEnv),
		Port:           defaultRemotePort,
	}
	if portStr := os.Getenv(SandRemotePortEnv); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return TargetConfig{}, fmt.Errorf("%s=%q is not a valid port number: %w", SandRemotePortEnv, portStr, err)
		}
		cfg.Port = port
	}
	return cfg, nil
}

// Resolve reads sand's opt-in provider-selection environment and constructs
// the selected backend along with the registry.Scope that owns its
// managed-VM entries. This is the sibling to NewDefault that task 4 adds:
// NewDefault stays the zero-argument, always-local constructor task 3
// centralised; Resolve wraps it with the selection layer while keeping the
// unconfigured path behaviourally IDENTICAL — an unconfigured environment
// resolves to exactly what NewDefault returns, paired with registry.LocalScope.
//
// A remote selection (SandProviderEnv=RemoteLimaProviderID) validates that a
// host was supplied — a clear config error beats a confusing silent fallback to
// local — and then constructs the remote-Lima-over-SSH provider (NewRemoteLima),
// paired with the remote registry.Scope its created VMs are tagged with so a
// remote host's instances never mix with the local list.
func Resolve() (Provider, registry.Scope, error) {
	cfg, err := resolveTargetConfig()
	if err != nil {
		return nil, registry.Scope{}, err
	}
	switch cfg.Provider {
	case "", registry.LocalProviderID:
		p, err := NewDefault()
		if err != nil {
			return nil, registry.Scope{}, err
		}
		return p, registry.LocalScope, nil
	case RemoteLimaProviderID:
		if cfg.Host == "" {
			return nil, registry.Scope{}, fmt.Errorf("%s=%s requires %s to be set", SandProviderEnv, cfg.Provider, SandRemoteHostEnv)
		}
		p, err := NewRemoteLima(cfg)
		if err != nil {
			return nil, registry.Scope{}, err
		}
		return p, cfg.Scope(), nil
	default:
		return nil, registry.Scope{}, fmt.Errorf("%s=%q is not a known provider (want %q or %q)",
			SandProviderEnv, cfg.Provider, registry.LocalProviderID, RemoteLimaProviderID)
	}
}
