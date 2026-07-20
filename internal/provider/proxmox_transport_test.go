package provider

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
)

// proxmox_transport_test.go covers the guest-path/identity helpers and the
// remaining transport surface that the lifecycle tests do not reach directly:
// the status-vocabulary mapping the UI branches on, the guest home/user derived
// from the cloud-init user, GuestPath's resolved-vs-deferred forms, and a
// streaming Copy over the recorded ssh transport.

func TestProxmoxLimaStatusMapping(t *testing.T) {
	cases := []struct{ in, want string }{
		{"running", "Running"},
		{"stopped", "Stopped"},
		{"", ""},
		{"paused", "Paused"}, // unfamiliar state: capitalised, not forced
		{"prelaunch", "Prelaunch"},
	}
	for _, c := range cases {
		if got := limaStatus(c.in); got != c.want {
			t.Errorf("limaStatus(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestProxmoxByteString(t *testing.T) {
	if got := byteString(0); got != "" {
		t.Errorf("byteString(0) = %q; want empty (unknown, not a VM with no memory)", got)
	}
	if got := byteString(-5); got != "" {
		t.Errorf("byteString(negative) = %q; want empty", got)
	}
	if got := byteString(2048); got != "2048" {
		t.Errorf("byteString(2048) = %q; want the decimal string", got)
	}
}

func TestProxmoxGuestHomeAndUser(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m, func(c *TargetConfig) { c.User = "dev" })
	if got := p.GuestHome(vm.VM{}); got != "/home/dev" {
		t.Errorf("GuestHome = %q; want /home/dev", got)
	}
	if got := p.GuestUser(vm.VM{}); got != "dev" {
		t.Errorf("GuestUser = %q; want dev", got)
	}
	if got := p.HostUser(); got != "dev" {
		t.Errorf("HostUser = %q; want the guest login user dev", got)
	}
}

func TestProxmoxGuestPathResolvedVsDeferred(t *testing.T) {
	_, p := withGuest(t) // caches web -> 192.168.1.50

	// A cached address yields scp's own user@host:path form.
	if got := p.GuestPath("web", "/home/dev/x"); got != "dev@192.168.1.50:/home/dev/x" {
		t.Errorf("GuestPath(cached) = %q; want dev@192.168.1.50:/home/dev/x", got)
	}
	// An unknown VM yields the deferred name:path form for Copy to resolve.
	if got := p.GuestPath("api", "/home/dev/y"); got != "api:/home/dev/y" {
		t.Errorf("GuestPath(unknown) = %q; want the deferred api:/home/dev/y", got)
	}
}

func TestProxmoxCopyRunsSCPToTheGuest(t *testing.T) {
	_, p := withGuest(t)
	argvs := recordSSH(p)

	var out bytes.Buffer
	if err := p.Copy(context.Background(), &out, false, p.GuestPath("web", "/home/dev/f"), "/tmp/f"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if len(*argvs) == 0 {
		t.Fatal("Copy issued no scp command")
	}
	joined := strings.Join((*argvs)[0], " ")
	if !strings.Contains(joined, "dev@192.168.1.50:/home/dev/f") {
		t.Errorf("scp argv %q does not carry the resolved guest source", joined)
	}
}

// TestProxmoxShellStreamOutFoldsStderr covers the binary-safe stdout path: on
// failure, stderr is folded into the error rather than into the payload stream.
func TestProxmoxShellStreamOutFoldsStderr(t *testing.T) {
	_, p := withGuest(t)
	// A transport that writes a byte to stdout, a diagnostic to stderr, and fails.
	p.runSSH = func(_ context.Context, _ []string, _ io.Reader, stdout, stderr io.Writer) error {
		_, _ = stdout.Write([]byte{0x00})
		_, _ = io.WriteString(stderr, "disk warning")
		return errAfterOutput{}
	}
	var out bytes.Buffer
	err := p.ShellStreamOut(context.Background(), "web", nil, &out, "tar", "-czf", "-", ".")
	if err == nil {
		t.Fatal("ShellStreamOut returned nil on a failing command")
	}
	if !strings.Contains(err.Error(), "disk warning") {
		t.Errorf("error %q does not carry the folded stderr", err)
	}
	if out.String() != "\x00" {
		t.Errorf("stdout payload = %q; stderr must NOT be mixed into it", out.String())
	}
}

// TestProxmoxShellInteractive covers the empty-argv (interactive login shell)
// form of Shell, which merges stdout and stderr onto one writer for display.
func TestProxmoxShellInteractive(t *testing.T) {
	_, p := withGuest(t)
	argvs := recordSSH(p)
	if err := p.Shell(context.Background(), "web", nil, &bytes.Buffer{}); err != nil {
		t.Fatalf("Shell (interactive): %v", err)
	}
	if len(*argvs) != 1 {
		t.Fatalf("ran %d commands; want 1", len(*argvs))
	}
	if !strings.Contains(strings.Join((*argvs)[0], " "), "dev@192.168.1.50") {
		t.Errorf("interactive shell argv missing the guest target: %v", (*argvs)[0])
	}
}

// errAfterOutput is a stand-in for an ssh exit error that arrives after the
// command has already written some output.
type errAfterOutput struct{}

func (errAfterOutput) Error() string { return "exit status 2" }
