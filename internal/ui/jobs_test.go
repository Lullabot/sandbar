package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// teaLoop is a minimal stand-in for Bubble Tea's runtime: it runs tea.Cmds on
// their own goroutines (exactly as Bubble Tea does) and applies the messages
// they return to the model on ONE goroutine. Driving the job registry through
// it — rather than calling Update in a straight line — is what makes
// `go test -race` meaningful here: the provision functions and the pipe readers
// really do run concurrently with the update loop, as they do in the real
// program.
type teaLoop struct {
	t    *testing.T
	m    model
	msgs chan tea.Msg
}

func newTeaLoop(t *testing.T, m model) *teaLoop {
	t.Helper()
	return &teaLoop{t: t, m: m, msgs: make(chan tea.Msg, 256)}
}

// exec runs cmd off the update goroutine and feeds its message back into the
// loop, unwrapping tea.Batch the way the real runtime does. Spinner ticks are
// dropped: they are a self-perpetuating timer loop with nothing to assert on.
func (l *teaLoop) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		switch msg := cmd().(type) {
		case nil:
		case tea.BatchMsg:
			for _, c := range msg {
				l.exec(c)
			}
		case spinner.TickMsg:
		default:
			select {
			case l.msgs <- msg:
			case <-time.After(5 * time.Second):
			}
		}
	}()
}

// send applies one message on the update goroutine (the test's own), dispatching
// whatever command it returns.
func (l *teaLoop) send(msg tea.Msg) {
	l.t.Helper()
	next, cmd := l.m.Update(msg)
	l.m = next.(model)
	l.exec(cmd)
}

// pump drains async messages into the model until want reports the model has
// reached the state under test, failing the test if it never does.
func (l *teaLoop) pump(what string, want func(model) bool) {
	l.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		if want(l.m) {
			return
		}
		select {
		case msg := <-l.msgs:
			l.send(msg)
		case <-time.After(5 * time.Millisecond):
		case <-deadline:
			l.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// fakeJob is a provisionFunc under the test's control: it writes exactly the
// chunks the test hands it, blocks until told to finish, and honours ctx (as
// the real provisioner does, by killing its limactl subprocess).
type fakeJob struct {
	out  chan string
	done chan error
}

func newFakeJob() *fakeJob {
	return &fakeJob{out: make(chan string), done: make(chan error, 1)}
}

func (f *fakeJob) run(ctx context.Context, _ vm.CreateConfig, out io.Writer) error {
	for {
		select {
		case chunk := <-f.out:
			if _, err := io.WriteString(out, chunk); err != nil {
				return err
			}
		case err := <-f.done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// stream is run in streamFunc shape — what beginStream (a file transfer) takes,
// as opposed to the provisionFunc beginProvision takes.
func (f *fakeJob) stream(ctx context.Context, out io.Writer) error {
	return f.run(ctx, vm.CreateConfig{}, out)
}

// write feeds one chunk into the job's stream and waits until the model has
// folded it into THAT RUN's log — so a test can assert on ordering.
func (f *fakeJob) write(l *teaLoop, key jobKey, chunk string) {
	l.t.Helper()
	select {
	case f.out <- chunk:
	case <-time.After(5 * time.Second):
		l.t.Fatalf("%s: nobody read the chunk %q", key.vm, chunk)
	}
	l.pump("output of "+key.vm, func(m model) bool {
		s, ok := m.jobs.snapshot(key)
		return ok && strings.Contains(s.Output, chunk)
	})
}

// seedJob registers a running job for name with no goroutine behind it, so a
// test can drive the messages a job produces (provisionOutputMsg,
// provisionDoneMsg, a ctrl+c cancel) without a real stream. A non-zero cfg makes
// it a PROVISION; the zero value makes it a transfer. It leaves the model on that
// job's progress screen, which is where beginStream would have left it.
func seedJob(t *testing.T, m *model, name string, cfg vm.CreateConfig) {
	t.Helper()
	if m.jobs == nil {
		m.jobs = newJobRegistry()
	}
	key := provisionKey(registry.LocalScope, name)
	if cfg.Name == "" {
		key = transferKey(registry.LocalScope, name)
	}
	if !m.jobs.begin(&job{
		key:    key,
		title:  "Creating " + name,
		state:  jobRunning,
		cancel: func() {},
	}) {
		t.Fatalf("seedJob: %s already has a run of this kind in flight", name)
	}
	m.jobs.markProvision(registry.LocalScope, name, cfg, false)
	m.progressJob = key
	m.view = viewProgress
}

// jobOutput is the job registry's accumulated log for a run.
func jobOutput(m model, key jobKey) string {
	s, ok := m.jobs.snapshot(key)
	if !ok {
		return ""
	}
	return s.Output
}

// TWO JOBS IN FLIGHT — the load-bearing test of this task, and the one that has
// to be written first. Two provisions run at once; each job's output must land
// in ITS OWN VM's buffer and in no other, cancelling one must not touch the
// other, and the whole thing must be race-free (`go test -race`).
func TestTwoJobsInFlight(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 100, 30)
	l := newTeaLoop(t, m)

	alpha, beta := newFakeJob(), newFakeJob()

	// Start both provisions. beginProvision is what the create form calls.
	cmdA := l.m.beginProvision("Creating alpha", alpha.run, vm.CreateConfig{Name: "alpha", BaseName: "sandbar-base"})
	l.exec(cmdA)
	cmdB := l.m.beginProvision("Creating beta", beta.run, vm.CreateConfig{Name: "beta", BaseName: "sandbar-base"})
	l.exec(cmdB)

	if !l.m.jobs.isRunning(registry.LocalScope, "alpha") || !l.m.jobs.isRunning(registry.LocalScope, "beta") {
		t.Fatal("both provisions should be in flight at once")
	}
	// Neither VM exists in `limactl list` yet — a create's clone lands minutes into
	// its own build — so the registry is the only thing that knows they are being
	// built at all. The board reads them from here to raise their tiles.
	if got := l.m.jobs.names(registry.LocalScope); len(got) != 2 {
		t.Fatalf("the registry should list both jobs (a building VM has no Lima record yet), got %v", got)
	}

	// A concurrent reader models the tile renderer (task 07), which reads the
	// registry from whatever goroutine the render happens on while Update is
	// mutating it. It holds the REGISTRY POINTER, not the model — that is exactly
	// what a renderer gets: a by-value copy of the model whose jobs field aims at
	// this same shared registry. Without the registry's mutex, this is the read the
	// race detector has to catch.
	reg := l.m.jobs
	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
				for _, n := range []string{"alpha", "beta"} {
					s, ok := reg.snapshot(provisionKey(registry.LocalScope, n))
					_ = deriveStatus(vm.VM{Name: n, Status: "Running"}, s, ok)
				}
			}
		}
	}()
	defer func() {
		close(stop)
		readers.Wait()
	}()

	// Interleave the two streams. Each chunk must land in its own VM's buffer.
	alpha.write(l, provisionKey(registry.LocalScope, "alpha"), "TASK [base : alpha-one]\n")
	beta.write(l, provisionKey(registry.LocalScope, "beta"), "TASK [base : beta-one]\n")
	alpha.write(l, provisionKey(registry.LocalScope, "alpha"), "TASK [base : alpha-two]\n")
	beta.write(l, provisionKey(registry.LocalScope, "beta"), "TASK [base : beta-two]\n")

	outA, outB := jobOutput(l.m, provisionKey(registry.LocalScope, "alpha")), jobOutput(l.m, provisionKey(registry.LocalScope, "beta"))
	if strings.Contains(outA, "beta-") {
		t.Fatalf("beta's output leaked into alpha's buffer:\n%s", outA)
	}
	if strings.Contains(outB, "alpha-") {
		t.Fatalf("alpha's output leaked into beta's buffer:\n%s", outB)
	}
	if !strings.Contains(outA, "alpha-one") || !strings.Contains(outA, "alpha-two") {
		t.Fatalf("alpha's buffer is missing its own output:\n%s", outA)
	}
	if !strings.Contains(outB, "beta-one") || !strings.Contains(outB, "beta-two") {
		t.Fatalf("beta's buffer is missing its own output:\n%s", outB)
	}

	// Each job parses its OWN Ansible progress — the counters must not be shared.
	sa, _ := l.m.jobs.snapshot(provisionKey(registry.LocalScope, "alpha"))
	if sa.Progress.Task != "alpha-two" || sa.Progress.Index != 2 {
		t.Fatalf("alpha's parsed progress = %+v, want task alpha-two at index 2", sa.Progress)
	}
	sb, _ := l.m.jobs.snapshot(provisionKey(registry.LocalScope, "beta"))
	if sb.Progress.Task != "beta-two" || sb.Progress.Index != 2 {
		t.Fatalf("beta's parsed progress = %+v, want task beta-two at index 2", sb.Progress)
	}

	// CANCELLATION IS PER-JOB. Reopen alpha's log (the retained-run verb) so the
	// progress screen targets alpha, then ctrl+c: only alpha may be cancelled.
	l.m.view = viewBoard
	l.m.focusVM.Name = "alpha"
	l.send(runeKey('l'))
	if l.m.view != viewProgress || l.m.progressJob != provisionKey(registry.LocalScope, "alpha") {
		t.Fatalf("reopening alpha's log should show it in the progress view (view=%v job=%+v)", l.m.view, l.m.progressJob)
	}
	l.send(ctrlKey('c'))

	l.pump("alpha to finish cancelling", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "alpha") })

	if !l.m.jobs.isRunning(registry.LocalScope, "beta") {
		t.Fatal("cancelling alpha must not touch beta")
	}
	if s, _ := l.m.jobs.snapshot(provisionKey(registry.LocalScope, "alpha")); !s.Canceled {
		t.Fatalf("alpha should be marked cancelled, got %+v", s)
	}
	// A cancelled run leaves partial state: it is not recorded as managed, and it
	// is not a failure (so the tile must not go red for it).
	if l.m.reg.IsManaged("alpha") {
		t.Fatal("a cancelled provision must not be recorded as managed")
	}
	if got := l.m.statusOf(registry.LocalScope, vm.VM{Name: "alpha", Status: "Running"}); got != statusRunning {
		t.Fatalf("a cancelled job's VM should fall back to Lima's status, got %v", got)
	}

	// beta is untouched and still streaming — its buffer never saw alpha's cancel.
	beta.write(l, provisionKey(registry.LocalScope, "beta"), "TASK [base : beta-three]\n")
	if strings.Contains(jobOutput(l.m, provisionKey(registry.LocalScope, "beta")), "^C") {
		t.Fatalf("alpha's cancel notice leaked into beta's buffer:\n%s", jobOutput(l.m, provisionKey(registry.LocalScope, "beta")))
	}

	// beta finishes cleanly: it (and only it) is recorded as managed.
	beta.done <- nil
	l.pump("beta to finish", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "beta") })
	if !l.m.reg.IsManaged("beta") {
		t.Fatal("a successful provision should be recorded as managed")
	}
	if s, _ := l.m.jobs.snapshot(provisionKey(registry.LocalScope, "beta")); s.State != jobSucceeded {
		t.Fatalf("beta should have succeeded, got %+v", s)
	}
}

