//go:build limae2e

// This file boots REAL Lima VMs and so is gated behind the `limae2e` build
// tag (and the LIMA_E2E env var) — it never runs in the normal
// `go test ./...`. It is task 10's insurance policy: the four e2e tests for
// this plan's claims that cross a process or machine boundary and so cannot
// be proven in-process, plus the empirical answers to the plan's open Lima
// questions.
//
// Run (needs limactl + nested virt/KVM; downloads the Debian 13 image once):
//
//	LIMA_E2E=1 go test -tags limae2e -timeout 45m -run TestE2E ./internal/ui/
//
// The FIFTH real-Lima e2e test for this package — the secrets save-on-a-
// running-VM round trip — already exists, in secrets_e2e_test.go (task 06). It
// is NOT duplicated here; see this file's tests below for the other four:
//
//  1. TestE2EHeartbeatParsesRealGuestAndCPUMoves — the streaming shell against
//     a real guest yields plausible cpu/mem samples, and cpu% visibly moves
//     under real load.
//  2. TestE2EHeartbeatTerminatesWhenVMStoppedUnderneath — stopping the VM out
//     from under a live heartbeat ends it cleanly, with no leaked goroutine
//     and no stuck gauge.
//  3. TestE2ELastUsedAfterRealStopAndNeverStarted — the ha.stderr.log mtime
//     probe against a real stopped VM, and the never-started VM's absence.
//  4. TestE2ETwoVMsProvisionConcurrently — two real provisions in flight at
//     once, output routed to the right VM, cancellation scoped to one job.
//  5. TestE2EFailedProvisionRendersFailedStatusAndKeepsLogReopenable — THE
//     plan's most dangerous failure mode: Lima reports a provisioning VM as
//     Running even after its Ansible run has failed, so the derived tile
//     status is the only thing standing between the user and a false "your
//     sandbox is healthy".
//
// # A deliberate deviation from "every test cleans up its VMs unconditionally"
//
// Tests 4 and 5 both need a REAL base image and both only care about what
// happens AFTER one already exists (clone/configure/start/finalize, or a
// finalize that fails) — the base-phase playbook run itself (base + user +
// samba + dev-tools + claude-code: real apt/docker/golang/JDK installs) is
// the single most expensive step in this whole suite and is identical
// between them. Building it twice would not exercise anything the shared
// build doesn't, and risks blowing the 45-minute suite timeout for no
// coverage gained. So ensureSharedBase below builds ONE base, once, shared by
// both tests — every clone off it still gets its own pre-emptive delete and
// its own unconditional t.Cleanup, exactly like the existing e2e tests; only
// the shared base itself is torn down once, unconditionally, in TestMain.
package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// e2eCreateVM adapts p.Create to provisionFunc's shape (the same shape
// Provisioner.CreateVM has — a plain create with no extra options), so
// beginProvision can drive the model's own provider exactly the way the
// create form does, without this file reaching past the model for a raw
// *provision.Provisioner.
func e2eCreateVM(p provider.Provider) provisionFunc {
	return func(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error {
		return p.Create(ctx, cfg, provision.CreateOptions{}, out)
	}
}

// e2eMinimalOverlay writes secretsE2EOverlay — the same ansible-free, disk-
// floor overlay secrets_e2e_test.go (task 06) boots from — to a temp file and
// returns its path. Shared rather than redeclared: a second `const
// secretsE2EOverlay = ...` in this package would not even compile.
func e2eMinimalOverlay(t *testing.T) string {
	t.Helper()
	overlay := filepath.Join(t.TempDir(), "base.yaml")
	if err := os.WriteFile(overlay, []byte(secretsE2EOverlay), 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	return overlay
}

// pumpTimeout is teaLoop.pump (jobs_test.go) with a caller-supplied deadline.
// pump's own is a hardcoded 10 seconds — right for a hand-fed fake stream, far
// too short for a real `limactl` clone/configure/start/finalize, which can
// legitimately take minutes.
func pumpTimeout(t *testing.T, l *teaLoop, what string, timeout time.Duration, want func(model) bool) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if want(l.m) {
			return
		}
		select {
		case msg := <-l.msgs:
			l.send(msg)
		case <-time.After(5 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
	}
}

// sharedBaseName is the one real base image tests 4 and 5 clone from. Prefixed
// like every VM this file creates so it can never collide with — or be
// mistaken for — the host's own real `sandbar-base`.
const sharedBaseName = "sand-e2e-shared-base"

// sharedBase holds the shared base built once for the whole e2e binary run —
// see the package doc above for why. cli/prov are non-nil only once the build
// has actually succeeded; TestMain uses cli's presence to decide whether
// there is anything to tear down.
var sharedBase struct {
	once sync.Once
	cli  *lima.Client
	prov *provision.Provisioner
	cfg  vm.CreateConfig
	err  error
}

// ensureSharedBase builds sharedBaseName at most once per test binary run and
// returns the (cli, prov, cfg) triple to clone it from. cfg carries no Name —
// callers copy it and set Name/GitName/GitEmail/CloneURL for their own clone.
func ensureSharedBase(t *testing.T) (*lima.Client, *provision.Provisioner, vm.CreateConfig) {
	t.Helper()
	sharedBase.once.Do(func() {
		playbookDir, err := provision.LocatePlaybook()
		if err != nil {
			sharedBase.err = fmt.Errorf("locate playbook: %w", err)
			return
		}
		cli := lima.New(lima.NewExecRunner())
		prov := &provision.Provisioner{Lima: cli, PlaybookDir: playbookDir}
		cfg := vm.CreateConfig{
			BaseName: sharedBaseName,
			User:     vm.HostUser(),
			// *** Modest and explicit: the test host has 16 cores and 15GiB of
			// RAM, and the base's default allocation (8 CPUs/8GiB) would not
			// leave room for a second VM built from it at the same time. ***
			CPUs:   2,
			Memory: "2GiB",
			Disk:   vm.BaseDiskFloor,
			Domain: "lan",
			Locale: "en_US.UTF-8",
		}
		// Pre-emptive: a prior interrupted run may have left a half-built one.
		_ = cli.Delete(sharedBaseName, true)
		var buildLog bytes.Buffer
		if err := prov.BuildBase(context.Background(), cfg, &buildLog); err != nil {
			sharedBase.err = fmt.Errorf("build shared e2e base %q: %w\n%s", sharedBaseName, err, buildLog.String())
			return
		}
		sharedBase.cli, sharedBase.prov, sharedBase.cfg = cli, prov, cfg
	})
	if sharedBase.err != nil {
		t.Fatalf("shared base unavailable: %v", sharedBase.err)
	}
	return sharedBase.cli, sharedBase.prov, sharedBase.cfg
}

// TestMain tears the shared base down, unconditionally, once every test in
// this binary has run — the suite-level counterpart to the per-test
// t.Cleanup every other VM in this file gets. It is a no-op when no test ever
// needed the shared base (cli stays nil), and it always deletes when one did,
// whether the tests that used it passed or failed.
func TestMain(m *testing.M) {
	code := m.Run()
	if sharedBase.cli != nil {
		_ = sharedBase.cli.Delete(sharedBaseName, true)
	}
	os.Exit(code)
}

// e2eWaitForLonger is waitFor (heartbeat_lifecycle_test.go) with a
// caller-supplied deadline, for assertions timing real Lima subprocess
// teardown rather than an instant fake stream.
func e2eWaitForLonger(t *testing.T, what string, timeout time.Duration, want func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !want() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(time.Millisecond)
	}
}

// e2eStressGuest backgrounds a two-core cpu burner inside the guest for
// roughly d, then lets it die on its own — `nohup … &` returns almost at once
// (the work is backgrounded before the wrapping `sh -c` exits), so this does
// not block the caller.
func e2eStressGuest(t *testing.T, cli *lima.Client, name string, d time.Duration) {
	t.Helper()
	secs := int(d / time.Second)
	script := fmt.Sprintf(
		"nohup sh -c 'yes > /dev/null & yes > /dev/null & sleep %d && pkill yes' > /dev/null 2>&1 < /dev/null &",
		secs)
	if _, err := cli.ShellOut(context.Background(), name, "sh", "-c", script); err != nil {
		t.Fatalf("start guest load: %v", err)
	}
}

// THE HEARTBEAT PARSER AGAINST A REAL GUEST. A canned Runner proves the
// parser handles the /proc/stat and /proc/meminfo shape the test author
// imagined; it cannot prove that shape matches what a REAL guest's kernel
// actually emits. This test settles that: real samples, a plausible cpu%, and
// — the part a static fixture cannot give at all — the cpu number visibly
// MOVING when real load is applied inside the guest.
func TestE2EHeartbeatParsesRealGuestAndCPUMoves(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima e2e tests")
	}
	cli := lima.New(lima.NewExecRunner())
	const name = "sand-e2e-heartbeat"
	_ = cli.Delete(name, true)
	t.Cleanup(func() { _ = cli.Delete(name, true) })

	if err := cli.Create(name, e2eMinimalOverlay(t)); err != nil {
		t.Fatalf("create: %v", err)
	}

	r := newHeartbeats(cli)
	_, ch, ok := r.start(registry.LocalScope, name)
	if !ok {
		t.Fatal("start: heartbeat did not open")
	}
	t.Cleanup(r.stopAll)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var baseline guestSample
	haveBaseline, loadStarted := false, false
	var peak float64
	var samples []guestSample

readLoop:
	for {
		select {
		case s, open := <-ch:
			if !open {
				t.Fatalf("the heartbeat ended before the test finished collecting samples (samples so far: %+v)", samples)
			}
			samples = append(samples, s)

			if s.HasCPU {
				if s.CPUPct < 0 || s.CPUPct > 100 {
					t.Fatalf("implausible cpu%%: %.2f (want [0,100])", s.CPUPct)
				}
				switch {
				case !haveBaseline:
					baseline, haveBaseline = s, true
					// Now that we have one plausible idle-ish reading, pin both
					// cores for a while and watch the NEXT several samples for a
					// real jump.
					e2eStressGuest(t, cli, name, 25*time.Second)
					loadStarted = true
				case loadStarted:
					if s.CPUPct > peak {
						peak = s.CPUPct
					}
				}
			}
			if s.HasMem() {
				if s.MemTotal == 0 {
					t.Fatal("mem total should be > 0")
				}
				if s.MemUsed >= s.MemTotal {
					t.Fatalf("mem used (%d) should be < mem total (%d)", s.MemUsed, s.MemTotal)
				}
			}

			// A clear, unambiguous jump — not noise — is what "moves" means here.
			if loadStarted && peak >= baseline.CPUPct+30 {
				break readLoop
			}
		case <-ctx.Done():
			break readLoop
		}
	}

	if !haveBaseline {
		t.Fatalf("never got a cpu reading from the real guest in 90s; samples: %+v", samples)
	}
	if peak <= baseline.CPUPct {
		t.Fatalf("cpu%% never moved under a 2-core stress load: baseline=%.1f%% peak=%.1f%% samples=%+v",
			baseline.CPUPct, peak, samples)
	}
	if peak < baseline.CPUPct+10 {
		t.Fatalf("cpu%% moved only from %.1f%% to %.1f%% under a 2-core stress load — expected a much bigger jump; samples=%+v",
			baseline.CPUPct, peak, samples)
	}
	t.Logf("real guest cpu%%: baseline=%.1f%% peak-under-load=%.1f%% (%d samples)", baseline.CPUPct, peak, len(samples))
}

