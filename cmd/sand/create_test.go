package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// stubProvisioner is a headlessProvisioner double: CreateVM/Recreate both
// "succeed" without touching lima, recording the options they were handed so a
// test can assert what intent doHeadlessCreate passed DOWN to the provisioner
// (rather than acting on itself). called is set by either method, so a test
// gating on a refusal (e.g. --recreate on an unmanaged VM) can assert the
// provisioner was never reached at all.
type stubProvisioner struct {
	created   int
	recreated int
	called    bool
	opts      provision.CreateOptions
}

func (s *stubProvisioner) CreateVMWithOptions(_ context.Context, _ vm.CreateConfig, opts provision.CreateOptions, _ io.Writer) error {
	s.created++
	s.called = true
	s.opts = opts
	return nil
}

func (s *stubProvisioner) RecreateWithOptions(_ context.Context, _ vm.CreateConfig, opts provision.CreateOptions, _ io.Writer) error {
	s.recreated++
	s.called = true
	s.opts = opts
	return nil
}

// TestHeadlessCreateRecordsManagedVM is the load-bearing parity guarantee
// called out in task 3: a headless `sand create` must record the VM as
// managed with its CreateConfig, exactly like the interactive TUI does on a
// successful provision (internal/ui/model.go's provisionDoneMsg handling,
// shared via internal/manage), so a headless-created VM is flagged managed
// and stays recreate-able just like one made through the TUI.
func TestHeadlessCreateRecordsManagedVM(t *testing.T) {
	reg := registry.NewEmpty()
	cfg := vm.CreateConfig{
		Name:     "claude",
		BaseName: "claude-base",
		GitName:  "Ada Lovelace",
		GitEmail: "ada@example.com",
		CPUs:     4,
		Memory:   "8GiB",
		Disk:     "100GiB",
	}

	err := doHeadlessCreate(context.Background(), reg, &stubProvisioner{}, cfg, false, false, io.Discard)
	if err != nil {
		t.Fatalf("doHeadlessCreate: %v", err)
	}

	if !reg.IsManaged(cfg.Name) {
		t.Fatalf("headless create did not record %q as managed", cfg.Name)
	}
	got, ok := reg.Config(cfg.Name)
	if !ok {
		t.Fatalf("registry has no config recorded for %q", cfg.Name)
	}
	if got != cfg {
		t.Fatalf("recorded config = %+v, want %+v (round-trip mismatch)", got, cfg)
	}
}

// TestHeadlessCreatePassesRebuildDownToTheProvisioner is the CLI half of the race
// this task closes.
//
// doHeadlessCreate used to force-delete the base image ITSELF, before calling the
// provisioner — and therefore before the base lock (internal/provision/baselock.go)
// was ever taken. A concurrent create holding that lock could be mid-clone from the
// base being deleted underneath it. The delete now happens inside the provisioner,
// under the lock; the CLI only passes the INTENT down. This test is what stops
// anyone from "helpfully" reintroducing the up-front delete: doHeadlessCreate no
// longer even has a lima client to do it with, and the intent must arrive at the
// provisioner instead.
func TestHeadlessCreatePassesRebuildDownToTheProvisioner(t *testing.T) {
	cfg := vm.CreateConfig{Name: "claude", BaseName: "claude-base", GitName: "A", GitEmail: "a@b.c", CPUs: 2}

	for _, tc := range []struct {
		name    string
		rebuild bool
	}{
		{"rebuild", true},
		{"no rebuild", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prov := &stubProvisioner{}
			if err := doHeadlessCreate(context.Background(), registry.NewEmpty(), prov, cfg, false, tc.rebuild, io.Discard); err != nil {
				t.Fatalf("doHeadlessCreate: %v", err)
			}
			if prov.created != 1 {
				t.Fatalf("CreateVMWithOptions called %d times, want 1", prov.created)
			}
			if prov.opts.Rebuild != tc.rebuild {
				t.Fatalf("provisioner got Rebuild=%v, want %v — the rebuild intent must reach the provisioner, which destroys the base UNDER the base lock", prov.opts.Rebuild, tc.rebuild)
			}
		})
	}
}

// TestHeadlessRecreatePassesRebuildDownToTheProvisioner: --recreate and --rebuild
// are independent (one targets the clone, the other the base) and may be combined,
// so the recreate path has to carry the rebuild intent down too.
func TestHeadlessRecreatePassesRebuildDownToTheProvisioner(t *testing.T) {
	cfg := vm.CreateConfig{Name: "claude", BaseName: "claude-base", GitName: "A", GitEmail: "a@b.c", CPUs: 2}
	reg := registry.NewEmpty()
	if err := reg.Add(cfg); err != nil { // recreate is gated on the VM being sand-managed
		t.Fatalf("seed registry: %v", err)
	}

	prov := &stubProvisioner{}
	if err := doHeadlessCreate(context.Background(), reg, prov, cfg, true, true, io.Discard); err != nil {
		t.Fatalf("doHeadlessCreate(--recreate --rebuild): %v", err)
	}
	if prov.recreated != 1 || prov.created != 0 {
		t.Fatalf("--recreate took the wrong path (recreated=%d created=%d)", prov.recreated, prov.created)
	}
	if !prov.opts.Rebuild {
		t.Fatal("--recreate --rebuild did not pass the rebuild intent to the provisioner")
	}
}

// TestHeadlessRecreateRefusedForUnmanagedVM is the CLI half of the recreate
// gate in internal/manage: recreate clones from a Claude base image and would
// replace ANY instance it is pointed at, so --recreate must be refused for a
// VM sand did not create — and refused BEFORE the provisioner is ever
// touched, not just reported as an error after a clone already ran.
func TestHeadlessRecreateRefusedForUnmanagedVM(t *testing.T) {
	cfg := vm.CreateConfig{Name: "claude", BaseName: "claude-base", GitName: "A", GitEmail: "a@b.c", CPUs: 2}
	reg := registry.NewEmpty() // no managed entry for cfg.Name

	prov := &stubProvisioner{}
	err := doHeadlessCreate(context.Background(), reg, prov, cfg, true, false, io.Discard)
	if err == nil {
		t.Fatal("doHeadlessCreate(--recreate, unmanaged VM): got nil error, want a recreate refusal")
	}
	if !strings.Contains(err.Error(), "recreate refused") {
		t.Fatalf("doHeadlessCreate(--recreate, unmanaged VM) error = %q, want it to contain %q", err.Error(), "recreate refused")
	}
	if prov.called {
		t.Fatal("doHeadlessCreate(--recreate, unmanaged VM) invoked the provisioner despite the refusal")
	}
}