// A failed job is RETAINED — with its log — so the tile's Failed status is
// sticky. It must survive a subsequent vmsLoadedMsg refresh: a job dropped on
// failure leaves a half-built VM rendering as a reassuring green "Running".
func TestFailedJobSurvivesRefresh(t *testing.T) {
	m := newTestModel(t)
	l := newTeaLoop(t, m)

	fail := newFakeJob()
	l.exec(l.m.beginProvision("Creating web", fail.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))
	fail.write(l, provisionKey(registry.LocalScope, "web"), "TASK [dev-tools : Install Docker]\nfatal: [localhost]: FAILED!\n")

	fail.done <- errors.New("provisioning (base) failed for \"web\"")
	l.pump("web to fail", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })

	v := vm.VM{Name: "web", Status: "Running"} // Lima still calls the half-built VM Running
	if got := l.m.statusOf(registry.LocalScope, v); got != statusFailed {
		t.Fatalf("a failed provision must derive Failed, got %v", got)
	}

	// The refresh tick that would have dropped it.
	l.send(vmsLoadedMsg{vms: []vm.VM{v}})

	s, ok := l.m.jobs.snapshot(provisionKey(registry.LocalScope, "web"))
	if !ok {
		t.Fatal("a failed job must survive a list refresh — dropping it turns the tile green")
	}
	if s.State != jobFailed || s.Err == nil {
		t.Fatalf("the retained job should still carry its failure, got %+v", s)
	}
	if !strings.Contains(s.Output, "FAILED!") {
		t.Fatalf("the retained job should still carry its log, got %q", s.Output)
	}
	if got := l.m.statusOf(registry.LocalScope, v); got != statusFailed {
		t.Fatalf("Failed must stay sticky across a refresh, got %v", got)
	}
	// And the user can read WHY: the retained run's log is reopenable.
	if !l.m.vmHasRetainedRun(registry.LocalScope, "web") {
		t.Fatal("a failed VM must offer its retained log — a red tile with no diagnostic is an alarm with no explanation")
	}

	// Deleting the VM is the user acting on it: the retained run goes with it.
	l.send(actionDoneMsg{action: "delete", name: "web"})
	if _, ok := l.m.jobs.snapshot(provisionKey(registry.LocalScope, "web")); ok {
		t.Fatal("deleting a VM should drop its retained run")
	}
}

