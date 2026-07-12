package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

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

// write feeds one chunk into the job's stream and waits until the model has
// folded it into that VM's log — so a test can assert on ordering.
func (f *fakeJob) write(l *teaLoop, name, chunk string) {
	l.t.Helper()
	select {
	case f.out <- chunk:
	case <-time.After(5 * time.Second):
		l.t.Fatalf("%s: nobody read the chunk %q", name, chunk)
	}
	l.pump("output of "+name, func(m model) bool {
		s, ok := m.jobs.snapshot(name)
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
	if !m.jobs.begin(&job{
		name:   name,
		title:  "Creating " + name,
		back:   viewList,
		state:  jobRunning,
		cancel: func() {},
	}) {
		t.Fatalf("seedJob: %s already has a job in flight", name)
	}
	m.jobs.markProvision(name, cfg, false)
	m.progressVM = name
	m.view = viewProgress
}

// jobOutput is the job registry's accumulated log for name.
func jobOutput(m model, name string) string {
	s, ok := m.jobs.snapshot(name)
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
	cmdA := l.m.beginProvision("Creating alpha", alpha.run, vm.CreateConfig{Name: "alpha", BaseName: "claude-base"})
	l.exec(cmdA)
	cmdB := l.m.beginProvision("Creating beta", beta.run, vm.CreateConfig{Name: "beta", BaseName: "claude-base"})
	l.exec(cmdB)

	if !l.m.jobs.isRunning("alpha") || !l.m.jobs.isRunning("beta") {
		t.Fatal("both provisions should be in flight at once")
	}
	// Neither VM exists in `limactl list` yet — a create's clone lands minutes into
	// its own build — so the registry is the only thing that knows they are being
	// built at all. The board reads them from here to raise their tiles.
	if got := l.m.jobs.names(); len(got) != 2 {
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
					s, ok := reg.snapshot(n)
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
	alpha.write(l, "alpha", "TASK [base : alpha-one]\n")
	beta.write(l, "beta", "TASK [base : beta-one]\n")
	alpha.write(l, "alpha", "TASK [base : alpha-two]\n")
	beta.write(l, "beta", "TASK [base : beta-two]\n")

	outA, outB := jobOutput(l.m, "alpha"), jobOutput(l.m, "beta")
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
	sa, _ := l.m.jobs.snapshot("alpha")
	if sa.Progress.Task != "alpha-two" || sa.Progress.Index != 2 {
		t.Fatalf("alpha's parsed progress = %+v, want task alpha-two at index 2", sa.Progress)
	}
	sb, _ := l.m.jobs.snapshot("beta")
	if sb.Progress.Task != "beta-two" || sb.Progress.Index != 2 {
		t.Fatalf("beta's parsed progress = %+v, want task beta-two at index 2", sb.Progress)
	}

	// CANCELLATION IS PER-JOB. Reopen alpha's log (the retained-run verb) so the
	// progress screen targets alpha, then ctrl+c: only alpha may be cancelled.
	l.m.view = viewDetail
	l.m.detail = vm.VM{Name: "alpha", Status: "Running"}
	l.send(runeKey('l'))
	if l.m.view != viewProgress || l.m.progressVM != "alpha" {
		t.Fatalf("reopening alpha's log should show it in the progress view (view=%v vm=%q)", l.m.view, l.m.progressVM)
	}
	l.send(ctrlKey('c'))

	l.pump("alpha to finish cancelling", func(m model) bool { return !m.jobs.isRunning("alpha") })

	if !l.m.jobs.isRunning("beta") {
		t.Fatal("cancelling alpha must not touch beta")
	}
	if s, _ := l.m.jobs.snapshot("alpha"); !s.Canceled {
		t.Fatalf("alpha should be marked cancelled, got %+v", s)
	}
	// A cancelled run leaves partial state: it is not recorded as managed, and it
	// is not a failure (so the tile must not go red for it).
	if l.m.reg.IsManaged("alpha") {
		t.Fatal("a cancelled provision must not be recorded as managed")
	}
	if got := l.m.statusOf(vm.VM{Name: "alpha", Status: "Running"}); got != statusRunning {
		t.Fatalf("a cancelled job's VM should fall back to Lima's status, got %v", got)
	}

	// beta is untouched and still streaming — its buffer never saw alpha's cancel.
	beta.write(l, "beta", "TASK [base : beta-three]\n")
	if strings.Contains(jobOutput(l.m, "beta"), "^C") {
		t.Fatalf("alpha's cancel notice leaked into beta's buffer:\n%s", jobOutput(l.m, "beta"))
	}

	// beta finishes cleanly: it (and only it) is recorded as managed.
	beta.done <- nil
	l.pump("beta to finish", func(m model) bool { return !m.jobs.isRunning("beta") })
	if !l.m.reg.IsManaged("beta") {
		t.Fatal("a successful provision should be recorded as managed")
	}
	if s, _ := l.m.jobs.snapshot("beta"); s.State != jobSucceeded {
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
	l.exec(l.m.beginProvision("Creating web", fail.run, vm.CreateConfig{Name: "web", BaseName: "claude-base"}))
	fail.write(l, "web", "TASK [dev-tools : Install Docker]\nfatal: [localhost]: FAILED!\n")

	fail.done <- errors.New("provisioning (base) failed for \"web\"")
	l.pump("web to fail", func(m model) bool { return !m.jobs.isRunning("web") })

	v := vm.VM{Name: "web", Status: "Running"} // Lima still calls the half-built VM Running
	if got := l.m.statusOf(v); got != statusFailed {
		t.Fatalf("a failed provision must derive Failed, got %v", got)
	}

	// The refresh tick that would have dropped it.
	l.send(vmsLoadedMsg{vms: []vm.VM{v}})

	s, ok := l.m.jobs.snapshot("web")
	if !ok {
		t.Fatal("a failed job must survive a list refresh — dropping it turns the tile green")
	}
	if s.State != jobFailed || s.Err == nil {
		t.Fatalf("the retained job should still carry its failure, got %+v", s)
	}
	if !strings.Contains(s.Output, "FAILED!") {
		t.Fatalf("the retained job should still carry its log, got %q", s.Output)
	}
	if got := l.m.statusOf(v); got != statusFailed {
		t.Fatalf("Failed must stay sticky across a refresh, got %v", got)
	}
	// And the user can read WHY: the retained run's log is reopenable.
	if !l.m.vmHasRetainedRun("web") {
		t.Fatal("a failed VM must offer its retained log — a red tile with no diagnostic is an alarm with no explanation")
	}

	// Deleting the VM is the user acting on it: the retained run goes with it.
	l.send(actionDoneMsg{action: "delete", name: "web"})
	if _, ok := l.m.jobs.snapshot("web"); ok {
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
	l.exec(l.m.beginStream("web", "Uploading to web", viewDetail, run))
	<-blocked

	// The VM is present at first — that is what makes a later absence evidence of
	// a DISAPPEARANCE rather than of a create whose VM does not exist yet.
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "web", Status: "Running"}}})
	if !l.m.jobs.isRunning("web") {
		t.Fatal("the job should still be running while its VM is present")
	}

	// It vanishes.
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "other", Status: "Running"}}})

	if _, ok := l.m.jobs.snapshot("web"); ok {
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
	l.exec(l.m.beginProvision("Creating web", job.run, vm.CreateConfig{Name: "web", BaseName: "claude-base"}))
	job.write(l, "web", "==> Building base image\n")

	// Several refreshes in which the VM being built does not exist yet.
	for i := 0; i < 3; i++ {
		l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "unrelated", Status: "Running"}}})
	}
	if !l.m.jobs.isRunning("web") {
		t.Fatal("a create whose VM has not appeared yet must NOT be reaped — that would kill every build")
	}

	job.done <- nil
	l.pump("web to finish", func(m model) bool { return !m.jobs.isRunning("web") })
}

