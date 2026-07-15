package provider_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/vm"
)

// newRemote builds the remote provider. NewRemoteLima's host-access handle
// lives on the Provisioner it constructs (Provisioner.HostFiles), not a
// process-global, so there is nothing here for the serial suite to leak
// between tests and nothing to restore afterwards.
func newRemote(t *testing.T) provider.Provider {
	t.Helper()
	p, err := provider.NewRemoteLima(provider.TargetConfig{
		Provider: provider.RemoteLimaProviderID,
		Host:     "example.com",
		User:     "dev",
	})
	if err != nil {
		t.Fatalf("NewRemoteLima: %v", err)
	}
	return p
}

// TestRemoteProviderAttachArgv proves the remote provider produces the ssh-wrapped
// attach argv (`ssh -t dev@example.com limactl shell <name> bash -c <expr>`) so
// `sand shell` and the TUI `S` verb get the remote form with zero drift. With an
// empty Dir the guest home cannot be read (no remote round trip), so --workdir is
// omitted, exactly mirroring the local provider's documented fallback. The guest
// tmux expression is preserved byte-for-byte.
func TestRemoteProviderAttachArgv(t *testing.T) {
	p := newRemote(t)
	got := p.AttachArgv(vm.VM{Name: "web"})

	wantHead := []string{"ssh", "-t", "dev@example.com", "limactl", "shell", "web", "bash", "-c"}
	if !slices.Equal(got[:len(wantHead)], wantHead) {
		t.Fatalf("remote AttachArgv head = %v\nwant %v", got[:len(wantHead)], wantHead)
	}
	if !strings.Contains(got[len(got)-1], "tmux") {
		t.Fatalf("last argv element should be the guest tmux expression, got %q", got[len(got)-1])
	}
	// No --workdir with an unknown guest home (empty Dir): passing it empty would
	// point limactl at nowhere — same fallback the local provider takes.
	if slices.Contains(got, "--workdir") {
		t.Fatalf("remote AttachArgv emitted --workdir with an empty guest home: %v", got)
	}
}

// TestRemoteProviderGuestIdentityFallback: with no instance dir there are no
// remote instance files to read, so GuestHome/GuestUser return "" and the caller
// falls back — the same contract the local provider honours, proven here without a
// remote host.
func TestRemoteProviderGuestIdentityFallback(t *testing.T) {
	p := newRemote(t)
	if got := p.GuestHome(vm.VM{Name: "web"}); got != "" {
		t.Fatalf("GuestHome(empty Dir) = %q, want \"\"", got)
	}
	if got := p.GuestUser(vm.VM{Name: "web"}); got != "" {
		t.Fatalf("GuestUser(empty Dir) = %q, want \"\"", got)
	}
}