// A job whose VM disappears (deleted, or gone from `limactl list`) must be
// cancelled and reaped — no leaked goroutine, no ghost tile. The run function
// here deliberately IGNORES its context, so only closing the pipe can unblock
// it: that is the leak this reaps.
func TestJobReapedWhenVMDisappears(t *testing.T) {
	m := newTestModel(t)
	l := newTeaLoop(t, m)

	exited := make(chan struct{})
	blocked := make(chan struct{})
	run := func(_ context.Context, out io.Writer) error {
		defer close(exited)
		close(blocked)
		for { // ignores ctx entirely: only a closed pipe can stop it
			if _, err := io.WriteString(out, "still going\n"); err != nil {
				return err
			}
			time.Sleep(time.Millisecond)
		}
	}
	cmd, _ := l.m.beginStream(transferKey(registry.LocalScope, "web"), "Uploading to web", run)
	l.exec(cmd)
	<-blocked

	// The VM is present at first — that is what makes a later absence evidence of
	// a DISAPPEARANCE rather than of a create whose VM does not exist yet.
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "web", Status: "Running"}}})
	if !l.m.jobs.isRunning(registry.LocalScope, "web") {
		t.Fatal("the job should still be running while its VM is present")
	}

	// It vanishes.
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "other", Status: "Running"}}})

	if _, ok := l.m.jobs.snapshot(transferKey(registry.LocalScope, "web")); ok {
		t.Fatal("a job whose VM disappeared must be reaped from the registry")
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the reaped job's goroutine leaked — reaping must close its pipe, not just cancel its context")
	}
}

// The trap in reaping: a CREATE's VM does not exist in `limactl list` for the
// first several minutes of its own build (the base build, then the clone). If
// absence alone reaped jobs, every create would be killed on its first refresh.
func TestCreateJobNotReapedBeforeItsVMAppears(t *testing.T) {
	m := newTestModel(t)
	l := newTeaLoop(t, m)

	job := newFakeJob()
	l.exec(l.m.beginProvision("Creating web", job.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))
	job.write(l, provisionKey(registry.LocalScope, "web"), "==> Building base image\n")

	// Several refreshes in which the VM being built does not exist yet.
	for i := 0; i < 3; i++ {
		l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "unrelated", Status: "Running"}}})
	}
	if !l.m.jobs.isRunning(registry.LocalScope, "web") {
		t.Fatal("a create whose VM has not appeared yet must NOT be reaped — that would kill every build")
	}

	job.done <- nil
	l.pump("web to finish", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })
}

// A reset deletes and re-creates its own VM, so its VM legitimately vanishes
// from `limactl list` mid-run. The reaper must not mistake that for a VM
// deleted out from under a build.
func TestResetJobSurvivesItsOwnDelete(t *testing.T) {
	m := newTestModel(t)
	l := newTeaLoop(t, m)

	job := newFakeJob()
	l.exec(l.m.beginReset("Resetting web", job.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))
	job.write(l, provisionKey(registry.LocalScope, "web"), "==> Deleting web\n")

	// Present, then gone (the reset just deleted it), then back (the re-clone).
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "web", Status: "Running"}}})
	l.send(vmsLoadedMsg{vms: []vm.VM{}})
	if !l.m.jobs.isRunning(registry.LocalScope, "web") {
		t.Fatal("a reset must survive the deletion it performs itself")
	}
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "web", Status: "Running"}}})
	if !l.m.jobs.isRunning(registry.LocalScope, "web") {
		t.Fatal("a reset must survive its own delete/re-clone cycle")
	}

	job.done <- nil
	l.pump("web to finish", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })
}

// deriveStatus is the pure function the tile renderer (task 07) consumes: the
// job registry is consulted FIRST, Lima's Running/Stopped is the fallback.
func TestDeriveStatus(t *testing.T) {
	provision := func(state jobState, canceled bool) jobSnapshot {
		return jobSnapshot{State: state, Canceled: canceled, Provision: true}
	}
	cases := []struct {
		name   string
		v      vm.VM
		job    jobSnapshot
		hasJob bool
		want   derivedStatus
	}{
		{"lima running, no job", vm.VM{Status: "Running"}, jobSnapshot{}, false, statusRunning},
		{"lima stopped, no job", vm.VM{Status: "Stopped"}, jobSnapshot{}, false, statusStopped},
		{"building: lima says Running, ansible is still inside it", vm.VM{Status: "Running"}, provision(jobRunning, false), true, statusBuilding},
		{"failed provision must NOT read as a green Running", vm.VM{Status: "Running"}, provision(jobFailed, false), true, statusFailed},
		{"a succeeded run falls back to Lima", vm.VM{Status: "Running"}, provision(jobSucceeded, false), true, statusRunning},
		{"a cancelled run is not a failure", vm.VM{Status: "Stopped"}, provision(jobFailed, true), true, statusStopped},
		{"a running transfer is not a build", vm.VM{Status: "Running"}, jobSnapshot{State: jobRunning}, true, statusRunning},
		{"a failed transfer does not make the VM broken", vm.VM{Status: "Running"}, jobSnapshot{State: jobFailed}, true, statusRunning},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveStatus(c.v, c.job, c.hasJob); got != c.want {
				t.Fatalf("deriveStatus = %v, want %v", got, c.want)
			}
		})
	}
	// The four statuses must be distinguishable as words, not just as colours.
	for _, s := range []derivedStatus{statusRunning, statusStopped, statusBuilding, statusFailed} {
		if s.String() == "" {
			t.Fatalf("status %d has no label", s)
		}
	}
}

// The keyboard is no longer frozen while a job runs: esc leaves the progress
// screen (the build keeps going in the background) and every key stays live —
// the user can immediately start a SECOND VM. This is the whole point of the
// task, and the behaviour the old model-wide running flag made impossible.
func TestKeyboardStaysLiveWhileBuilding(t *testing.T) {
	m := newTestModel(t)
	l := newTeaLoop(t, m)

	job := newFakeJob()
	l.exec(l.m.beginProvision("Creating web", job.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))
	job.write(l, provisionKey(registry.LocalScope, "web"), "TASK [base : Install]\n")

	// esc backs out of the progress screen WITHOUT cancelling the build.
	l.send(tea.KeyPressMsg{Code: tea.KeyEsc})
	if l.m.view != viewBoard {
		t.Fatalf("esc during a build should return to the list, got view %v", l.m.view)
	}
	if !l.m.jobs.isRunning(registry.LocalScope, "web") {
		t.Fatal("leaving the progress screen must NOT cancel the build")
	}

	// And a second VM can be started right away.
	l.send(runeKey('n'))
	if l.m.view != viewForm {
		t.Fatalf("'n' during a build should open the create form, got view %v", l.m.view)
	}

	// The still-running job keeps streaming into its own buffer while the user is
	// elsewhere, and reopening its log shows it.
	job.write(l, provisionKey(registry.LocalScope, "web"), "TASK [base : Node]\n")
	l.m.view = viewBoard
	l.m.focusVM.Name = "web"
	l.send(runeKey('l'))
	if l.m.view != viewProgress {
		t.Fatalf("'l' should reopen the run's log, got view %v", l.m.view)
	}
	if !strings.Contains(l.m.viewport.View(), "Node") {
		t.Fatalf("the reopened log should show output streamed while the user was away:\n%s", l.m.viewport.View())
	}

	job.done <- nil
	l.pump("web to finish", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })
}

