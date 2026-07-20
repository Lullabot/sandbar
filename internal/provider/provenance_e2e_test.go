//go:build limae2e

// provenance_e2e_test.go proves target-attached provenance over the
// REAL SSH host-access transport (internal/lima.SSHHost), gated exactly like
// remote_e2e_test.go: build tag limae2e, the LIMA_REMOTE_E2E opt-in, and the
// same clean-skip reachability probe (skipUnlessRemoteE2EConfigured, shared
// from that file).
//
// The convergence claim is: a marker written on a host by whoever controls it
// (its own local sand, or a remote client authenticated as the host user) is
// visible to EVERY OTHER controller that reaches the host as that same user.
// These tests model that faithfully — a controller is a provider instance
// connecting over ssh AS THE HOST USER, and "two controllers" is two such
// instances. (The CI loopback runs the remote user as a SEPARATE OS user from
// the runner, so a marker must be written THROUGH the ssh transport, as the
// host user, not by the local runner into a runner-owned dir — otherwise the
// second user cannot read it. Writing via the remote provider's own
// MarkManaged is both the correct-owner path and the production path.)
//
// The markers are written into a DEDICATED remote Lima home (not the real
// ~/.lima), so they never collide with TestE2ERemoteLima's booted instance and
// never leave a lima.yaml-less directory that a later `limactl list` would
// choke on. Discovery / the LIMA_HOME-export fix is proven by the real booted
// VM in TestE2ERemoteLima; this file proves the marker read/write plumbing.
package provider_test

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"testing"

	"github.com/lullabot/sandbar/internal/manage"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// sshCleanupRemoteHome removes a dedicated remote Lima home over ssh (as the
// host user), used to isolate these tests and to clean up after them. It runs
// both before (clearing any leftover from an aborted run) and via t.Cleanup.
// Best-effort: a cleanup failure is logged, never fatal.
func sshCleanupRemoteHome(t *testing.T, cfg provider.TargetConfig, remoteHomeDir string) {
	t.Helper()
	// remoteHomeDir is a fixed, relative, no-metacharacter path chosen by the
	// tests below — safe to interpolate into the remote command.
	args := append(sshBaseArgs(cfg), sshTarget(cfg), "rm", "-rf", remoteHomeDir)
	if out, err := exec.Command("ssh", args...).CombinedOutput(); err != nil {
		t.Logf("cleanup: ssh rm -rf %s: %v\n%s", remoteHomeDir, err, out)
	}
}

// sshBaseArgs is the ssh option set both helpers here dial with.
func sshBaseArgs(cfg provider.TargetConfig) []string {
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "StrictHostKeyChecking=accept-new"}
	if cfg.Port > 0 && cfg.Port != 22 {
		args = append(args, "-p", strconv.Itoa(cfg.Port))
	}
	if cfg.IdentityPath != "" {
		args = append(args, "-i", cfg.IdentityPath)
	}
	return args
}

// sshSeedRemoteInstance creates instance name's directory inside remoteHomeDir
// on the remote host, standing in for the `limactl clone` a real create would
// have run before anything marked it.
//
// These tests exercise the marker plumbing without ever booting a VM, so they
// have no instance directory unless they make one — and MarkManaged now REFUSES
// to mark an instance that does not exist, because a marker write that created
// its own parent would leave a lima.yaml-less directory under LIMA_HOME that
// makes every later `limactl list` fatal. Seeding here is not a workaround for
// that guard; it is what makes these tests model a real create, in which the
// clone always precedes the marker.
func sshSeedRemoteInstance(t *testing.T, cfg provider.TargetConfig, remoteHomeDir, name string) {
	t.Helper()
	args := append(sshBaseArgs(cfg), sshTarget(cfg), "mkdir", "-p", remoteHomeDir+"/"+name)
	if out, err := exec.Command("ssh", args...).CombinedOutput(); err != nil {
		t.Fatalf("seed remote instance dir %s/%s: %v\n%s", remoteHomeDir, name, err, out)
	}
}

