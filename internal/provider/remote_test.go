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
// attach argv (`ssh -t [mux flags] dev@example.com limactl shell <name> bash -c
// <expr>`) so `sand shell` and the TUI `S` verb get the remote form with zero
// drift. With an empty Dir the guest home cannot be read (no remote round trip),
// so --workdir is omitted, exactly mirroring the local provider's documented
// fallback. The guest tmux expression is preserved byte-for-byte.
//
// The target is located by value rather than by a fixed index: NewSSHHost may
// thread OpenSSH connection-multiplexing flags (-o ControlMaster=... etc, see
// internal/lima/sshhost.go) in between `-t` and the target, and this
// black-box test has no access to SSHHost's unexported controlDir to predict
// whether it did.
func TestRemoteProviderAttachArgv(t *testing.T) {
	p := newRemote(t)
	got := p.AttachArgv(vm.VM{Name: "web"})

	if len(got) < 2 || got[0] != "ssh" || got[1] != "-t" {
		t.Fatalf("remote AttachArgv must start `ssh -t …`, got %v", got)
	}
	idx := slices.Index(got, "dev@example.com")
	if idx < 0 {
		t.Fatalf("remote AttachArgv missing the ssh target dev@example.com: %v", got)
	}
	wantTail := []string{"limactl", "shell", "web", "bash", "-c"}
	if idx+1+len(wantTail) > len(got) || !slices.Equal(got[idx+1:idx+1+len(wantTail)], wantTail) {
		t.Fatalf("remote AttachArgv after the target = %v\nwant %v", got[idx+1:], wantTail)
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

// TestRemoteProviderForwardArgv proves the remote provider bridges the
// workstation to the REMOTE host's loopback with `ssh -N -L
// <hostPort>:127.0.0.1:<guestPort> …`: -N because the caller execs this as a
// long-running child solely to forward, and the far end is 127.0.0.1 because
// that is where Lima's own auto-forward already landed the guest port on the
// remote host (see the local provider's ForwardArgv doc for why local Lima
// needs no such hop at all). The target is located by value, exactly as
// TestRemoteProviderAttachArgv does, since NewSSHHost may thread
// connection-multiplexing flags this black-box test cannot predict.
func TestRemoteProviderForwardArgv(t *testing.T) {
	p := newRemote(t)
	got := p.ForwardArgv(vm.VM{Name: "web"}, 8080, 3000)
	t.Logf("remote ForwardArgv(web, 8080, 3000) = %v", got)

	if len(got) == 0 || got[0] != "ssh" {
		t.Fatalf("remote ForwardArgv must start with `ssh`, got %v", got)
	}
	if !slices.Contains(got, "-N") {
		t.Fatalf("remote ForwardArgv missing -N (no remote command): %v", got)
	}
	idx := slices.Index(got, "-L")
	if idx < 0 || idx+1 >= len(got) || got[idx+1] != "8080:127.0.0.1:3000" {
		t.Fatalf("remote ForwardArgv missing `-L 8080:127.0.0.1:3000`: %v", got)
	}
	if got[len(got)-1] != "dev@example.com" {
		t.Fatalf("remote ForwardArgv must target dev@example.com, got %v", got)
	}
	// A port forward must never ride (or become) the shared ControlMaster — see
	// WithoutMux — so it carries the explicit opt-out, not ControlMaster=auto.
	if slices.Contains(got, "ControlMaster=auto") {
		t.Fatalf("remote ForwardArgv must not multiplex: %v", got)
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
