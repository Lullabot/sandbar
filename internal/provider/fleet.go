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

// newDefault and newRemoteLima indirect this file's two provider
// constructors through package-level vars so a test can stub a construction
// failure (see fleet_test.go's TestBuildFleet_BadRemoteBecomesErrorBinding) —
// neither NewDefault nor NewRemoteLima performs a network round-trip at
// construction time (that is Preflight's job, deliberately left to the
// caller — see BuildFleet's doc comment), so no real profile makes either
// fail today; this seam is what makes that error-handling path exercisable
// anyway, and is where a future validating constructor would plug in.
var (
	newDefault    = NewDefault
	newRemoteLima = NewRemoteLima
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
// itself never calls Preflight — that stays the caller's job (task 5
// preflights the one CLI-selected profile; task 7 preflights every fleet
// member asynchronously in the TUI), so building the fleet is always fast
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
// profile's Type must be recognised (TypeLocal or TypeRemoteSSH) and, for
// TypeRemoteSSH, must carry a non-empty Host — LoadFrom deliberately loads a
// hand-edited profile that fails either check rather than locking out the
// rest of the file (see profiles.LoadFrom/validate), so those two error
// conditions surface HERE, as a clear per-profile error binding, rather than
// being silently treated as local (finding 3) or reaching NewRemoteLima only
// to fail later with a cryptic `ssh user@` error (finding 9).
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

// targetConfigFor converts a RemoteSSH profile into the secret-free
// TargetConfig that NewRemoteLima and TargetConfig.Scope() consume — a
// direct field-for-field mapping, reusing the existing scope-key derivation
// rather than inventing a new one. IdentityPath and LimaHome carry across
// unchanged (a path to a private key file and a remote LIMA_HOME path,
// respectively — never secret material, matching TargetConfig's own
// contract).
func targetConfigFor(p profiles.Profile) TargetConfig {
	return TargetConfig{
		Provider:       RemoteLimaProviderID,
		Host:           p.Host,
		User:           p.User,
		Port:           p.Port,
		IdentityPath:   p.IdentityPath,
		RemoteLimaHome: p.LimaHome,
	}
}
