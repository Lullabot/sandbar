//go:build limae2e

// forward_e2e_test.go closes the one gap the review feature shipped with: every
// other test of Provider.ForwardArgv asserts the SHAPE of the argv it returns
// and stops there, so nothing ever ran that argv through the real ssh binary
// against a real target. The forwarding seam is what makes `sand land --review`
// reachable on the remote-Lima and Proxmox backends, and an argv that is
// well-formed but wrong — a missing -N, a mis-ordered -L, an option the local
// ssh rejects — would pass every unit test and fail on contact.
//
// It needs no VM. The remote provider's ForwardArgv bridges
// workstation:hostPort -> the REMOTE HOST's 127.0.0.1:guestPort, because that
// is where Lima's own auto-forward has already landed the guest port (see
// remote.go's ForwardArgv doc). Anything listening on the remote's loopback
// therefore stands in for the guest server exactly — and the remote's own sshd
// is guaranteed to be one, since this suite just connected to it. So the tunnel
// is proven end to end against a genuine remote with no guest boot, no extra
// package on the far side, and seconds rather than minutes of runtime.
//
// Gated identically to remote_e2e_test.go: the `limae2e` build tag plus
// LIMA_REMOTE_E2E=1 and a reachable target, skipping cleanly otherwise. It
// shares that file's helpers (skipUnlessRemoteE2EConfigured, remoteE2EPortEnv),
// so both run in the same `-run TestE2E ./internal/provider/` invocation the
// remote-lima-e2e CI job already makes.
package provider_test

import (
	"bufio"
	"context"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/vm"
)

// freeLocalPort returns a port free on this machine, closing the listener
// before returning it — the same inherently racy "pick a port" that
// landreview.freePort does, and for the same reason: ssh must do the binding
// itself, so the port has to be released first.
func freeLocalPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a local port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}
	return port
}

// remoteSSHPort is the port the remote's own sshd answers on — the stand-in
// listener this test forwards to.
func remoteSSHPort(cfg provider.TargetConfig) int {
	if cfg.Port > 0 {
		return cfg.Port
	}
	return 22
}

// startForward runs a ForwardArgv command as a child, exactly as
// landreview.Session does in production, and returns it plus a buffer of its
// stderr. The caller is responsible for killing it.
func startForward(t *testing.T, ctx context.Context, argv []string) (*exec.Cmd, *strings.Builder) {
	t.Helper()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var errOut strings.Builder
	cmd.Stderr = &errOut
	cmd.Stdout = &errOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the forwarder %v: %v", argv, err)
	}
	return cmd, &errOut
}

// TestE2ERemoteForwardArgvCarriesTraffic is the assertion the unit tests cannot
// make: that the argv ForwardArgv builds, run through the real ssh binary
// against a real target, actually moves bytes.
func TestE2ERemoteForwardArgvCarriesTraffic(t *testing.T) {
	cfg := skipUnlessRemoteE2EConfigured(t)

	p, err := provider.NewRemoteLima(cfg)
	if err != nil {
		t.Fatalf("NewRemoteLima: %v", err)
	}

	hostPort := freeLocalPort(t)
	guestPort := remoteSSHPort(cfg)

	argv := p.ForwardArgv(vm.VM{Name: "forward-e2e"}, hostPort, guestPort)
	if len(argv) == 0 {
		t.Fatal("remote ForwardArgv returned nothing: the remote backend is NOT already reachable, so it must return a real command (nil means 'no tunnel needed', which is only true for local Lima)")
	}
	t.Logf("ForwardArgv = %v", argv)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, errOut := startForward(t, ctx, argv)
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Poll rather than sleep: the ssh handshake is the variable part.
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort))
	var conn net.Conn
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			t.Fatalf("the forwarder exited before the tunnel came up:\n%s", errOut.String())
		}
		c, dialErr := net.DialTimeout("tcp", addr, 2*time.Second)
		if dialErr == nil {
			conn = c
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("nothing accepted a connection on %s within 30s — the forward never came up:\n%s", addr, errOut.String())
	}
	defer conn.Close()

	// The far end is the remote's sshd, so the first thing it sends is its
	// version banner. Reading it proves bytes travelled the whole path —
	// workstation loopback, through the ssh child, out to the remote host, and
	// back from a listener on the REMOTE's loopback — rather than merely that
	// something accepted a TCP connection locally.
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	banner, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read from the tunnelled connection: %v (forwarder output:\n%s)", err, errOut.String())
	}
	if !strings.HasPrefix(banner, "SSH-") {
		t.Fatalf("tunnelled read returned %q, want the remote sshd's SSH- banner — the -L mapping did not reach the remote's loopback", strings.TrimSpace(banner))
	}
	t.Logf("tunnelled banner from the remote's loopback: %s", strings.TrimSpace(banner))
}