// Submitting the create form lands on the BOARD, not on a full-screen Ansible log.
//
// This is the plan's signature moment, and the one the demo is built around: the
// screen does not go dark with a full-screen dump — a tile appears, already
// building, and the board stays live enough to start a second VM. It shipped
// broken past this very suite, because beginStream flipped the view for EVERY
// caller and the tests asserted the flip. So this test drives the real user path —
// `n`, fill, `ctrl+s` — rather than calling beginProvision directly, and asserts on
// what the user is looking at afterwards.
func TestSubmittingTheCreateFormLandsOnTheBoardNotTheLog(t *testing.T) {
	m := newTestModel(t)
	l := newTeaLoop(t, m)

	l.send(runeKey('n'))
	if l.m.view != viewForm {
		t.Fatalf("'n' should open the create form, got view %v", l.m.view)
	}
	for field, value := range map[int]string{
		fName: "web", fHostname: "web-host", fUser: "ada",
		fGitName: "Ada Lovelace", fGitEmail: "ada@example.com",
		fCPUs: "2", fMemory: "2GiB", fDisk: vm.BaseDiskFloor,
	} {
		l.m.inputs[field].SetValue(value)
	}
	l.send(ctrlKey('s'))

	if !l.m.jobs.isRunning(registry.LocalScope, "web") {
		t.Fatal("submitting the form should start the build")
	}
	if l.m.view != viewBoard {
		t.Fatalf("submitting the create form must land on the board, not the full-screen log — got view %v", l.m.view)
	}
	// The build is visible WHERE THE USER IS: the tile says Building, so the log
	// they were not dumped into is not the only sign the VM is coming up.
	if got := l.m.statusOf(registry.LocalScope, vm.VM{Name: "web", Status: "Stopped"}); got != statusBuilding {
		t.Fatalf("the new VM's tile should read Building, got %v", got)
	}
	// And the board is live: a second VM can be started without touching the first.
	l.send(runeKey('n'))
	if l.m.view != viewForm {
		t.Fatalf("the board should stay live during a build ('n' → form), got view %v", l.m.view)
	}
	l.send(tea.KeyPressMsg{Code: tea.KeyEsc})

	// The log is not lost — it is one 'l' away from the tile.
	l.m.view = viewBoard
	l.m.focusVM.Name = "web"
	l.send(runeKey('l'))
	if l.m.view != viewProgress || l.m.progressJob != provisionKey(registry.LocalScope, "web") {
		t.Fatalf("'l' should reopen the build's log (view=%v job=%+v)", l.m.view, l.m.progressJob)
	}

	l.send(ctrlKey('c'))
	l.pump("web to finish cancelling", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })
}

// The reopen-log verb is state-gated on the VM having a run to show: a VM that
// has never been built offers no log and pressing 'l' does nothing.
func TestReopenLogGatedOnARetainedRun(t *testing.T) {
	m := newTestModel(t)
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Running"})

	if strings.Contains(boardVerbs(m), "l log") {
		t.Fatalf("a VM with no run must not offer the log verb, got:\n%s", boardVerbs(m))
	}
	m, cmd := pressDispatch(t, m, runeKey('l'))
	if m.view != viewBoard || cmd != nil {
		t.Fatalf("'l' with no retained run should be a silent no-op (view=%v cmd=%v)", m.view, cmd)
	}
}

// A FILE TRANSFER MUST NEVER DESTROY A RETAINED FAILED BUILD.
//
// The registry retains a failed provision precisely so the tile can keep saying
// Failed and the log can still explain why — Lima has no idea Ansible ever ran and
// goes on calling the half-built VM "Running", which is the whole reason
// deriveStatus consults the registry FIRST. Upload is offered on that tile (its
// enabledFor asks Lima, and Lima says Running), so the user can absolutely press
// `u` on it — and a registry with one slot per VM let that copy EVICT the failed
// build: the tile fell back to Lima and went green, and the Ansible log went with
// it. Unrecoverable for the session, and the exact lie the status derivation
// exists to prevent.
func TestATransferNeverEvictsARetainedFailedBuild(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 100, 30)
	l := newTeaLoop(t, m)

	build := newFakeJob()
	l.exec(l.m.beginProvision("Creating web", build.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))
	build.write(l, provisionKey(registry.LocalScope, "web"), "TASK [base : Install Docker] ***\nfatal: [web]: FAILED! => the play exploded\n")
	build.done <- errAnsibleBoom
	l.pump("the build to fail", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })

	// Lima reports the half-built VM as Running; it always does.
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "web", Status: "Running"}}})
	web, _ := l.m.lookupVM(registry.LocalScope, "web")
	if got := l.m.statusOf(registry.LocalScope, web); got != statusFailed {
		t.Fatalf("precondition: a failed build's tile must read Failed, got %v", got)
	}

	// The user uploads a file to it.
	upload := newFakeJob()
	uploadCmd, started := l.m.beginStream(transferKey(registry.LocalScope, "web"), "Uploading notes.txt", upload.stream)
	// beginStream starts a job; it does not pick a screen. Mirror what the real
	// caller (confirmDest, transfer.go) does after it, so this test exercises the
	// transfer as the user gets it — a build would NOT do this, which is the whole
	// difference between the two callers.
	if started {
		l.m.focusJob(transferKey(registry.LocalScope, "web"))
	}
	l.exec(uploadCmd)

	// The transfer gets its own progress surface…
	if l.m.view != viewProgress {
		t.Fatalf("a transfer should open its own progress screen, got view %v", l.m.view)
	}
	if s, ok := l.m.shownJob(); !ok || s.Title != "Uploading notes.txt" || !s.Running() {
		t.Fatalf("the progress screen should show the running transfer, got %+v (ok=%v)", s, ok)
	}
	// … and the failed build is untouched underneath it.
	if got := l.m.statusOf(registry.LocalScope, web); got != statusFailed {
		t.Fatalf("a file copy must not flip a failed build's tile back to green: statusOf(web) = %v, want Failed", got)
	}
	// The reopen-log verb still reaches the Ansible failure — the only record of WHY.
	l.m.showJobLog(registry.LocalScope, "web")
	s, ok := l.m.shownJob()
	if !ok || !strings.Contains(s.Output, "the play exploded") {
		t.Fatalf("`l` must still reopen the failed build's log, got (ok=%v):\n%s", ok, s.Output)
	}

	upload.done <- nil
	l.pump("the upload to finish", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })

	// And the tile STILL says Failed once the copy is done: only the user acting on
	// the VM (a reset, a delete) clears that.
	if got := l.m.statusOf(registry.LocalScope, web); got != statusFailed {
		t.Fatalf("after the copy finished, statusOf(web) = %v, want Failed", got)
	}
}

