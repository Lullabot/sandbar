package ui

// jobs.go is the VM-keyed job registry: sand's only concurrent subsystem, and
// the source of the board's Building and Failed statuses.
//
// Before it, one reader/output/cancel triple on the model served exactly one job
// and a single model-wide running flag froze every key for the minutes a
// provision took. The registry generalizes that into N jobs keyed by VM name, so
// several provisions (and transfers) can be in flight while the board stays live
// — and so a building VM can render a progress bar on its own tile.
//
// # The concurrency contract
//
// Bubble Tea passes the model BY VALUE and runs Update on a single goroutine, so
// mutable state that must outlive one Update call cannot live on the model: a
// copy would fork it. The registry is therefore a POINTER field on the model and
// guards everything behind its own mutex, so every model copy shares the one
// registry. Exactly two kinds of goroutine touch a job:
//
//   - The Update goroutine begins jobs, folds their output in, finishes,
//     cancels, and reaps them. Every one of those goes through a method here and
//     takes the lock.
//   - The tea.Cmd goroutines — the run function feeding the pipe, and
//     readNextCmd draining it — touch ONLY the io.Pipe, never the registry. A
//     chunk read off the pipe travels back to Update as a VM-keyed
//     provisionOutputMsg, and it is Update, under the lock, that appends and
//     parses it. That is why the parser can be a mutable struct on the job.
//
// Nothing outside this file ever holds a *job. Readers (the progress view today,
// the tile renderer tomorrow) get a jobSnapshot: a value copy taken under the
// lock, safe to render from any goroutine.
//
// # Scope
//
// The LAST RUN PER VM, IN MEMORY. Deliberately not: persistence across restarts,
// a multi-run history, a storage format, pruning, or schema versioning. The
// plan's decision log draws that line.

import (
	"context"
	"strings"
	"sync"

	"github.com/lullabot/sandbar/internal/vm"
)

// jobState is a job's lifecycle state. A finished job — succeeded OR failed —
// stays in the registry with its log: that retention is what makes the tile's
// Failed status sticky, and what lets the user reopen the run that explains it.
type jobState int

const (
	jobRunning jobState = iota
	jobSucceeded
	jobFailed
)

// cancelNotice is appended to a job's log when the user cancels it, so the
// reopened log says why it stops where it does.
const cancelNotice = "\n^C — canceling, cleaning up…\n"

// job is one VM's run: in flight, or retained after it finished. It lives only
// inside the registry, always behind a pointer, and is never copied — which is
// what makes the strings.Builder below safe here even though model.go bans one
// on the model. (A Builder copied after first use panics on the next write; a
// copied model would silently fork the log.)
type job struct {
	name  string
	title string // the progress screen's heading, e.g. `Creating web`
	back  view   // the screen esc returns to from this job's progress view

	state    jobState
	err      error
	canceled bool // the user pressed ctrl+c: partial state they asked for, NOT a failure

	// cfg is the provision config for a create/reset, and the zero value for a
	// transfer. A non-empty cfg.Name is what marks a job a PROVISION — the same
	// distinction the old model.provCfg drew, now per-job: only a provision is
	// recorded in the managed registry on success, and only a provision derives
	// the Building/Failed tile status (see deriveStatus).
	cfg vm.CreateConfig

	// recreates marks a job that deletes and re-creates its own VM (Reset). Such a
	// job's VM legitimately vanishes from `limactl list` mid-run, so the
	// disappeared-VM reaper must not mistake it for a VM deleted out from under a
	// build. See reconcile.
	recreates bool

	// seen records that the job's VM has appeared in a `limactl list` refresh
	// since the job started. A create's VM does not exist for the first minutes of
	// its own build, so absence is only evidence of a DISAPPEARANCE once the VM
	// has been seen present at least once. Without this, the reaper would kill
	// every create on its first refresh.
	seen bool

	output strings.Builder
	parser ansibleParser
	cancel context.CancelFunc
	reader *readPipe
}

// jobSnapshot is a race-free value copy of one job, taken under the registry's
// lock. It is what every reader outside the registry works from.
type jobSnapshot struct {
	VM       string
	Title    string
	Back     view
	State    jobState
	Err      error
	Canceled bool

	// Provision is true for a create/reset — a job that owns this VM's existence —
	// and false for a transfer. It deliberately carries no vm.CreateConfig: the
	// config holds the clone token, and nothing a renderer can reach should.
	Provision bool

	Output   string
	Progress ansibleProgress
}