// TestE2ERemoteForwardArgvExitsWhenTheLocalBindFails covers the failure mode
// that made a collision dangerous rather than merely annoying.
//
// freePort releases its port before the forward binds it, so another process
// can take it in the gap. Without -o ExitOnForwardFailure=yes, ssh treats a
// failed LOCAL bind as a warning: the child keeps running, landreview's
// readiness probe — which accepts ANY HTTP response, by design — succeeds
// against whatever else holds that port, and the reviewer's browser opens onto
// an unrelated application while Run blocks forever waiting for a review that
// can never be submitted. With the option, ssh exits non-zero and the session
// reports a real failure instead.
func TestE2ERemoteForwardArgvExitsWhenTheLocalBindFails(t *testing.T) {
	cfg := skipUnlessRemoteE2EConfigured(t)

	p, err := provider.NewRemoteLima(cfg)
	if err != nil {
		t.Fatalf("NewRemoteLima: %v", err)
	}

	// Hold the port for the whole test, so ssh cannot possibly bind it.
	squatter, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy a local port: %v", err)
	}
	defer squatter.Close()
	hostPort := squatter.Addr().(*net.TCPAddr).Port

	argv := p.ForwardArgv(vm.VM{Name: "forward-e2e"}, hostPort, remoteSSHPort(cfg))
	if len(argv) == 0 {
		t.Fatal("remote ForwardArgv returned nothing")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, errOut := startForward(t, ctx, argv)

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	select {
	case err := <-waitErr:
		if err == nil {
			t.Fatalf("the forwarder exited 0 despite being unable to bind %d; it must fail loudly:\n%s", hostPort, errOut.String())
		}
		t.Logf("forwarder exited as required: %v\n%s", err, errOut.String())
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("the forwarder was still running 30s after failing to bind %d — without ExitOnForwardFailure=yes a port collision leaves a live child and a readiness probe that can succeed against an unrelated local service:\n%s", hostPort, errOut.String())
	}
}

// TestE2ERemoteForwardArgvBindsLoopbackOnly asserts on the wire the security
// property internal/landreview's package doc commits to: an unfinished review
// must never be reachable from anywhere but the workstation's own loopback. ssh
// binds -L forwards to loopback unless GatewayPorts/-g says otherwise, and this
// proves ForwardArgv does not say otherwise.
func TestE2ERemoteForwardArgvBindsLoopbackOnly(t *testing.T) {
	cfg := skipUnlessRemoteE2EConfigured(t)

	p, err := provider.NewRemoteLima(cfg)
	if err != nil {
		t.Fatalf("NewRemoteLima: %v", err)
	}

	hostPort := freeLocalPort(t)
	argv := p.ForwardArgv(vm.VM{Name: "forward-e2e"}, hostPort, remoteSSHPort(cfg))
	if len(argv) == 0 {
		t.Fatal("remote ForwardArgv returned nothing")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, errOut := startForward(t, ctx, argv)
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	loopback := net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort))
	deadline := time.Now().Add(30 * time.Second)
	var up bool
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", loopback, 2*time.Second); err == nil {
			_ = c.Close()
			up = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !up {
		t.Fatalf("the forward never came up on %s:\n%s", loopback, errOut.String())
	}

	// Every non-loopback address this host has must refuse the connection.
	// (A remote attacker's view; nothing here needs to be reachable off-box.)
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("enumerate interface addresses: %v", err)
	}
	var checked int
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		checked++
		target := net.JoinHostPort(ipnet.IP.String(), strconv.Itoa(hostPort))
		c, err := net.DialTimeout("tcp", target, 2*time.Second)
		if err == nil {
			_ = c.Close()
			t.Errorf("the review forward accepted a connection on %s — it must bind the workstation's loopback ONLY, or unreviewed work is reachable from the network", target)
		}
	}
	if checked == 0 {
		t.Log("no non-loopback IPv4 interface to probe; loopback reachability confirmed only")
	}
}
