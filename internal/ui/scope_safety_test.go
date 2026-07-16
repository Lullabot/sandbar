package ui

// scope_safety_test.go is the regression suite for the HIGH-severity
// data-loss guard: a VM name is a LABEL, not an identity, and two connection
// profiles in the fleet can legitimately label two entirely different VMs
// the same thing. Before scopes were threaded through, every per-VM store here
// (the heartbeat registry, the job registry, and both of model.go's prune
// sites) keyed — or pruned — by bare name. A reconcile or an explicit delete
// run against one profile's "web" could silently reach into another
// profile's same-named "web" and stop its heartbeat, evict its retained job,
// or — worst of all — delete its host secrets (GH_TOKEN included).
//
// These tests construct two registry.Scope values sharing one VM name and
// prove that acting on one scope leaves the other's state — secrets,
// heartbeat, and job — completely untouched. Some drive the registries
// directly (the store/handler level the task calls out as sufficient before
// the full fleet model exists); TestReconcileDropOnlyPrunesItsOwnScopesSecrets
// and TestExplicitDeleteOnlyPrunesItsOwnScopesState drive the actual model.go
// prune sites end to end.

import (
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"
)

// foreignScope stands in for a second connection profile (e.g. a remote-Lima
// target) in every test below. It is never this package's default
// registry.LocalScope, and it is a distinct Scope value from it by
// construction (LocalScope has no RemoteTarget).
var foreignScope = registry.Scope{Provider: "lima-remote", RemoteTarget: "user@build-host:22"}

// TestJobReconcileNeverReapsAnotherScopesJob is the load-bearing test for
// jobRegistry.reconcile's scope guard (jobs.go): a listing for one scope must
// have NO OPINION whatsoever about a job under a different scope, even one
// sharing the exact same VM name and even when that job would otherwise look
// like a genuine disappearance (running, already seen, absent from the
// listing).
func TestJobReconcileNeverReapsAnotherScopesJob(t *testing.T) {
	r := newJobRegistry()

	mine := &job{key: provisionKey(registry.LocalScope, "web"), state: jobRunning, seen: true, cancel: func() {}}
	theirs := &job{key: provisionKey(foreignScope, "web"), state: jobRunning, seen: true, cancel: func() {}}
	if !r.begin(mine) {
		t.Fatal("precondition: begin mine")
	}
	if !r.begin(theirs) {
		t.Fatal("precondition: begin theirs")
	}

	// A listing for MY scope alone says "web" is now absent — a genuine
	// disappearance, since the job is running and has already been seen once.
	// It says NOTHING about the foreign scope's identically-named "web".
	reaped, _ := r.reconcile(registry.LocalScope, map[string]bool{})
	if len(reaped) != 1 || reaped[0] != "web" {
		t.Fatalf("reconcile(LocalScope) should reap my own disappeared web, got reaped=%v", reaped)
	}
	if _, ok := r.snapshot(provisionKey(registry.LocalScope, "web")); ok {
		t.Fatal("my own job should be gone after its scope's reconcile")
	}

	// THE GUARD: the foreign scope's same-named job must survive completely
	// untouched — still running, never cancelled — even though it was
	// present in the very same registry the reap just ran against.
	theirsSnap, ok := r.snapshot(provisionKey(foreignScope, "web"))
	if !ok {
		t.Fatal("a foreign scope's identically-named job must survive my scope's reconcile — it was wrongly reaped")
	}
	if !theirsSnap.Running() {
		t.Fatalf("the foreign scope's job must still be running, got state=%+v", theirsSnap)
	}
}

