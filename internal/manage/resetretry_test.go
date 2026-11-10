package manage

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"
)

// transportErr is a real exit status 255, the way a dropped ssh reports it.
func transportErr(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 255").Run()
	if err == nil {
		t.Fatal("sh exited 0")
	}
	return err
}

// fakeBackend scripts a reset backend: each Reset call takes the next result,
// and the transport probe fails probeFailures times before it answers.
type fakeBackend struct {
	results       []error
	resets        []provision.ResetOptions
	exists        bool
	probeFailures int
	probes        int
	deleted       []string
}

func (f *fakeBackend) Reset(_ context.Context, _ vm.CreateConfig, opts provision.ResetOptions, _ io.Writer) error {
	f.resets = append(f.resets, opts)
	if len(f.results) == 0 {
		return nil
	}
	err := f.results[0]
	f.results = f.results[1:]
	return err
}

func (f *fakeBackend) Get(name string) (vm.VM, error) {
	if !f.exists {
		return vm.VM{}, vm.ErrNotFound
	}
	return vm.VM{Name: name, Status: "Running"}, nil
}

func (f *fakeBackend) Delete(name string, _ bool) error {
	f.deleted = append(f.deleted, name)
	f.exists = false
	return nil
}

func (f *fakeBackend) ShellOut(context.Context, string, ...string) ([]byte, error) {
	return nil, f.probe()
}

func (f *fakeBackend) Preflight() error { return f.probe() }

func (f *fakeBackend) probe() error {
	f.probes++
	if f.probes <= f.probeFailures {
		return errors.New("agent refused operation")
	}
	return nil
}

// fastRetries shrinks the recovery policy and makes waiting instant.
func fastRetries(t *testing.T) {
	t.Helper()
	oldBudget, oldPoll, oldSleep := resetRetryBudget, resetPollInterval, sleepCtx
	t.Cleanup(func() { resetRetryBudget, resetPollInterval, sleepCtx = oldBudget, oldPoll, oldSleep })
	resetRetryBudget, resetPollInterval = time.Hour, time.Second
	sleepCtx = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
}

// The case this exists for: the VM was deleted, the backup staged, and the
// connection dropped. Once it is back the reset removes its own half-built
// replacement and resumes from the backup — with the backup deciding what
// comes back, not the original preserve flags.
func TestResetResumesFromTheBackupAfterATransportFailure(t *testing.T) {
	fastRetries(t)
	dir := backupWithRebuild(t, "abc123")
	b := &fakeBackend{
		results:       []error{&provision.StagedResetError{Dir: dir, Name: "web", Err: transportErr(t)}, nil},
		exists:        true,
		probeFailures: 3,
	}
	prov := markers{"web": {RebuildID: "abc123", Provisioning: true}}
	opts := provision.ResetOptions{PreserveHome: true, TemplateSource: "golden", Profile: provision.ProfileRef{ID: "p1"}}
	var out strings.Builder

	if err := Reset(context.Background(), b, vm.CreateConfig{Name: "web"}, opts, &out, prov); err != nil {
		t.Fatalf("Reset: %v\n%s", err, out.String())
	}
	if len(b.resets) != 2 {
		t.Fatalf("Reset ran %d times, want the original and one resume", len(b.resets))
	}
	resume := b.resets[1]
	if resume.RestoreFrom != dir || resume.PreserveHome || resume.TemplateSource != "golden" || resume.Profile.ID != "p1" {
		t.Errorf("resume options = %+v, want RestoreFrom=%s, no preserve flags, template and profile kept", resume, dir)
	}
	if len(b.deleted) != 1 || b.deleted[0] != "web" {
		t.Errorf("deleted %v, want the half-built web removed before the resume", b.deleted)
	}
	if b.probes != 4 {
		t.Errorf("probed %d times, want 3 failures then success", b.probes)
	}
	if !strings.Contains(out.String(), "Your backup is safe in "+dir) {
		t.Errorf("the wait did not say where the backup is:\n%s", out.String())
	}
}

