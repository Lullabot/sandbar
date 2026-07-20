package ui

// jobs.go is the job registry: sand's only concurrent subsystem, and the source
// of the board's Building and Failed statuses.
//
// Before it, one reader/output/cancel triple on the model served exactly one job
// and a single model-wide running flag froze every key for the minutes a
// provision took. The registry generalizes that into N jobs, so several
// provisions (and transfers) can be in flight while the board stays live — and so
// a building VM can render a progress bar on its own tile.
//
// # What a run is keyed by
//
// A jobKey: THE VM, AND WHICH OF ITS TWO RUNS THIS IS — a provision (create or
// reset) or a file transfer. Keying by VM name alone is not a simplification, it
// is a bug, and a severe one: a create that fails is RETAINED as a failed
// provision precisely so its tile stays red and its Ansible log stays readable,
// while Lima — which has no idea Ansible ever ran — goes on calling the
// half-built VM "Running". Upload is offered on that tile (its enabledFor asks
// Lima), so pressing `u` on it registered a transfer under the same key, EVICTED
// the failed build, and the tile fell back to Lima and went a reassuring green
// with the log destroyed along with it. A VM can legitimately hold both runs at
// once, so it gets a slot for each, and only the PROVISION slot may move its
// status (see deriveStatus).
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
//     chunk read off the pipe travels back to Update as a KEYED
//     provisionOutputMsg, and it is Update, under the lock, that appends and
//     parses it. That is why the parser can be a mutable struct on the job.
//
// Nothing outside this file ever holds a *job. Readers (the progress view, the
// tile renderer) get a jobSnapshot: a value copy taken under the lock, safe to
// render from any goroutine.
//
// # Scope
//
// The LAST RUN OF EACH KIND, PER VM, IN MEMORY. Deliberately not: persistence
// across restarts, a multi-run history, a storage format, pruning, or schema
// versioning. Those were considered and deliberately ruled out.

