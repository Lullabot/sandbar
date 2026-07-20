package landgh

import (
	"context"
	"errors"
	"testing"
)

func TestAvailable(t *testing.T) {
	ctx := context.Background()

	t.Run("gh on PATH and authed", func(t *testing.T) {
		run := &fakeRunner{
			outputs: map[string][]byte{argvKey([]string{"auth", "status"}): []byte("Logged in\n")},
		}
		c := &Client{run: run, lookPath: func(string) (string, error) { return "/usr/bin/gh", nil }}
		if !c.Available(ctx) {
			t.Fatal("Available() = false, want true")
		}
	})

	t.Run("gh missing from PATH", func(t *testing.T) {
		run := &fakeRunner{}
		c := &Client{run: run, lookPath: func(string) (string, error) { return "", errGhMissing }}
		if c.Available(ctx) {
			t.Fatal("Available() = true, want false when gh is not on PATH")
		}
		// Must not fall through to running gh at all once LookPath fails.
		if len(run.calls) != 0 {
			t.Fatalf("expected no gh invocation when LookPath fails, got %v", run.calls)
		}
	})

	t.Run("gh on PATH but not authenticated", func(t *testing.T) {
		run := &fakeRunner{err: errors.New("not logged in to any GitHub hosts")}
		c := &Client{run: run, lookPath: func(string) (string, error) { return "/usr/bin/gh", nil }}
		if c.Available(ctx) {
			t.Fatal("Available() = true, want false when gh auth status fails")
		}
	})
}

// TestAvailabilityDistinguishesTheTwoFailures pins that the probe reports WHY
// it failed, not just that it did. The two modes need different fixes —
// install gh, versus authenticate the gh you already have — and the Landing
// pane's header text is built directly from this distinction. The
// unauthenticated case is what a shell-alias credential injector (the
// 1Password gh plugin, say) produces, since gh is exec'd argv-only and never
// through a shell.
func TestAvailabilityDistinguishesTheTwoFailures(t *testing.T) {
	ctx := context.Background()

	authed := (&Client{
		run:      &fakeRunner{outputs: map[string][]byte{argvKey([]string{"auth", "status"}): []byte("Logged in\n")}},
		lookPath: func(string) (string, error) { return "/usr/bin/gh", nil },
	}).Availability(ctx)
	if !authed.OK() || !authed.Installed || !authed.Authenticated {
		t.Fatalf("Availability() = %+v, want fully OK", authed)
	}
	if authed.Reason() != "" {
		t.Fatalf("Reason() = %q, want \"\" for a usable gh", authed.Reason())
	}

	missing := (&Client{
		run:      &fakeRunner{},
		lookPath: func(string) (string, error) { return "", errGhMissing },
	}).Availability(ctx)
	if missing.OK() || missing.Installed {
		t.Fatalf("Availability() = %+v, want not-installed", missing)
	}
	if missing.Reason() != "not installed" {
		t.Fatalf("Reason() = %q, want %q", missing.Reason(), "not installed")
	}

	unauthed := (&Client{
		run:      &fakeRunner{err: errors.New("not logged in to any GitHub hosts")},
		lookPath: func(string) (string, error) { return "/usr/bin/gh", nil },
	}).Availability(ctx)
	if unauthed.OK() {
		t.Fatalf("Availability() = %+v, want not-OK", unauthed)
	}
	// The load-bearing half: gh IS installed, and the result must say so
	// rather than collapsing into the same verdict as a missing binary.
	if !unauthed.Installed {
		t.Fatalf("Availability() = %+v, want Installed true — gh resolved on PATH", unauthed)
	}
	if unauthed.Reason() != "not authenticated" {
		t.Fatalf("Reason() = %q, want %q", unauthed.Reason(), "not authenticated")
	}
}
