package providerfake

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestZeroValueNeverPanics is the fake's whole reason to exist over the
// nil-embedded-interface trick it replaces: every method must be safely
// callable on a bare &Provider{} (every field unset), returning the same
// "nothing here" zero value a real backend would report for an
// empty/absent result — never a nil-pointer panic from an un-set method.
func TestZeroValueNeverPanics(t *testing.T) {
	f := &Provider{}
	ctx := context.Background()

	if got, err := f.List(); got != nil || err != nil {
		t.Errorf("List() = (%v, %v), want (nil, nil)", got, err)
	}
	if got, err := f.Get("x"); got != (vm.VM{}) || err != nil {
		t.Errorf("Get() = (%+v, %v), want (zero vm.VM, nil)", got, err)
	}
	if got, err := f.Status("x"); got != "" || err != nil {
		t.Errorf("Status() = (%q, %v), want (\"\", nil)", got, err)
	}
	if err := f.Start("x"); err != nil {
		t.Errorf("Start() = %v, want nil", err)
	}
	if err := f.Stop("x"); err != nil {
		t.Errorf("Stop() = %v, want nil", err)
	}
	if err := f.Delete("x", true); err != nil {
		t.Errorf("Delete() = %v, want nil", err)
	}
	if err := f.StartStreaming(ctx, "x", io.Discard); err != nil {
		t.Errorf("StartStreaming() = %v, want nil", err)
	}
	if err := f.StopStreaming(ctx, "x", io.Discard); err != nil {
		t.Errorf("StopStreaming() = %v, want nil", err)
	}
	if err := f.Create(ctx, vm.CreateConfig{}, provision.CreateOptions{}, io.Discard); err != nil {
		t.Errorf("Create() = %v, want nil", err)
	}
	if err := f.Recreate(ctx, vm.CreateConfig{}, provision.CreateOptions{}, io.Discard); err != nil {
		t.Errorf("Recreate() = %v, want nil", err)
	}
	if err := f.Reset(ctx, vm.CreateConfig{}, provision.ResetOptions{}, io.Discard); err != nil {
		t.Errorf("Reset() = %v, want nil", err)
	}
	if err := f.Shell(ctx, "x", nil, io.Discard); err != nil {
		t.Errorf("Shell() = %v, want nil", err)
	}
	if err := f.ShellStreamOut(ctx, "x", nil, io.Discard); err != nil {
		t.Errorf("ShellStreamOut() = %v, want nil", err)
	}
	if got, err := f.ShellOut(ctx, "x"); got != nil || err != nil {
		t.Errorf("ShellOut() = (%v, %v), want (nil, nil)", got, err)
	}
	if err := f.Copy(ctx, io.Discard, false, "a", "b"); err != nil {
		t.Errorf("Copy() = %v, want nil", err)
	}
	if got := f.AttachArgv(vm.VM{}); got != nil {
		t.Errorf("AttachArgv() = %v, want nil", got)
	}
	if got := f.GuestHome(vm.VM{}); got != "" {
		t.Errorf("GuestHome() = %q, want \"\"", got)
	}
	if got := f.GuestUser(vm.VM{}); got != "" {
		t.Errorf("GuestUser() = %q, want \"\"", got)
	}
	if got := f.GuestPath("web", "/x"); got != "web:/x" {
		t.Errorf("GuestPath() = %q, want %q", got, "web:/x")
	}
	if err := f.Preflight(); err != nil {
		t.Errorf("Preflight() = %v, want nil", err)
	}
}

// TestOverridesRunInsteadOfTheDefault proves a set field's function actually
// runs (and receives the real arguments), for a representative sample across
// each method group rather than one assertion per method.
func TestOverridesRunInsteadOfTheDefault(t *testing.T) {
	wantErr := errors.New("boom")
	var gotName string
	f := &Provider{
		ListFunc:       func() ([]vm.VM, error) { return []vm.VM{{Name: "web"}}, nil },
		StartFunc:      func(name string) error { gotName = name; return wantErr },
		AttachArgvFunc: func(v vm.VM) []string { return []string{"ssh", "-t", v.Name} },
		GuestPathFunc:  func(name, path string) string { return "custom:" + name + path },
	}

	vms, err := f.List()
	if err != nil || len(vms) != 1 || vms[0].Name != "web" {
		t.Fatalf("List() override = (%v, %v), want ([{web}], nil)", vms, err)
	}
	if err := f.Start("claude"); !errors.Is(err, wantErr) || gotName != "claude" {
		t.Fatalf("Start() override = (name=%q, err=%v), want (claude, %v)", gotName, err, wantErr)
	}
	if got := f.AttachArgv(vm.VM{Name: "claude"}); len(got) != 3 || got[2] != "claude" {
		t.Fatalf("AttachArgv() override = %v, want it to reflect the passed vm.VM", got)
	}
	if got := f.GuestPath("claude", "/home"); got != "custom:claude/home" {
		t.Fatalf("GuestPath() override = %q, want %q", got, "custom:claude/home")
	}
}
