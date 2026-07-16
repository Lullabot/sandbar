package provider_test

import (
	"testing"

	"github.com/lullabot/sandbar/internal/provider"
)

// TestNewDefaultReturnsAUsableLocalProvider is the central regression
// tripwire for the centralised constructor: every one of the three sand
// entrypoints (cmd/sand/main.go, create.go, shell.go) now calls
// provider.NewDefault instead of separately wiring lima.New +
// provision.Provisioner, so a break here would break all three at once. It
// asserts NewDefault succeeds and hands back a non-nil Provider — the part of
// its contract testable without a real limactl (Preflight, and every VM
// lifecycle call, still need one — see cmd/sand's limae2e suite for that).
func TestNewDefaultReturnsAUsableLocalProvider(t *testing.T) {
	p, err := provider.NewDefault()
	if err != nil {
		t.Fatalf("NewDefault() error = %v, want nil", err)
	}
	if p == nil {
		t.Fatal("NewDefault() returned a nil Provider")
	}
}
