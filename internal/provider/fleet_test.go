package provider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestBuildFleet_UnknownTypeBecomesErrorBinding is finding 3's regression
// test: a hand-edited profiles.yaml with a typo'd Type (e.g. "remote_ssh"
// instead of "remote-ssh") must NOT be silently treated as LOCAL — that
// would build a VM on the local machine for a profile the user believes is
// remote, and would create a duplicate LocalScope member in the TUI. LoadFrom
// must not lock the good entries out (profiles.TestLoadFromToleratesUnknownTypeEntry
// covers that), so the unknown-type entry reaches BuildFleet as-is; BuildFleet
// must turn it into a clear error binding instead.
func TestBuildFleet_UnknownTypeBecomesErrorBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	yamlContent := `version: 1
profiles:
  - id: local
    name: local
    type: local
    enabled: true
  - id: weird1
    name: weird
    type: remote_ssh
    enabled: true
    host: example.com
    user: dev
    port: 22
`
	if err := os.WriteFile(path, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store, err := profiles.LoadFrom(path)
	if err != nil {
		t.Fatalf("profiles.LoadFrom: %v", err)
	}

	fleet := BuildFleet(store)
	if len(fleet) != 2 {
		t.Fatalf("BuildFleet() = %d bindings, want 2 (local + error binding for the bad type)", len(fleet))
	}

	var sawLocal, sawErr bool
	for _, b := range fleet {
		switch b.Profile.ID {
		case "local":
			sawLocal = true
			if b.Err != nil {
				t.Fatalf("local binding Err = %v, want nil — an unrelated bad entry must not poison it", b.Err)
			}
			if b.Prov == nil {
				t.Fatal("local binding Prov is nil")
			}
		case "weird1":
			sawErr = true
			if b.Err == nil {
				t.Fatal("unknown-type binding Err = nil, want a clear error")
			}
			if b.Prov != nil {
				t.Fatalf("unknown-type binding Prov = %v, want nil", b.Prov)
			}
			if !strings.Contains(b.Err.Error(), "remote_ssh") {
				t.Errorf("unknown-type binding Err = %q, want it to name the bad type %q", b.Err.Error(), "remote_ssh")
			}
		}
	}
	if !sawLocal || !sawErr {
		t.Fatalf("fleet should contain one healthy local binding and one unknown-type error binding, got %+v", fleet)
	}
}

