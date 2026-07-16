package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/registry"
)

// newTestStore returns an in-memory-backed profiles.Store (a real temp file,
// so Store.Add/SetLastUsed's persistence code paths still run, but isolated
// from the developer's real ~/.config/sandbar/profiles.yaml).
func newTestStore(t *testing.T) *profiles.Store {
	t.Helper()
	s, err := profiles.LoadFrom(filepath.Join(t.TempDir(), "profiles.yaml"))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	return s
}

// TestResolveProfileNameUnknown verifies `sand create --profile <name>`
// fails with a clear error naming the bad value when no profile has that
// name — an explicit name is a promise to use exactly that profile, so it
// must never silently fall back to last-used/Local.
func TestResolveProfileNameUnknown(t *testing.T) {
	store := newTestStore(t) // seeded with only the permanent "local" profile

	_, err := resolveProfileName(store, "bogus")
	if err == nil {
		t.Fatal("resolveProfileName: want error for unknown profile name, got nil")
	}
	if !strings.Contains(err.Error(), `"bogus"`) {
		t.Errorf("resolveProfileName error = %q, want it to name the bad value %q", err.Error(), "bogus")
	}
}

// TestResolveProfileNameDisabled verifies an explicit --profile naming a
// disabled profile is refused with a distinct, clear error rather than the
// generic "unknown" one.
func TestResolveProfileNameDisabled(t *testing.T) {
	store := newTestStore(t)
	added, err := store.Add(profiles.Profile{
		Name: "work", Type: profiles.TypeRemoteSSH,
		Host: "work.example.com", User: "dev", Port: 22,
		Enabled: false,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	_, err = resolveProfileName(store, added.Name)
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("resolveProfileName(disabled profile) error = %v, want it to mention %q", err, "disabled")
	}
}

// TestResolveProfileNameDefaultsToLastUsed verifies the default (no explicit
// --profile) resolution order: the store's last-used profile wins over
// Local when it names an enabled profile.
func TestResolveProfileNameDefaultsToLastUsed(t *testing.T) {
	store := newTestStore(t)
	work, err := store.Add(profiles.Profile{
		Name: "work", Type: profiles.TypeRemoteSSH,
		Host: "work.example.com", User: "dev", Port: 22,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.SetLastUsed(work.ID); err != nil {
		t.Fatalf("SetLastUsed: %v", err)
	}

	got, err := resolveProfileName(store, "")
	if err != nil {
		t.Fatalf("resolveProfileName(\"\"): unexpected error: %v", err)
	}
	if got.ID != work.ID {
		t.Fatalf("resolveProfileName(\"\") = %q, want the last-used profile %q", got.ID, work.ID)
	}
}

// TestResolveProfileNameDefaultsToLocalWithNoLastUsed verifies the
// permanent-Local fallback when the store has never recorded a last-used
// profile (a fresh/unconfigured install).
func TestResolveProfileNameDefaultsToLocalWithNoLastUsed(t *testing.T) {
	store := newTestStore(t)

	got, err := resolveProfileName(store, "")
	if err != nil {
		t.Fatalf("resolveProfileName(\"\"): unexpected error: %v", err)
	}
	if got.ID != profiles.LocalProfileID {
		t.Fatalf("resolveProfileName(\"\") = %q, want %q", got.ID, profiles.LocalProfileID)
	}
}

// fakeOwnership is a registryOwnership test double that reports whatever
// name/scope pairs it is seeded with, independent of the real on-disk
// registry's one-entry-per-name storage constraint — letting the ambiguous
// (more-than-one-owner) branch of resolveShellProvider be exercised
// directly, without relying on real limactl or a real registry file.
type fakeOwnership map[registry.Scope]bool

func (f fakeOwnership) IsManagedInScope(_ string, scope registry.Scope) bool {
	return f[scope]
}

// TestResolveShellProviderAmbiguous verifies `sand shell NAME` refuses to
// guess when NAME is owned by more than one enabled connection profile: it
// must list the candidate profiles by name (not just error out blindly), and
// an explicit --profile must disambiguate.
func TestResolveShellProviderAmbiguous(t *testing.T) {
	store := newTestStore(t) // has the permanent, enabled "local" profile
	work, err := store.Add(profiles.Profile{
		Name: "work", Type: profiles.TypeRemoteSSH,
		Host: "work.example.com", User: "dev", Port: 22,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	owned := fakeOwnership{
		registry.LocalScope:   true,
		scopeForProfile(work): true,
	}

	_, err = resolveShellProvider(store, owned, "shared", "")
	if err == nil {
		t.Fatal("resolveShellProvider: want an ambiguous-name error, got nil")
	}
	for _, want := range []string{"more than one", "local", "work"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("resolveShellProvider ambiguous error = %q, want it to contain %q", err.Error(), want)
		}
	}

	// An explicit --profile disambiguates.
	prov, err := resolveShellProvider(store, owned, "shared", "work")
	if err != nil {
		t.Fatalf("resolveShellProvider(--profile work): unexpected error: %v", err)
	}
	if prov == nil {
		t.Fatal("resolveShellProvider(--profile work): want a non-nil provider")
	}
}

// TestResolveShellProviderUnknownName verifies that with more than one
// enabled profile and no owner found in the registry, resolveShellProvider
// reports a clean "no such VM" error rather than guessing a profile.
func TestResolveShellProviderUnknownName(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Add(profiles.Profile{
		Name: "work", Type: profiles.TypeRemoteSSH,
		Host: "work.example.com", User: "dev", Port: 22,
		Enabled: true,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	_, err := resolveShellProvider(store, fakeOwnership{}, "ghost", "")
	if err == nil {
		t.Fatal("resolveShellProvider: want error for a name owned by no profile, got nil")
	}
	if !strings.Contains(err.Error(), `"ghost"`) {
		t.Errorf("resolveShellProvider error = %q, want it to name the missing VM", err.Error())
	}
}

// TestResolveShellProviderSingleProfileIgnoresRegistry verifies that with
// only one enabled profile (the common, unconfigured case), resolveShellProvider
// uses it directly without consulting the registry at all — preserving `sand
// shell`'s original behaviour of attaching to ANY VM the one configured
// backend knows about, managed or not.
func TestResolveShellProviderSingleProfileIgnoresRegistry(t *testing.T) {
	store := newTestStore(t) // only "local" is enabled

	prov, err := resolveShellProvider(store, fakeOwnership{}, "whatever-unmanaged-name", "")
	if err != nil {
		t.Fatalf("resolveShellProvider: unexpected error: %v", err)
	}
	if prov == nil {
		t.Fatal("resolveShellProvider: want a non-nil provider")
	}
}
