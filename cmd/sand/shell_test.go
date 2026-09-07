package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

// stubVMLister is a no-limactl vmGetter double: it answers from a fixed instance
// list so shellAttachArgv's decision logic (not-running message, unknown-name
// message) can be exercised without a real limactl (AGENTS.md, hard rule).
type stubVMLister struct {
	vms []vm.VM
	err error
}

// Get mirrors the provider's Get: the named instance, or ErrNoSuchInstance.
func (s stubVMLister) Get(name string) (vm.VM, error) {
	if s.err != nil {
		return vm.VM{}, s.err
	}
	for _, v := range s.vms {
		if v.Name == name {
			return v, nil
		}
	}
	return vm.VM{}, fmt.Errorf("%w: %s", lima.ErrNoSuchInstance, name)
}

// AttachArgv mirrors the local provider's own AttachArgv (lima.AttachArgv +
// lima.GuestHome) closely enough for shellAttachArgv's tests: they only need
// a non-empty, limactl-shaped argv, never a real guest.
func (s stubVMLister) AttachArgv(v vm.VM) []string {
	return lima.AttachArgv(v.Name, lima.GuestHome(v.Dir), "")
}

// AttachArgvControl mirrors AttachArgv in tmux control mode, the same way the
// real providers do.
func (s stubVMLister) AttachArgvControl(v vm.VM) []string {
	return lima.AttachArgvMode(v.Name, lima.GuestHome(v.Dir), "", lima.AttachControl)
}

// TestShellAttachArgvNotRunning verifies the central refusal: a VM that
// exists but is not Running must produce a clear, actionable message quoting
// its actual status — not a raw limactl error and not a silent attempt to
// attach to a stopped VM.
func TestShellAttachArgvNotRunning(t *testing.T) {
	l := stubVMLister{vms: []vm.VM{{Name: "foo", Status: "Stopped", Dir: "/tmp/foo"}}}

	_, err := shellAttachArgv(l, "foo", false)
	if err == nil {
		t.Fatal("shellAttachArgv: want error for a stopped VM, got nil")
	}
	msg := err.Error()
	for _, want := range []string{`"foo"`, "not running", "Stopped", "start it first"} {
		if !strings.Contains(msg, want) {
			t.Errorf("shellAttachArgv error = %q, want it to contain %q", msg, want)
		}
	}
}

// TestShellAttachArgvUnknownInstance verifies an instance name that is not in
// the live list fails cleanly with a readable message rather than a stack
// trace or a raw exec error further down the line.
func TestShellAttachArgvUnknownInstance(t *testing.T) {
	l := stubVMLister{vms: []vm.VM{{Name: "other", Status: "Running"}}}

	_, err := shellAttachArgv(l, "missing", false)
	if err == nil {
		t.Fatal("shellAttachArgv: want error for an unknown instance, got nil")
	}
	if !strings.Contains(err.Error(), `"missing"`) {
		t.Errorf("shellAttachArgv error = %q, want it to name the missing instance", err.Error())
	}
}

// TestShellAttachArgvListError verifies a List failure (e.g. the raced-delete
// error List can surface) is wrapped and surfaced rather than silently
// swallowed or panicking on a nil slice.
func TestShellAttachArgvListError(t *testing.T) {
	wantErr := errors.New("boom")
	l := stubVMLister{err: wantErr}

	_, err := shellAttachArgv(l, "foo", false)
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("shellAttachArgv error = %v, want it to wrap %v", err, wantErr)
	}
}

// TestShellAttachArgvRunning verifies the happy path returns a non-empty argv
// built through lima.AttachArgv rather than constructing its own guest-attach
// command.
func TestShellAttachArgvRunning(t *testing.T) {
	l := stubVMLister{vms: []vm.VM{{Name: "foo", Status: "Running", Dir: "/nonexistent/instance/dir"}}}

	argv, err := shellAttachArgv(l, "foo", false)
	if err != nil {
		t.Fatalf("shellAttachArgv: unexpected error: %v", err)
	}
	if len(argv) == 0 || argv[0] != "limactl" {
		t.Fatalf("shellAttachArgv argv = %v, want it to start with limactl", argv)
	}
}