// mustNotRun is a provisionFunc that fails the test if it is ever called: the
// refused create must not merely be un-recorded, it must not RUN.
func mustNotRun(t *testing.T) provisionFunc {
	return func(context.Context, vm.CreateConfig, io.Writer) error {
		t.Error("a create refused because the VM already has a run in flight must never run")
		return nil
	}
}

// BEGINSTREAM REFUSES A SECOND RUN FOR A VM THAT ALREADY HAS ONE — and beginJob
// used to mark the job it did not start as a provision ANYWAY, mutating the run
// that WAS in flight. Consequence (a): a file upload in flight gets PROMOTED to a
// build. Its tile renders Building with an Ansible progress bar for a `limactl
// copy`, Delete is disabled on a perfectly healthy VM, a failed copy paints the
// tile red — the exact lie deriveStatus promises never to tell — and on success
// the copy is recorded in the managed index under a config the VM was never built
// from, seeding its GH_TOKEN from a form the user filled in for a different VM.
func TestARefusedCreateDoesNotPromoteAnInFlightTransfer(t *testing.T) {
	m := newTestModel(t)
	l := newTeaLoop(t, m)

	upload := newFakeJob()
	uploadCmd, _ := l.m.beginStream(transferKey(registry.LocalScope, "web"), "Uploading notes.txt", upload.stream)
	l.exec(uploadCmd)
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "web", Status: "Running"}}})

	// The user presses n and types the name of the VM being copied to. Nothing
	// rejects that — vm.CreateConfig.Validate is a pure value check and cannot see
	// the job registry — so the form submits and beginProvision is called.
	wrong := vm.CreateConfig{Name: "web", BaseName: "other-base", CPUs: 8, CloneToken: "ghp_secret"}
	l.exec(l.m.beginProvision("Creating web", mustNotRun(t), wrong))

	web, _ := l.m.lookupVM(registry.LocalScope, "web")
	if got := l.m.statusOf(registry.LocalScope, web); got != statusRunning {
		t.Fatalf("a file copy must never render as a build: statusOf(web) = %v, want Running", got)
	}
	if l.m.vmBuilding(registry.LocalScope, "web") {
		t.Fatal("a copy in flight must not gate Delete the way a build does")
	}
	if cfg, _ := l.m.jobs.config(registry.LocalScope, "web"); cfg.Name != "" {
		t.Fatalf("the refused create's config must not be attached to the copy in flight, got %+v", cfg)
	}

	upload.done <- nil
	l.pump("the upload to finish", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })
}

// Consequence (b) of the same bug: a create for a VM that is ALREADY BUILDING
// swapped the running build's config for the second form's. On success
// RecordSuccess then recorded the WRONG cpus/memory/clone-URL/token for the VM
// that was actually built, and a later Reset would rebuild it from that config.
func TestASecondCreateDoesNotSwapTheRunningBuildsConfig(t *testing.T) {
	m := newTestModel(t)
	l := newTeaLoop(t, m)

	build := newFakeJob()
	real := vm.CreateConfig{Name: "web", BaseName: "sandbar-base", CPUs: 2, Memory: "4GiB", CloneURL: "https://github.com/acme/real"}
	l.exec(l.m.beginProvision("Creating web", build.run, real))
	build.write(l, provisionKey(registry.LocalScope, "web"), "==> Cloning web from base image\n")

	wrong := vm.CreateConfig{Name: "web", BaseName: "other-base", CPUs: 8, Memory: "32GiB", CloneURL: "https://github.com/acme/wrong", CloneToken: "ghp_secret"}
	l.exec(l.m.beginProvision("Creating web", mustNotRun(t), wrong))

	if got := l.m.jobs.names(registry.LocalScope); len(got) != 1 {
		t.Fatalf("the refused create must not register a second run for web, got %v", got)
	}
	got, ok := l.m.jobs.config(registry.LocalScope, "web")
	if !ok {
		t.Fatal("the build in flight should still carry its own config")
	}
	if got.CPUs != real.CPUs || got.Memory != real.Memory || got.BaseName != real.BaseName ||
		got.CloneURL != real.CloneURL || got.CloneToken != "" {
		t.Fatalf("the running build's config was swapped for the refused form's: %+v", got)
	}
	if !strings.Contains(l.m.lastMessage(), "in flight") {
		t.Fatalf("the user must be told the create was refused, got %q", l.m.lastMessage())
	}

	build.done <- nil
	l.pump("the build to finish", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })
}

// THE FORM REFUSES A NAME THAT IS ALREADY BUSY, and says so with the name still on
// screen to edit. vm.CreateConfig.Validate is a pure value check and cannot see the
// job registry, so nothing used to stop a create for a VM that was already building
// — it submitted, beginStream refused the second run, and the second form's config
// was stamped onto the FIRST one's build. This is where that is caught, because it
// is the only place the user can still fix it.
func TestCreateFormRefusesANameWithARunInFlight(t *testing.T) {
	m := newTestModel(t)
	building := vm.CreateConfig{Name: "web", BaseName: "sandbar-base", CPUs: 2, Memory: "4GiB"}
	seedJob(t, &m, "web", building)
	m.view = viewBoard

	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)
	if m.view != viewForm {
		t.Fatalf("precondition: 'n' should open the create form, got view %v", m.view)
	}
	// Name it after the VM that is already building; fill in what Validate needs.
	m.inputs[fName].SetValue("web")
	m.inputs[fGitName].SetValue("Ada Lovelace")
	m.inputs[fGitEmail].SetValue("ada@example.com")
	m.inputs[fCPUs].SetValue("8")

	submitted, cmd := m.Update(ctrlKey('s'))
	m = submitted.(model)

	if m.view != viewForm {
		t.Fatalf("a refused create must keep the form open so the name can be edited, got view %v", m.view)
	}
	if m.formErr == nil || !strings.Contains(m.formErr.Error(), "in flight") {
		t.Fatalf("formErr = %v, want an honest refusal saying web already has a run in flight", m.formErr)
	}
	if cmd != nil {
		t.Fatal("a refused create must dispatch nothing")
	}
	// And the build it collided with is untouched — same config, still the only run.
	if got := m.jobs.names(registry.LocalScope); len(got) != 1 {
		t.Fatalf("the refused create must not register a run, got %v", got)
	}
	if got, _ := m.jobs.config(registry.LocalScope, "web"); got != building {
		t.Fatalf("the running build's config = %+v, want it untouched (%+v)", got, building)
	}
}

