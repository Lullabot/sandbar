package provider_test

import (
	"context"
	"io"
	"reflect"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"
)

// var _ provider.Provider = ... is the DoD's compile-time assertion, in an
// external test package so it can name provider.Provider explicitly (local.go
// carries the in-package (*limaProvider)(nil) form). NewLocalLima's own Provider
// return type is the load-bearing check; this restates it at the seam boundary.
var _ provider.Provider = provider.NewLocalLima(nil, nil)

// fakeRunner records the argv of every call and returns canned bytes/errors, so
// the local provider is exercised over a FAKE host-access/runner seam and never
// spawns a real limactl (AGENTS.md, hard rule). It mirrors the fakeRunner style
// in internal/lima/*_test.go and implements lima.Runner. Outputs are keyed by
// the first argument.
type fakeRunner struct {
	calls   [][]string
	outputs map[string][]byte
	err     error
}

func (f *fakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	return f.outputs[args[0]], f.err
}

func (f *fakeRunner) Stream(_ context.Context, _ io.Reader, _ io.Writer, args ...string) error {
	f.calls = append(f.calls, args)
	return f.err
}

func (f *fakeRunner) StreamOut(_ context.Context, _ io.Reader, _ io.Writer, args ...string) error {
	f.calls = append(f.calls, args)
	return f.err
}

// newLocal builds the local Lima provider over a fake runner and returns both so
// a test can drive the provider and inspect the resulting limactl argv. The
// provisioner shares the same core, exactly as sand wires it (main.go).
func newLocal(f *fakeRunner) provider.Provider {
	core := lima.New(f)
	return provider.NewLocalLima(core, &provision.Provisioner{Lima: core})
}