import (
	"context"
	"strings"
	"sync"

	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// jobKind is WHICH of a VM's runs a job is. It is half of the registry's key, so
// a copy can never be mistaken for — or overwrite — a build.
type jobKind int

const (
	// kindProvision is a create or a reset: the run that owns the VM's existence,
	// and the ONLY kind that moves the VM's derived status (Building/Failed).
	kindProvision jobKind = iota
	// kindTransfer is a file copy in either direction. It says nothing about the
	// VM's own state — a VM whose upload failed is a healthy running VM with a
	// failed copy — so it never touches the tile's status word.
	kindTransfer
)

// jobKey identifies one run: the connection scope, the VM, and which of its
// runs. Scope is part of the identity — not decoration — so a job for "web"
// under one profile can never be looked up, retained-run-checked, or reaped
// as if it were "web" under another member of the fleet. Every constructor below
// takes scope explicitly for exactly that reason.
type jobKey struct {
	scope registry.Scope
	vm    string
	kind  jobKind
}

func provisionKey(scope registry.Scope, name string) jobKey {
	return jobKey{scope: scope, vm: name, kind: kindProvision}
}
func transferKey(scope registry.Scope, name string) jobKey {
	return jobKey{scope: scope, vm: name, kind: kindTransfer}
}

// keysFor is a VM's two possible slots, provision first. Every "does this VM have
// a run" question is two map lookups rather than a scan, and the fixed order is
// what makes the answers deterministic.
func keysFor(scope registry.Scope, name string) [2]jobKey {
	return [2]jobKey{provisionKey(scope, name), transferKey(scope, name)}
}

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

// job is one run: in flight, or retained after it finished. It lives only inside
// the registry, always behind a pointer, and is never copied — which is what makes
// the strings.Builder below safe here even though model.go bans one on the model.
// (A Builder copied after first use panics on the next write; a copied model would
// silently fork the log.)
type job struct {
	key   jobKey
	title string // the progress screen's heading, e.g. `Creating web`

	// seq is the order this job was begun in, registry-wide. It is what lets the
	// reopen-log verb pick between a VM's two retained runs (see logKey) without
	// reaching for a clock.
	seq uint64

	state    jobState
	err      error
	canceled bool // the user pressed ctrl+c: partial state they asked for, NOT a failure

	// cfg is the provision config for a create/reset, and the zero value for a
	// transfer. Only a provision is recorded in the managed registry on success.
	// The KEY, not this field, is what makes a job a provision: a config attached
	// to the wrong job used to be enough to turn a `limactl copy` into a "build".
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
	Title    string
	State    jobState
	Err      error
	Canceled bool

	// Provision is true for a create/reset — a job that owns this VM's existence —
	// and false for a transfer. It deliberately carries no vm.CreateConfig: the
	// config holds the clone token, and nothing a renderer can reach should.
	Provision bool

	// Recreates mirrors job.recreates: true for a Reset (deletes and re-creates
	// its own VM), false for a fresh Create. The provisionDoneMsg handler
	// (model.go) uses this to persist last-used-profile bookkeeping only for a
	// genuine CREATE — a reset targets its VM's own already-fixed profile, not
	// one the user picked from the create form's selector, so it must not
	// silently override the create form's default the next time it opens.
	Recreates bool

	Output   string
	Progress ansibleProgress
}

// Running reports whether the job is still in flight.
func (s jobSnapshot) Running() bool { return s.State == jobRunning }

// Failed reports whether the job ended in a REAL failure. A user cancellation is
// not one: it leaves exactly the partial state the user asked for, so it must not
// paint the VM red.
func (s jobSnapshot) Failed() bool { return s.State == jobFailed && !s.Canceled }

// jobRegistry holds every run, keyed by jobKey. The zero value is unusable; use
// newJobRegistry. A nil *jobRegistry is safe to call every method on and reports
// "no jobs", so a model built by hand (as tests do) needs no registry.
type jobRegistry struct {
	mu   sync.Mutex
	jobs map[jobKey]*job
	seq  uint64 // hands each job its begin-order number
}

func newJobRegistry() *jobRegistry {
	return &jobRegistry{jobs: make(map[jobKey]*job)}
}

// Building reports whether (scope, name) has a PROVISION in flight — a build, not
// a file transfer. It gates Delete: a VM must not be deleted out from under its
// own build.
func (r *jobRegistry) Building(scope registry.Scope, name string) bool {
	s, ok := r.snapshot(provisionKey(scope, name))
	return ok && s.Running()
}

// HasRetainedRun reports whether (scope, name) has a run — of either kind —
// whose log can be reopened. That includes a run still IN FLIGHT: the whole
// point of the un-frozen keyboard is that the user can walk away from a build
// and come back to it, so "reopen this VM's log" must work while it is still
// being written.
func (r *jobRegistry) HasRetainedRun(scope registry.Scope, name string) bool {
	_, ok := r.logKey(scope, name)
	return ok
}

// begin registers a new job, replacing any run this VM had retained IN THE SAME
// SLOT — a re-run of the same kind supersedes the last one, and a run of the OTHER
// kind never touches it. It REFUSES to replace a job that is still running and
// reports false: two goroutines sharing one key would orphan the first job's
// cancel func and leak its goroutine, so the check and the insert have to be one
// atomic step rather than a caller's guess.
func (r *jobRegistry) begin(j *job) bool {
	if r == nil || j == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.jobs[j.key]; ok && old.state == jobRunning {
		return false
	}
	r.seq++
	j.seq = r.seq
	r.jobs[j.key] = j
	return true
}

// markProvision attaches a provision's config to the build just begun for it: the
// config a successful run is recorded in the managed registry with, and reproduced
// from on a future recreate. recreates flags a Reset — a job that deletes and
// re-creates its own VM (see reconcile).
//
// It only ever touches the PROVISION slot, and only while that build is still
// running. Both halves matter, and the second is the one that bit: beginJob used
// to call this even when beginStream had REFUSED to start anything, so the config
// from a form the user filled in for a second VM landed on whatever run was
// already in flight for that name — swapping a running build's cpus/memory/clone
// URL/token, or turning a `limactl copy` into a "build" outright. beginStream now
// reports whether it started the job, and only a started job is marked.
func (r *jobRegistry) markProvision(scope registry.Scope, name string, cfg vm.CreateConfig, recreates bool) {
	if r == nil || cfg.Name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[provisionKey(scope, name)]
	if !ok || j.state != jobRunning {
		return
	}
	j.cfg = cfg
	j.recreates = recreates
}

// addOutput folds one chunk of a job's stream into its log and its parsed Ansible
// progress. It reports whether the job still exists: a reaped job's late chunks
// are dropped, and the caller stops re-issuing its reader.
func (r *jobRegistry) addOutput(key jobKey, chunk string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[key]
	if !ok {
		return false
	}
	if chunk == "" {
		return true
	}
	// chunk is untrusted guest output, stored verbatim (not escape-stripped).
	// See model.setOutput for why the retained terminal control sequences are
	// safe to render today and what would make them unsafe.
	j.output.WriteString(chunk)
	j.parser.feed(chunk)
	return true
}

// finish marks a job done and RETAINS it — with its log — so a failure stays
// visible on the board until the user acts on it. It reports false when the job
// is gone (reaped mid-flight), in which case its done message is stale and the
// caller must do nothing with it.
func (r *jobRegistry) finish(key jobKey, err error) (jobSnapshot, bool) {
	if r == nil {
		return jobSnapshot{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[key]
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

// cancelJob cancels one in-flight run — and only that one. It reports whether a
// running job was actually cancelled.
func (r *jobRegistry) cancelJob(key jobKey) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	j, ok := r.jobs[key]
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

// remove drops EVERY run a VM has — both slots, in scope — cancelling whichever
// are still in flight. This is "the user acted on it": deleting a VM leaves its
// retained runs with nothing to describe, and a copy still streaming into a VM
// that no longer exists is not work worth finishing.
//
// Scoped to (scope, name): removing this VM's runs must never reach into
// another scope's same-named VM's runs.
func (r *jobRegistry) remove(scope registry.Scope, name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	var stopped []stopper
	for _, k := range keysFor(scope, name) {
		if j, ok := r.jobs[k]; ok {
			stopped = append(stopped, stopperFor(j))
			delete(r.jobs, k)
		}
	}
	r.mu.Unlock()
	for _, s := range stopped {
		s.stop()
	}
}

// reconcile folds a fresh `limactl list` into the registry: it marks each running
// job's VM as seen while it is present, and REAPS any running job whose VM has
// disappeared — cancelling it and closing its pipe so its goroutine cannot leak.
// It returns the names it reaped, each once however many of its runs went with it.
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
//
// It also returns PROTECTED: the VMs whose absence those two exemptions excuse.
// They are the same fact, so they are decided here, once, under the one lock — and
// they have a second consumer. `manage.Reconcile` drops any managed VM missing from
// the list, and the caller then deletes that VM's host secrets; a VM that is merely
// mid-reset would have had its GH_TOKEN erased. The caller used to re-derive the
// exemption from outside with a DIFFERENT question (`Building(name)`), which is not
// the same predicate and cannot be kept the same by hoping. Feeding `protected` into
// the list `manage.Reconcile` sees makes the ordering a data dependency instead of a
// paragraph of prose.
//
// SCOPED: present is one profile's listing, so only THIS scope's jobs may be
// examined against it — a job key belonging to a different scope is skipped
// entirely, untouched by either the "seen" bookkeeping or the reaper. Without
// this, a job for a same-named VM under another profile would be reaped (or
// falsely marked seen) by a listing that says nothing about it at all. This is
// the HIGH-severity guard: `dropped`/`protected` feed straight into a host
// secrets deletion (see the caller in model.go), so a cross-scope reap here
// would delete the wrong VM's secrets.
func (r *jobRegistry) reconcile(scope registry.Scope, present map[string]bool) (reaped, protected []string) {
	if r == nil {
		return nil, nil
	}
	r.mu.Lock()
	seen := make(map[string]bool)
	spared := make(map[string]bool)
	var stopped []stopper
	for key, j := range r.jobs {
		if key.scope != scope {
			// Not this profile's job: leave it entirely alone, whether it is running,
			// finished, seen, or not — this listing has no opinion about it.
			continue
		}
		if present[key.vm] {
			if j.state == jobRunning {
				j.seen = true
			}
			continue
		}

		// The VM is ABSENT from `limactl list`. Is that a disappearance, or did one of
		// our own jobs put it there?
		switch {
		case j.state == jobRunning && (!j.seen || j.recreates):
			// A build whose clone has not landed yet, or a reset that has deleted its own
			// VM and not yet cloned it back.
		case j.recreates && j.state != jobSucceeded:
			// A RESET THAT FAILED (or was cancelled) deleted the VM itself and never
			// brought it back, so the VM is absent BECAUSE OF US — the job merely stopped
			// running before it could finish the job. Its absence is still self-inflicted.
			//
			// Treating it as a disappearance is not a cosmetic error: it unmanages the VM
			// and DELETES ITS HOST SECRETS, so the user loses the GH_TOKEN the reset was
			// going to re-apply AND the ability to retry (`R` needs a managed VM), while
			// the failed tile sits there telling them to. The user can still clear it
			// deliberately — `d` removes the job, the registry entry and the secrets
			// together — which is the only way that state should ever be reachable.
		default:
			// A genuine disappearance. Reap a run still going against a VM that is gone;
			// leave a finished one alone (a failed build is retained so the board keeps
			// saying so).
			if j.state != jobRunning {
				continue
			}
			stopped = append(stopped, stopperFor(j))
			delete(r.jobs, key) // deleting during a range is defined behaviour
			if !seen[key.vm] {
				seen[key.vm] = true
				reaped = append(reaped, key.vm)
			}
			continue
		}

		if !spared[key.vm] {
			spared[key.vm] = true
			protected = append(protected, key.vm)
		}
		continue
	}
	r.mu.Unlock()

	for _, s := range stopped {
		s.stop()
	}
	return reaped, protected
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

// snapshot returns a value copy of one run, and whether it exists.
func (r *jobRegistry) snapshot(key jobKey) (jobSnapshot, bool) {
	if r == nil {
		return jobSnapshot{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[key]
	if !ok {
		return jobSnapshot{}, false
	}
	return snapshotOf(j), true
}

// snapshotOf copies a job for a reader. Callers hold the lock.
func snapshotOf(j *job) jobSnapshot {
	return jobSnapshot{
		Title:     j.title,
		State:     j.state,
		Err:       j.err,
		Canceled:  j.canceled,
		Provision: j.key.kind == kindProvision,
		Recreates: j.recreates,
		Output:    j.output.String(),
		Progress:  j.parser.progress,
	}
}

// config returns the provision config of name's BUILD — the create form's input,
// including its clone token. It is deliberately NOT on jobSnapshot: only the
// provisionDoneMsg handler needs it (to record the VM as managed and seed its
// GH_TOKEN), and nothing that renders should be able to reach a token. A VM with
// no build (only a copy, or nothing) has none, which is exactly why a copy is
// never recorded as a managed VM.
func (r *jobRegistry) config(scope registry.Scope, name string) (vm.CreateConfig, bool) {
	if r == nil {
		return vm.CreateConfig{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[provisionKey(scope, name)]
	if !ok {
		return vm.CreateConfig{}, false
	}
	return j.cfg, true
}

// reader returns the read end of one job's pipe, for the next readNextCmd.
func (r *jobRegistry) reader(key jobKey) *readPipe {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[key]; ok {
		return j.reader
	}
	return nil
}

// isRunning reports whether (scope, name) has ANY run in flight — a build or a
// copy. It is what refuses a create/reset for a VM that is already busy, and
// what the quit confirmation counts.
func (r *jobRegistry) isRunning(scope registry.Scope, name string) bool {
	_, ok := r.runningKey(scope, name)
	return ok
}

// runningKey names the run in flight for (scope, name) — the build if both are (a
// copy can legitimately be running against a VM that a reset is about to
// rebuild), since the build is the one whose loss would cost the most.
func (r *jobRegistry) runningKey(scope registry.Scope, name string) (jobKey, bool) {
	if r == nil {
		return jobKey{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range keysFor(scope, name) {
		if j, ok := r.jobs[k]; ok && j.state == jobRunning {
			return k, true
		}
	}
	return jobKey{}, false
}

// runningInScope reports the key of ANY job in flight anywhere under scope —
// a build or a file transfer, on any VM — and whether one was found. It is
// the profile-level counterpart of Building/isRunning above (both per-VM):
// the profile management screen (profilesview.go) gates a whole profile's
// disable/delete/connection-field-edit on it being IDLE, which means no run
// in flight ACROSS ITS ENTIRE SCOPE, not just on one particular VM name —
// mirroring the existing per-VM Delete gate (commandreg.go's notBuilding) one
// level up, rather than inventing a second gating mechanism.
func (r *jobRegistry) runningInScope(scope registry.Scope) (jobKey, bool) {
	if r == nil {
		return jobKey{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, j := range r.jobs {
		if k.scope == scope && j.state == jobRunning {
			return k, true
		}
	}
	return jobKey{}, false
}

// logKey picks WHICH of a VM's runs the reopen-log verb ('l') shows, and it is not
// simply "the newest":
//
//   - A BUILD THAT IS RUNNING OR FAILED WINS OUTRIGHT. It is the run the tile's own
//     status word is asserting, and a user pressing `l` on a red tile is asking WHY
//     it is red. A copy run afterwards must never bury that answer — it used to
//     delete it.
//   - Otherwise the most recently begun run wins: a succeeded build is settled
//     history, and the copy the user just ran is what "show me the log" means.
//
// It reports false when the VM has no run at all, which is what gates the verb off.
func (r *jobRegistry) logKey(scope registry.Scope, name string) (jobKey, bool) {
	if r == nil {
		return jobKey{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	var best *job
	for _, k := range keysFor(scope, name) {
		j, ok := r.jobs[k]
		if !ok {
			continue
		}
		if j.key.kind == kindProvision && (j.state == jobRunning || (j.state == jobFailed && !j.canceled)) {
			return j.key, true
		}
		if best == nil || j.seq > best.seq {
			best = j
		}
	}
	if best == nil {
		return jobKey{}, false
	}
	return best.key, true
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

// exists reports whether this exact run is still in the registry.
func (r *jobRegistry) exists(key jobKey) bool {
	_, ok := r.snapshot(key)
	return ok
}

// running reports whether this exact run is in flight.
func (r *jobRegistry) running(key jobKey) bool {
	s, ok := r.snapshot(key)
	return ok && s.Running()
}

// names lists every VM IN scope that has a run — once each, however many kinds it
// has — in no order. The board (board.go) needs it because a VM being CREATED does
// not appear in `limactl list` until its clone lands — minutes into its own build
// — so a board that walked only the Lima list would show nothing at all for
// exactly the span the user is waiting on, and the signature moment of the whole
// flow (press n, a building tile appears) would not happen.
//
// Scoped to scope: a board built from one profile's roster must never gain a
// tile for another profile's same-named build.
func (r *jobRegistry) names(scope registry.Scope) []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[string]bool, len(r.jobs))
	names := make([]string, 0, len(r.jobs))
	for key := range r.jobs {
		if key.scope != scope || seen[key.vm] {
			continue
		}
		seen[key.vm] = true
		names = append(names, key.vm)
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
// Rendering v.Status directly is the failure this exists to prevent: a build in flight
// would look identical to a healthy idle VM, and — far worse — a FAILED provision
// would leave a reassuring green "Running" tile, failing quietly at the exact
// moment the user most needs to be told.
//
// Only a PROVISION job moves the VM's status, which is why the registry keys one
// slot per kind and statusOf below reaches for the provision slot BY NAME. A file
// transfer running (or failing) against a VM says nothing about the VM's own
// state: a VM whose upload failed is a healthy running VM with a failed copy, and
// painting its tile red would be its own small lie. The transfer's failure
// surfaces where it belongs — on the status line, and in its reopenable log.
func deriveStatus(v vm.VM, job jobSnapshot, hasJob bool) derivedStatus {
	if hasJob && job.Provision {
		switch {
		case job.Running():
			return statusBuilding
		case job.Failed():
			return statusFailed
		}
	}
	if v.Status == limaRunning {
		return statusRunning
	}
	return statusStopped
}

// statusOf is deriveStatus with the registry lookup already done — the form the
// tile renderer and the board call. It looks up the VM's BUILD, never "whatever
// run it happens to have": a copy in flight is not a build, and must not be able
// to become one by standing in the same slot.
func (m model) statusOf(scope registry.Scope, v vm.VM) derivedStatus {
	job, ok := m.jobs.snapshot(provisionKey(scope, v.Name))
	return deriveStatus(v, job, ok)
}
