//go:build limae2e

// This file boots REAL Lima VMs and so is gated behind the `limae2e` build tag
// (and the LIMA_E2E env var) — it never runs in the normal `go test ./...`. It
// formalises, as asserted Go tests, the two cross-process claims the
// `lima-e2e` CI job otherwise only checks informally against the headless
// `sand create` entrypoint:
//
//  1. TestE2EHeadlessCreateRecordsManaged — a headless `sand create` (driven
//     via doHeadlessCreate, exactly as runCreate calls it) produces a VM that
//     both `limactl list` and the on-disk managed-VM registry agree exists —
//     the registry write must actually survive a reload, not just live in the
//     in-memory Registry the call was handed.
//  2. TestE2ERecreateRoundTrip — the managed-gate that internal/manage enforces
//     holds against a REAL registry and a REAL provisioner: a sand-created VM
//     can be recreated via --recreate (and the recreate genuinely REPLACES the
//     instance, proved with an in-guest sentinel file that must NOT survive
//     it), while --recreate against a VM sand did not create is refused with
//     the "recreate refused" error and leaves that VM completely untouched
//     (proved the same way: a sentinel file planted before the refused call is
//     still there after it).
//
// Run (needs limactl + nested virt/KVM; downloads the Debian 13 image once):
//
//	LIMA_E2E=1 go test -tags limae2e -timeout 30m -run E2E ./cmd/sand/
//
// This runs in the `lima-e2e` CI job (.github/workflows/test.yml), not the
// fast `unit` job.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// cmdE2EBaseName is the one real base image these tests clone from. Prefixed
// distinctly from both the host's real `sandbar-base` and internal/ui's own
// "sand-e2e-shared-base" (a different Go test binary; only the Lima instance
// name needs to stay unique) so a run of this suite can never collide with —
// or be mistaken for — either.
const cmdE2EBaseName = "sand-cmde2e-base"

// cmdE2ESharedBase holds the shared base built once for the whole test binary
// run. cli/prov are non-nil only once the build has actually succeeded;
// TestMain uses cli's presence to decide whether there is anything to tear
// down.
var cmdE2ESharedBase struct {
	once sync.Once
	cli  *lima.Client
	prov *provision.Provisioner
	cfg  vm.CreateConfig
	err  error
}

// ensureCmdE2EBase builds cmdE2EBaseName at most once per test binary run and
// returns the (cli, prov, cfg) triple to clone it from. cfg carries no
// Name/GitName/GitEmail — callers copy it and set those for their own clone.
//
// Every tool-set flag is explicitly off: these tests only exercise the
// create/recreate + managed-registry bookkeeping, never the base's installed
// tooling, so there is nothing to gain from paying for Claude/DDEV/Go/Java
// installs here — this is the "tiny resources" the task calls for.
func ensureCmdE2EBase(t *testing.T) (*lima.Client, *provision.Provisioner, vm.CreateConfig) {
	t.Helper()
	cmdE2ESharedBase.once.Do(func() {
		playbookDir, err := provision.LocatePlaybook()
		if err != nil {
			cmdE2ESharedBase.err = fmt.Errorf("locate playbook: %w", err)
			return
		}
		cli := lima.New(lima.NewExecRunner())
		prov := &provision.Provisioner{Lima: cli, PlaybookDir: playbookDir}
		cfg := vm.CreateConfig{
			BaseName: cmdE2EBaseName,
			User:     vm.HostUser(),
			CPUs:     2,
			Memory:   "2GiB",
			Disk:     vm.BaseDiskFloor,
			Domain:   "lan",
			Locale:   "en_US.UTF-8",
			// WithClaude/WithDDEV/WithGo/WithJava deliberately left at their zero
			// value (false): see the doc comment above.
		}
		// Pre-emptive: a prior interrupted run may have left a half-built one.
		_ = cli.Delete(cmdE2EBaseName, true)
		var buildLog bytes.Buffer
		if err := prov.BuildBase(context.Background(), cfg, &buildLog); err != nil {
			cmdE2ESharedBase.err = fmt.Errorf("build shared e2e base %q: %w\n%s", cmdE2EBaseName, err, buildLog.String())
			return
		}
		cmdE2ESharedBase.cli, cmdE2ESharedBase.prov, cmdE2ESharedBase.cfg = cli, prov, cfg
	})
	if cmdE2ESharedBase.err != nil {
		t.Fatalf("shared base unavailable: %v", cmdE2ESharedBase.err)
	}
	return cmdE2ESharedBase.cli, cmdE2ESharedBase.prov, cmdE2ESharedBase.cfg
}

