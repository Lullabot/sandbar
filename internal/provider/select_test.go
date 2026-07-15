package provider

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
)

// clearSelectionEnv resets every provider-selection env var this package
// reads, via t.Setenv (which t auto-restores after the test), so a test can
// set only the variables it cares about without inheriting whatever the
// developer's real shell — or an earlier test in this same process — left
// behind.
func clearSelectionEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		SandProviderEnv,
		SandRemoteHostEnv,
		SandRemoteUserEnv,
		SandRemotePortEnv,
		SandRemoteIdentityEnv,
		SandRemoteLimaHomeEnv,
	} {
		t.Setenv(name, "")
	}
}

// TestResolve_UnconfiguredIsLocal is the zero-config regression this task's
// selection surface must never break: an unconfigured environment must
// resolve to exactly what NewDefault returns, paired with registry.LocalScope
// — an unconfigured `sand` behaves exactly as it does today.
func TestResolve_UnconfiguredIsLocal(t *testing.T) {
	clearSelectionEnv(t)

	p, scope, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}
	if p == nil {
		t.Fatal("Resolve() returned a nil Provider for the unconfigured (local) path")
	}
	if scope != registry.LocalScope {
		t.Fatalf("Resolve() scope = %+v, want registry.LocalScope (%+v)", scope, registry.LocalScope)
	}
}

// TestResolve_ExplicitLocalIsLocal: SAND_PROVIDER=lima is the explicit,
// spelled-out form of the default and must behave identically to leaving it
// unset.
func TestResolve_ExplicitLocalIsLocal(t *testing.T) {
	clearSelectionEnv(t)
	t.Setenv(SandProviderEnv, registry.LocalProviderID)

	p, scope, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}
	if p == nil {
		t.Fatal("Resolve() returned a nil Provider")
	}
	if scope != registry.LocalScope {
		t.Fatalf("Resolve() scope = %+v, want registry.LocalScope", scope)
	}
}

// TestResolve_RemoteWithoutHostErrors: selecting the remote provider without a
// host is a configuration mistake, and must be reported as one rather than
// silently falling back to local (which would run the wrong provider without
// telling the user).
func TestResolve_RemoteWithoutHostErrors(t *testing.T) {
	clearSelectionEnv(t)
	t.Setenv(SandProviderEnv, RemoteLimaProviderID)

	p, scope, err := Resolve()
	if err == nil {
		t.Fatal("Resolve() with lima-remote and no host: want an error, got nil")
	}
	if p != nil {
		t.Fatalf("Resolve() should not return a Provider on error, got %v", p)
	}
	if scope != (registry.Scope{}) {
		t.Fatalf("Resolve() should not return a populated Scope on error, got %+v", scope)
	}
	if !strings.Contains(err.Error(), SandRemoteHostEnv) {
		t.Fatalf("error should name %s, got: %v", SandRemoteHostEnv, err)
	}
}

// TestResolve_RemoteResolvesToWorkingProvider: with a host supplied, selecting
// the remote provider now CONSTRUCTS the remote-Lima-over-SSH backend (plan 15
// task 5) and pairs it with the remote registry.Scope its VMs are tagged with —
// so a remote host's instances are owned by the right backend and never mix with
// the local list. Constructing the provider does not connect (the SSH command
// only runs when a method is invoked), so this needs no remote host.
//
// It restores provision's host-access seam afterwards because NewRemoteLima
// points that process-global seam at the remote host (see provision.SetHostFiles):
// the serial suite must not leak a remote seam into a later local test.
func TestResolve_RemoteResolvesToWorkingProvider(t *testing.T) {
	t.Cleanup(func() { provision.SetHostFiles(lima.LocalFiles()) })
	clearSelectionEnv(t)
	t.Setenv(SandProviderEnv, RemoteLimaProviderID)
	t.Setenv(SandRemoteHostEnv, "example.com")
	t.Setenv(SandRemoteUserEnv, "dev")
	t.Setenv(SandRemotePortEnv, "2222")

	p, scope, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}
	if p == nil {
		t.Fatal("Resolve() returned a nil Provider for the remote path")
	}
	want := registry.Scope{Provider: RemoteLimaProviderID, RemoteTarget: "dev@example.com:2222"}
	if scope != want {
		t.Fatalf("Resolve() scope = %+v, want %+v", scope, want)
	}
}

// TestResolve_UnknownProviderErrors guards against a typo in SAND_PROVIDER
// silently falling back to local Lima instead of being reported.
func TestResolve_UnknownProviderErrors(t *testing.T) {
	clearSelectionEnv(t)
	t.Setenv(SandProviderEnv, "not-a-real-provider")

	if _, _, err := Resolve(); err == nil {
		t.Fatal("Resolve() with an unknown SAND_PROVIDER value: want an error, got nil")
	}
}

// TestResolve_InvalidPortErrors: a non-numeric SAND_REMOTE_PORT must be
// reported clearly rather than panicking or being silently ignored.
func TestResolve_InvalidPortErrors(t *testing.T) {
	clearSelectionEnv(t)
	t.Setenv(SandRemotePortEnv, "not-a-port")

	_, _, err := Resolve()
	if err == nil {
		t.Fatal("Resolve() with an invalid SAND_REMOTE_PORT: want an error, got nil")
	}
	if !strings.Contains(err.Error(), SandRemotePortEnv) {
		t.Fatalf("error should name %s, got: %v", SandRemotePortEnv, err)
	}
}

// TestTargetConfig_Scope covers the pure identity-derivation logic Resolve
// relies on: local scope for an empty/local Provider, and a stable,
// secret-free remote scope (never carrying IdentityPath or any key material)
// for a remote target.
func TestTargetConfig_Scope(t *testing.T) {
	if got := (TargetConfig{}).Scope(); got != registry.LocalScope {
		t.Fatalf("zero-value TargetConfig.Scope() = %+v, want registry.LocalScope", got)
	}
	if got := (TargetConfig{Provider: registry.LocalProviderID}).Scope(); got != registry.LocalScope {
		t.Fatalf("explicit local TargetConfig.Scope() = %+v, want registry.LocalScope", got)
	}

	remote := TargetConfig{
		Provider:     RemoteLimaProviderID,
		Host:         "example.com",
		User:         "dev",
		Port:         2222,
		IdentityPath: "/home/dev/.ssh/id_ed25519",
	}
	scope := remote.Scope()
	if scope.Provider != RemoteLimaProviderID {
		t.Fatalf("remote scope Provider = %q, want %q", scope.Provider, RemoteLimaProviderID)
	}
	wantTarget := "dev@example.com:2222"
	if scope.RemoteTarget != wantTarget {
		t.Fatalf("remote scope RemoteTarget = %q, want %q", scope.RemoteTarget, wantTarget)
	}
	if strings.Contains(scope.RemoteTarget, remote.IdentityPath) {
		t.Fatalf("remote scope must never carry the identity path, got %+v", scope)
	}
}