// TestLocalProviderArgv is the meaningful test: it proves the local provider's
// discovery / power / transport methods delegate to the lima core and produce
// the exact limactl argv the concrete client produced before the seam existed —
// so wrapping *lima.Client behind Provider changed no observable command.
func TestLocalProviderArgv(t *testing.T) {
	// A one-line valid `limactl list --format json` payload so List/Get parse and
	// return instead of erroring on empty output.
	listJSON := []byte(`{"name":"web","status":"Running","cpus":4,"memory":42,"disk":99,"dir":"/home/u/.lima/web"}` + "\n")

	cases := []struct {
		name string
		call func(provider.Provider)
		want []string
	}{
		{"list", func(p provider.Provider) { _, _ = p.List() }, []string{"list", "--format", "json"}},
		{"get", func(p provider.Provider) { _, _ = p.Get("web") }, []string{"list", "web", "--format", "json"}},
		{"status", func(p provider.Provider) { _, _ = p.Status("web") }, []string{"list", "web", "--format", "{{.Status}}"}},
		{"start", func(p provider.Provider) { _ = p.Start("web") }, []string{"start", "web"}},
		{"stop", func(p provider.Provider) { _ = p.Stop("web") }, []string{"stop", "web"}},
		{"delete", func(p provider.Provider) { _ = p.Delete("web", false) }, []string{"delete", "web"}},
		{"delete-force", func(p provider.Provider) { _ = p.Delete("web", true) }, []string{"delete", "web", "-f"}},
		{"start-streaming", func(p provider.Provider) { _ = p.StartStreaming(context.Background(), "web", io.Discard) }, []string{"start", "web"}},
		{"stop-streaming", func(p provider.Provider) { _ = p.StopStreaming(context.Background(), "web", io.Discard) }, []string{"stop", "web"}},
		// exec-merged
		{"shell", func(p provider.Provider) { _ = p.Shell(context.Background(), "web", nil, io.Discard, "ls", "-la") }, []string{"shell", "web", "ls", "-la"}},
		// exec-stdout-only
		{"shell-stream-out", func(p provider.Provider) {
			_ = p.ShellStreamOut(context.Background(), "web", nil, io.Discard, "tar", "-c")
		}, []string{"shell", "web", "tar", "-c"}},
		// exec-with-captured-stderr
		{"shell-out", func(p provider.Provider) { _, _ = p.ShellOut(context.Background(), "web", "getent", "passwd", "u") }, []string{"shell", "web", "getent", "passwd", "u"}},
		// copy pins --backend=scp; a recursive download inserts -r and GuestPath
		// prefixes the instance with "<vm>:".
		{"copy-upload", func(p provider.Provider) {
			_ = p.Copy(context.Background(), io.Discard, false, "/host/file.txt", p.GuestPath("web", "/home/u/dir"))
		}, []string{"copy", "-v", "--backend=scp", "/host/file.txt", "web:/home/u/dir"}},
		{"copy-download-recursive", func(p provider.Provider) {
			_ = p.Copy(context.Background(), io.Discard, true, p.GuestPath("web", "/home/u/src"), "/host/dst")
		}, []string{"copy", "-v", "--backend=scp", "-r", "web:/home/u/src", "/host/dst"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{outputs: map[string][]byte{"list": listJSON}}
			p := newLocal(f)
			tc.call(p)
			if len(f.calls) != 1 {
				t.Fatalf("got %d calls, want 1: %v", len(f.calls), f.calls)
			}
			if got := f.calls[0]; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("argv = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLocalProviderPreflightArgv checks the two-probe preflight (limactl exists
// and supports `clone`) delegates unchanged.
func TestLocalProviderPreflightArgv(t *testing.T) {
	f := &fakeRunner{outputs: map[string][]byte{}}
	if err := newLocal(f).Preflight(); err != nil {
		t.Fatalf("Preflight() error: %v", err)
	}
	want := [][]string{{"--version"}, {"clone", "--help"}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("Preflight argv = %v, want %v", f.calls, want)
	}
}

// TestLocalProviderGuestPath pins the guest endpoint form the provider produces,
// so no consumer hand-builds "<vm>:<path>".
func TestLocalProviderGuestPath(t *testing.T) {
	p := newLocal(&fakeRunner{outputs: map[string][]byte{}})
	if got, want := p.GuestPath("web", "/tmp/x"), "web:/tmp/x"; got != want {
		t.Fatalf("GuestPath = %q, want %q", got, want)
	}
}

// TestLocalProviderGuestIdentityFallback pins the documented empty-Dir fallback:
// with no instance dir there are no Lima instance files to read, so GuestHome and
// GuestUser return "" and the caller falls back rather than getting a guess. This
// exercises the provider's guest-identity delegation without needing real Lima
// files on disk.
func TestLocalProviderGuestIdentityFallback(t *testing.T) {
	p := newLocal(&fakeRunner{outputs: map[string][]byte{}})
	if got := p.GuestHome(vm.VM{Name: "web"}); got != "" {
		t.Fatalf("GuestHome(empty Dir) = %q, want \"\"", got)
	}
	if got := p.GuestUser(vm.VM{Name: "web"}); got != "" {
		t.Fatalf("GuestUser(empty Dir) = %q, want \"\"", got)
	}
}

// TestLocalProviderAttachArgv proves the provider produces the tmux-aware attach
// argv itself. With an empty Dir the guest home cannot be read, so --workdir is
// omitted (the documented fallback) and the argv is the bare
// `limactl shell <name> bash -c <expr>`. This exercises the AttachArgv seam
// without needing real Lima instance files.
func TestLocalProviderAttachArgv(t *testing.T) {
	p := newLocal(&fakeRunner{outputs: map[string][]byte{}})
	got := p.AttachArgv(vm.VM{Name: "web"})
	// Must be limactl shell … bash -c <expr>, with NO --workdir (Dir empty).
	if len(got) < 5 || got[0] != "limactl" || got[1] != "shell" || got[2] != "web" || got[3] != "bash" || got[4] != "-c" {
		t.Fatalf("AttachArgv = %v, want limactl shell web bash -c <expr> with no --workdir", got)
	}
	for _, a := range got {
		if a == "--workdir" {
			t.Fatalf("AttachArgv unexpectedly emitted --workdir with an empty guest home: %v", got)
		}
	}
	// The provider must NOT run limactl to build the argv (it is pure).
	// (No fakeRunner call is asserted because AttachArgv performs no exec.)
}
