package provider

import (
	"fmt"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/registry"
)

// Binding is one enabled profile's constructed provider, paired with the
// registry.Scope its managed-VM entries are owned by. Err is set (and Prov
// left nil) when the profile's provider failed to construct — see
// BuildFleet, which turns a construction failure into an error binding
// rather than aborting the whole fleet.
type Binding struct {
	Profile profiles.Profile
	Prov    Provider
	Scope   registry.Scope
	Err     error
}

// Fleet is every ENABLED profile's binding, in the profiles store's stable
// (insertion) order.
type Fleet []Binding

// newDefault, newRemoteLima and newProxmox indirect this file's provider
// constructors through package-level vars so a test can stub a construction
// failure (see fleet_test.go's TestBuildFleet_BadRemoteBecomesErrorBinding) —
// neither NewDefault nor NewRemoteLima performs a network round-trip at
// construction time (that is Preflight's job, deliberately left to the
// caller — see BuildFleet's doc comment), so no real profile makes either
// fail today; this seam is what makes that error-handling path exercisable
// anyway, and is where a future validating constructor would plug in.
// NewProxmox is the one real exception: it reads its token file from disk at
// construction (see its own doc comment), so a bad token file DOES make it
// fail for a real, hand-edited profile — TestBuildFleet_ProxmoxBadTokenBecomesErrorBinding
// exercises that directly, without needing the stub seam at all.
var (
	newDefault    = NewDefault
	newRemoteLima = NewRemoteLima
	newProxmox    = NewProxmox
)

// BuildFleet constructs one Binding per ENABLED profile in store: a Local
// profile becomes NewDefault + registry.LocalScope; a RemoteSSH profile is
// converted to a TargetConfig (targetConfigFor) and becomes NewRemoteLima +
// cfg.Scope() — the existing "user@host:port" scope-key derivation
// (TargetConfig.Scope, select.go), not a new format invented here.
//
// Construction is cheap and never blocks on the network: neither NewDefault
// nor NewRemoteLima performs a round-trip at construction time, only when a
// method (Preflight, List, ...) is later called on the result. BuildFleet
// itself never calls Preflight — that stays the caller's job (the CLI
// preflights only the one selected profile, e.g. cmd/sand/create.go; the TUI
// preflights every fleet member asynchronously, in internal/ui/commands.go's
// refreshCmd), so building the fleet is always fast
// regardless of how many profiles are enabled or reachable.
//
// A profile whose provider fails to construct becomes an error binding (Err
// set, Prov nil) rather than aborting the whole build, so one misconfigured
// remote never hides — or breaks — the rest of the fleet.
func BuildFleet(store *profiles.Store) Fleet {
	var fleet Fleet
	for _, p := range store.List() {
		if !p.Enabled {
			continue
		}
		fleet = append(fleet, buildBinding(p))
	}
	return fleet
}

// buildBinding constructs the single Binding for one enabled profile. A
// profile's Type must be recognised (TypeLocal, TypeRemoteSSH or
// TypeProxmox) and, for TypeRemoteSSH/TypeProxmox, must carry a non-empty
// Host — LoadFrom deliberately loads a hand-edited profile that fails either
// check rather than locking out the rest of the file (see
// profiles.LoadFrom/validate), so those two error conditions surface HERE, as
// a clear per-profile error binding, rather than being silently treated as
// local (finding 3) or reaching NewRemoteLima/NewProxmox only to fail later
// with a cryptic low-level error (finding 9).
func buildBinding(p profiles.Profile) Binding {
	switch p.Type {
	case profiles.TypeRemoteSSH:
		if p.Host == "" {
			return Binding{Profile: p, Err: fmt.Errorf("profile %q has no host", p.Name)}
		}
		cfg := targetConfigFor(p)
		prov, err := newRemoteLima(cfg)
		if err != nil {
			return Binding{Profile: p, Err: err}
		}
		return Binding{Profile: p, Prov: prov, Scope: cfg.Scope()}
	case profiles.TypeProxmox:
		if p.Host == "" {
			return Binding{Profile: p, Err: fmt.Errorf("profile %q has no host", p.Name)}
		}
		cfg := targetConfigFor(p)
		prov, err := newProxmox(cfg)
		if err != nil {
			return Binding{Profile: p, Err: err}
		}
		return Binding{Profile: p, Prov: prov, Scope: cfg.Scope()}
	case profiles.TypeLocal:
		prov, err := newDefault()
		if err != nil {
			return Binding{Profile: p, Err: err}
		}
		return Binding{Profile: p, Prov: prov, Scope: registry.LocalScope}
	default:
		return Binding{Profile: p, Err: fmt.Errorf("profile %q: unknown profile type %q", p.Name, p.Type)}
	}
}

// targetConfigFor converts a RemoteSSH or Proxmox profile into the
// secret-free TargetConfig that NewRemoteLima/NewProxmox and
// TargetConfig.Scope() consume — a direct field-for-field mapping, reusing
// the existing scope-key derivation rather than inventing a new one.
// IdentityPath, LimaHome and TokenFile all carry across unchanged: each is a
// PATH (a private key file, a remote LIMA_HOME, a token credential file
// respectively), never secret material itself, matching TargetConfig's own
// secret-free contract.
//
// This function stays TOTAL (no error return) even though the Proxmox path's
// token can fail to load: NewProxmox itself calls profiles.LoadToken(cfg.TokenFile)
// at construction (see that constructor's doc comment) rather than this
// function loading it eagerly. That keeps TargetConfig secret-free (only the
// path is ever carried) and keeps this conversion — and buildBinding's shape
// above, which already has a distinct error path for construction failure —
// simple, rather than plumbing a second error return through both this
// function and its cmd/sand/resolve.go duplicate.
func targetConfigFor(p profiles.Profile) TargetConfig {
	if p.Type == profiles.TypeProxmox {
		return TargetConfig{
			Provider:     ProxmoxProviderID,
			Host:         p.Host,
			User:         p.User,
			Node:         p.Node,
			Pool:         p.Pool,
			Storage:      p.Storage,
			ImageStorage: p.ImageStorage,
			Bridge:       p.Bridge,
			TokenFile:    p.TokenFile,
			Insecure:     p.Insecure,
			CAFile:       p.CAFile,
		}
	}
	return TargetConfig{
		Provider:       RemoteLimaProviderID,
		Host:           p.Host,
		User:           p.User,
		Port:           p.Port,
		IdentityPath:   p.IdentityPath,
		RemoteLimaHome: p.LimaHome,
	}
}
