package provider

import (
	"errors"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/registry"
)

// newTestStore builds an in-memory (no-disk) profiles.Store seeded with the
// given profiles, bypassing profiles.Load/LoadFrom's disk seeding so these
// tests never touch the filesystem.
func newTestStore(t *testing.T, ps ...profiles.Profile) *profiles.Store {
	t.Helper()
	s, err := profiles.LoadFrom(t.TempDir() + "/profiles.yaml")
	if err != nil {
		t.Fatalf("profiles.LoadFrom: %v", err)
	}
	// LoadFrom seeds a permanent Local profile automatically; only add the
	// profiles the test asked for that are not already present.
	for _, p := range ps {
		if p.Type == profiles.TypeLocal {
			continue // the seeded Local profile already covers this case
		}
		if _, err := s.Add(p); err != nil {
			t.Fatalf("store.Add(%+v): %v", p, err)
		}
	}
	return s
}

// TestBuildFleet_AllLocal covers the default, unconfigured store: the seeded
// Local profile is the only enabled one, so the fleet must contain exactly
// one binding, owning registry.LocalScope, with no error.
func TestBuildFleet_AllLocal(t *testing.T) {
	store := newTestStore(t)

	fleet := BuildFleet(store)
	if len(fleet) != 1 {
		t.Fatalf("BuildFleet() = %d bindings, want 1 (local only)", len(fleet))
	}
	b := fleet[0]
	if b.Err != nil {
		t.Fatalf("local binding Err = %v, want nil", b.Err)
	}
	if b.Prov == nil {
		t.Fatal("local binding Prov is nil")
	}
	if b.Scope != registry.LocalScope {
		t.Fatalf("local binding Scope = %+v, want registry.LocalScope", b.Scope)
	}
	if b.Profile.ID != profiles.LocalProfileID {
		t.Fatalf("local binding Profile.ID = %q, want %q", b.Profile.ID, profiles.LocalProfileID)
	}
}

// TestBuildFleet_DisabledProfileExcluded confirms only ENABLED profiles
// produce a binding.
func TestBuildFleet_DisabledProfileExcluded(t *testing.T) {
	store := newTestStore(t, profiles.Profile{
		Name: "off", Type: profiles.TypeRemoteSSH, Enabled: false,
		Host: "example.com", User: "dev", Port: 22,
	})

	fleet := BuildFleet(store)
	if len(fleet) != 1 {
		t.Fatalf("BuildFleet() = %d bindings, want 1 (disabled remote excluded)", len(fleet))
	}
	if fleet[0].Profile.Type != profiles.TypeLocal {
		t.Fatalf("only binding should be the Local profile, got %+v", fleet[0].Profile)
	}
}

// TestBuildFleet_TwoEnabledProfiles: a store with the Local profile plus one
// enabled RemoteSSH profile yields two bindings, the remote one carrying the
// user@host:port scope derived via TargetConfig.Scope (select.go:86) — the
// fleet must not invent its own scope-key format.
func TestBuildFleet_TwoEnabledProfiles(t *testing.T) {
	store := newTestStore(t, profiles.Profile{
		Name: "prod", Type: profiles.TypeRemoteSSH, Enabled: true,
		Host: "example.com", User: "dev", Port: 2222,
	})

	fleet := BuildFleet(store)
	if len(fleet) != 2 {
		t.Fatalf("BuildFleet() = %d bindings, want 2", len(fleet))
	}

	var sawLocal, sawRemote bool
	for _, b := range fleet {
		if b.Err != nil {
			t.Fatalf("binding for profile %+v has unexpected Err: %v", b.Profile, b.Err)
		}
		if b.Prov == nil {
			t.Fatalf("binding for profile %+v has nil Prov", b.Profile)
		}
		switch b.Profile.Type {
		case profiles.TypeLocal:
			sawLocal = true
			if b.Scope != registry.LocalScope {
				t.Fatalf("local binding Scope = %+v, want registry.LocalScope", b.Scope)
			}
		case profiles.TypeRemoteSSH:
			sawRemote = true
			want := registry.Scope{Provider: RemoteLimaProviderID, RemoteTarget: "dev@example.com:2222"}
			if b.Scope != want {
				t.Fatalf("remote binding Scope = %+v, want %+v", b.Scope, want)
			}
		}
	}
	if !sawLocal || !sawRemote {
		t.Fatalf("fleet should contain one local and one remote binding, got %+v", fleet)
	}
}

