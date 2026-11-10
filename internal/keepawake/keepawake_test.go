package keepawake

import (
	"errors"
	"sync"
	"testing"
)

// fakeBackend counts OS-level acquires and releases so the reference counting
// in Hold can be checked without touching real power management.
type fakeBackend struct {
	mu       sync.Mutex
	acquires int
	releases int
	reasons  []string
	err      error // returned by the next acquire when non-nil
}

func (f *fakeBackend) acquire(reason string) (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.acquires++
	f.reasons = append(f.reasons, reason)
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.releases++
		return nil
	}, nil
}

func (f *fakeBackend) counts() (acquires, releases int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquires, f.releases
}

// useFake swaps the platform backend for a fake and resets the package's
// holder state, restoring both when the test ends.
func useFake(t *testing.T) *fakeBackend {
	t.Helper()
	f := &fakeBackend{}
	prev := acquire
	acquire = f.acquire
	mu.Lock()
	holders, releaseOS = 0, nil
	mu.Unlock()
	t.Cleanup(func() {
		acquire = prev
		mu.Lock()
		holders, releaseOS = 0, nil
		mu.Unlock()
	})
	return f
}

func TestNestedHoldsShareOneAssertion(t *testing.T) {
	f := useFake(t)

	r1, err := Hold("first")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Hold("second")
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := f.counts(); a != 1 {
		t.Fatalf("acquires = %d, want 1: a second holder must share the first's assertion", a)
	}
	if f.reasons[0] != "first" {
		t.Fatalf("reason = %q, want the first holder's", f.reasons[0])
	}

	r1()
	if _, r := f.counts(); r != 0 {
		t.Fatalf("releases = %d after one of two holders released, want 0", r)
	}
	r2()
	if _, r := f.counts(); r != 1 {
		t.Fatalf("releases = %d after the last holder released, want 1", r)
	}

	// A fresh Hold after everything was released takes a new assertion.
	r3, err := Hold("third")
	if err != nil {
		t.Fatal(err)
	}
	r3()
	if a, r := f.counts(); a != 2 || r != 2 {
		t.Fatalf("acquires, releases = %d, %d, want 2, 2", a, r)
	}
}

// A release called twice (an explicit call plus a defer, say) must not drop
// another holder's share and release the OS assertion out from under it.
func TestReleaseIsIdempotent(t *testing.T) {
	f := useFake(t)

	r1, _ := Hold("a")
	r2, _ := Hold("b")
	r1()
	r1()
	if _, r := f.counts(); r != 0 {
		t.Fatalf("releases = %d, want 0: a repeated release dropped the other holder's share", r)
	}
	r2()
	r2()
	if _, r := f.counts(); r != 1 {
		t.Fatalf("releases = %d, want exactly 1", r)
	}
}

func TestFailedAcquireReturnsNoOpAndIsRetried(t *testing.T) {
	f := useFake(t)
	boom := errors.New("no logind")
	f.err = boom

	rel, err := Hold("x")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if rel == nil {
		t.Fatal("release is nil on failure; callers must be able to call it unconditionally")
	}
	rel() // a no-op; must not underflow the holder count
	mu.Lock()
	h := holders
	mu.Unlock()
	if h != 0 {
		t.Fatalf("holders = %d after a failed Hold's release, want 0", h)
	}

	// The failure is not cached: once the OS cooperates, Hold succeeds.
	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	rel, err = Hold("y")
	if err != nil {
		t.Fatalf("Hold after recovery: %v", err)
	}
	rel()
	if a, r := f.counts(); a != 1 || r != 1 {
		t.Fatalf("acquires, releases = %d, %d, want 1, 1", a, r)
	}
}

// Many goroutines holding and releasing at once must end with every OS
// assertion released and the count back at zero. Run under -race.
func TestConcurrentHolders(t *testing.T) {
	f := useFake(t)

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := Hold("concurrent")
			if err != nil {
				t.Error(err)
				return
			}
			rel()
			rel()
		}()
	}
	wg.Wait()

	a, r := f.counts()
	if a == 0 || a != r {
		t.Fatalf("acquires, releases = %d, %d, want equal and non-zero", a, r)
	}
	mu.Lock()
	h, rosNil := holders, releaseOS == nil
	mu.Unlock()
	if h != 0 || !rosNil {
		t.Fatalf("holders = %d, releaseOS cleared = %v, want 0, true", h, rosNil)
	}
}
