package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// fakeSecretStore is an in-memory secretStore: enough to see what the CLI
// writes to the host store and what it hands the guest, with no XDG data dir
// involved.
type fakeSecretStore struct {
	scopes  map[string]map[string]string // directory scope -> KEY -> VALUE
	setErr  error
	setCall int
}

func newFakeSecretStore() *fakeSecretStore {
	return &fakeSecretStore{scopes: map[string]map[string]string{}}
}

func (f *fakeSecretStore) Get(_ string, _ registry.Scope) map[string]string {
	out := map[string]string{}
	for k, v := range f.scopes[""] {
		out[k] = v
	}
	return out
}

func (f *fakeSecretStore) Set(_ string, _ registry.Scope, pairs map[string]string) error {
	f.setCall++
	if f.setErr != nil {
		return f.setErr
	}
	cp := map[string]string{}
	for k, v := range pairs {
		cp[k] = v
	}
	f.scopes[""] = cp
	return nil
}

func (f *fakeSecretStore) GetAll(_ string, _ registry.Scope) map[string]map[string]string {
	out := map[string]map[string]string{}
	for scope, pairs := range f.scopes {
		cp := map[string]string{}
		for k, v := range pairs {
			cp[k] = v
		}
		out[scope] = cp
	}
	return out
}

// captureApply swaps the guest-apply seam for the duration of a test and
// returns what it was called with.
type applyCall struct {
	name, user string
	scopes     map[string]map[string]string
	called     bool
}

func captureApply(t *testing.T, err error) *applyCall {
	t.Helper()
	got := &applyCall{}
	prev := guestSecretsApplier
	guestSecretsApplier = func(_ context.Context, _ provider.Provider, name, user string, scopes map[string]map[string]string, _ io.Writer) error {
		got.called, got.name, got.user, got.scopes = true, name, user, scopes
		return err
	}
	t.Cleanup(func() { guestSecretsApplier = prev })
	return got
}

// TestSettleSecretsSeedsTheCloneToken is the create half of CLI/TUI parity: the
// token passed for the clone becomes the VM's GH_TOKEN secret, so it can be
// rotated later from the secrets editor without a rebuild. The TUI has always
// done this with the create form's token; the headless path recorded nothing,
// so the same VM built from the CLI had no secret to edit.
func TestSettleSecretsSeedsTheCloneToken(t *testing.T) {
	store := newFakeSecretStore()
	applied := captureApply(t, nil)

	cfg := vm.CreateConfig{Name: "web", User: "ada", CloneToken: "ghp_secret"}
	settleSecretsIn(context.Background(), nil, store, registry.LocalScope, cfg, io.Discard)

	if got := store.scopes[""]["GH_TOKEN"]; got != "ghp_secret" {
		t.Errorf("GH_TOKEN in the host store = %q, want the clone token", got)
	}
	if !applied.called {
		t.Fatal("the VM's secrets were never applied to the guest")
	}
	if applied.name != "web" || applied.user != "ada" {
		t.Errorf("applied to %q as %q, want web as ada", applied.name, applied.user)
	}
	if applied.scopes[""]["GH_TOKEN"] != "ghp_secret" {
		t.Errorf("the guest apply did not carry the freshly seeded token: %v", applied.scopes)
	}
}

// TestSettleSecretsAppliesExistingSecretsWithoutAToken is the reset half: a
// rebuilt VM comes up with none of the secrets the old one had, and nothing
// else will write them until its next start. `sand create --recreate` skipped
// this entirely, so a recreated VM silently lost its secrets until someone
// happened to start it from the TUI.
func TestSettleSecretsAppliesExistingSecretsWithoutAToken(t *testing.T) {
	store := newFakeSecretStore()
	store.scopes[""] = map[string]string{"GH_TOKEN": "already-here", "OTHER": "x"}
	applied := captureApply(t, nil)

	cfg := vm.CreateConfig{Name: "web", User: "ada"} // no --clone-token
	settleSecretsIn(context.Background(), nil, store, registry.LocalScope, cfg, io.Discard)

	if store.setCall != 0 {
		t.Errorf("no token was passed, so nothing should have been written to the store (Set called %d times)", store.setCall)
	}
	if !applied.called {
		t.Fatal("a reset must push the VM's stored secrets into the rebuilt guest")
	}
	if applied.scopes[""]["GH_TOKEN"] != "already-here" || applied.scopes[""]["OTHER"] != "x" {
		t.Errorf("the guest apply did not carry the stored secrets: %v", applied.scopes)
	}
}

// TestSettleSecretsFailureIsAWarning: the VM is already up by this point. A
// secret that could not be written is worth saying loudly and is not worth
// reporting as a failed build — which would invite a retry of the whole thing.
func TestSettleSecretsFailureIsAWarning(t *testing.T) {
	store := newFakeSecretStore()
	store.setErr = errors.New("store is read-only")
	captureApply(t, errors.New("guest unreachable"))

	var out strings.Builder
	cfg := vm.CreateConfig{Name: "web", User: "ada", CloneToken: "ghp_secret"}
	settleSecretsIn(context.Background(), nil, store, registry.LocalScope, cfg, &out)

	for _, want := range []string{"token could not be saved", "secrets were not applied"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q; got:\n%s", want, out.String())
		}
	}
}

// TestSettleSecretsDefaultsTheGuestUser: a config with no recorded user (an old
// index entry) still applies as SOMEONE — the host user, matching how every
// other entrypoint defaults it — rather than shelling out as an empty account.
func TestSettleSecretsDefaultsTheGuestUser(t *testing.T) {
	applied := captureApply(t, nil)
	settleSecretsIn(context.Background(), nil, newFakeSecretStore(), registry.LocalScope, vm.CreateConfig{Name: "web"}, io.Discard)

	if applied.user == "" {
		t.Error("secrets were applied as an empty user")
	}
	if applied.user != vm.HostUser() {
		t.Errorf("applied as %q, want the host user %q", applied.user, vm.HostUser())
	}
}
