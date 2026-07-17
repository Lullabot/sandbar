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