// TestHeartbeatStopAndEndedNeverTouchAnotherScope is the load-bearing test
// for heartbeatRegistry's composite (scope, name) key (heartbeat.go): both
// ways a heartbeat leaves the registry — a deliberate stop() and a stream
// ending on its own (ended()) — must reach only the (scope, name) they were
// asked about, never a same-named entry under a different scope.
func TestHeartbeatStopAndEndedNeverTouchAnotherScope(t *testing.T) {
	r := newHeartbeats(nil) // no shell: this test drives the map directly, not start()

	mine := vmHandle{Scope: registry.LocalScope, Name: "web"}
	theirs := vmHandle{Scope: foreignScope, Name: "web"}
	r.beats[mine] = &heartbeat{epoch: 1, cancel: func() {}, ch: make(chan guestSample, 1), last: guestSample{MemTotal: 111, MemUsed: 11}, seen: true}
	r.beats[theirs] = &heartbeat{epoch: 1, cancel: func() {}, ch: make(chan guestSample, 1), last: guestSample{MemTotal: 222, MemUsed: 22}, seen: true}

	// stop() — the deliberate teardown path (the VM left Running, or the
	// board is no longer the active screen).
	r.stop(registry.LocalScope, "web")
	if _, ok := r.latest(registry.LocalScope, "web"); ok {
		t.Fatal("my own heartbeat should be gone after stop")
	}
	if s, ok := r.latest(foreignScope, "web"); !ok || s.MemTotal != 222 {
		t.Fatalf("stopping my scope's heartbeat must not touch the foreign scope's identically-named one, got ok=%v s=%+v", ok, s)
	}

	// ended() — the stream-died-on-its-own path. Re-seed mine, then end it,
	// and check the foreign entry (never touched by the stop above) again.
	r.beats[mine] = &heartbeat{epoch: 2, cancel: func() {}, ch: make(chan guestSample, 1), last: guestSample{MemTotal: 333}, seen: true}
	r.ended(registry.LocalScope, "web", 2)
	if _, ok := r.latest(registry.LocalScope, "web"); ok {
		t.Fatal("my own heartbeat should be gone after ended")
	}
	if s, ok := r.latest(foreignScope, "web"); !ok || s.MemTotal != 222 {
		t.Fatalf("ending my scope's heartbeat must not touch the foreign scope's identically-named one, got ok=%v s=%+v", ok, s)
	}

	// names(scope) must list only that scope's VMs.
	if got := r.names(foreignScope); len(got) != 1 || got[0] != "web" {
		t.Fatalf("names(foreignScope) = %v, want exactly [\"web\"] (my scope's heartbeat is already gone)", got)
	}
	if got := r.names(registry.LocalScope); len(got) != 0 {
		t.Fatalf("names(LocalScope) = %v, want none (mine was ended)", got)
	}
}

// TestReconcileDropOnlyPrunesItsOwnScopesSecrets drives the ACTUAL prune site
// in model.go's vmsLoadedMsg handler (the "a dropped VM's HOST SECRETS ARE
// DELETED" comment) end to end: a managed VM that vanishes from the listing
// has its host secrets removed — but only in the model's OWN scope. A
// same-named secret filed under a different profile, sitting in the very
// same (shared) secrets.Store, must survive completely intact. This is the
// HIGH-severity data-loss risk made concrete: the prune call used to be
// hardcoded to registry.LocalScope regardless of which scope actually
// owned the VM.
func TestReconcileDropOnlyPrunesItsOwnScopesSecrets(t *testing.T) {
	m := newTestModel(t) // m.scope == registry.LocalScope
	m = resized(m, 100, 30)

	// A shared secrets store — standing in for the single on-disk secrets
	// file every profile's model loads from — holding a same-named secret
	// under both scopes.
	sec := secrets.NewEmpty()
	if err := sec.Set("shared-name", registry.LocalScope, map[string]string{"GH_TOKEN": "local-token"}); err != nil {
		t.Fatalf("seed local secret: %v", err)
	}
	if err := sec.Set("shared-name", foreignScope, map[string]string{"GH_TOKEN": "foreign-token"}); err != nil {
		t.Fatalf("seed foreign secret: %v", err)
	}
	m.sec = sec

	// "shared-name" is managed (under my own scope) and present — it gets a
	// tile and a clean bill of health from Reconcile.
	m = loadManaged(t, m, vm.VM{Name: "shared-name", Status: "Running"})
	if !m.reg.IsManagedInScope("shared-name", registry.LocalScope) {
		t.Fatal("precondition: shared-name should be recorded managed under my scope")
	}

	// Now it vanishes from the listing entirely — deleted outside sand.
	// manage.Reconcile drops it from the managed index, and the handler
	// prunes its secrets: `m.sec.Remove(name, m.scope)`.
	next, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{}})
	m = next.(model)

	if m.reg.IsManagedInScope("shared-name", registry.LocalScope) {
		t.Fatal("precondition: shared-name should have been dropped from the managed index")
	}
	if got := sec.Get("shared-name", registry.LocalScope); got["GH_TOKEN"] != "" {
		t.Fatalf("my own scope's secret should have been pruned, got %+v", got)
	}

	// THE GUARD: the foreign scope's identically-named secret, in the exact
	// same store, must be completely untouched.
	if got := sec.Get("shared-name", foreignScope); got["GH_TOKEN"] != "foreign-token" {
		t.Fatalf("a foreign scope's identically-named secret must survive my reconcile's prune, got %+v — it was wrongly deleted", got)
	}
}