// A reset deletes and re-creates its own VM, so its VM legitimately vanishes
// from `limactl list` mid-run. The reaper must not mistake that for a VM
// deleted out from under a build.
func TestResetJobSurvivesItsOwnDelete(t *testing.T) {
	m := newTestModel(t)
	l := newTeaLoop(t, m)

	job := newFakeJob()
	l.exec(l.m.beginReset("Resetting web", job.run, vm.CreateConfig{Name: "web", BaseName: "claude-base"}))
	job.write(l, "web", "==> Deleting web\n")

	// Present, then gone (the reset just deleted it), then back (the re-clone).
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "web", Status: "Running"}}})
	l.send(vmsLoadedMsg{vms: []vm.VM{}})
	if !l.m.jobs.isRunning("web") {
		t.Fatal("a reset must survive the deletion it performs itself")
	}
	l.send(vmsLoadedMsg{vms: []vm.VM{{Name: "web", Status: "Running"}}})
	if !l.m.jobs.isRunning("web") {
		t.Fatal("a reset must survive its own delete/re-clone cycle")
	}

	job.done <- nil
	l.pump("web to finish", func(m model) bool { return !m.jobs.isRunning("web") })
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
	l.exec(l.m.beginProvision("Creating web", job.run, vm.CreateConfig{Name: "web", BaseName: "claude-base"}))
	job.write(l, "web", "TASK [base : Install]\n")

	// esc backs out of the progress screen WITHOUT cancelling the build.
	l.send(tea.KeyPressMsg{Code: tea.KeyEsc})
	if l.m.view != viewList {
		t.Fatalf("esc during a build should return to the list, got view %v", l.m.view)
	}
	if !l.m.jobs.isRunning("web") {
		t.Fatal("leaving the progress screen must NOT cancel the build")
	}

	// And a second VM can be started right away.
	l.send(runeKey('n'))
	if l.m.view != viewForm {
		t.Fatalf("'n' during a build should open the create form, got view %v", l.m.view)
	}

	// The still-running job keeps streaming into its own buffer while the user is
	// elsewhere, and reopening its log shows it.
	job.write(l, "web", "TASK [base : Node]\n")
	l.m.view = viewDetail
	l.m.detail = vm.VM{Name: "web", Status: "Running"}
	l.send(runeKey('l'))
	if l.m.view != viewProgress {
		t.Fatalf("'l' should reopen the run's log, got view %v", l.m.view)
	}
	if !strings.Contains(l.m.viewport.View(), "Node") {
		t.Fatalf("the reopened log should show output streamed while the user was away:\n%s", l.m.viewport.View())
	}

	job.done <- nil
	l.pump("web to finish", func(m model) bool { return !m.jobs.isRunning("web") })
}

// The reopen-log verb is state-gated on the VM having a run to show: a VM that
// has never been built offers no log and pressing 'l' does nothing.
func TestReopenLogGatedOnARetainedRun(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDetail
	m.detail = vm.VM{Name: "claude", Status: "Running"}

	if strings.Contains(plainHelp(m.detailView()), "l log") {
		t.Fatalf("a VM with no run must not offer the log verb, got:\n%s", plainHelp(m.detailView()))
	}
	after, cmd := m.Update(runeKey('l'))
	m = after.(model)
	if m.view != viewDetail || cmd != nil {
		t.Fatalf("'l' with no retained run should be a silent no-op (view=%v cmd=%v)", m.view, cmd)
	}
}