// THE HEARTBEAT DIES CLEANLY WHEN ITS VM IS STOPPED UNDERNEATH IT, and its
// goroutine is REAPED — not merely "the test doesn't hang", but a positive
// count-based assertion, because a leaked goroutine here is a leaked SSH
// connection into a guest that no longer exists.
func TestE2EHeartbeatTerminatesWhenVMStoppedUnderneath(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima e2e tests")
	}
	cli := lima.New(lima.NewExecRunner())
	const name = "sand-e2e-hb-stop"
	_ = cli.Delete(name, true)
	t.Cleanup(func() { _ = cli.Delete(name, true) })

	if err := cli.Create(name, e2eMinimalOverlay(t)); err != nil {
		t.Fatalf("create: %v", err)
	}

	r := newHeartbeats(cli)
	base := runtime.NumGoroutine()

	epoch, ch, ok := r.start(registry.LocalScope, name)
	if !ok {
		t.Fatal("start: heartbeat did not open")
	}

	// Drive the read loop exactly as the real Update dispatch does
	// (model.go's heartbeatSampleMsg case): read, fold, read again — and, on
	// the channel closing, call ended(). THAT LAST STEP IS LOAD-BEARING: fold
	// alone never drops the registry entry; only ended() does (see
	// heartbeat.go). A reader that drains the channel but never calls ended()
	// leaves r.latest() reporting true forever, which is a bug in the test's
	// own harness, not evidence of anything wrong in production.
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			s, open := <-ch
			if !open {
				r.ended(registry.LocalScope, name, epoch)
				return
			}
			if next := r.fold(registry.LocalScope, name, epoch, s); next == nil {
				return
			}
		}
	}()

	waitFor(t, "the first sample from the real guest", func() bool {
		_, ok := r.latest(registry.LocalScope, name)
		return ok
	})

	// Stop the VM out from under the live heartbeat.
	if err := cli.Stop(name); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// NO GAUGE LEFT STUCK: the reading must go the moment the stream does.
	// waitFor's own deadline (10s, heartbeat_lifecycle_test.go) was sized for
	// an instant fake stream; give a real `limactl shell` teardown (task 05
	// measured ~300ms once the guest is actually gone, but scheduling and
	// process-exit slop are real against an actual VM) more room.
	e2eWaitForLonger(t, "the heartbeat to end on its own after the VM stops", 30*time.Second, func() bool {
		_, ok := r.latest(registry.LocalScope, name)
		return !ok
	})
	readers.Wait()

	// A LEAKED GOROUTINE IS A FAILURE, NOT A COSMETIC ISSUE.
	waitForGoroutines(t, base)
}