// A COPY THAT FINISHES MUST NOT BE MISTAKEN FOR THE BUILD THAT BUILT THE VM.
//
// A VM can now hold both runs at once, so the provisionDoneMsg handler can be
// handed a COPY's done message for a VM whose BUILD is sitting right there in the
// registry, config and all. Keying "was this a provision?" off the presence of a
// config would then re-record the VM as managed and re-seed its GH_TOKEN every time
// a file finished copying — a write to the host secrets store, triggered by a
// `limactl copy`. The KIND is what answers that question.
func TestACompletedCopyIsNotRecordedAsABuild(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 100, 30)
	l := newTeaLoop(t, m)

	// web was built by sand, with a clone token, and the build has finished.
	build := newFakeJob()
	cfg := vm.CreateConfig{Name: "web", BaseName: "sandbar-base", CPUs: 2, CloneToken: "ghp_secret"}
	l.exec(l.m.beginProvision("Creating web", build.run, cfg))
	build.done <- nil
	l.pump("the build to finish", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "web", Status: "Running"}}})
	if !l.m.reg.IsManaged("web") {
		t.Fatal("precondition: a successful build records the VM as managed")
	}

	// Its GH_TOKEN is dropped from the store (the user cleared it, say). A file copy
	// finishing must not put it back — the copy did not build anything.
	if err := l.m.sec.Set("web", registry.LocalScope, map[string]string{}); err != nil {
		t.Fatalf("clear secrets: %v", err)
	}

	upload := newFakeJob()
	uploadCmd, started := l.m.beginStream(transferKey(registry.LocalScope, "web"), "Uploading notes.txt", upload.stream)
	if !started {
		t.Fatal("a copy must start on a VM whose build has finished")
	}
	l.exec(uploadCmd)
	upload.done <- nil
	l.pump("the copy to finish", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })

	if got := l.m.sec.Get("web", registry.LocalScope); got["GH_TOKEN"] != "" {
		t.Fatalf("a finished copy re-seeded the build's clone token into the secrets store: %v", got)
	}
}

// WHICH LOG `l` REOPENS when a VM holds two of them. A FAILED build wins outright
// (TestATransferNeverEvictsARetainedFailedBuild pins that half — it is the run the
// red tile is talking about). Otherwise the most recent run wins: a build that
// SUCCEEDED is settled history, and the copy the user just ran is what "show me
// this VM's log" means.
func TestReopenLogShowsTheMostRecentRunWhenTheBuildSucceeded(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 100, 30)
	l := newTeaLoop(t, m)

	build := newFakeJob()
	l.exec(l.m.beginProvision("Creating web", build.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))
	build.write(l, provisionKey(registry.LocalScope, "web"), "TASK [base : Install Docker] ***\n")
	build.done <- nil
	l.pump("the build to finish", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "web", Status: "Running"}}})

	upload := newFakeJob()
	uploadCmd, _ := l.m.beginStream(transferKey(registry.LocalScope, "web"), "Uploading notes.txt", upload.stream)
	l.exec(uploadCmd)
	upload.write(l, transferKey(registry.LocalScope, "web"), "copied notes.txt\n")
	upload.done <- nil
	l.pump("the copy to finish", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })

	l.m.view = viewBoard
	l.m.showJobLog(registry.LocalScope, "web")
	s, ok := l.m.shownJob()
	if !ok || !strings.Contains(s.Output, "copied notes.txt") {
		t.Fatalf("`l` should reopen the most recent run once the build has succeeded, got (ok=%v):\n%s", ok, s.Output)
	}
	// Both runs are still retained — the copy did not evict the build.
	if _, ok := l.m.jobs.snapshot(provisionKey(registry.LocalScope, "web")); !ok {
		t.Fatal("the succeeded build must still be retained alongside the copy")
	}
}

// A RESET DELETES ITS OWN VM AND CLONES IT BACK, and the refresh tick that fires
// during that window must not read the gap as "this VM disappeared".
//
// It did, and the cost was permanent: manage.Reconcile dropped the absent VM from
// the managed index and the handler then called sec.Remove on it, ERASING THE
// USER'S HOST-STORED SECRETS — including the GH_TOKEN seeded at create. The reset
// would finish, rebuild the VM, and apply an empty secrets.env to it, so git auth
// inside the rebuilt sandbox stopped working with nothing on screen to say why.
//
// jobs.reconcile already exempted exactly this case (`!j.seen || j.recreates`).
// The managed index and the secrets store did not. Unreachable before the board
// went live, because a reset used to pin the user to the progress screen and no
// list refresh ran underneath it.
func TestARefreshDuringAResetDoesNotWipeTheVMsSecrets(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Running"})
	if err := m.sec.Set("web", registry.LocalScope, map[string]string{"GH_TOKEN": "ghp_precious"}); err != nil {
		t.Fatalf("seed the VM's secrets: %v", err)
	}

	l := newTeaLoop(t, m)
	job := newFakeJob()
	l.exec(l.m.beginReset("Resetting web", job.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))
	if !l.m.jobs.isRunning(registry.LocalScope, "web") {
		t.Fatal("precondition: the reset should be in flight")
	}

	// The reset has deleted the instance; the refresh tick lists Lima and web is gone.
	l.send(vmsLoadedMsg{vms: []vm.VM{}})

	if got := l.m.sec.Get("web", registry.LocalScope)["GH_TOKEN"]; got != "ghp_precious" {
		t.Fatalf("the reset's own delete must not erase the VM's secrets: GH_TOKEN = %q, want it intact", got)
	}
	if !l.m.reg.IsManaged("web") {
		t.Fatal("a VM mid-reset must stay managed — its absence from limactl list is expected, not a disappearance")
	}
	if l.m.jobs.isRunning(registry.LocalScope, "web") {
		job.done <- nil
		l.pump("the reset to finish", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })
	}
}

