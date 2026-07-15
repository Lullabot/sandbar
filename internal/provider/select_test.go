package provider

import (
	"errors"
	"strings"
	"testing"

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

// TestResolve_RemoteNotImplemented: with a host supplied, selecting the
// remote provider must fail with ErrRemoteNotImplemented — the seam plan 15
// task 5 fills in — rather than building anything or silently using local
// Lima instead.
func TestResolve_RemoteNotImplemented(t *testing.T) {
	clearSelectionEnv(t)
	t.Setenv(SandProviderEnv, RemoteLimaProviderID)
	t.Setenv(SandRemoteHostEnv, "example.com")

	p, _, err := Resolve()
	if p != nil {
		t.Fatalf("Resolve() should not return a Provider for the unimplemented remote path, got %v", p)
	}
	if !errors.Is(err, ErrRemoteNotImplemented) {
		t.Fatalf("Resolve() error = %v, want ErrRemoteNotImplemented", err)
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
