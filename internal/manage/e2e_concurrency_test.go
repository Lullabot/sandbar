package manage

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestHeadlineTwoProcessRace_ConcurrentCreateSurvivesStaleReconcile is plan
// 18's headline end-to-end proof (Self Validation step 1): it reproduces the
// two-`sand`-process race WITHOUT spawning a real VM or a real second
// process, by driving the registry/secrets/manage code paths directly
// against the same on-disk files, exactly as the plan's Self Validation
// explicitly allows.
//
// Scenario (mirrors the Work Order's "Concrete failure" and
// internal/ui/model.go:951-972's reconcile-then-prune-secrets sequence):
//
//   - "web" and "old" are already-managed VMs with secrets, from before
//     either process below starts.
//   - P1 (the long-lived TUI) loads the registry and secrets store and HOLDS
//     them in memory — it knows about "web" and "old", not "foo".
//   - P2 (a headless `sand create foo`) concurrently records "foo" as
//     managed and sets its secret. P1 never sees this in memory.
//   - P1 then runs its routine reconcile-save with a `live` snapshot that
//     predates "foo" (its own `limactl list` call returned before "foo"
//     existed) and, mirroring model.go:970-972, prunes the dropped names'
//     secrets too.
//
// The assertions confirm "foo" — the concurrently-created VM — is still
// present and still tagged managed on disk, and that its secret was not
// swept up in the reconcile's secrets cascade, while "old" — genuinely
// absent from both P1's live list AND actually gone — is correctly pruned
// from both files. This is the known ∩ absent pruning basis (Task 2) and the
// reload-merge write path (Tasks 2-3) working together; a bottom comment
// demonstrates why the pre-fix pruning basis would have erased "foo".
func TestHeadlineTwoProcessRace_ConcurrentCreateSurvivesStaleReconcile(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "managed-vms.json")
	secPath := filepath.Join(dir, "secrets.json")
	scope := registry.LocalScope

	// Bootstrap: "web" and "old" are already managed, with secrets, before
	// either P1 or P2 exists.
	boot, err := registry.LoadFrom(regPath)
	if err != nil {
		t.Fatalf("bootstrap registry load: %v", err)
	}
	for _, name := range []string{"web", "old"} {
		if err := boot.AddScoped(vm.CreateConfig{Name: name, BaseName: "sandbar-base"}, scope); err != nil {
			t.Fatalf("bootstrap add %s: %v", name, err)
		}
	}
	bootSec, err := secrets.LoadFrom(secPath)
	if err != nil {
		t.Fatalf("bootstrap secrets load: %v", err)
	}
	for _, name := range []string{"web", "old"} {
		if err := bootSec.Set(name, scope, map[string]string{"TOKEN": "secret-" + name}); err != nil {
			t.Fatalf("bootstrap secret %s: %v", name, err)
		}
	}

	// P1: loads and HOLDS its in-memory view. At this instant it knows about
	// "web" and "old" — NOT "foo", which does not exist yet.
	p1Reg, err := registry.LoadFrom(regPath)
	if err != nil {
		t.Fatalf("P1 registry load: %v", err)
	}
	p1Sec, err := secrets.LoadFrom(secPath)
	if err != nil {
		t.Fatalf("P1 secrets load: %v", err)
	}

	// P2: an independent process/instance records a brand-new VM and its
	// secret. P1's in-memory snapshot above is never told about this.
	p2Reg, err := registry.LoadFrom(regPath)
	if err != nil {
		t.Fatalf("P2 registry load: %v", err)
	}
	if err := p2Reg.AddScoped(vm.CreateConfig{Name: "foo", BaseName: "sandbar-base"}, scope); err != nil {
		t.Fatalf("P2 AddScoped foo: %v", err)
	}
	p2Sec, err := secrets.LoadFrom(secPath)
	if err != nil {
		t.Fatalf("P2 secrets load: %v", err)
	}
	if err := p2Sec.Set("foo", scope, map[string]string{"TOKEN": "secret-foo"}); err != nil {
		t.Fatalf("P2 set foo secret: %v", err)
	}

	// P1 now runs its reconcile-save. Its `live` snapshot (its own
	// `limactl list` result) predates "foo": it still sees "web" running,
	// "old" no longer appears (deleted outside sand), and "foo" is simply
	// absent — exactly as it would be if P1's list() raced ahead of P2's
	// create.
	live := []vm.VM{{Name: "web"}}
	dropped, err := Reconcile(p1Reg, live, scope)
	if err != nil {
		t.Fatalf("P1 reconcile: %v", err)
	}

	// Mirrors internal/ui/model.go:970-972: prune the dropped names' host
	// secrets too, scoped identically.
	for _, name := range dropped {
		_ = p1Sec.Remove(name, scope)
	}

	// --- Assertions against what's ACTUALLY on disk now ---

	if len(dropped) != 1 || dropped[0] != "old" {
		t.Fatalf("expected reconcile to drop exactly [old] (the genuinely-gone VM), got %v", dropped)
	}

	finalReg, err := registry.LoadFrom(regPath)
	if err != nil {
		t.Fatalf("final registry load: %v", err)
	}
	if !finalReg.IsManagedInScope("foo", scope) {
		t.Error("foo must still be present and managed: it was concurrently created and must survive P1's stale reconcile-save")
	}
	if !finalReg.IsManagedInScope("web", scope) {
		t.Error("web must still be managed")
	}
	if finalReg.IsManagedInScope("old", scope) {
		t.Error("old should have been pruned: it was genuinely absent from P1's live list and P1 knew about it")
	}

	finalSec, err := secrets.LoadFrom(secPath)
	if err != nil {
		t.Fatalf("final secrets load: %v", err)
	}
	if got := finalSec.Get("foo", scope)["TOKEN"]; got != "secret-foo" {
		t.Errorf("foo's secret must survive the reconcile's secrets cascade, got %q", got)
	}
	if got := finalSec.Get("web", scope)["TOKEN"]; got != "secret-web" {
		t.Errorf("web's secret must be untouched, got %q", got)
	}
	if got := finalSec.Get("old", scope); len(got) != 0 {
		t.Errorf("old's secret should have been pruned along with its registry entry, got %v", got)
	}

	// --- Why this would FAIL under the pre-fix pruning basis ---
	//
	// Before this plan (see registry.go's history, commit 2f532bb^),
	// ReconcileScoped(scope, present) pruned by walking the CALLER's
	// possibly-stale IN-MEMORY map (never reloading disk) and then
	// unconditionally blind-overwrote the WHOLE in-memory map on save. That
	// basis is reproduced here — without touching any production code —
	// purely to show it disagrees with the fix.
	//
	// P1's in-memory registry, at the moment it calls reconcile, knows only
	// {"web", "old"} — it has never heard of "foo". The pre-fix code would
	// prune "old" from that same two-element map (present only contains
	// "web") and persist the SURVIVING in-memory set verbatim:
	preFixInMemory := map[string]bool{"web": true, "old": true}
	present := map[string]bool{"web": true}
	preFixWriteBasis := map[string]bool{}
	for name := range preFixInMemory {
		if present[name] {
			preFixWriteBasis[name] = true
		}
	}
	if preFixWriteBasis["foo"] {
		t.Fatal("test setup error: the pre-fix basis should never have known about foo")
	}
	if len(preFixWriteBasis) != 1 || !preFixWriteBasis["web"] {
		t.Fatalf("pre-fix basis sanity check failed, got %v", preFixWriteBasis)
	}
	// preFixWriteBasis == {"web"}: a pre-fix reconcile-save would have
	// blind-overwritten managed-vms.json with ONLY "web" — "foo" was never
	// even a pruning candidate, because a whole-map overwrite doesn't need
	// a candidate to erase something; it erases everything not already in
	// the writer's stale view. The assertions above prove the ACTUAL
	// (Task 2-corrected) code does not do this: "foo" survived on disk, and
	// its secret was never touched by the cascade at model.go:970-972. This
	// was independently confirmed empirically by running an equivalent test
	// against the pre-fix registry/manage code (commit 2f532bb^): it fails
	// there with "foo" absent from managed-vms.json.
}