// NO VERB MAY DISRUPT A BUILD. Lima reports a provisioning VM as `Running` — the
// guest is booted, Ansible is executing inside it — so every gate that reads only
// vm.Status offers a verb that would kill the build it is watching: `x stop` and
// `r restart` stop the VM mid-Ansible, `X` sweeps it into stop-all, `u`/`g` copy
// files into a VM a reset is about to destroy, and `s start` even fires on a VM
// Lima has never heard of (a create's clone has not landed, so the record is
// synthetic with an empty Status).
//
// Only Delete consulted the job registry. The rest were protected by ACCIDENT: the
// old full-screen progress view froze the keyboard for the whole build, so no key
// could reach them. This plan removed that freeze on purpose — it is the headline
// feature — and these gates are what replaces it.
func TestNoVerbCanDisruptABuild(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 200, 40)
	l := newTeaLoop(t, m)

	job := newFakeJob()
	l.exec(l.m.beginProvision("Creating web", job.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))
	// Lima reports the provisioning guest as Running — this is the whole trap.
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "web", Status: "Running", CPUs: 2}}})
	l.m.focusVM.Name = "web"

	web, _ := l.m.lookupVM(registry.LocalScope, "web")
	if got := l.m.statusOf(registry.LocalScope, web); got != statusBuilding {
		t.Fatalf("precondition: the tile must read Building, got %v", got)
	}
	if web.Status != limaRunning {
		t.Fatalf("precondition: Lima must report the building VM as Running, got %q", web.Status)
	}

	// Not advertised…
	verbs := boardVerbs(l.m)
	for _, v := range []string{"x stop", "r restart", "s start", "R reset", "u upload", "g download", "S shell"} {
		if strings.Contains(verbs, v) {
			t.Errorf("a building VM must not advertise %q:\n%s", v, verbs)
		}
	}
	// …and not dispatched.
	for _, k := range []rune{'x', 'r', 's', 'R', 'u', 'g', 'S'} {
		after, cmd := pressDispatch(t, l.m, runeKey(k))
		if cmd != nil {
			t.Errorf("%q on a building VM must dispatch nothing", k)
		}
		if after.acting || after.view != viewBoard {
			t.Errorf("%q on a building VM must change nothing (acting=%v view=%v)", k, after.acting, after.view)
		}
	}
	// And stop-all must not sweep it up.
	if got := l.m.stopAllTargets(); len(got) != 0 {
		t.Errorf("stop-all must not target a VM mid-build, got %v", got)
	}

	job.done <- nil
	l.pump("the build to finish", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })
}

// A VM DELETED OUTSIDE SAND MUST LOSE ITS TILE. The job registry retains every run
// for the whole session, and the roster admitted any VM with a provision job — so a
// VM created in sand and then deleted with `limactl delete` kept its tile forever:
// Reconcile dropped it from the managed index, but the retained SUCCEEDED job kept
// re-admitting it. The tile rendered from a synthetic record ("○ Stopped · disk ?/?
// · never used") and every verb on it failed with "instance not found", with no way
// to clear it short of restarting sand.
//
// A succeeded build's VM is in the managed index — that is what success means — so
// the roster does not need the job to vouch for it. A failed or in-flight one still
// does, and still gets its tile.
func TestAVMDeletedOutsideSandLosesItsTile(t *testing.T) {
	m := newTestModel(t)
	l := newTeaLoop(t, m)

	job := newFakeJob()
	l.exec(l.m.beginProvision("Creating web", job.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))
	job.done <- nil
	l.pump("the build to succeed", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "web", Status: "Running"}}})

	if !l.m.reg.IsManaged("web") {
		t.Fatal("precondition: a succeeded build should record its VM as managed")
	}
	if len(l.m.boardVMs()) != 1 {
		t.Fatalf("precondition: the built VM should have a tile, got %v", boardNames(l.m))
	}

	// The user deletes it from another terminal. Lima no longer reports it.
	l.send(vmsLoadedMsg{vms: []vm.VM{}})

	if got := boardNames(l.m); len(got) != 0 {
		t.Fatalf("a VM deleted outside sand must lose its tile, got %v (the retained succeeded job re-admitted it)", got)
	}
	// The run's log is still retained — that is not what was wrong.
	if _, ok := l.m.jobs.snapshot(provisionKey(registry.LocalScope, "web")); !ok {
		t.Fatal("the run itself should still be retained; only the ROSTER must stop counting it")
	}
}

// The mirror: a FAILED build still gets a tile even with no Lima record at all —
// that tile is the only place its failure is reported and its log reopened.
func TestAFailedBuildKeepsItsTileWithNoLimaRecord(t *testing.T) {
	m := newTestModel(t)
	l := newTeaLoop(t, m)

	job := newFakeJob()
	l.exec(l.m.beginProvision("Creating web", job.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))
	job.done <- errAnsibleBoom
	l.pump("the build to fail", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })
	l.send(vmsLoadedMsg{vms: []vm.VM{}})

	if got := boardNames(l.m); len(got) != 1 || got[0] != "web" {
		t.Fatalf("a failed build must keep its tile — it is the only report of the failure, got %v", got)
	}
}

// A CLONE BREAKS `limactl list` FOR THE WHOLE MINUTE IT RUNS (lima-vm/lima#5236):
// the instance directory exists before its lima.yaml does, and limactl aborts the
// listing on the first instance it cannot load rather than skipping it. Every
// refresh during a create or a reset therefore fails.
//
// That must not read as a fault. The board keeps the fleet it already has — the
// other VMs have not changed, and the building VM's tile comes from the job
// registry, not from Lima — and the condition is stated ONCE, not on every 5s tick
// for the duration of the build.
func TestAListRacingACloneKeepsTheBoardAndIsSaidOnce(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m,
		vm.VM{Name: "api", Status: "Running"},
		vm.VM{Name: "db", Status: "Stopped"},
	)
	before := len(m.boardVMs())

	raced := vmsLoadedMsg{err: fmt.Errorf("%w: exit status 1", lima.ErrListRacedInstanceDir)}
	for i := 0; i < 5; i++ { // five refresh ticks, as a real clone would produce
		next, _ := m.Update(raced)
		m = next.(model)
	}

	if got := len(m.boardVMs()); got != before {
		t.Fatalf("a failed refresh must not empty the board: %d tiles, want %d", got, before)
	}
	if n := countMessages(m, "lima#5236"); n != 1 {
		t.Fatalf("the pause must be reported once, not %d times — it lasts the whole build", n)
	}
	if countMessages(m, "list failed") != 0 {
		t.Fatalf("a clone window is not a list failure and must not be reported as one:\n%v", m.messages)
	}

	// The clone finishes and listing recovers: the state resets, so a LATER clone
	// says so again rather than staying quiet forever.
	next, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{{Name: "api", Status: "Running"}, {Name: "db", Status: "Stopped"}}})
	m = next.(model)
	if m.members[0].listRace != 0 {
		t.Fatal("a successful list must clear the paused state")
	}
	next, _ = m.Update(raced)
	m = next.(model)
	if n := countMessages(m, "lima#5236"); n != 2 {
		t.Fatalf("a second clone should report the pause again, got %d reports", n)
	}
}

