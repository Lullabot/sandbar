package manage

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
)

// lookupFails is a backend whose lookups fail outright — the connection is
// down.
type lookupFails struct{ fakeBackend }

func (*lookupFails) Get(string) (vm.VM, error) { return vm.VM{}, errors.New("exit status 255") }

// Clearing the way for a resume never deletes what it cannot vouch for: a
// lookup that fails, or a marker that cannot be read, is a refusal.
func TestReplaceOwnRebuildRefusesWhatItCannotEstablish(t *testing.T) {
	ctx := context.Background()
	dir := backupWithRebuild(t, "abc123")

	if err := replaceOwnRebuild(ctx, &lookupFails{}, markers{}, dir, "web", io.Discard); err == nil || !strings.Contains(err.Error(), "check whether") {
		t.Errorf("failed lookup: err = %v", err)
	}
	if err := replaceOwnRebuild(ctx, &fakeBackend{}, markers{}, dir, "web", io.Discard); err != nil {
		t.Errorf("no instance: err = %v, want nothing to do", err)
	}
	b := &fakeBackend{exists: true}
	if err := replaceOwnRebuild(ctx, b, brokenMarkers{}, dir, "web", io.Discard); err == nil || len(b.deleted) != 0 {
		t.Errorf("unreadable marker: err = %v, deleted %v", err, b.deleted)
	}
}

// While the replacement is running, the probe is a command inside it — the
// very path that failed — and a lookup error counts as still down.
func TestProbeTransport(t *testing.T) {
	ctx := context.Background()
	if err := probeTransport(ctx, &lookupFails{}, "web"); err == nil {
		t.Error("a failed lookup counted as a working connection")
	}
	b := &fakeBackend{exists: true, probeFailures: 1}
	if err := probeTransport(ctx, b, "web"); err == nil {
		t.Error("a failing guest command counted as a working connection")
	}
	if err := probeTransport(ctx, b, "web"); err != nil {
		t.Errorf("guest command answered, probe = %v", err)
	}
}