// TestExplicitDeleteOnlyPrunesItsOwnScopesState drives the OTHER prune site —
// the explicit-delete branch of the actionDoneMsg handler in model.go — end
// to end, covering both the job registry and the secrets store in one pass.
// Deleting a VM under my own scope must retain (not cancel or evict) a
// foreign scope's identically-named retained job, and must leave the foreign
// scope's identically-named secret alone.
func TestExplicitDeleteOnlyPrunesItsOwnScopesState(t *testing.T) {
	m := newTestModel(t) // m.scope == registry.LocalScope

	sec := secrets.NewEmpty()
	if err := sec.Set("shared-name", registry.LocalScope, map[string]string{"GH_TOKEN": "local-token"}); err != nil {
		t.Fatalf("seed local secret: %v", err)
	}
	if err := sec.Set("shared-name", foreignScope, map[string]string{"GH_TOKEN": "foreign-token"}); err != nil {
		t.Fatalf("seed foreign secret: %v", err)
	}
	m.sec = sec

	// A retained (finished) job under each scope, same VM name — the
	// "delete" branch must remove mine and leave theirs.
	mine := &job{key: provisionKey(registry.LocalScope, "shared-name"), state: jobFailed, cancel: func() {}}
	theirs := &job{key: provisionKey(foreignScope, "shared-name"), state: jobRunning, cancel: func() {}}
	if !m.jobs.begin(mine) {
		t.Fatal("precondition: begin mine")
	}
	if !m.jobs.begin(theirs) {
		t.Fatal("precondition: begin theirs")
	}

	next, _ := m.Update(actionDoneMsg{action: "delete", name: "shared-name"})
	m = next.(model)

	if _, ok := m.jobs.snapshot(provisionKey(registry.LocalScope, "shared-name")); ok {
		t.Fatal("my own scope's job should be gone after an explicit delete")
	}
	if got := sec.Get("shared-name", registry.LocalScope); got["GH_TOKEN"] != "" {
		t.Fatalf("my own scope's secret should have been pruned by the delete, got %+v", got)
	}

	// THE GUARD: the foreign scope's job and secret must both survive,
	// completely untouched — the delete only ever named "shared-name" with
	// no scope attached to the message, so the handler has to supply m.scope
	// itself rather than reaching for every same-named entry it can find.
	theirsSnap, ok := m.jobs.snapshot(provisionKey(foreignScope, "shared-name"))
	if !ok {
		t.Fatal("a foreign scope's identically-named job must survive my explicit delete — it was wrongly removed")
	}
	if !theirsSnap.Running() {
		t.Fatalf("the foreign scope's job must still be running, got %+v", theirsSnap)
	}
	if got := sec.Get("shared-name", foreignScope); got["GH_TOKEN"] != "foreign-token" {
		t.Fatalf("a foreign scope's identically-named secret must survive my explicit delete, got %+v — it was wrongly deleted", got)
	}
}