// Running reports whether the job is still in flight.
func (s jobSnapshot) Running() bool { return s.State == jobRunning }

// Failed reports whether the job ended in a REAL failure. A user cancellation is
// not one: it leaves exactly the partial state the user asked for, so it must not
// paint the VM red.
func (s jobSnapshot) Failed() bool { return s.State == jobFailed && !s.Canceled }

// jobRegistry holds every VM's run, keyed by VM name. The zero value is unusable;
// use newJobRegistry. A nil *jobRegistry is safe to call every method on and
// reports "no jobs", so a model built by hand (as tests do) needs no registry.
type jobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*job
}

func newJobRegistry() *jobRegistry {
	return &jobRegistry{jobs: make(map[string]*job)}
}

// jobLookup (commandreg.go) is the seam task 02 left for this registry: it gates
// Delete while a VM builds, and the reopen-log verb on whether a run exists to
// reopen.
var _ jobLookup = (*jobRegistry)(nil)

// Building reports whether name has a PROVISION in flight — a build, not a file
// transfer. It gates Delete: a VM must not be deleted out from under its own
// build.
func (r *jobRegistry) Building(name string) bool {
	s, ok := r.snapshot(name)
	return ok && s.Running() && s.Provision
}

// HasRetainedRun reports whether name has a run whose log can be reopened. That
// includes a run still IN FLIGHT: the whole point of the un-frozen keyboard is
// that the user can walk away from a build and come back to it, so "reopen this
// VM's log" must work while it is still being written.
func (r *jobRegistry) HasRetainedRun(name string) bool {
	_, ok := r.snapshot(name)
	return ok
}

// begin registers a new job, replacing any run name had retained. It REFUSES to
// replace a job that is still running and reports false: two goroutines sharing
// one key would orphan the first job's cancel func and leak its goroutine, so the
// check and the insert have to be one atomic step rather than a caller's guess.
func (r *jobRegistry) begin(j *job) bool {
	if r == nil || j == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.jobs[j.name]; ok && old.state == jobRunning {
		return false
	}
	r.jobs[j.name] = j
	return true
}

// markProvision attaches a provision's config to the job just begun for it,
// which is what makes it a PROVISION rather than a transfer: only a job carrying
// a config is recorded in the managed registry on success, and only one derives
// the Building/Failed tile status. recreates flags a Reset — a job that deletes
// and re-creates its own VM (see reconcile).
//
// It is a separate step from begin() on purpose: beginStream registers every job
// WITHOUT a config, and only beginProvision/beginReset add one afterwards. That
// is the same ordering the old code relied on (beginStream cleared m.provCfg;
// beginProvision set it after), preserved per-job so a transfer can never be
// mistaken for a build.
func (r *jobRegistry) markProvision(name string, cfg vm.CreateConfig, recreates bool) {
	if r == nil || cfg.Name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[name]
	if !ok || j.state != jobRunning {
		return
	}
	j.cfg = cfg
	j.recreates = recreates
}