// `LAST USED` AFTER A REAL STOP, and the never-started VM's honest absence.
// The in-process tests fabricate an mtime; this is the only thing that can
// prove Lima actually writes ha.stderr.log where — and when — the plan
// assumes it does.
func TestE2ELastUsedAfterRealStopAndNeverStarted(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima e2e tests")
	}
	cli := lima.New(lima.NewExecRunner())
	const name = "sand-e2e-lastused"
	_ = cli.Delete(name, true)
	t.Cleanup(func() { _ = cli.Delete(name, true) })

	if err := cli.Create(name, e2eMinimalOverlay(t)); err != nil {
		t.Fatalf("create: %v", err)
	}

	before := time.Now()
	if err := cli.Stop(name); err != nil {
		t.Fatalf("stop: %v", err)
	}
	after := time.Now()

	dir := e2eInstanceDir(t, cli, name)
	got, ok := lastUsed(dir)
	if !ok {
		t.Fatalf("lastUsed(%q) reported no reading for a VM that was just stopped", dir)
	}
	if got.Before(before.Add(-5*time.Second)) || got.After(after.Add(time.Minute)) {
		t.Fatalf("ha.stderr.log mtime = %v, want it within (near) the stop window [%v, %v]", got, before, after)
	}
	if age := time.Since(got); age < 0 || age > 5*time.Minute {
		t.Fatalf("lastUsed reported an implausible age: %v (mtime %v)", age, got)
	}
	t.Logf("real ha.stderr.log mtime landed %v after the stop call returned", got.Sub(after))

	// NEVER-STARTED VM. `limactl create` (unlike our Client.Create, which is
	// `limactl start --name …` and always boots) makes the instance directory
	// WITHOUT booting it — the real Lima operation the "no ha.stderr.log yet"
	// codepath in lastUsed exists for. There is no wrapper for this on
	// lima.Client (sand never needs a create-without-start), so this drives
	// limactl directly, exactly as sand's own runner ultimately does.
	const neverName = "sand-e2e-neverstarted"
	_ = cli.Delete(neverName, true)
	t.Cleanup(func() { _ = cli.Delete(neverName, true) })

	cmd := exec.Command("limactl", "create", "--name", neverName, "--tty=false", e2eMinimalOverlay(t))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("limactl create (no start): %v\n%s", err, out)
	}
	neverDir := e2eInstanceDir(t, cli, neverName)
	if _, ok := lastUsed(neverDir); ok {
		t.Fatalf("a never-started VM should report NO last-used reading, got one for dir %s", neverDir)
	}
}

