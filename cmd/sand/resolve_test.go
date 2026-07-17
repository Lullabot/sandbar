package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
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

// stubNoProvenanceOwners stubs provenanceOfForProfile to report NO marker
// for every profile, so resolveShellProvider's new provenance-first probe
// (task 4) never attempts a real provider construction + ProvenanceOf call —
// which, for a profile like the "work" RemoteSSH fixture below, would
// otherwise be a genuine SSH round trip against a host that does not exist
// (AGENTS.md's hard rule against real limactl/SSH in tests). Every test in
// this file that seeds a fakeOwnership registry double to drive
// resolveShellProvider's decision needs this, so provenance falls straight
// through to that registry double exactly as it did before this task.
func stubNoProvenanceOwners(t *testing.T) {
	t.Helper()
	orig := provenanceOfForProfile
	t.Cleanup(func() { provenanceOfForProfile = orig })
	provenanceOfForProfile = func(profiles.Profile, string) (bool, error) { return false, nil }
}

// TestResolveShellProviderAmbiguous verifies `sand shell NAME` refuses to
// guess when NAME is owned by more than one enabled connection profile: it
// must list the candidate profiles by name (not just error out blindly), and
// an explicit --profile must disambiguate.
func TestResolveShellProviderAmbiguous(t *testing.T) {
	stubNoProvenanceOwners(t)
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
// enabled profile and no owner found in the registry NOR in the finding-7
// unmanaged-VM probe fallback, resolveShellProvider reports a clean "no such
// VM" error rather than guessing a profile. listForProfile is stubbed to a
// no-op (never hits a real backend — see probeUnmanagedOwners' tests for the
// fallback's own behaviour) so this stays a pure unit test.
func TestResolveShellProviderUnknownName(t *testing.T) {
	stubNoProvenanceOwners(t)
	store := newTestStore(t)
	if _, err := store.Add(profiles.Profile{
		Name: "work", Type: profiles.TypeRemoteSSH,
		Host: "work.example.com", User: "dev", Port: 22,
		Enabled: true,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	orig := listForProfile
	t.Cleanup(func() { listForProfile = orig })
	listForProfile = func(profiles.Profile) ([]vm.VM, error) { return nil, nil }

	_, err := resolveShellProvider(store, fakeOwnership{}, "ghost", "")
	if err == nil {
		t.Fatal("resolveShellProvider: want error for a name owned by no profile, got nil")
	}
	if !strings.Contains(err.Error(), `"ghost"`) {
		t.Errorf("resolveShellProvider error = %q, want it to name the missing VM", err.Error())
	}
}

// TestProviderForProfileUnknownTypeErrors is finding 3's regression test for
// the CLI's own conversion path (mirroring provider.BuildFleet's buildBinding):
// a profile with an unrecognised Type (e.g. a hand-edited "remote_ssh" typo)
// must be a hard error here too, not silently constructed as the local
// backend.
func TestProviderForProfileUnknownTypeErrors(t *testing.T) {
	p := profiles.Profile{
		ID: "weird1", Name: "weird", Type: profiles.Type("remote_ssh"), Enabled: true,
		Host: "example.com", User: "dev", Port: 22,
	}

	_, _, err := providerForProfile(p)
	if err == nil {
		t.Fatal("providerForProfile: want error for an unknown profile type, got nil")
	}
	if !strings.Contains(err.Error(), "remote_ssh") {
		t.Errorf("providerForProfile error = %q, want it to name the bad type %q", err.Error(), "remote_ssh")
	}
}

// TestProviderForProfileEmptyHostErrors is finding 9's regression test for
// the CLI's conversion path: a RemoteSSH profile with an empty Host must be
// a clear error here, not an obscure `ssh user@` failure later.
func TestProviderForProfileEmptyHostErrors(t *testing.T) {
	p := profiles.Profile{
		ID: "nohost", Name: "nohost", Type: profiles.TypeRemoteSSH, Enabled: true,
		Host: "", User: "dev", Port: 22,
	}

	_, _, err := providerForProfile(p)
	if err == nil {
		t.Fatal("providerForProfile: want error for an empty host, got nil")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("providerForProfile error = %q, want it to mention the missing host", err.Error())
	}
}

// TestResolveShellProviderFallsBackToUnmanagedProbeWhenRegistryEmpty is
// finding 7's regression test: before this task, `sand shell NAME` attached
// to ANY VM the (single) configured backend listed, managed or not (e.g. the
// base image `sand-base`, or a hand-made limactl VM). With more than one
// enabled profile, the registry alone now decides ownership, so an
// UNMANAGED VM yields zero registry owners and used to hard-fail with "no
// such VM" even though it plainly exists on one of the profiles. The fix:
// when the registry comes up empty (and no --profile was given), probe each
// enabled profile's provider List() for the name — exactly one hit must
// resolve, with local probed before remotes (stubbed here via the
// listForProfile seam, since a real List() would need limactl/SSH).
func TestResolveShellProviderFallsBackToUnmanagedProbeWhenRegistryEmpty(t *testing.T) {
	stubNoProvenanceOwners(t)
	store := newTestStore(t)
	work, err := store.Add(profiles.Profile{
		Name: "work", Type: profiles.TypeRemoteSSH,
		Host: "work.example.com", User: "dev", Port: 22,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	orig := listForProfile
	t.Cleanup(func() { listForProfile = orig })
	var probedOrder []string
	listForProfile = func(p profiles.Profile) ([]vm.VM, error) {
		probedOrder = append(probedOrder, p.ID)
		if p.ID == work.ID {
			return []vm.VM{{Name: "sand-base"}}, nil
		}
		return nil, nil // local: knows nothing about it via the registry-free probe
	}

	// Registry reports zero owners for "sand-base" (it is unmanaged).
	prov, err := resolveShellProvider(store, fakeOwnership{}, "sand-base", "")
	if err != nil {
		t.Fatalf("resolveShellProvider: unexpected error: %v", err)
	}
	if prov == nil {
		t.Fatal("resolveShellProvider: want a non-nil provider from the unmanaged-probe fallback")
	}
	if len(probedOrder) < 2 || probedOrder[0] != profiles.LocalProfileID {
		t.Errorf("probe order = %v, want local probed before the remote profile", probedOrder)
	}
}

// TestResolveShellProviderUnmanagedProbeAmbiguous verifies the fallback's
// multi-hit branch behaves exactly like the registry's own ambiguous case:
// more than one profile's List() reporting the name requires --profile.
func TestResolveShellProviderUnmanagedProbeAmbiguous(t *testing.T) {
	stubNoProvenanceOwners(t)
	store := newTestStore(t)
	if _, err := store.Add(profiles.Profile{
		Name: "work", Type: profiles.TypeRemoteSSH,
		Host: "work.example.com", User: "dev", Port: 22,
		Enabled: true,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	orig := listForProfile
	t.Cleanup(func() { listForProfile = orig })
	listForProfile = func(p profiles.Profile) ([]vm.VM, error) {
		return []vm.VM{{Name: "shared"}}, nil // every profile reports it
	}

	_, err := resolveShellProvider(store, fakeOwnership{}, "shared", "")
	if err == nil {
		t.Fatal("resolveShellProvider: want an ambiguous-name error from the fallback probe, got nil")
	}
	if !strings.Contains(err.Error(), "more than one") {
		t.Errorf("resolveShellProvider fallback ambiguous error = %q, want it to mention %q", err.Error(), "more than one")
	}
}

// TestResolveShellProviderUnmanagedProbeToleratesListErrors verifies the
// fallback's best-effort contract: a List() failure on one profile (e.g. an
// unreachable remote) must be treated as "not there", never abort the whole
// command — only when every profile comes up empty (or erroring) does the
// original "no such VM" error apply.
func TestResolveShellProviderUnmanagedProbeToleratesListErrors(t *testing.T) {
	stubNoProvenanceOwners(t)
	store := newTestStore(t)
	work, err := store.Add(profiles.Profile{
		Name: "work", Type: profiles.TypeRemoteSSH,
		Host: "work.example.com", User: "dev", Port: 22,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	orig := listForProfile
	t.Cleanup(func() { listForProfile = orig })
	listForProfile = func(p profiles.Profile) ([]vm.VM, error) {
		if p.ID == profiles.LocalProfileID {
			return nil, errors.New("boom: local unreachable somehow")
		}
		if p.ID == work.ID {
			return []vm.VM{{Name: "only-on-work"}}, nil
		}
		return nil, nil
	}

	prov, err := resolveShellProvider(store, fakeOwnership{}, "only-on-work", "")
	if err != nil {
		t.Fatalf("resolveShellProvider: want the List error on one profile to be tolerated, got: %v", err)
	}
	if prov == nil {
		t.Fatal("resolveShellProvider: want a non-nil provider")
	}

	// When NO profile has it, still a clean "no such VM" — not an aborted command.
	_, err = resolveShellProvider(store, fakeOwnership{}, "truly-nowhere", "")
	if err == nil || !strings.Contains(err.Error(), `"truly-nowhere"`) {
		t.Fatalf("resolveShellProvider(nowhere) error = %v, want a clean \"no such VM\" naming it", err)
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

// TestResolveShellProviderProvenanceOwnerWins is task 4's central shell-
// routing regression: with more than one enabled profile, a candidate whose
// PROVENANCE marker names NAME must resolve as the owner even though the
// registry (fakeOwnership) reports NO owner at all for it — proving
// provenance is consulted first and is authoritative on its own, not merely
// a tie-breaker alongside the registry.
func TestResolveShellProviderProvenanceOwnerWins(t *testing.T) {
	store := newTestStore(t) // has the permanent, enabled "local" profile
	work, err := store.Add(profiles.Profile{
		Name: "work", Type: profiles.TypeRemoteSSH,
		Host: "work.example.com", User: "dev", Port: 22,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	orig := provenanceOfForProfile
	t.Cleanup(func() { provenanceOfForProfile = orig })
	provenanceOfForProfile = func(p profiles.Profile, name string) (bool, error) {
		return p.ID == work.ID && name == "claude", nil
	}

	// The registry double reports NOTHING managed anywhere — if it were
	// consulted first (or at all), this would fall through to the (stubbed
	// to empty) unmanaged-VM probe and fail with "no such VM".
	prov, err := resolveShellProvider(store, fakeOwnership{}, "claude", "")
	if err != nil {
		t.Fatalf("resolveShellProvider: unexpected error: %v", err)
	}
	if prov == nil {
		t.Fatal("resolveShellProvider: want a non-nil provider resolved from the provenance marker")
	}
}

// TestResolveShellProviderProvenanceAmbiguous verifies provenance's ambiguous
// case behaves exactly like the registry's: more than one candidate's marker
// naming NAME requires --profile to disambiguate, and lists the candidates.
func TestResolveShellProviderProvenanceAmbiguous(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Add(profiles.Profile{
		Name: "work", Type: profiles.TypeRemoteSSH,
		Host: "work.example.com", User: "dev", Port: 22,
		Enabled: true,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	orig := provenanceOfForProfile
	t.Cleanup(func() { provenanceOfForProfile = orig })
	provenanceOfForProfile = func(profiles.Profile, string) (bool, error) {
		return true, nil // every candidate's marker claims the name
	}

	_, err := resolveShellProvider(store, fakeOwnership{}, "shared", "")
	if err == nil {
		t.Fatal("resolveShellProvider: want an ambiguous-name error from provenance, got nil")
	}
	if !strings.Contains(err.Error(), "more than one") {
		t.Errorf("resolveShellProvider provenance-ambiguous error = %q, want it to mention %q", err.Error(), "more than one")
	}
}

// TestResolveShellProviderProvenanceErrorFallsBackToRegistry verifies the
// best-effort contract of the provenance probe: a candidate whose provenance
// read errors (e.g. an unreachable remote) is treated as "no marker", not a
// hard failure — resolution still falls through to the registry (LEGACY,
// one-release fallback) rather than aborting the whole lookup.
func TestResolveShellProviderProvenanceErrorFallsBackToRegistry(t *testing.T) {
	store := newTestStore(t)
	work, err := store.Add(profiles.Profile{
		Name: "work", Type: profiles.TypeRemoteSSH,
		Host: "work.example.com", User: "dev", Port: 22,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	orig := provenanceOfForProfile
	t.Cleanup(func() { provenanceOfForProfile = orig })
	provenanceOfForProfile = func(profiles.Profile, string) (bool, error) {
		return false, errors.New("boom: host unreachable")
	}

	owned := fakeOwnership{scopeForProfile(work): true}

	prov, err := resolveShellProvider(store, owned, "claude", "")
	if err != nil {
		t.Fatalf("resolveShellProvider: want the provenance error tolerated via registry fallback, got: %v", err)
	}
	if prov == nil {
		t.Fatal("resolveShellProvider: want a non-nil provider from the registry fallback")
	}
}