// TestMain tears the shared base down, unconditionally, once every test in
// this binary has run — the suite-level counterpart to the per-VM
// t.Cleanup every test below also registers. It is a no-op when no test ever
// needed the shared base (cli stays nil), and it always deletes when one did,
// whether the tests that used it passed or failed.
func TestMain(m *testing.M) {
	code := m.Run()
	if cmdE2ESharedBase.cli != nil {
		_ = cmdE2ESharedBase.cli.Delete(cmdE2EBaseName, true)
	}
	os.Exit(code)
}

// TestE2EHeadlessCreateRecordsManaged is the create half of this task: driving
// doHeadlessCreate — the exact function runCreate (sand create's real entry
// point) calls — against a real Lima client and a real, file-backed registry
// must leave the new VM both listed by `limactl list` AND recorded managed,
// and that managed record must survive a fresh load from disk (not just live
// in the Registry value the call happened to be handed).
func TestE2EHeadlessCreateRecordsManaged(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima e2e tests")
	}

	cli, prov, baseCfg := ensureCmdE2EBase(t)

	const name = "sand-cmde2e-create"
	_ = cli.Delete(name, true)
	t.Cleanup(func() { _ = cli.Delete(name, true) })

	cfg := baseCfg
	cfg.Name, cfg.GitName, cfg.GitEmail = name, "Sand CmdE2E Create", "sand-cmde2e-create@example.com"

	regPath := filepath.Join(t.TempDir(), "managed-vms.json")
	reg, err := registry.LoadFrom(regPath)
	if err != nil {
		t.Fatalf("LoadFrom(%s): %v", regPath, err)
	}

	if err := doHeadlessCreate(context.Background(), reg, prov, cfg, registry.LocalScope, false, false, io.Discard); err != nil {
		t.Fatalf("doHeadlessCreate: %v", err)
	}

	// Exists in `limactl list`.
	vms, err := cli.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, v := range vms {
		if v.Name == name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("%s not found in `limactl list` after doHeadlessCreate: %+v", name, vms)
	}

	// Recorded managed in the registry doHeadlessCreate was handed.
	if !reg.IsManaged(name) {
		t.Fatalf("doHeadlessCreate did not record %s as managed", name)
	}

	// AND that record actually persisted to disk: reload the registry from the
	// same path an entirely separate process (a later `sand` invocation) would
	// use, and confirm it agrees.
	reloaded, err := registry.LoadFrom(regPath)
	if err != nil {
		t.Fatalf("reload registry from %s: %v", regPath, err)
	}
	if !reloaded.IsManaged(name) {
		t.Fatalf("managed record for %s did not survive a reload from %s — the registry write did not really persist", name, regPath)
	}
}

