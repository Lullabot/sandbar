package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

// stubVMGetter is a no-limactl vmLookup double for pasteImageTarget's
// decision logic (unknown-name / not-running messages), mirroring
// shell_test.go's stubVMLister but narrowed to just Get since pasteImageTarget
// never needs AttachArgv.
type stubVMGetter struct {
	vms []vm.VM
	err error
}

func (s stubVMGetter) Get(name string) (vm.VM, error) {
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

// TestPasteImageTargetNotRunning verifies the Running guard: a VM that exists
// but is not Running must refuse cleanly with a message naming the VM and its
// actual status, mirroring shellAttachArgv's contract.
func TestPasteImageTargetNotRunning(t *testing.T) {
	l := stubVMGetter{vms: []vm.VM{{Name: "foo", Status: "Stopped"}}}

	_, err := pasteImageTarget(l, "foo")
	if err == nil {
		t.Fatal("pasteImageTarget: want error for a stopped VM, got nil")
	}
	msg := err.Error()
	for _, want := range []string{`"foo"`, "not running", "Stopped", "start it first"} {
		if !strings.Contains(msg, want) {
			t.Errorf("pasteImageTarget error = %q, want it to contain %q", msg, want)
		}
	}
}

// TestPasteImageTargetUnknownInstance verifies an unknown VM name fails
// cleanly with a readable message rather than a raw lima error.
func TestPasteImageTargetUnknownInstance(t *testing.T) {
	l := stubVMGetter{vms: []vm.VM{{Name: "other", Status: "Running"}}}

	_, err := pasteImageTarget(l, "missing")
	if err == nil {
		t.Fatal("pasteImageTarget: want error for an unknown instance, got nil")
	}
	if !strings.Contains(err.Error(), `"missing"`) {
		t.Errorf("pasteImageTarget error = %q, want it to name the missing instance", err.Error())
	}
}

// TestPasteImageTargetGetError verifies a Get failure is wrapped rather than
// silently swallowed.
func TestPasteImageTargetGetError(t *testing.T) {
	wantErr := errors.New("boom")
	l := stubVMGetter{err: wantErr}

	_, err := pasteImageTarget(l, "foo")
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("pasteImageTarget error = %v, want it to wrap %v", err, wantErr)
	}
}

// TestPasteImageTargetRunning verifies the happy path returns the found VM
// unchanged.
func TestPasteImageTargetRunning(t *testing.T) {
	l := stubVMGetter{vms: []vm.VM{{Name: "foo", Status: "Running", Dir: "/nonexistent/instance/dir"}}}

	found, err := pasteImageTarget(l, "foo")
	if err != nil {
		t.Fatalf("pasteImageTarget: unexpected error: %v", err)
	}
	if found.Name != "foo" {
		t.Fatalf("pasteImageTarget vm = %+v, want Name %q", found, "foo")
	}
}

// TestRunPasteImageRequiresExactlyOneName verifies the arg-count guard:
// missing (or extra) positional arguments must fail with a readable usage
// error rather than attempting to resolve a zero-value name.
func TestRunPasteImageRequiresExactlyOneName(t *testing.T) {
	for _, args := range [][]string{{}, {"a", "b"}} {
		if err := runPasteImage(args); err == nil {
			t.Errorf("runPasteImage(%v): want error for wrong arg count, got nil", args)
		}
	}
}