// TestReconcileDropPrunesTheModelsOwnScopeNotAHardcodedOne pins the exact
// regression scope threading fixes: every prune call used to be hardcoded to
// registry.LocalScope regardless of what the model's OWN scope actually was. That bug is invisible whenever the model's
// scope happens to BE LocalScope (every other test in this file uses the
// default local model, so none of them can tell "hardcoded LocalScope" apart
// from "correctly threaded m.scope" — they are the same value). Here the
// model's scope is deliberately something else, so a reconcile-drop that
// still reached for a hardcoded LocalScope would prune the WRONG secret: it
// would leave the real owning profile's secret in place forever and delete
// an unrelated profile's same-named one instead.
func TestReconcileDropPrunesTheModelsOwnScopeNotAHardcodedOne(t *testing.T) {
	m := newTestModel(t)
	m.members[0].scope = foreignScope // this model is a non-local connection profile

	sec := secrets.NewEmpty()
	if err := sec.Set("shared-name", foreignScope, map[string]string{"GH_TOKEN": "mine"}); err != nil {
		t.Fatalf("seed my (foreign-scope) secret: %v", err)
	}
	if err := sec.Set("shared-name", registry.LocalScope, map[string]string{"GH_TOKEN": "unrelated-local-profile"}); err != nil {
		t.Fatalf("seed the unrelated LocalScope secret: %v", err)
	}
	m.sec = sec

	// Managed under MY scope (foreignScope), not LocalScope.
	if err := m.reg.AddScoped(vm.CreateConfig{Name: "shared-name", BaseName: "sandbar-base"}, foreignScope); err != nil {
		t.Fatalf("seed managed registry entry: %v", err)
	}
	next, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{{Name: "shared-name", Status: "Running"}}})
	m = next.(model)
	if !m.reg.IsManagedInScope("shared-name", foreignScope) {
		t.Fatal("precondition: shared-name should be recorded managed under my (foreign) scope")
	}

	// It vanishes from the listing.
	next, _ = m.Update(vmsLoadedMsg{vms: []vm.VM{}})
	m = next.(model)

	// THE FIX: my own (foreign) scope's secret is the one that gets pruned —
	// not a hardcoded LocalScope.
	if got := sec.Get("shared-name", foreignScope); got["GH_TOKEN"] != "" {
		t.Fatalf("my own scope's secret should have been pruned, got %+v — a hardcoded registry.LocalScope would have missed it entirely", got)
	}
	// THE GUARD: the unrelated LocalScope profile's identically-named secret
	// must be untouched — a hardcoded registry.LocalScope prune would have
	// wrongly deleted exactly this.
	if got := sec.Get("shared-name", registry.LocalScope); got["GH_TOKEN"] != "unrelated-local-profile" {
		t.Fatalf("an unrelated LocalScope profile's secret must survive, got %+v — a hardcoded registry.LocalScope prune would have deleted it", got)
	}
}

// TestExplicitDeletePrunesTheModelsOwnScopeNotAHardcodedOne is
// TestReconcileDropPrunesTheModelsOwnScopeNotAHardcodedOne's counterpart for
// the explicit-delete prune site (actionDoneMsg's "delete" branch), covering
// both the job registry and the secrets store.
func TestExplicitDeletePrunesTheModelsOwnScopeNotAHardcodedOne(t *testing.T) {
	m := newTestModel(t)
	m.members[0].scope = foreignScope // this model is a non-local connection profile

	sec := secrets.NewEmpty()
	if err := sec.Set("shared-name", foreignScope, map[string]string{"GH_TOKEN": "mine"}); err != nil {
		t.Fatalf("seed my (foreign-scope) secret: %v", err)
	}
	if err := sec.Set("shared-name", registry.LocalScope, map[string]string{"GH_TOKEN": "unrelated-local-profile"}); err != nil {
		t.Fatalf("seed the unrelated LocalScope secret: %v", err)
	}
	m.sec = sec

	mine := &job{key: provisionKey(foreignScope, "shared-name"), state: jobFailed, cancel: func() {}}
	unrelated := &job{key: provisionKey(registry.LocalScope, "shared-name"), state: jobRunning, cancel: func() {}}
	if !m.jobs.begin(mine) {
		t.Fatal("precondition: begin mine")
	}
	if !m.jobs.begin(unrelated) {
		t.Fatal("precondition: begin unrelated")
	}

	next, _ := m.Update(actionDoneMsg{action: "delete", name: "shared-name"})
	m = next.(model)

	// THE FIX: my own (foreign) scope's job and secret are the ones pruned.
	if _, ok := m.jobs.snapshot(provisionKey(foreignScope, "shared-name")); ok {
		t.Fatal("my own scope's job should be gone after an explicit delete")
	}
	if got := sec.Get("shared-name", foreignScope); got["GH_TOKEN"] != "" {
		t.Fatalf("my own scope's secret should have been pruned, got %+v — a hardcoded registry.LocalScope would have missed it entirely", got)
	}

	// THE GUARD: the unrelated LocalScope profile's job and secret survive —
	// a hardcoded registry.LocalScope prune would have wrongly hit exactly
	// these instead.
	unrelatedSnap, ok := m.jobs.snapshot(provisionKey(registry.LocalScope, "shared-name"))
	if !ok || !unrelatedSnap.Running() {
		t.Fatalf("an unrelated LocalScope profile's job must survive, got ok=%v snap=%+v — a hardcoded registry.LocalScope prune would have removed it", ok, unrelatedSnap)
	}
	if got := sec.Get("shared-name", registry.LocalScope); got["GH_TOKEN"] != "unrelated-local-profile" {
		t.Fatalf("an unrelated LocalScope profile's secret must survive, got %+v — a hardcoded registry.LocalScope prune would have deleted it", got)
	}
}