// AND THE SUPPRESSION IS BOUNDED. The signature is an error string; it cannot tell a
// clone in flight from an instance directory that is permanently broken — a killed
// clone leaves exactly the same half-written directory, and `limactl list` then fails
// forever. Suppressed, that would leave sand on an empty board with one stale line
// about a clone that finished hours ago, and no way to find out why.
func TestAPermanentListFailureIsNotHiddenAsACloneWindow(t *testing.T) {
	m := newTestModel(t)
	raced := vmsLoadedMsg{err: fmt.Errorf("%w: unable to load instance ghost: lima.yaml missing", lima.ErrListRacedInstanceDir)}

	for i := 0; i < listRaceLimit; i++ {
		next, _ := m.Update(raced)
		m = next.(model)
	}

	if n := countMessages(m, "lima#5236"); n != 1 {
		t.Fatalf("the pause itself should still be said once, got %d", n)
	}
	if countMessages(m, "STILL failing") != 1 {
		t.Fatalf("after %d consecutive failures sand must stop believing it is a clone window:\n%v", listRaceLimit, m.messages)
	}
	// And the surfaced message carries limactl's own diagnosis, not just a shrug.
	if countMessages(m, "unable to load instance ghost") != 1 {
		t.Fatalf("the escalation must name the instance limactl cannot load:\n%v", m.messages)
	}
}

// A REAL list failure is still reported, every time. The workaround above is
// narrow on purpose: it must not turn a broken limactl into silence.
func TestARealListFailureIsStillReported(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 2; i++ {
		next, _ := m.Update(vmsLoadedMsg{err: errors.New("limactl list: exit status 127: not found")})
		m = next.(model)
	}
	if n := countMessages(m, "list failed"); n != 2 {
		t.Fatalf("a genuine list failure must be reported every time, got %d", n)
	}
}

// countMessages counts the messages in the ring whose text contains want.
func countMessages(m model, want string) int {
	n := 0
	for _, msg := range m.messages {
		if strings.Contains(msg.text, want) {
			n++
		}
	}
	return n
}

// A RESET THAT FAILED STILL DELETED THE VM — and the secrets must survive that too.
//
// The protection added for a reset in flight only covered RUNNING jobs, so the
// moment the reset FAILED (after it had already deleted the instance and before it
// could clone it back), the next refresh saw the VM missing, called it a
// disappearance, unmanaged it and ERASED ITS HOST SECRETS. The user was then left
// with a red tile they could not retry — `R` needs a managed VM — telling them to
// retry, and a GH_TOKEN that was gone for good.
//
// The absence is still self-inflicted: OUR job deleted the VM. The user can clear
// the state deliberately with `d`, which drops the job, the registry entry and the
// secrets together — and that is the only way it should ever be reachable.
func TestAFAILEDResetDoesNotWipeTheVMsSecrets(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Running"})
	if err := m.sec.Set("web", registry.LocalScope, map[string]string{"GH_TOKEN": "ghp_precious"}); err != nil {
		t.Fatalf("seed secrets: %v", err)
	}

	l := newTeaLoop(t, m)
	job := newFakeJob()
	l.exec(l.m.beginReset("Resetting web", job.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))

	// The reset deletes the instance, then FAILS before cloning it back.
	job.done <- errAnsibleBoom
	l.pump("the reset to fail", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web") })

	// The refresh lands: web is gone from Lima, because the reset deleted it.
	l.send(vmsLoadedMsg{vms: []vm.VM{}})

	if got := l.m.sec.Get("web", registry.LocalScope)["GH_TOKEN"]; got != "ghp_precious" {
		t.Fatalf("a FAILED reset must not erase the VM's secrets: GH_TOKEN = %q, want it intact", got)
	}
	if !l.m.reg.IsManaged("web") {
		t.Fatal("a VM whose reset failed must stay managed — otherwise R is hidden and the user cannot retry the very thing the red tile is telling them to")
	}
	if _, ok := l.m.jobs.snapshot(provisionKey(registry.LocalScope, "web")); !ok {
		t.Fatal("the failed run must be retained so the tile can report it")
	}

	// And `d` still clears everything deliberately.
	l.m.focusVM.Name = "web"
	l.send(actionDoneMsg{action: "delete", name: "web"})
	if got := l.m.sec.Get("web", registry.LocalScope)["GH_TOKEN"]; got != "" {
		t.Fatalf("deleting the VM must take its secrets with it, got %q", got)
	}
}

// ^C on a building VM now DELETES the half-built instance (the create cleans up
// after itself — internal/provision/cleanup.go), so the VM is gone from `limactl
// list`. The board must not keep a tile for it: it rendered from a synthetic
// record whose status is "", which the tile paints as "○ Stopped" — a VM that does
// not exist, reported as merely stopped, with every verb on it doomed to fail and
// `d` with nothing to delete.
func TestCanceledBuildWhoseVMIsGoneLeavesNoTile(t *testing.T) {
	m := newTestModel(t)
	l := newTeaLoop(t, m)

	job := newFakeJob()
	l.exec(l.m.beginProvision("Creating web", job.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))
	job.write(l, provisionKey(registry.LocalScope, "web"), "TASK [base : Install]\n")
	if !l.m.jobs.isRunning(registry.LocalScope, "web") {
		t.Fatal("precondition: web must be mid-build")
	}

	// ^C, and the run reports back cancelled.
	l.m.jobs.cancelJob(provisionKey(registry.LocalScope, "web"))
	l.send(provisionDoneMsg{job: provisionKey(registry.LocalScope, "web"), err: context.Canceled})

	// The refresh that follows: the VM is gone — the create cleaned it up.
	l.send(vmsLoadedMsg{vms: nil})

	for _, v := range l.m.boardVMs() {
		if v.Name == "web" {
			t.Fatalf("a cancelled build whose VM was cleaned up still has a tile (status %q) — limactl has never heard of it", v.Status)
		}
	}
}

// …but a ^C during the PLAYBOOK leaves a booted, half-provisioned VM behind (the
// cleanup deliberately keeps it: it exists, and its lima.yaml is valid). That one
// must keep its tile — it is real, it is not managed, and the tile is how `d`
// clears it.
func TestCanceledBuildWhoseVMSurvivesKeepsItsTile(t *testing.T) {
	m := newTestModel(t)
	l := newTeaLoop(t, m)

	job := newFakeJob()
	l.exec(l.m.beginProvision("Creating web", job.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))
	job.write(l, provisionKey(registry.LocalScope, "web"), "TASK [base : Install]\n")

	l.m.jobs.cancelJob(provisionKey(registry.LocalScope, "web"))
	l.send(provisionDoneMsg{job: provisionKey(registry.LocalScope, "web"), err: context.Canceled})

	// The VM booted before the ^C, so it is still there.
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "web", Status: "Running"}}})

	found := false
	for _, v := range l.m.boardVMs() {
		if v.Name == "web" {
			found = true
		}
	}
	if !found {
		t.Fatal("a cancelled build whose VM survived lost its tile — it exists, it is unmanaged, and the tile is the only way to delete it")
	}
}
