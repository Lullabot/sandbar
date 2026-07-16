package provider

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/registry"
)

// TestTargetConfig_Scope covers the pure identity-derivation logic BuildFleet
// (fleet.go) relies on: local scope for an empty/local Provider, and a
// stable, secret-free remote scope (never carrying IdentityPath or any key
// material) for a remote target. Fleet-construction behaviour itself
// (BuildFleet, the local/remote/error-binding cases) is covered by
// fleet_test.go.
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