// e2eInstanceDir resolves name's Lima instance directory off a real `limactl
// list`, failing the test if the instance is not there.
func e2eInstanceDir(t *testing.T, cli *lima.Client, name string) string {
	t.Helper()
	vms, err := cli.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, v := range vms {
		if v.Name == name {
			if v.Dir == "" {
				t.Fatalf("%s has no Dir in `limactl list`", name)
			}
			return v.Dir
		}
	}
	t.Fatalf("%s not found in `limactl list`: %+v", name, vms)
	return ""
}

// TWO VMs PROVISION CONCURRENTLY, FOR REAL, and the board stays live. This is
// TestTwoJobsInFlight (jobs_test.go) — the load-bearing in-process test for
// this exact claim — replayed against two REAL `limactl` processes, two REAL
// io.Pipes, and a REAL provisioner instead of two hand-fed fakeJobs. A fake
// stream cannot deadlock against a slow reader, cannot race the registry
// under real OS scheduling, and cannot mis-attribute output that was never
// actually produced by two independent subprocesses — this can.
func TestE2ETwoVMsProvisionConcurrently(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima e2e tests")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cli, prov, baseCfg := ensureSharedBase(t)

	const nameA, nameB = "sand-e2e-concurrent-a", "sand-e2e-concurrent-b"
	_ = cli.Delete(nameA, true)
	_ = cli.Delete(nameB, true)
	t.Cleanup(func() {
		_ = cli.Delete(nameA, true)
		_ = cli.Delete(nameB, true)
	})

	cfgA := baseCfg
	cfgA.Name, cfgA.GitName, cfgA.GitEmail = nameA, "Sand E2E A", "sand-e2e-a@example.com"
	cfgB := baseCfg
	cfgB.Name, cfgB.GitName, cfgB.GitEmail = nameB, "Sand E2E B", "sand-e2e-b@example.com"

	m, ok := New(singleFleet(provider.NewLocalLima(cli, prov), registry.LocalScope)).(model)
	if !ok {
		t.Fatal("New did not return a model")
	}
	m = resized(m, 100, 30)
	l := newTeaLoop(t, m)

	// Start both provisions — beginProvision is exactly what the create form
	// calls. Both goroutines are running against real limactl the instant exec
	// returns.
	cmdA := l.m.beginProvision("Creating "+nameA, e2eCreateVM(l.m.provFor(registry.LocalScope)), cfgA)
	l.exec(cmdA)
	cmdB := l.m.beginProvision("Creating "+nameB, e2eCreateVM(l.m.provFor(registry.LocalScope)), cfgB)
	l.exec(cmdB)

	if !l.m.jobs.isRunning(registry.LocalScope, nameA) || !l.m.jobs.isRunning(registry.LocalScope, nameB) {
		t.Fatal("both provisions should be in flight at once")
	}

	// BOTH JOBS MAKE INDEPENDENT PROGRESS. If B were secretly serialized behind
	// A (a shared-state bug that only shows up under real concurrency), this
	// would time out long before A's own clone+configure+start+finalize even
	// finishes, since B would not even have started.
	pumpTimeout(t, l, "both jobs to report real Ansible progress", 15*time.Minute, func(m model) bool {
		sa, okA := m.jobs.snapshot(provisionKey(registry.LocalScope, nameA))
		sb, okB := m.jobs.snapshot(provisionKey(registry.LocalScope, nameB))
		return okA && okB && sa.Progress.Total > 0 && sb.Progress.Total > 0
	})

	// EACH JOB'S OUTPUT ROUTES TO ITS OWN VM, AND ONLY ITS OWN. The provisioner
	// writes the VM's own name into its phase banners (`Cloning "name" from
	// base image …`), so real cross-routing would leak the OTHER VM's name
	// into this job's buffer — a failure mode two independently fed fake
	// streams cannot exhibit, because nothing routes them but the test itself.
	outA, outB := jobOutput(l.m, provisionKey(registry.LocalScope, nameA)), jobOutput(l.m, provisionKey(registry.LocalScope, nameB))
	if strings.Contains(outA, nameB) {
		t.Fatalf("%s's job log contains %s's name — output crossed streams:\n%s", nameA, nameB, outA)
	}
	if strings.Contains(outB, nameA) {
		t.Fatalf("%s's job log contains %s's name — output crossed streams:\n%s", nameB, nameA, outB)
	}

	// PER-JOB CANCELLATION TARGETS ONLY ITS OWN JOB. Reopen A's log (the
	// retained-run verb) and ctrl+c: only A's real limactl subprocess may be
	// killed.
	l.m.view = viewBoard
	l.m.focusVM.Name = nameA
	l.send(runeKey('l'))
	if l.m.view != viewProgress || l.m.progressJob != provisionKey(registry.LocalScope, nameA) {
		t.Fatalf("reopening %s's log should show it on the progress view (view=%v job=%+v)", nameA, l.m.view, l.m.progressJob)
	}
	l.send(ctrlKey('c'))

	pumpTimeout(t, l, nameA+" to finish cancelling", time.Minute, func(m model) bool {
		return !m.jobs.isRunning(registry.LocalScope, nameA)
	})
	if s, _ := l.m.jobs.snapshot(provisionKey(registry.LocalScope, nameA)); !s.Canceled {
		t.Fatalf("%s should be marked cancelled", nameA)
	}
	if !l.m.jobs.isRunning(registry.LocalScope, nameB) {
		t.Fatal("cancelling A must not touch B — B should still be running")
	}

	// B RUNS TO COMPLETION, UNTOUCHED BY A's CANCELLATION.
	pumpTimeout(t, l, nameB+" to finish", 15*time.Minute, func(m model) bool {
		return !m.jobs.isRunning(registry.LocalScope, nameB)
	})
	sb, ok := l.m.jobs.snapshot(provisionKey(registry.LocalScope, nameB))
	if !ok {
		t.Fatal("B's job should be retained")
	}
	if sb.Failed() {
		t.Fatalf("B should have succeeded (a real create against a real, already-built base), got err=%v\noutput:\n%s", sb.Err, sb.Output)
	}
	if strings.Contains(jobOutput(l.m, provisionKey(registry.LocalScope, nameB)), "^C") {
		t.Fatalf("A's cancel notice leaked into B's buffer:\n%s", jobOutput(l.m, provisionKey(registry.LocalScope, nameB)))
	}
}

