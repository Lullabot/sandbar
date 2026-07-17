//go:build limae2e

// provenance_e2e_test.go exercises the plan's cross-mode/two-controller
// convergence, deleted-for-free lifecycle, and the non-default LIMA_HOME
// resolution assertion (strikethroo plan 17--target-attached-vm-provenance,
// task 07) — ALL against the REAL SSH host-access transport
// (internal/lima.SSHHost), gated exactly like remote_e2e_test.go: build tag
// limae2e, the same LIMA_REMOTE_E2E opt-in env var, and the same clean-skip
// reachability probe (skipUnlessRemoteE2EConfigured, reused from that file —
// this file shares its package and its build tag, so the helper is directly
// callable).
//
// Unlike remote_e2e_test.go, this file does NOT boot a real Lima VM: the
// provenance layer (Provenance/ProvenanceOf/MarkManaged/Unmark) is pure file
// I/O against the instance directory (see limaprovenance.go), and "listed"
// only needs `limactl list` to enumerate a STOPPED instance with a resolved
// lima.yaml — exactly what a real `limactl clone` leaves behind before it is
// ever started (see internal/lima/configure_strip_test.go's
// resolvedCloneFixtureTemplate, whose shape this borrows). A full boot would
// need nested virtualization on both ends of the SSH hop for no additional
// coverage of the marker plumbing this task is proving, so — per the task's
// own guidance ("if a scenario is infeasible on the CI runner … implement it
// against the loopback/local path and note the limitation explicitly rather
// than skipping silently") — this test targets the loopback/local path and
// notes that limitation here rather than paying for a VM boot it does not
// need.
package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/manage"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// provenanceE2EFixtureLimaYAML is a minimal RESOLVED instance lima.yaml (no
// unresolved `base:` template reference) — the same shape
// internal/lima/configure_strip_test.go's resolvedCloneFixtureTemplate uses —
// so `limactl list`/`list <name>` can enumerate and parse a STOPPED instance
// without ever booting it.
const provenanceE2EFixtureLimaYAML = `images:
- location: "https://example.invalid/debian-13.qcow2"
  arch: "x86_64"
  digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"
cpus: 2
memory: "2GiB"
disk: "20GiB"
`