// captureStderr redirects os.Stderr to a pipe for the duration of fn and
// returns everything written to it. The three stores' warnf helpers all
// write their best-effort degradation notices to os.Stderr (see
// registry.go, secrets.go, store.go's warnf), so this is the seam that lets
// a test observe "a note is emitted" without the stores exposing a logging
// interface just for tests.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	var buf strings.Builder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&buf, r)
	}()

	fn()

	_ = w.Close()
	wg.Wait()
	_ = r.Close()
	return buf.String()
}

// TestLockFailureDegradation_MutationStillPersistsAndWarns is plan 18's
// Self Validation step 3, at the integration level for all three stores: it
// points each store's lock FILE at a path that cannot be opened as a file
// (a pre-existing directory of the same name, so filelock.Acquire's
// os.OpenFile(O_CREATE|O_RDWR) fails with EISDIR — the test seam Task 1's
// filelock.Acquire already exercises in isolation) and confirms two things
// the best-effort posture promises: the mutation still reaches disk, and a
// visible warning is emitted. The store's own data directory is left
// writable throughout, so only the LOCK acquisition — never the underlying
// write — is forced to fail.
//
// filelock's own degradation (Acquire returning a non-nil error + safe
// no-op release) is unit-tested in internal/filelock/filelock_test.go
// (TestAcquireDegradesOnUnwritableParent); this test instead proves the
// property Task 5 is responsible for: that EVERY store's mutation path
// actually degrades gracefully through that failure rather than aborting
// the write, which none of the Task 3/4 per-store unit test suites
// currently exercise end-to-end.
func TestLockFailureDegradation_MutationStillPersistsAndWarns(t *testing.T) {
	t.Run("registry", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "managed-vms.json")
		if err := os.MkdirAll(path+".lock", 0o755); err != nil {
			t.Fatalf("pre-create lock path as a directory: %v", err)
		}

		var addErr error
		stderr := captureStderr(t, func() {
			r, err := registry.LoadFrom(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			addErr = r.AddScoped(vm.CreateConfig{Name: "x", BaseName: "sandbar-base"}, registry.LocalScope)
		})
		if addErr != nil {
			t.Fatalf("AddScoped must succeed (degraded, not failed) when the lock cannot be taken: %v", addErr)
		}
		if !strings.Contains(stderr, "could not lock") {
			t.Errorf("expected a lock-failure warning on stderr, got %q", stderr)
		}

		r2, err := registry.LoadFrom(path)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if !r2.IsManaged("x") {
			t.Error("the mutation must still have persisted to disk despite the lock failure")
		}
	})

	t.Run("secrets", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "secrets.json")
		if err := os.MkdirAll(path+".lock", 0o755); err != nil {
			t.Fatalf("pre-create lock path as a directory: %v", err)
		}

		var setErr error
		stderr := captureStderr(t, func() {
			s, err := secrets.LoadFrom(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			setErr = s.Set("x", registry.LocalScope, map[string]string{"K": "V"})
		})
		if setErr != nil {
			t.Fatalf("Set must succeed (degraded, not failed) when the lock cannot be taken: %v", setErr)
		}
		if !strings.Contains(stderr, "could not lock") {
			t.Errorf("expected a lock-failure warning on stderr, got %q", stderr)
		}

		s2, err := secrets.LoadFrom(path)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if got := s2.Get("x", registry.LocalScope)["K"]; got != "V" {
			t.Errorf("the mutation must still have persisted to disk despite the lock failure, got %q", got)
		}
	})

	t.Run("profiles", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "profiles.yaml")
		if err := os.MkdirAll(path+".lock", 0o755); err != nil {
			t.Fatalf("pre-create lock path as a directory: %v", err)
		}

		// LoadFrom's own seed-on-missing-file save is the unlocked
		// process-start path (see profiles/store.go's LoadFrom doc comment)
		// and is unaffected by the pre-created lock directory; the
		// lock-protected mutation under test is the subsequent Add.
		s, err := profiles.LoadFrom(path)
		if err != nil {
			t.Fatalf("load: %v", err)
		}

		var addErr error
		stderr := captureStderr(t, func() {
			_, addErr = s.Add(profiles.Profile{
				Type: profiles.TypeRemoteSSH,
				Name: "r1",
				Host: "example.com",
			})
		})
		if addErr != nil {
			t.Fatalf("Add must succeed (degraded, not failed) when the lock cannot be taken: %v", addErr)
		}
		if !strings.Contains(stderr, "could not lock") {
			t.Errorf("expected a lock-failure warning on stderr, got %q", stderr)
		}

		s2, err := profiles.LoadFrom(path)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if _, ok := s2.GetByName("r1"); !ok {
			t.Error("the mutation must still have persisted to disk despite the lock failure")
		}
	})
}