// A VM under the name that does not carry this backup's RebuildID is never
// deleted: the retry gives up and returns the original error, whose message
// says how to resume by hand.
func TestResetNeverDeletesAVMItCannotProveIsItsOwn(t *testing.T) {
	fastRetries(t)
	dir := backupWithRebuild(t, "abc123")
	orig := &provision.StagedResetError{Dir: dir, Name: "web", Err: transportErr(t)}
	b := &fakeBackend{results: []error{orig}, exists: true}
	for name, prov := range map[string]markers{
		"different id": {"web": {RebuildID: "someone-else"}},
		"no marker":    {},
	} {
		t.Run(name, func(t *testing.T) {
			b.resets, b.deleted, b.results, b.exists = nil, nil, []error{orig}, true
			err := Reset(context.Background(), b, vm.CreateConfig{Name: "web"}, provision.ResetOptions{PreserveHome: true}, io.Discard, prov)
			if !errors.Is(err, orig) {
				t.Errorf("err = %v, want the original failure", err)
			}
			if len(b.deleted) != 0 || len(b.resets) != 1 {
				t.Errorf("deleted %v and reset %d times; want neither a delete nor a retry", b.deleted, len(b.resets))
			}
		})
	}
}

// With nothing staged there is nothing to resume from: the same reset runs
// again.
func TestResetRerunsAnUnstagedResetAfterATransportFailure(t *testing.T) {
	fastRetries(t)
	b := &fakeBackend{results: []error{transportErr(t), nil}}
	opts := provision.ResetOptions{TemplateSource: "golden"}
	if err := Reset(context.Background(), b, vm.CreateConfig{Name: "web"}, opts, io.Discard, nil); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if len(b.resets) != 2 || b.resets[1].RestoreFrom != "" || b.resets[1].TemplateSource != "golden" {
		t.Errorf("resets = %+v, want the same reset run twice", b.resets)
	}
}

// Only a transport failure is worth waiting out; a guest failure would fail
// the same way again, and a cancelled context means stop.
func TestResetDoesNotRetryOtherFailures(t *testing.T) {
	fastRetries(t)
	t.Run("guest failure", func(t *testing.T) {
		b := &fakeBackend{results: []error{errors.New("exit status 2")}}
		if err := Reset(context.Background(), b, vm.CreateConfig{Name: "web"}, provision.ResetOptions{}, io.Discard, nil); err == nil || len(b.resets) != 1 {
			t.Errorf("err = %v after %d resets; want the failure, unretried", err, len(b.resets))
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		b := &fakeBackend{results: []error{transportErr(t)}}
		if err := Reset(ctx, b, vm.CreateConfig{Name: "web"}, provision.ResetOptions{}, io.Discard, nil); err == nil || len(b.resets) != 1 {
			t.Errorf("err = %v after %d resets; want the failure, unretried", err, len(b.resets))
		}
	})
}

// A connection that never comes back ends the wait at the budget, with the
// original error and its recovery hint.
func TestResetGivesUpWhenTheBudgetRunsOut(t *testing.T) {
	fastRetries(t)
	clock := time.Unix(0, 0)
	oldNow := now
	t.Cleanup(func() { now = oldNow })
	now = func() time.Time { return clock }
	sleepCtx = func(context.Context, time.Duration) error { clock = clock.Add(10 * time.Minute); return nil }

	orig := transportErr(t)
	b := &fakeBackend{results: []error{orig}, probeFailures: 1 << 30}
	if err := Reset(context.Background(), b, vm.CreateConfig{Name: "web"}, provision.ResetOptions{}, io.Discard, nil); !errors.Is(err, orig) {
		t.Errorf("err = %v, want the original failure", err)
	}
	if len(b.resets) != 1 {
		t.Errorf("reset %d times, want no retry once the budget ran out", len(b.resets))
	}
}

// A connection that keeps dropping cannot retry forever.
func TestResetStopsAfterMaxAttempts(t *testing.T) {
	fastRetries(t)
	results := make([]error, resetMaxAttempts+3)
	for i := range results {
		results[i] = transportErr(t)
	}
	b := &fakeBackend{results: results}
	if err := Reset(context.Background(), b, vm.CreateConfig{Name: "web"}, provision.ResetOptions{}, io.Discard, nil); err == nil {
		t.Fatal("want the last failure")
	}
	if len(b.resets) != resetMaxAttempts+1 {
		t.Errorf("reset %d times, want %d", len(b.resets), resetMaxAttempts+1)
	}
}