// TestE2EProvenanceConvergenceAndTwoController proves, over a real ssh hop:
//  1. a marker written by one controller (through manage.RecordSuccess — the
//     same production path cmd/sand/create.go and the TUI use — handed the
//     REMOTE provider) is read back by that controller;
//  2. two-controller convergence: a SECOND, independently-constructed remote
//     provider (a distinct controller) reads the identical marker, corroborated
//     against a completely empty registry — proving the marker, never the
//     registry, drives visibility;
//  3. clearing the marker (Unmark, what a delete does) makes it read unmanaged.
func TestE2EProvenanceConvergenceAndTwoController(t *testing.T) {
	cfg := skipUnlessRemoteE2EConfigured(t)

	const remoteHome = ".sand-prov-e2e-conv"
	cfg.RemoteLimaHome = remoteHome
	sshCleanupRemoteHome(t, cfg, remoteHome)
	t.Cleanup(func() { sshCleanupRemoteHome(t, cfg, remoteHome) })

	const name = "sand-prov-e2e"
	const base = "sand-prov-e2e-base"
	ctx := context.Background()
	sshSeedRemoteInstance(t, cfg, remoteHome, name)

	controllerA, err := provider.NewRemoteLima(cfg)
	if err != nil {
		t.Fatalf("NewRemoteLima (controller A): %v", err)
	}
	provA, ok := controllerA.(provider.Provenancer)
	if !ok {
		t.Fatal("remote Lima provider does not satisfy provider.Provenancer")
	}

	// --- controller A "creates" the VM: RecordSuccess writes the marker over
	// ssh through the SAME production path every entrypoint uses.
	vmCfg := vm.CreateConfig{Name: name, BaseName: base, User: cfg.User, CPUs: 2, Memory: "2GiB", Disk: vm.BaseDiskFloor}
	if err := manage.RecordSuccess(registry.NewEmpty(), vmCfg, cfg.Scope(), provA); err != nil {
		t.Fatalf("RecordSuccess (controller A, over ssh): %v", err)
	}

	got, ok, err := provA.ProvenanceOf(ctx, name)
	if err != nil {
		t.Fatalf("controller A ProvenanceOf: %v", err)
	}
	if !ok {
		t.Fatal("controller A does not see the marker it just wrote over ssh")
	}
	if got.Base != base || got.Config.Name != name {
		t.Fatalf("marker = %+v, want Base=%s Config.Name=%s", got, base, name)
	}
	if got.Provisioning {
		t.Fatal("a completed create wrote Provisioning=true, want a ready marker")
	}

	// --- 2. two-controller convergence: a SECOND controller, with its own
	// empty registry, sees the identical marker over ssh.
	controllerB, err := provider.NewRemoteLima(cfg)
	if err != nil {
		t.Fatalf("NewRemoteLima (controller B): %v", err)
	}
	provB := controllerB.(provider.Provenancer)
	emptyReg := registry.NewEmpty()
	if len(emptyReg.ManagedInScope(cfg.Scope())) != 0 {
		t.Fatal("sanity: a fresh registry must not claim any VM managed under this scope")
	}
	got2, ok, err := provB.ProvenanceOf(ctx, name)
	if err != nil {
		t.Fatalf("controller B ProvenanceOf: %v", err)
	}
	if !ok {
		t.Fatal("a second controller with an empty registry did not see the marker — convergence failed")
	}
	if got2 != got {
		t.Fatalf("controller B marker = %+v, want identical to controller A's %+v", got2, got)
	}

	// --- 2a. the guard, over the REAL transport: marking an instance that does
	// not exist must refuse, not manufacture its directory on the remote host.
	// The local unit test pins the behaviour; this pins that SSHHost.Stat
	// resolves the (relative, $HOME-anchored) instance path the same way the
	// write does, so the check cannot pass locally and silently no-op over ssh.
	// The failure it prevents is severe and REMOTE: a lima.yaml-less directory
	// under the host's LIMA_HOME makes `limactl list` fatal for every controller
	// of that host, over a VM nobody ever created.
	const neverCloned = "sand-prov-e2e-never-cloned"
	if err := provA.MarkManaged(ctx, neverCloned, provider.NewProvenance(vmCfg, true)); !errors.Is(err, provider.ErrNoInstance) {
		t.Fatalf("MarkManaged(%s) over ssh = %v, want ErrNoInstance", neverCloned, err)
	}

	// --- 2b. the BATCHED read, over the same ssh hop. This is the call the TUI
	// roster actually makes on every refresh (refreshCmd -> Provenancer.
	// Provenance), and it is a DIFFERENT remote implementation from
	// ProvenanceOf above: single-marker reads are a plain `cat`, while the
	// batched read runs a length-framed scan script whose output is decoded by
	// parseMarkerStream. Asserting only ProvenanceOf therefore proves nothing
	// about the roster — which is exactly how a desynchronized frame in that
	// script shipped: every marker was written correctly and readable one at a
	// time, while the batched read errored, silently dropping every controller
	// back to its own registry.
	batch, err := provB.Provenance(ctx)
	if err != nil {
		t.Fatalf("controller B batched Provenance over ssh: %v", err)
	}
	inBatch, ok := batch[name]
	if !ok {
		t.Fatalf("the batched read did not return %q, though ProvenanceOf did — the roster would fall back to the legacy registry. Got %d markers: %v", name, len(batch), batch)
	}
	if inBatch != got {
		t.Fatalf("batched marker for %q = %+v, want identical to the single read's %+v", name, inBatch, got)
	}
	// The refused mark above left NOTHING behind: the batched read walks every
	// directory under the remote Lima home, so its absence here is proof no
	// instance directory was conjured on the host.
	if _, ok := batch[neverCloned]; ok {
		t.Fatalf("marking a non-existent instance created %q on the remote host — that is the lima.yaml-less directory that wedges `limactl list`", neverCloned)
	}

	// --- 3. clearing the marker (what a delete does) reads unmanaged on both.
	if err := provA.Unmark(ctx, name); err != nil {
		t.Fatalf("Unmark: %v", err)
	}
	if _, ok, err := provB.ProvenanceOf(ctx, name); err != nil || ok {
		t.Fatalf("controller B ProvenanceOf after Unmark = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

// TestE2EInFlightProvisioningMarkerVisibleRemotely proves FIX A over real ssh:
// the in-flight (Provisioning=true) marker one controller writes at clone time
// is visible to ANOTHER controller while the build is still running (so that
// controller shows the VM Building), and RecordSuccess then flips the same
// marker to ready, which the other controller also sees.
func TestE2EInFlightProvisioningMarkerVisibleRemotely(t *testing.T) {
	cfg := skipUnlessRemoteE2EConfigured(t)

	const remoteHome = ".sand-prov-e2e-inflight"
	cfg.RemoteLimaHome = remoteHome
	sshCleanupRemoteHome(t, cfg, remoteHome)
	t.Cleanup(func() { sshCleanupRemoteHome(t, cfg, remoteHome) })

	const name = "sand-inflight-e2e"
	const base = "sand-inflight-e2e-base"
	ctx := context.Background()
	sshSeedRemoteInstance(t, cfg, remoteHome, name)

	builder, err := provider.NewRemoteLima(cfg)
	if err != nil {
		t.Fatalf("NewRemoteLima (builder): %v", err)
	}
	builderProv := builder.(provider.Provenancer)
	observer, err := provider.NewRemoteLima(cfg)
	if err != nil {
		t.Fatalf("NewRemoteLima (observer): %v", err)
	}
	observerProv := observer.(provider.Provenancer)

	vmCfg := vm.CreateConfig{Name: name, BaseName: base, User: cfg.User, CPUs: 2, Memory: "2GiB", Disk: vm.BaseDiskFloor}

	// --- clone time: the builder writes an in-flight marker (Provisioning=true).
	if err := builderProv.MarkManaged(ctx, name, provider.NewProvenance(vmCfg, true)); err != nil {
		t.Fatalf("write in-flight marker over ssh: %v", err)
	}
	got, ok, err := observerProv.ProvenanceOf(ctx, name)
	if err != nil {
		t.Fatalf("observer ProvenanceOf (in-flight): %v", err)
	}
	if !ok {
		t.Fatal("a second controller did not see the in-flight (building) VM over ssh")
	}
	if !got.Provisioning {
		t.Fatal("in-flight marker read back Provisioning=false, want true (so the observer's board shows it Building)")
	}
	if got.Base != base {
		t.Fatalf("in-flight marker Base = %q, want %q", got.Base, base)
	}

	// --- mid-build: the builder republishes its position at a role boundary, and
	// the observer's BATCHED read (what its board actually calls) picks it up.
	// This is the only channel build progress has across controllers — the bar on
	// the builder's own tile is parsed from a stdout stream that exists solely in
	// that process — so proving it over real ssh is proving the observer's bar
	// can move at all.
	inFlight := provider.NewProvenance(vmCfg, true)
	inFlight.Progress = provider.BuildProgress{Role: "claude-code", Index: 30, Total: 120}
	if err := builderProv.MarkManaged(ctx, name, inFlight); err != nil {
		t.Fatalf("republish progress over ssh: %v", err)
	}
	batch, err := observerProv.Provenance(ctx)
	if err != nil {
		t.Fatalf("observer batched Provenance (mid-build): %v", err)
	}
	seen, ok := batch[name]
	if !ok {
		t.Fatalf("the batched read did not return the in-flight VM %q; got %d markers", name, len(batch))
	}
	if !seen.Provisioning {
		t.Error("the republished marker lost its Provisioning flag — the observer would show it Running mid-build")
	}
	if seen.Progress != inFlight.Progress {
		t.Fatalf("observed progress = %+v, want %+v — the observer's bar would not move", seen.Progress, inFlight.Progress)
	}

	// --- build succeeds: RecordSuccess flips the same marker to ready.
	if err := manage.RecordSuccess(registry.NewEmpty(), vmCfg, cfg.Scope(), builderProv); err != nil {
		t.Fatalf("RecordSuccess (flip to ready): %v", err)
	}
	got, ok, err = observerProv.ProvenanceOf(ctx, name)
	if err != nil || !ok {
		t.Fatalf("observer ProvenanceOf (ready) = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if got.Provisioning {
		t.Fatal("marker still Provisioning=true after RecordSuccess; the ready flip is not visible remotely")
	}
}
