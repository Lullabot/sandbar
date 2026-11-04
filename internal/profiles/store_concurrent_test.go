package profiles

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// These tests model two sand processes sharing one profiles.yaml — a
// long-running TUI and a headless `sandbar create` — as two Stores loaded
// from the same path. Each loaded the file once, so each holds a stale copy
// of the other's writes; the property under test is that neither write
// erases the other.

// TestSetLastUsedKeepsProfileAddedByAnotherProcess: the create's
// SetLastUsed at the end of its build must not drop a profile the TUI added
// while the build ran.
func TestSetLastUsedKeepsProfileAddedByAnotherProcess(t *testing.T) {
	path := testPath(t)
	cli, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom(cli) error = %v", err)
	}
	tui, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom(tui) error = %v", err)
	}

	added, err := tui.Add(Profile{Name: "mini", Type: TypeRemoteSSH, Enabled: true, Host: "mini", User: "u", Port: 22})
	if err != nil {
		t.Fatalf("tui.Add() error = %v", err)
	}
	if err := cli.SetLastUsed(LocalProfileID); err != nil {
		t.Fatalf("cli.SetLastUsed() error = %v", err)
	}

	reloaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if _, ok := reloaded.Get(added.ID); !ok {
		t.Fatal("SetLastUsed from a stale store erased a profile another process added")
	}
	if got := reloaded.LastUsed(); got != LocalProfileID {
		t.Errorf("LastUsed() = %q, want %q", got, LocalProfileID)
	}
	// The writer's own view is refreshed too, so its later reads agree.
	if _, ok := cli.Get(added.ID); !ok {
		t.Error("the writing store should see the profile it re-read under the lock")
	}
}

// TestProfileEditKeepsLastUsedSetByAnotherProcess is the reverse direction:
// a TUI edit after a create finished must not roll back the create's
// last-used pointer.
func TestProfileEditKeepsLastUsedSetByAnotherProcess(t *testing.T) {
	path := testPath(t)
	tui, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom(tui) error = %v", err)
	}
	remote, err := tui.Add(Profile{Name: "mini", Type: TypeRemoteSSH, Enabled: true, Host: "mini", User: "u", Port: 22})
	if err != nil {
		t.Fatalf("tui.Add() error = %v", err)
	}

	cli, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom(cli) error = %v", err)
	}
	if err := cli.SetLastUsed(remote.ID); err != nil {
		t.Fatalf("cli.SetLastUsed() error = %v", err)
	}

	if err := tui.Disable(LocalProfileID); err != nil {
		t.Fatalf("tui.Disable() error = %v", err)
	}

	reloaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if got := reloaded.LastUsed(); got != remote.ID {
		t.Errorf("LastUsed() = %q after a stale store's edit, want %q (the other process's write)", got, remote.ID)
	}
	if p, _ := reloaded.Get(LocalProfileID); p.Enabled {
		t.Error("the edit itself was not persisted")
	}
}

// TestUpdateDoesNotResurrectProfileRemovedByAnotherProcess: validation runs
// against the re-read file, so editing a profile another process deleted
// fails instead of writing it back.
func TestUpdateDoesNotResurrectProfileRemovedByAnotherProcess(t *testing.T) {
	path := testPath(t)
	a, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom(a) error = %v", err)
	}
	p, err := a.Add(Profile{Name: "mini", Type: TypeRemoteSSH, Enabled: true, Host: "mini", User: "u", Port: 22})
	if err != nil {
		t.Fatalf("a.Add() error = %v", err)
	}
	b, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom(b) error = %v", err)
	}
	if err := a.Remove(p.ID); err != nil {
		t.Fatalf("a.Remove() error = %v", err)
	}

	p.Name = "renamed"
	if _, err := b.Update(p); err == nil {
		t.Fatal("Update() of a profile another process removed: want error, got nil")
	}
	reloaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if _, ok := reloaded.Get(p.ID); ok {
		t.Error("a stale Update wrote a removed profile back")
	}
}

// TestMutateWaitsForTheFileLock checks that writes are actually serialized
// on statelock's "<path>.lock", not merely re-read: while another holder has
// the lock, a write must not land.
func TestMutateWaitsForTheFileLock(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("flock: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.SetLastUsed(LocalProfileID) }()

	select {
	case err := <-done:
		t.Fatalf("SetLastUsed returned (%v) while another holder had the lock", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetLastUsed() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetLastUsed never completed after the lock was released")
	}
}