// writeProvenanceFixtureInstance creates (or re-creates) a stopped,
// never-booted Lima instance directory at instDir with a resolved
// lima.yaml, so a real `limactl list` enumerates it.
func writeProvenanceFixtureInstance(t *testing.T, instDir string) {
	t.Helper()
	if err := os.MkdirAll(instDir, 0o700); err != nil {
		t.Fatalf("mkdir instance dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(instDir, "lima.yaml"), []byte(provenanceE2EFixtureLimaYAML), 0o644); err != nil {
		t.Fatalf("write fixture lima.yaml: %v", err)
	}
}

// TestE2EProvenanceConvergenceLifecycleAndTwoController is this task's
// combined real-SSH integration test: it drives the REAL local Lima provider
// (real limactl, a fixture instance directory) and the REAL remote-Lima-
// over-SSH provider (a genuine ssh loopback hop, real HostFiles reads) side
// by side against the SAME Lima home, proving:
//
//  1. cross-mode convergence: a VM "created" via the local provider (through
//     manage.RecordSuccess, the same production path cmd/sand/create.go
//     uses) is seen as managed by a remote provider pointed at the same
//     host/LIMA_HOME — the marker decodes to the expected base+config on
//     both sides;
//  2. the LIMA_HOME fix: a non-default RemoteLimaHome is where BOTH
//     discovery (`limactl list` over ssh) and the marker read resolve;
//  3. two-controller convergence: a SECOND remote provider construction,
//     corroborated against a completely empty registry scope (no
//     XDG_DATA_HOME entries at all for this scope), still sees the exact
//     same marker — proving the marker, never the registry, drives
//     visibility;
//  4. deleted-for-free lifecycle: deleting the instance directory (what
//     `limactl delete` does) takes the marker with it, and a fresh instance
//     directory reusing the same name is listed but reads back unmanaged
//     until marked again.
func TestE2EProvenanceConvergenceLifecycleAndTwoController(t *testing.T) {
	cfg := skipUnlessRemoteE2EConfigured(t)

	limaHome := t.TempDir()
	cfg.RemoteLimaHome = limaHome // non-default: also exercises the LIMA_HOME assertion
	t.Setenv("LIMA_HOME", limaHome)

	const name = "sand-provenance-e2e"
	const base = "sand-provenance-e2e-base"
	instDir := filepath.Join(limaHome, name)
	writeProvenanceFixtureInstance(t, instDir)
	t.Cleanup(func() { _ = os.RemoveAll(instDir) })

	localCore := lima.New(lima.NewExecRunner())
	local := provider.NewLocalLima(localCore, nil)
	localProv, ok := local.(provider.Provenancer)
	if !ok {
		t.Fatal("local Lima provider does not satisfy provider.Provenancer")
	}

	// --- 1. "create" via the local provider: RecordSuccess writes the real
	// marker through the SAME production path cmd/sand/create.go uses.
	reg := registry.NewEmpty()
	vmCfg := vm.CreateConfig{Name: name, BaseName: base, User: vm.HostUser(), CPUs: 2, Memory: "2GiB", Disk: vm.BaseDiskFloor}
	if err := manage.RecordSuccess(reg, vmCfg, registry.LocalScope, localProv); err != nil {
		t.Fatalf("RecordSuccess (local): %v", err)
	}

	ctx := context.Background()

	// --- cross-mode convergence + LIMA_HOME assertion: a remote provider
	// pointed at the SAME (non-default) Lima home, over a real ssh hop, sees
	// the marker the local provider just wrote.
	remote, err := provider.NewRemoteLima(cfg)
	if err != nil {
		t.Fatalf("NewRemoteLima: %v", err)
	}
	remoteProv, ok := remote.(provider.Provenancer)
	if !ok {
		t.Fatal("remote Lima provider does not satisfy provider.Provenancer")
	}

	got, ok, err := remoteProv.ProvenanceOf(ctx, name)
	if err != nil {
		t.Fatalf("remote ProvenanceOf: %v", err)
	}
	if !ok {
		t.Fatalf("remote provider (loopback ssh, LIMA_HOME=%s) does not see the marker the local provider wrote", limaHome)
	}
	if got.Base != base {
		t.Fatalf("remote-read marker Base = %q, want %q", got.Base, base)
	}
	if got.Config.Name != name || got.Config.BaseName != base {
		t.Fatalf("remote-read marker Config = %+v, want Name=%s BaseName=%s", got.Config, name, base)
	}

	// discovery: the remote provider's List() (real `limactl list` run over
	// ssh with LIMA_HOME=<limaHome> exported on the remote argv — see
	// internal/lima/sshhost.go's limactlArgv) must ALSO resolve the same
	// directory and enumerate the fixture instance.
	remoteVMs, err := remote.List()
	if err != nil {
		t.Fatalf("remote List: %v", err)
	}
	if !containsVM(remoteVMs, name) {
		t.Fatalf("remote List() (LIMA_HOME=%s) did not enumerate %s: %+v", limaHome, name, remoteVMs)
	}

	// --- 2. two-controller convergence: a SECOND remote provider
	// construction, corroborated against a totally empty registry scope
	// (simulating an empty XDG_DATA_HOME — no managed-VM index entries for
	// this scope at all), still sees the same marker: provenance, never the
	// registry, drives visibility.
	remote2, err := provider.NewRemoteLima(cfg)
	if err != nil {
		t.Fatalf("NewRemoteLima (second controller): %v", err)
	}
	remote2Prov, ok := remote2.(provider.Provenancer)
	if !ok {
		t.Fatal("second remote Lima provider does not satisfy provider.Provenancer")
	}
	emptyReg := registry.NewEmpty() // this controller's registry never heard of `name`
	if len(emptyReg.ManagedInScope(cfg.Scope())) != 0 {
		t.Fatal("sanity: fresh empty registry must not already claim any VM managed under the remote scope")
	}
	got2, ok, err := remote2Prov.ProvenanceOf(ctx, name)
	if err != nil {
		t.Fatalf("second controller ProvenanceOf: %v", err)
	}
	if !ok {
		t.Fatal("a second controller with an empty registry did not see the same managed VM (marker)")
	}
	if got2 != got {
		t.Fatalf("second controller's marker read = %+v, want it identical to the first controller's %+v", got2, got)
	}

	// --- 3. deleted-for-free lifecycle: removing the instance directory
	// (what `limactl delete` does) takes the marker with it.
	if err := os.RemoveAll(instDir); err != nil {
		t.Fatalf("remove instance dir (simulating limactl delete): %v", err)
	}
	if _, ok, err := localProv.ProvenanceOf(ctx, name); err != nil || ok {
		t.Fatalf("ProvenanceOf after instance deletion = (ok=%v, err=%v), want (false, nil)", ok, err)
	}

	// A fresh instance directory reusing the SAME name (no marker) must be
	// LISTED but read back unmanaged until marked again.
	writeProvenanceFixtureInstance(t, instDir)

	vmsAfter, err := local.List()
	if err != nil {
		t.Fatalf("local List after re-creating the instance dir: %v", err)
	}
	if !containsVM(vmsAfter, name) {
		t.Fatalf("re-created (unmarked) instance %s was not listed: %+v", name, vmsAfter)
	}
	if _, ok, err := localProv.ProvenanceOf(ctx, name); err != nil || ok {
		t.Fatalf("re-created instance ProvenanceOf = (ok=%v, err=%v), want (false, nil) — unmanaged until marked", ok, err)
	}
}

// TestE2EInFlightProvisioningMarkerVisibleRemotely proves FIX A over the real
// ssh transport: a PROVISIONAL marker (Provisioning=true) — the one the local
// provider writes at clone time, before the long finalize step (see
// local.go Create / provision OnCloned) — is visible to a REMOTE controller
// while the build is still in flight, so that controller shows the VM as a
// building tile rather than not at all. Then, on success, RecordSuccess flips
// the same marker to ready (Provisioning=false), which the remote controller
// also sees. This is the "in-flight builds converge" scenario, end to end.
func TestE2EInFlightProvisioningMarkerVisibleRemotely(t *testing.T) {
	cfg := skipUnlessRemoteE2EConfigured(t)

	limaHome := t.TempDir()
	cfg.RemoteLimaHome = limaHome
	t.Setenv("LIMA_HOME", limaHome)

	const name = "sand-inflight-e2e"
	const base = "sand-inflight-e2e-base"
	instDir := filepath.Join(limaHome, name)
	writeProvenanceFixtureInstance(t, instDir)
	t.Cleanup(func() { _ = os.RemoveAll(instDir) })

	local := provider.NewLocalLima(lima.New(lima.NewExecRunner()), nil)
	localProv := local.(provider.Provenancer)
	remote, err := provider.NewRemoteLima(cfg)
	if err != nil {
		t.Fatalf("NewRemoteLima: %v", err)
	}
	remoteProv := remote.(provider.Provenancer)
	ctx := context.Background()

	vmCfg := vm.CreateConfig{Name: name, BaseName: base, User: vm.HostUser(), CPUs: 2, Memory: "2GiB", Disk: vm.BaseDiskFloor}

	// --- clone time: the provider writes an IN-FLIGHT marker (Provisioning=true).
	if err := localProv.MarkManaged(ctx, name, provider.NewProvenance(vmCfg, true)); err != nil {
		t.Fatalf("write in-flight marker: %v", err)
	}
	got, ok, err := remoteProv.ProvenanceOf(ctx, name)
	if err != nil {
		t.Fatalf("remote ProvenanceOf (in-flight): %v", err)
	}
	if !ok {
		t.Fatal("a remote controller did not see the in-flight (building) VM over ssh")
	}
	if !got.Provisioning {
		t.Fatalf("remote-read in-flight marker Provisioning = false, want true (so the remote board shows it Building)")
	}
	if got.Base != base {
		t.Fatalf("in-flight marker Base = %q, want %q", got.Base, base)
	}

	// --- build succeeds: RecordSuccess flips the SAME marker to ready.
	if err := manage.RecordSuccess(registry.NewEmpty(), vmCfg, registry.LocalScope, localProv); err != nil {
		t.Fatalf("RecordSuccess (flip to ready): %v", err)
	}
	got, ok, err = remoteProv.ProvenanceOf(ctx, name)
	if err != nil || !ok {
		t.Fatalf("remote ProvenanceOf (ready) = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if got.Provisioning {
		t.Fatal("marker still Provisioning=true after RecordSuccess; the ready flip is not visible remotely")
	}
}
