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
	return lima.AttachArgv(v.Name, lima.GuestHome(v.Dir))
}

// TestShellAttachArgvNotRunning verifies task 3's central refusal: a VM that
// exists but is not Running must produce a clear, actionable message quoting
// its actual status — not a raw limactl error and not a silent attempt to
// attach to a stopped VM.
func TestShellAttachArgvNotRunning(t *testing.T) {
	l := stubVMLister{vms: []vm.VM{{Name: "foo", Status: "Stopped", Dir: "/tmp/foo"}}}

	_, err := shellAttachArgv(l, "foo")
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

	_, err := shellAttachArgv(l, "missing")
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

	_, err := shellAttachArgv(l, "foo")
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("shellAttachArgv error = %v, want it to wrap %v", err, wantErr)
	}
}

// TestShellAttachArgvRunning verifies the happy path returns a non-empty argv
// built through lima.AttachArgv (task 2's builder) rather than constructing
// its own guest-attach command.
func TestShellAttachArgvRunning(t *testing.T) {
	l := stubVMLister{vms: []vm.VM{{Name: "foo", Status: "Running", Dir: "/nonexistent/instance/dir"}}}

	argv, err := shellAttachArgv(l, "foo")
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