// TestRunShellRequiresExactlyOneName verifies the arg-count guard: missing (or
// extra) positional arguments must fail with a readable usage error rather
// than attempting to attach with a zero-value name.
func TestRunShellRequiresExactlyOneName(t *testing.T) {
	for _, args := range [][]string{{}, {"a", "b"}} {
		if err := runShell(args); err == nil {
			t.Errorf("runShell(%v): want error for wrong arg count, got nil", args)
		}
	}
}

// TestShellAttachArgvControlMode is the executable specification of `--cc`: the
// flag must reach the guest as a control-mode tmux client, and its absence must
// leave the argv exactly as it has always been.
func TestShellAttachArgvControlMode(t *testing.T) {
	t.Setenv("TMUX", "") // the refusal below is tested separately; here we are outside tmux
	l := stubVMLister{vms: []vm.VM{{Name: "web", Status: "Running", Dir: "/tmp/web"}}}

	cc, err := shellAttachArgv(l, "web", true)
	if err != nil {
		t.Fatalf("shellAttachArgv(--cc): unexpected error: %v", err)
	}
	// `-CC` rides directly after `tmux`, ahead of the clipboard commands the
	// attach expression also passes; what matters is that a new-session follows
	// it in the same invocation.
	got := strings.Join(cc, " ")
	if i := strings.Index(got, "tmux -CC "); i < 0 || !strings.Contains(got[i:], "new-session") {
		t.Errorf("--cc did not attach a control-mode client, so iTerm2 will draw a plain tmux instead of native tabs:\n\t%s", got)
	}

	plain, err := shellAttachArgv(l, "web", false)
	if err != nil {
		t.Fatalf("shellAttachArgv: unexpected error: %v", err)
	}
	if got := strings.Join(plain, " "); strings.Contains(got, "-CC") {
		t.Errorf("the DEFAULT attach became control mode. Every caller that did not ask for --cc must get the full-screen client:\n\t%s", got)
	}
}

// TestShellAttachArgvRefusesControlModeInsideTmux pins the refusal that exists
// because the combination cannot be made to work: a host tmux pane strips the
// DCS handshake control mode announces itself with, so the terminal outside
// never switches modes and the raw protocol scrolls past as gibberish.
//
// The message must name the way out, since "it doesn't work" is not something
// the user can act on.
func TestShellAttachArgvRefusesControlModeInsideTmux(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,7,0")
	l := stubVMLister{vms: []vm.VM{{Name: "web", Status: "Running", Dir: "/tmp/web"}}}

	_, err := shellAttachArgv(l, "web", true)
	if err == nil {
		t.Fatal("shellAttachArgv(--cc) inside tmux: want a refusal, got nil — this would print raw control protocol into the user's pane")
	}
	if !strings.Contains(err.Error(), "--cc") || !strings.Contains(err.Error(), "tmux") {
		t.Errorf("the refusal does not name the flag and the cause, so the user cannot act on it: %v", err)
	}

	// The SAME VM without --cc must still attach: the refusal is about the flag,
	// not about being inside tmux (where `S`'s fast path is the good case).
	if _, err := shellAttachArgv(l, "web", false); err != nil {
		t.Errorf("a plain attach from inside tmux was refused too: %v", err)
	}
}

// TestReorderShellFlagsHoistsCC covers --cc's one parsing hazard: it is a BOOL
// flag, so unlike --profile it must not consume the token after it. `sand shell
// --cc web` has to keep `web` as the VM name.
func TestReorderShellFlagsHoistsCC(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"trailing --cc", []string{"web", "--cc"}, []string{"--cc", "web"}},
		{"leading --cc keeps the name positional", []string{"--cc", "web"}, []string{"--cc", "web"}},
		{"single dash", []string{"web", "-cc"}, []string{"-cc", "web"}},
		{"explicit value", []string{"web", "--cc=true"}, []string{"--cc=true", "web"}},
		{"alongside --profile", []string{"web", "--cc", "--profile", "work"}, []string{"--cc", "--profile", "work", "web"}},
		{"--profile before --cc", []string{"--profile", "work", "web", "--cc"}, []string{"--profile", "work", "--cc", "web"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := reorderShellFlags(tc.args)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("reorderShellFlags(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}
