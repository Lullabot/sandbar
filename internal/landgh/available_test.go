package landgh

import (
	"context"
	"errors"
	"testing"
)

// TestAvailability covers the probe's three outcomes in one place. The two
// FAILURE modes are the point: they need different fixes — install gh, versus
// authenticate the gh you already have — and the Landing pane's header text is
// built directly from the distinction, so collapsing them would put the user
// on the wrong trail. The unauthenticated case is what a shell-alias
// credential injector (the 1Password gh plugin, say) produces, since gh is
// exec'd argv-only and never through a shell.
func TestAvailability(t *testing.T) {
	ctx := context.Background()

	t.Run("gh on PATH and authed", func(t *testing.T) {
		c := &Client{
			run:      &fakeRunner{outputs: map[string][]byte{argvKey([]string{"auth", "status"}): []byte("Logged in\n")}},
			lookPath: func(string) (string, error) { return "/usr/bin/gh", nil },
		}
		got := c.Availability(ctx)
		if !got.OK() || !got.Installed || !got.Authenticated {
			t.Fatalf("Availability() = %+v, want fully OK", got)
		}
	})

	t.Run("gh missing from PATH", func(t *testing.T) {
		run := &fakeRunner{}
		c := &Client{run: run, lookPath: func(string) (string, error) { return "", errGhMissing }}
		got := c.Availability(ctx)
		if got.OK() || got.Installed {
			t.Fatalf("Availability() = %+v, want not-installed", got)
		}
		// Must not fall through to running gh at all once LookPath fails.
		if len(run.calls) != 0 {
			t.Fatalf("expected no gh invocation when LookPath fails, got %v", run.calls)
		}
	})

	t.Run("gh on PATH but not authenticated", func(t *testing.T) {
		c := &Client{
			run:      &fakeRunner{err: errors.New("not logged in to any GitHub hosts")},
			lookPath: func(string) (string, error) { return "/usr/bin/gh", nil },
		}
		got := c.Availability(ctx)
		if got.OK() || got.Authenticated {
			t.Fatalf("Availability() = %+v, want not-OK", got)
		}
		// The load-bearing half: gh IS installed, and the result must say so
		// rather than collapsing into the same verdict as a missing binary.
		if !got.Installed {
			t.Fatalf("Availability() = %+v, want Installed true — gh resolved on PATH", got)
		}
	})
}