// TestActionDoneMsgWithOrphanedScopePrunesItsOwnScopeNotTheActiveOne pins
// a scope-fallback bug: actionDoneMsg's delete branch used to fall back to
// the ACTIVE member's scope whenever msg.scope no longer matched any CURRENT
// member — which happens whenever the profile that owned the action was deleted (or connection-edited, which rebuilds its member)
// while the action was still in flight. Lifecycle actions are NOT jobs, so
// the idle gate never blocks this. That fallback then ran the destructive
// prune (RemoveScoped + secrets.Remove) under the WRONG (active/local) scope
// — deleting the LOCAL "web"'s registry entry and host secrets when the
// REMOTE "web" was the one actually deleted, and leaving the real (remote)
// entry dangling forever.
//
// The fix: only a ZERO-VALUE scope (a hand-built test message, which never
// tags one) falls back to the active member; a genuinely-tagged scope that no
// longer matches any member is used AS-IS for pruning — mirroring routeIndex's
// own hardening for vmsLoadedMsg (fleet.go).
func TestActionDoneMsgWithOrphanedScopePrunesItsOwnScopeNotTheActiveOne(t *testing.T) {
	isolateHostState(t)
	fleet := provider.Fleet{
		{Profile: profiles.Profile{ID: profiles.LocalProfileID, Type: profiles.TypeLocal, Enabled: true}, Prov: &providerfake.Provider{}, Scope: registry.LocalScope},
		{Profile: profiles.Profile{ID: "remote", Type: profiles.TypeRemoteSSH, Enabled: true}, Prov: &providerfake.Provider{}, Scope: foreignScope},
	}
	m := New(fleet).(model)
	m = resized(m, 100, 30)

	sec := secrets.NewEmpty()
	if err := sec.Set("web", registry.LocalScope, map[string]string{"GH_TOKEN": "local-token"}); err != nil {
		t.Fatalf("seed local secret: %v", err)
	}
	if err := sec.Set("web", foreignScope, map[string]string{"GH_TOKEN": "remote-token"}); err != nil {
		t.Fatalf("seed remote secret: %v", err)
	}
	m.sec = sec

	if err := m.reg.AddScoped(vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}, registry.LocalScope); err != nil {
		t.Fatalf("seed local managed entry: %v", err)
	}
	if err := m.reg.AddScoped(vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}, foreignScope); err != nil {
		t.Fatalf("seed remote managed entry: %v", err)
	}

	// The remote member is gone by the time the action's result arrives — the
	// profile was deleted (or connection-edited and rebuilt) while the delete
	// it kicked off was still in flight. m.active still points at the LOCAL
	// member (index 0), which the buggy fallback would have reached for.
	idx, ok := m.memberIndex(foreignScope)
	if !ok {
		t.Fatal("precondition: the remote member should exist before it is removed")
	}
	m.members = append(m.members[:idx], m.members[idx+1:]...)
	if _, ok := m.memberIndex(foreignScope); ok {
		t.Fatal("precondition: the remote member should be gone")
	}

	next, _ := m.Update(actionDoneMsg{action: "delete", name: "web", scope: foreignScope})
	m = next.(model)

	// THE FIX: the REMOTE (foreign) scope is the one pruned — the VM that was
	// actually deleted — even though its member is gone.
	if m.reg.IsManagedInScope("web", foreignScope) {
		t.Fatal("the remote scope's registry entry should have been pruned")
	}
	if got := sec.Get("web", foreignScope); got["GH_TOKEN"] != "" {
		t.Fatalf("the remote scope's secret should have been pruned, got %+v", got)
	}

	// THE GUARD: the LOCAL VM's registry entry and secrets — completely
	// unrelated to this action — must survive untouched. The bug pruned
	// exactly these, by falling back to the active (local) scope.
	if !m.reg.IsManagedInScope("web", registry.LocalScope) {
		t.Fatal("the LOCAL web's registry entry must survive — it was wrongly pruned by the active-scope fallback")
	}
	if got := sec.Get("web", registry.LocalScope); got["GH_TOKEN"] != "local-token" {
		t.Fatalf("the LOCAL web's secret must survive, got %+v — it was wrongly pruned by the active-scope fallback", got)
	}
}