// TestBuildFleet_BadRemoteBecomesErrorBinding is the acceptance criterion at
// the heart of this task: a profile whose provider fails to construct must
// become an error binding (Err set, Prov nil) rather than aborting the whole
// build — one bad remote must never hide the rest of the fleet (and never
// stop the OTHER bindings from constructing). NewRemoteLima never itself
// round-trips over the network at construction time, so no real TargetConfig
// makes it fail today; this test stubs the construction seam (newRemoteLima,
// a package-level var precisely so this failure path is exercisable) to
// simulate the failure a future validating constructor could produce.
func TestBuildFleet_BadRemoteBecomesErrorBinding(t *testing.T) {
	orig := newRemoteLima
	t.Cleanup(func() { newRemoteLima = orig })
	wantErr := errors.New("boom: could not construct remote provider")
	newRemoteLima = func(cfg TargetConfig) (Provider, error) { return nil, wantErr }

	store := newTestStore(t) // one enabled Local profile only
	if _, err := store.Add(profiles.Profile{
		Name: "prod", Type: profiles.TypeRemoteSSH, Enabled: true,
		Host: "example.com", User: "dev", Port: 22,
	}); err != nil {
		t.Fatalf("store.Add: %v", err)
	}

	fleet := BuildFleet(store)
	if len(fleet) != 2 {
		t.Fatalf("BuildFleet() = %d bindings, want 2 (local + error) — a bad remote must not abort the build", len(fleet))
	}

	var sawLocal, sawErr bool
	for _, b := range fleet {
		switch b.Profile.Type {
		case profiles.TypeLocal:
			sawLocal = true
			if b.Err != nil {
				t.Fatalf("local binding Err = %v, want nil — a bad remote must not poison the local binding", b.Err)
			}
			if b.Prov == nil {
				t.Fatal("local binding Prov is nil")
			}
		case profiles.TypeRemoteSSH:
			sawErr = true
			if !errors.Is(b.Err, wantErr) {
				t.Fatalf("remote binding Err = %v, want it to wrap %v", b.Err, wantErr)
			}
			if b.Prov != nil {
				t.Fatalf("error binding Prov = %v, want nil", b.Prov)
			}
		}
	}
	if !sawLocal || !sawErr {
		t.Fatalf("fleet should contain one healthy local binding and one error binding, got %+v", fleet)
	}
}

// TestTargetConfigFor_ConvertsProfile pins the profiles.Profile ->
// provider.TargetConfig conversion the fleet builder relies on: a direct
// field-for-field mapping with the RemoteLimaProviderID tag, no defaulting or
// reinterpretation.
func TestTargetConfigFor_ConvertsProfile(t *testing.T) {
	p := profiles.Profile{
		ID: "abc123", Name: "prod", Type: profiles.TypeRemoteSSH, Enabled: true,
		Host: "example.com", User: "dev", Port: 2222,
		IdentityPath: "/home/dev/.ssh/id_ed25519", LimaHome: "/home/dev/.lima",
	}
	cfg := targetConfigFor(p)
	if cfg.Provider != RemoteLimaProviderID {
		t.Fatalf("targetConfigFor Provider = %q, want %q", cfg.Provider, RemoteLimaProviderID)
	}
	if cfg.Host != p.Host || cfg.User != p.User || cfg.Port != p.Port || cfg.IdentityPath != p.IdentityPath {
		t.Fatalf("targetConfigFor = %+v, did not carry profile fields across faithfully (%+v)", cfg, p)
	}
	if cfg.RemoteLimaHome != p.LimaHome {
		t.Fatalf("targetConfigFor RemoteLimaHome = %q, want profile LimaHome %q", cfg.RemoteLimaHome, p.LimaHome)
	}
	if strings.Contains(cfg.Scope().RemoteTarget, cfg.IdentityPath) {
		t.Fatalf("derived scope must never carry the identity path, got %+v", cfg.Scope())
	}
}