// TestE2ERecreateRoundTrip is the recreate half of this task: (a) a
// sand-created VM can be recreated via --recreate, and the recreate really
// replaces the instance rather than no-op'ing; (b) --recreate against a VM
// sand did not create is refused with the "recreate refused" error, and the
// refusal leaves that VM completely untouched.
func TestE2ERecreateRoundTrip(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima e2e tests")
	}

	cli, prov, baseCfg := ensureCmdE2EBase(t)

	// --- (a) recreate a sand-created VM. ---
	const managedName = "sand-cmde2e-recreate"
	_ = cli.Delete(managedName, true)
	t.Cleanup(func() { _ = cli.Delete(managedName, true) })

	managedCfg := baseCfg
	managedCfg.Name, managedCfg.GitName, managedCfg.GitEmail =
		managedName, "Sand CmdE2E Recreate", "sand-cmde2e-recreate@example.com"

	reg := registry.NewEmpty() // in-memory: disk persistence is proved by the other test
	if err := doHeadlessCreate(context.Background(), reg, prov, managedCfg, registry.LocalScope, false, false, io.Discard); err != nil {
		t.Fatalf("initial create: %v", err)
	}
	if !reg.IsManaged(managedName) {
		t.Fatalf("expected %s to be recorded managed after create", managedName)
	}

	// Plant a sentinel INSIDE the guest before recreating. A real --recreate
	// deletes and re-clones the instance from the base, so the sentinel must
	// NOT survive — proving the recreate genuinely replaced the VM rather than
	// silently doing nothing.
	if _, err := cli.ShellOut(context.Background(), managedName, "sh", "-c", "touch /tmp/sand-cmde2e-before-recreate"); err != nil {
		t.Fatalf("plant pre-recreate sentinel: %v", err)
	}

	if err := doHeadlessCreate(context.Background(), reg, prov, managedCfg, registry.LocalScope, true, false, io.Discard); err != nil {
		t.Fatalf("--recreate on a sand-managed VM should succeed, got: %v", err)
	}

	vms, err := cli.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, v := range vms {
		if v.Name == managedName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("%s missing from `limactl list` after --recreate", managedName)
	}

	out, err := cli.ShellOut(context.Background(), managedName, "sh", "-c",
		"test -f /tmp/sand-cmde2e-before-recreate && echo present || echo absent")
	if err != nil {
		t.Fatalf("check post-recreate sentinel: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "absent" {
		t.Fatalf("pre-recreate sentinel survived --recreate (got %q, want %q) — the VM was not actually replaced", got, "absent")
	}

	// --- (b) --recreate against a VM sand did not create is refused, and does
	// not touch it. ---
	const unmanagedName = "sand-cmde2e-unmanaged"
	_ = cli.Delete(unmanagedName, true)
	t.Cleanup(func() { _ = cli.Delete(unmanagedName, true) })

	unmanagedCfg := baseCfg
	unmanagedCfg.Name, unmanagedCfg.GitName, unmanagedCfg.GitEmail =
		unmanagedName, "Sand CmdE2E Unmanaged", "sand-cmde2e-unmanaged@example.com"

	// Create the VM directly through the provisioner, bypassing doHeadlessCreate
	// (and therefore registry.Add) entirely, so it is a real, running instance
	// that is simply NOT in the managed registry — exactly the "sand did not
	// create this" case --recreate must refuse.
	if err := prov.CreateVMWithOptions(context.Background(), unmanagedCfg, provision.CreateOptions{}, io.Discard); err != nil {
		t.Fatalf("create unmanaged VM directly via the provisioner: %v", err)
	}
	if _, err := cli.ShellOut(context.Background(), unmanagedName, "sh", "-c", "touch /tmp/sand-cmde2e-unmanaged-untouched"); err != nil {
		t.Fatalf("plant untouched-sentinel on the unmanaged VM: %v", err)
	}

	unmanagedReg := registry.NewEmpty() // no entry for unmanagedName: it is genuinely unmanaged
	err = doHeadlessCreate(context.Background(), unmanagedReg, prov, unmanagedCfg, registry.LocalScope, true, false, io.Discard)
	if err == nil {
		t.Fatal("--recreate against an unmanaged VM should be refused, got a nil error")
	}
	if !strings.Contains(err.Error(), "recreate refused") {
		t.Fatalf("--recreate refusal error = %q, want it to contain %q", err.Error(), "recreate refused")
	}

	// The refusal must happen BEFORE anything touches the VM: the sentinel
	// planted before the call must still be there.
	out, err = cli.ShellOut(context.Background(), unmanagedName, "sh", "-c",
		"test -f /tmp/sand-cmde2e-unmanaged-untouched && echo present || echo absent")
	if err != nil {
		t.Fatalf("check unmanaged VM's untouched-sentinel after the refused recreate: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "present" {
		t.Fatalf("unmanaged VM's sentinel = %q, want %q — a refused --recreate must leave the VM completely untouched", got, "present")
	}
}