// TestBuildFleet_EmptyHostBecomesErrorBinding is finding 9's regression test:
// a RemoteSSH profile with an empty Host (only reachable via a hand-edited
// file, since the store's validate() now refuses it on Add/Update) must
// become a clear error binding rather than an obscure `ssh user@` failure
// deep inside NewRemoteLima/Preflight.
func TestBuildFleet_EmptyHostBecomesErrorBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	yamlContent := `version: 1
profiles:
  - id: local
    name: local
    type: local
    enabled: true
  - id: nohost
    name: nohost
    type: remote-ssh
    enabled: true
    user: dev
    port: 22
`
	if err := os.WriteFile(path, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store, err := profiles.LoadFrom(path)
	if err != nil {
		t.Fatalf("profiles.LoadFrom: %v", err)
	}

	fleet := BuildFleet(store)
	if len(fleet) != 2 {
		t.Fatalf("BuildFleet() = %d bindings, want 2 (local + error binding for the missing host)", len(fleet))
	}

	var sawErr bool
	for _, b := range fleet {
		if b.Profile.ID == "nohost" {
			sawErr = true
			if b.Err == nil {
				t.Fatal("empty-host binding Err = nil, want a clear error")
			}
			if b.Prov != nil {
				t.Fatalf("empty-host binding Prov = %v, want nil", b.Prov)
			}
			if !strings.Contains(b.Err.Error(), "host") {
				t.Errorf("empty-host binding Err = %q, want it to mention the missing host", b.Err.Error())
			}
		}
	}
	if !sawErr {
		t.Fatalf("fleet should contain an error binding for the empty-host profile, got %+v", fleet)
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

// proxmoxTokenFile writes a valid, owner-only-readable token file and returns
// its path, so tests can construct a real Proxmox binding without touching
// the network (profiles.LoadToken refuses a group/other-readable file, so
// mode matters here).
func proxmoxTokenFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("sandbar@pve!prov=1234\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	return path
}

// TestBuildFleet_ProxmoxProfile confirms an enabled Proxmox profile produces
// a healthy Binding whose Scope is the "host:node/pool" identity
// TargetConfig.Scope derives — the same conversion the RemoteSSH case
// exercises above, extended to the third provider.
func TestBuildFleet_ProxmoxProfile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tokenFile := proxmoxTokenFile(t)

	store := newTestStore(t, profiles.Profile{
		Name: "cluster", Type: profiles.TypeProxmox, Enabled: true,
		Host: "pve.example.com", Node: "pve1", Pool: "sandbar",
		Storage: "local-lvm", Bridge: "vmbr0", TokenFile: tokenFile,
	})

	fleet := BuildFleet(store)
	if len(fleet) != 2 {
		t.Fatalf("BuildFleet() = %d bindings, want 2 (local + proxmox)", len(fleet))
	}

	var sawProxmox bool
	for _, b := range fleet {
		if b.Profile.Type != profiles.TypeProxmox {
			continue
		}
		sawProxmox = true
		if b.Err != nil {
			t.Fatalf("proxmox binding Err = %v, want nil", b.Err)
		}
		if b.Prov == nil {
			t.Fatal("proxmox binding Prov is nil")
		}
		want := registry.Scope{Provider: ProxmoxProviderID, RemoteTarget: "pve.example.com:pve1/sandbar"}
		if b.Scope != want {
			t.Fatalf("proxmox binding Scope = %+v, want %+v", b.Scope, want)
		}
	}
	if !sawProxmox {
		t.Fatal("fleet should contain a proxmox binding")
	}
}

// TestBuildFleet_ProxmoxBadTokenBecomesErrorBinding confirms a Proxmox
// profile whose token_file cannot be read (missing here) becomes an error
// binding — Err set, Prov nil — rather than aborting the whole fleet build or
// failing confusingly on first use.
func TestBuildFleet_ProxmoxBadTokenBecomesErrorBinding(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	store := newTestStore(t, profiles.Profile{
		Name: "cluster", Type: profiles.TypeProxmox, Enabled: true,
		Host: "pve.example.com", Node: "pve1", Pool: "sandbar",
		TokenFile: filepath.Join(t.TempDir(), "does-not-exist"),
	})

	fleet := BuildFleet(store)
	if len(fleet) != 2 {
		t.Fatalf("BuildFleet() = %d bindings, want 2 (local + error)", len(fleet))
	}

	var sawLocal, sawErr bool
	for _, b := range fleet {
		switch b.Profile.Type {
		case profiles.TypeLocal:
			sawLocal = true
			if b.Err != nil {
				t.Fatalf("local binding Err = %v, want nil — a bad proxmox profile must not poison it", b.Err)
			}
		case profiles.TypeProxmox:
			sawErr = true
			if b.Err == nil {
				t.Fatal("proxmox binding with a missing token file: Err = nil, want a clear error")
			}
			if b.Prov != nil {
				t.Fatalf("error binding Prov = %v, want nil", b.Prov)
			}
		}
	}
	if !sawLocal || !sawErr {
		t.Fatalf("fleet should contain one healthy local binding and one proxmox error binding, got %+v", fleet)
	}
}

// TestBuildFleet_ProxmoxEmptyHostBecomesErrorBinding mirrors the RemoteSSH
// empty-host regression: a hand-edited profile with no host (store.Add
// itself refuses one — see profiles.validate — so this is only reachable via
// a hand-edited profiles.yaml, exactly like TestBuildFleet_EmptyHostBecomesErrorBinding
// above) must be a clear per-profile error, not a construction attempt that
// fails cryptically.
func TestBuildFleet_ProxmoxEmptyHostBecomesErrorBinding(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tokenFile := proxmoxTokenFile(t)

	path := filepath.Join(t.TempDir(), "profiles.yaml")
	yamlContent := `version: 1
profiles:
  - id: local
    name: local
    type: local
    enabled: true
  - id: nohost
    name: nohost
    type: proxmox
    enabled: true
    node: pve1
    pool: sandbar
    token_file: ` + tokenFile + `
`
	if err := os.WriteFile(path, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store, err := profiles.LoadFrom(path)
	if err != nil {
		t.Fatalf("profiles.LoadFrom: %v", err)
	}

	fleet := BuildFleet(store)
	var sawErr bool
	for _, b := range fleet {
		if b.Profile.Type != profiles.TypeProxmox {
			continue
		}
		sawErr = true
		if b.Err == nil {
			t.Fatal("proxmox binding with empty host: Err = nil, want a clear error")
		}
		if !strings.Contains(b.Err.Error(), "host") {
			t.Errorf("proxmox binding Err = %q, want it to mention the missing host", b.Err.Error())
		}
	}
	if !sawErr {
		t.Fatal("fleet should contain an error binding for the empty-host proxmox profile")
	}
}

// TestBuildFleet_NoNetworkIO confirms BuildFleet never round-trips the
// network even for an unreachable Proxmox host: construction only reads the
// token file from disk (an allowed, deliberate exception — see NewProxmox's
// doc comment), so building the fleet against a host nothing answers on must
// still return promptly with a usable (if later-unreachable) binding.
// Reachability is Preflight's job, never BuildFleet's.
func TestBuildFleet_NoNetworkIO(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tokenFile := proxmoxTokenFile(t)

	store := newTestStore(t, profiles.Profile{
		Name: "unreachable", Type: profiles.TypeProxmox, Enabled: true,
		// A non-routable TEST-NET-1 address (RFC 5737): connecting to it
		// would hang or time out, so a fast return here proves no connection
		// was attempted.
		Host: "192.0.2.1", Node: "pve1", Pool: "sandbar", TokenFile: tokenFile,
	})

	start := time.Now()
	fleet := BuildFleet(store)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("BuildFleet() against an unreachable host took %s, want near-instant (construction must not touch the network)", elapsed)
	}

	var sawProxmox bool
	for _, b := range fleet {
		if b.Profile.Type != profiles.TypeProxmox {
			continue
		}
		sawProxmox = true
		if b.Err != nil {
			t.Fatalf("proxmox binding Err = %v, want nil (construction alone must succeed against an unreachable host)", b.Err)
		}
		if b.Prov == nil {
			t.Fatal("proxmox binding Prov is nil")
		}
	}
	if !sawProxmox {
		t.Fatal("fleet should contain a proxmox binding")
	}
}