// addOutput folds one chunk of a job's stream into its log and its parsed Ansible
// progress. It reports whether the job still exists: a reaped job's late chunks
// are dropped, and the caller stops re-issuing its reader.
func (r *jobRegistry) addOutput(name, chunk string) bool {
	if r == nil || chunk == "" {
		return r.exists(name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[name]
	if !ok {
		return false
	}
	j.output.WriteString(chunk)
	j.parser.feed(chunk)
	return true
}

// finish marks a job done and RETAINS it — with its log — so a failure stays
// visible on the board until the user acts on it. It reports false when the job
// is gone (reaped mid-flight), in which case its done message is stale and the
// caller must do nothing with it.
func (r *jobRegistry) finish(name string, err error) (jobSnapshot, bool) {
	if r == nil {
		return jobSnapshot{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[name]
	if !ok {
		return jobSnapshot{}, false
	}
	switch {
	case j.canceled:
		// A cancelled run did not succeed, but its error is the kill we caused, so
		// it is not reported as a failure (and, per deriveStatus, never reddens the
		// tile). This is the pre-registry behaviour, preserved per-job.
		j.state, j.err = jobFailed, nil
	case err != nil:
		j.state, j.err = jobFailed, err
	default:
		j.state, j.err = jobSucceeded, nil
	}
	j.cancel = nil // the context is done; drop the func so nothing calls it later
	return snapshotOf(j), true
}

// cancelJob cancels name's in-flight run — and only that one. It reports whether
// a running job was actually cancelled.
func (r *jobRegistry) cancelJob(name string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	j, ok := r.jobs[name]
	if !ok || j.state != jobRunning || j.canceled {
		r.mu.Unlock()
		return false
	}
	j.canceled = true
	j.output.WriteString(cancelNotice)
	cancel := j.cancel
	r.mu.Unlock()

	// Called outside the lock: a cancel func runs arbitrary teardown, and nothing
	// here needs to hold the registry while it does.
	if cancel != nil {
		cancel()
	}
	return true
}

// remove drops a VM's job outright, cancelling it first if it is still running.
// This is "the user acted on it": deleting a VM leaves its retained run with
// nothing to describe.
func (r *jobRegistry) remove(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	var s stopper
	if j, ok := r.jobs[name]; ok {
		s = stopperFor(j)
		delete(r.jobs, name)
	}
	r.mu.Unlock()
	s.stop()
}

// reconcile folds a fresh `limactl list` into the registry: it marks each running
// job's VM as seen while it is present, and REAPS any running job whose VM has
// disappeared — cancelling it and closing its pipe so its goroutine cannot leak.
// It returns the names it reaped.
//
// Two exemptions, both of them the difference between working and catastrophic:
//
//   - A job whose VM has never been seen is NOT reaped. A create's VM does not
//     exist in `limactl list` until its base is built and its clone lands, which
//     is most of the run. Reaping on absence alone would kill every build on its
//     first refresh.
//   - A job that RECREATES its VM (Reset) is not reaped. It deletes the VM itself,
//     by design, and clones it back moments later; that self-inflicted absence is
//     not a disappearance.
//
// Finished jobs are never reaped: a failed run is retained precisely so the board
// keeps saying so.
func (r *jobRegistry) reconcile(present map[string]bool) []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	var reaped []string
	var stopped []stopper
	for name, j := range r.jobs {
		if j.state != jobRunning {
			continue
		}
		if present[name] {
			j.seen = true
			continue
		}
		if !j.seen || j.recreates {
			continue
		}
		stopped = append(stopped, stopperFor(j))
		delete(r.jobs, name) // deleting during a range is defined behaviour
		reaped = append(reaped, name)
	}
	r.mu.Unlock()

	for _, s := range stopped {
		s.stop()
	}
	return reaped
}

// stopper is everything needed to tear a job down, COPIED OUT UNDER THE LOCK.
// Teardown itself must not hold the lock (a cancel func runs arbitrary code), but
// reading a job's fields outside it would be a race against finish() — so the two
// halves are separated rather than reaching back into the *job afterwards.
type stopper struct {
	cancel context.CancelFunc
	reader *readPipe
}

// stopperFor copies a job's teardown handles. Callers hold the lock.
func stopperFor(j *job) stopper {
	return stopper{cancel: j.cancel, reader: j.reader}
}

// stop tears the job down: it cancels the context AND closes the read end of its
// pipe. The close is not belt-and-braces — it is the actual guarantee. Cancelling
// only ASKS the run function to return; closing the reader makes its next write
// fail, so even a run that ignores its context (or is blocked writing to a pipe
// nobody is draining any more) unblocks and its goroutine exits. That is the
// difference between reaping a job and merely forgetting about it.
func (s stopper) stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.reader.close() // nil-safe
}

// snapshot returns a value copy of name's job, and whether it has one.
func (r *jobRegistry) snapshot(name string) (jobSnapshot, bool) {
	if r == nil {
		return jobSnapshot{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[name]
	if !ok {
		return jobSnapshot{}, false
	}
	return snapshotOf(j), true
}

// snapshotOf copies a job for a reader. Callers hold the lock.
func snapshotOf(j *job) jobSnapshot {
	return jobSnapshot{
		VM:        j.name,
		Title:     j.title,
		Back:      j.back,
		State:     j.state,
		Err:       j.err,
		Canceled:  j.canceled,
		Provision: j.cfg.Name != "",
		Output:    j.output.String(),
		Progress:  j.parser.progress,
	}
}

// config returns the provision config of name's job — the create form's input,
// including its clone token. It is deliberately NOT on jobSnapshot: only the
// provisionDoneMsg handler needs it (to record the VM as managed and seed its
// GH_TOKEN), and nothing that renders should be able to reach a token.
func (r *jobRegistry) config(name string) (vm.CreateConfig, bool) {
	if r == nil {
		return vm.CreateConfig{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[name]
	if !ok {
		return vm.CreateConfig{}, false
	}
	return j.cfg, true
}

// reader returns the read end of name's pipe, for the next readNextCmd.
func (r *jobRegistry) reader(name string) *readPipe {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[name]; ok {
		return j.reader
	}
	return nil
}

// isRunning reports whether name has a run in flight (of any kind: a provision or
// a transfer).
func (r *jobRegistry) isRunning(name string) bool {
	s, ok := r.snapshot(name)
	return ok && s.Running()
}

// anyRunning reports whether ANY job is in flight. It drives the spinner: the
// animation must keep ticking while work is happening on any VM, not just on the
// screen the user happens to be looking at.
func (r *jobRegistry) anyRunning() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		if j.state == jobRunning {
			return true
		}
	}
	return false
}

// exists reports whether name has a job at all.
func (r *jobRegistry) exists(name string) bool {
	_, ok := r.snapshot(name)
	return ok
}

// names lists every VM that has a job, in no order. The board (task 08) needs it
// because a VM being CREATED does not appear in `limactl list` until its clone
// lands — minutes into its own build — so a board that walked only the Lima list
// would show nothing at all for exactly the span the user is waiting on, and the
// signature moment of the whole plan (press n, a building tile appears) would not
// happen.
func (r *jobRegistry) names() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.jobs))
	for name := range r.jobs {
		names = append(names, name)
	}
	return names
}

// derivedStatus is a VM's status AS THE BOARD MUST SHOW IT, which is not the
// status Lima reports. Lima has only Running and Stopped: to Lima, a VM being
// provisioned is simply Running, because Ansible is just a process inside it.
// Building and Failed are sand-side states and they come from the job registry.
type derivedStatus int

const (
	statusStopped derivedStatus = iota
	statusRunning
	statusBuilding
	statusFailed
)

// String is the word the tile prints beside its status glyph. Colour is never the
// only carrier of meaning, so this label always accompanies it.
func (s derivedStatus) String() string {
	switch s {
	case statusRunning:
		return "Running"
	case statusBuilding:
		return "Building"
	case statusFailed:
		return "Failed"
	default:
		return "Stopped"
	}
}

// deriveStatus is THE status function — pure, and the only way a VM's status may
// reach a tile. THE JOB REGISTRY IS CONSULTED FIRST; Lima is the fallback.
//
// Rendering v.Status directly is the plan's top-billed failure: a build in flight
// would look identical to a healthy idle VM, and — far worse — a FAILED provision
// would leave a reassuring green "Running" tile, failing quietly at the exact
// moment the user most needs to be told.
//
// Only a PROVISION job moves the VM's status. A file transfer running (or
// failing) against a VM says nothing about the VM's own state: a VM whose upload
// failed is a healthy running VM with a failed copy, and painting its tile red
// would be its own small lie. The transfer's failure surfaces where it belongs —
// on the status line, and in its reopenable log.
func deriveStatus(v vm.VM, job jobSnapshot, hasJob bool) derivedStatus {
	if hasJob && job.Provision {
		switch {
		case job.Running():
			return statusBuilding
		case job.Failed():
			return statusFailed
		}
	}
	if v.Status == "Running" {
		return statusRunning
	}
	return statusStopped
}

// statusOf is deriveStatus with the registry lookup already done — the form the
// tile renderer and the board call.
func (m model) statusOf(v vm.VM) derivedStatus {
	job, ok := m.jobs.snapshot(v.Name)
	return deriveStatus(v, job, ok)
}