// A DELIBERATELY FAILED PROVISION RENDERS `Failed`, NOT A GREEN "Running".
// This is the plan's most dangerous single failure mode: Lima has no concept
// of "provisioning" — to `limactl list`, a VM whose finalize Ansible run just
// failed is indistinguishable from a perfectly healthy one, because the VM
// itself booted fine and is still up. Every layer below the job registry
// would happily report this VM healthy. Only deriveStatus, fed the job
// registry's retained failure, stands between the user and that false
// all-clear — and it must keep standing there across a subsequent refresh,
// and the run that explains the failure must still be readable afterwards.
func TestE2EFailedProvisionRendersFailedStatusAndKeepsLogReopenable(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima e2e tests")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cli, prov, baseCfg := ensureSharedBase(t)

	const name = "sand-e2e-failed"
	_ = cli.Delete(name, true)
	t.Cleanup(func() { _ = cli.Delete(name, true) })

	cfg := baseCfg
	cfg.Name, cfg.GitName, cfg.GitEmail = name, "Sand E2E", "sand-e2e@example.com"
	// The break: an unreachable clone URL. `.invalid` is reserved by RFC 2606
	// and never resolves, so the project role's `git clone` fails fast and
	// deterministically (GIT_TERMINAL_PROMPT=0 already rules out a credential
	// prompt hanging it) — AFTER the VM has already been cloned, configured,
	// and started, which is exactly the "Ansible failed inside an otherwise
	// healthy, running guest" shape this test exists to catch.
	cfg.CloneURL = "https://sand-e2e-does-not-exist.invalid/org/repo.git"

	m, ok := New(singleFleet(provider.NewLocalLima(cli, prov), registry.LocalScope)).(model)
	if !ok {
		t.Fatal("New did not return a model")
	}
	m = resized(m, 100, 30)
	l := newTeaLoop(t, m)

	cmd := l.m.beginProvision("Creating "+name, e2eCreateVM(l.m.provFor(registry.LocalScope)), cfg)
	l.exec(cmd)

	pumpTimeout(t, l, "the provision to finish (and fail)", 15*time.Minute, func(m model) bool {
		return !m.jobs.isRunning(registry.LocalScope, name)
	})

	snap, ok := l.m.jobs.snapshot(provisionKey(registry.LocalScope, name))
	if !ok {
		t.Fatal("the job should be retained after finishing")
	}
	if !snap.Failed() {
		t.Fatalf("the job should have FAILED (an unreachable clone URL breaks finalize), got state=%v err=%v canceled=%v\noutput:\n%s",
			snap.State, snap.Err, snap.Canceled, snap.Output)
	}

	// THE CRITICAL CHECK. Confirm the precondition Lima actually exhibits
	// (rather than assuming it): the VM is up.
	realVMs, err := cli.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	v, found := vm.VM{}, false
	for _, rv := range realVMs {
		if rv.Name == name {
			v, found = rv, true
		}
	}
	if !found {
		t.Fatalf("the VM should exist in `limactl list` (finalize runs after clone+configure+start): %+v", realVMs)
	}
	if v.Status != "Running" {
		t.Fatalf("PLAN ASSUMPTION CHECK FAILED: expected Lima to report the VM Running (finalize fails AFTER a successful boot), got %q — "+
			"if Lima's real behaviour has changed, this test's premise (and the plan's) needs revisiting, not a loosened assertion", v.Status)
	}
	if got := deriveStatus(v, snap, ok); got != statusFailed {
		t.Fatalf("Lima reports %q for a VM whose provision failed, and the derived tile status = %v — want Failed. "+
			"A failed provision must never render as a healthy Running tile.", v.Status, got)
	}

	// STAYS FAILED ACROSS A SUBSEQUENT REFRESH TICK — exactly the message the
	// real board sends itself every refreshInterval (see refresh.go). Simulate
	// the user having navigated back to the board first, which is where a
	// refresh tick actually lands in the real program.
	l.m.view = viewBoard
	l.send(vmsLoadedMsg{vms: realVMs})
	afterRefresh, ok := l.m.lookupVM(registry.LocalScope, name)
	if !ok {
		t.Fatalf("%s should still be on the board after a refresh", name)
	}
	if got := l.m.statusOf(registry.LocalScope, afterRefresh); got != statusFailed {
		t.Fatalf("after a refresh tick, status = %v — want it to STAY Failed", got)
	}

	// THE RETAINED LOG IS REOPENABLE: still there, and unchanged, after
	// navigating away and back.
	if !l.m.jobs.HasRetainedRun(registry.LocalScope, name) {
		t.Fatal("the failed run's log should be retained and reopenable")
	}
	if snap.Output == "" {
		t.Fatal("the retained log should hold the provisioner's streamed output, got none")
	}
	l.m.view = viewBoard
	l.m.showJobLog(registry.LocalScope, name)
	if l.m.view != viewProgress || l.m.progressJob != provisionKey(registry.LocalScope, name) {
		t.Fatalf("reopening the log should show the progress view for %s (view=%v job=%+v)", name, l.m.view, l.m.progressJob)
	}
	reopened, ok := l.m.jobs.snapshot(provisionKey(registry.LocalScope, name))
	if !ok || reopened.Output != snap.Output {
		t.Fatalf("the reopened log should be identical to the retained one (navigating away and back must not lose it)")
	}
	t.Logf("failed run's retained output (%d bytes):\n%s", len(reopened.Output), reopened.Output)
}
