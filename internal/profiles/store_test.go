package profiles

import (
	"os"
	"path/filepath"
	"testing"
)

func testPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "profiles.yaml")
}

func TestLoadFromSeedsLocalProfileWhenFileMissing(t *testing.T) {
	path := testPath(t)

	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	list := s.List()
	if len(list) != 1 {
		t.Fatalf("List() = %d profiles, want 1", len(list))
	}
	p := list[0]
	if p.ID != LocalProfileID {
		t.Errorf("seeded profile ID = %q, want %q", p.ID, LocalProfileID)
	}
	if p.Type != TypeLocal {
		t.Errorf("seeded profile Type = %q, want %q", p.Type, TypeLocal)
	}
	if !p.Enabled {
		t.Error("seeded profile should be enabled")
	}

	// The seed must have been persisted immediately.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected seeded file to be persisted at %s: %v", path, err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := testPath(t)

	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	remote := Profile{
		Name:         "prod",
		Type:         TypeRemoteSSH,
		Enabled:      true,
		Host:         "example.com",
		User:         "alice",
		Port:         22,
		IdentityPath: "/home/alice/.ssh/id_ed25519",
		LimaHome:     "/home/alice/.lima",
	}
	added, err := s.Add(remote)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if added.ID == "" {
		t.Fatal("Add() did not assign an ID")
	}
	if added.ID == LocalProfileID {
		t.Error("Add() must not reuse LocalProfileID for a remote profile")
	}

	s2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("second LoadFrom() error = %v", err)
	}
	list := s2.List()
	if len(list) != 2 {
		t.Fatalf("List() after reload = %d profiles, want 2", len(list))
	}

	var found bool
	for _, p := range list {
		if p.ID == added.ID {
			found = true
			if p.Name != "prod" || p.Host != "example.com" || p.User != "alice" || p.Port != 22 {
				t.Errorf("reloaded profile mismatch: %+v", p)
			}
		}
	}
	if !found {
		t.Error("reloaded store missing the added remote profile")
	}
}

func TestSaveIsAtomic(t *testing.T) {
	path := testPath(t)
	dir := filepath.Dir(path)

	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if _, err := s.Add(Profile{Name: "r1", Type: TypeRemoteSSH, Enabled: true, Host: "h1", User: "u1", Port: 22}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file after save: %s", e.Name())
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected final file at %s: %v", path, err)
	}
}

func TestCorruptFileIsQuarantined(t *testing.T) {
	path := testPath(t)
	if err := os.WriteFile(path, []byte("not: valid: yaml: [["), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	s, err := LoadFrom(path)
	if err == nil {
		t.Fatal("LoadFrom() on corrupt file: want error, got nil")
	}

	if _, statErr := os.Stat(path + ".corrupt"); statErr != nil {
		t.Errorf("expected corrupt file quarantined at %s.corrupt: %v", path, statErr)
	}
	quarantined, readErr := os.ReadFile(path + ".corrupt")
	if readErr != nil {
		t.Fatalf("ReadFile(%s.corrupt) error = %v", path, readErr)
	}
	if string(quarantined) != "not: valid: yaml: [[" {
		t.Errorf("quarantined file content = %q, want original corrupt content", quarantined)
	}
	// The seeded default is persisted back to path (a mangled file must not
	// brick startup), so path exists again with fresh, valid content.
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("expected reseeded file at %s: %v", path, statErr)
	}

	// The returned store must still be usable and seeded with Local.
	list := s.List()
	if len(list) != 1 || list[0].ID != LocalProfileID {
		t.Errorf("store after quarantine = %+v, want single seeded Local profile", list)
	}
}

func TestEnableDisableToggle(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	added, err := s.Add(Profile{Name: "r1", Type: TypeRemoteSSH, Enabled: true, Host: "h1", User: "u1", Port: 22})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if err := s.Disable(added.ID); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	p, ok := s.Get(added.ID)
	if !ok || p.Enabled {
		t.Errorf("after Disable(): Get() = %+v, ok=%v; want Enabled=false", p, ok)
	}

	if err := s.Enable(added.ID); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	p, ok = s.Get(added.ID)
	if !ok || !p.Enabled {
		t.Errorf("after Enable(): Get() = %+v, ok=%v; want Enabled=true", p, ok)
	}

	// Reload to confirm the toggle persisted without losing other config.
	s2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	p2, ok := s2.Get(added.ID)
	if !ok || !p2.Enabled || p2.Host != "h1" {
		t.Errorf("reloaded profile after toggle = %+v, ok=%v", p2, ok)
	}
}

func TestLastUsedByIDSurvivesRename(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	added, err := s.Add(Profile{Name: "prod", Type: TypeRemoteSSH, Enabled: true, Host: "h1", User: "u1", Port: 22})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if err := s.SetLastUsed(added.ID); err != nil {
		t.Fatalf("SetLastUsed() error = %v", err)
	}

	updated := added
	updated.Name = "prod-renamed"
	if _, err := s.Update(updated); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if got := s.LastUsed(); got != added.ID {
		t.Errorf("LastUsed() after rename = %q, want %q", got, added.ID)
	}

	// Reload from disk to confirm the pointer (stored by ID) persisted too.
	s2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if got := s2.LastUsed(); got != added.ID {
		t.Errorf("reloaded LastUsed() = %q, want %q", got, added.ID)
	}
	p, ok := s2.Get(added.ID)
	if !ok || p.Name != "prod-renamed" {
		t.Errorf("reloaded renamed profile = %+v, ok=%v", p, ok)
	}
}

func TestDuplicateTargetRejected(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if _, err := s.Add(Profile{Name: "a", Type: TypeRemoteSSH, Enabled: true, Host: "h", User: "u", Port: 22}); err != nil {
		t.Fatalf("first Add() error = %v", err)
	}

	_, err = s.Add(Profile{Name: "b", Type: TypeRemoteSSH, Enabled: true, Host: "h", User: "u", Port: 22})
	if err == nil {
		t.Fatal("Add() with duplicate target: want error, got nil")
	}

	// A disabled duplicate target must be allowed (only enabled ones collide).
	if _, err := s.Add(Profile{Name: "c", Type: TypeRemoteSSH, Enabled: false, Host: "h", User: "u", Port: 22}); err != nil {
		t.Errorf("Add() with disabled duplicate target: want no error, got %v", err)
	}
}

func TestRemoveDeletesRemoteProfileAndPersists(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	added, err := s.Add(Profile{Name: "r1", Type: TypeRemoteSSH, Enabled: true, Host: "h1", User: "u1", Port: 22})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if err := s.Remove(added.ID); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, ok := s.Get(added.ID); ok {
		t.Error("removed profile should no longer be present")
	}
	if list := s.List(); len(list) != 1 || list[0].ID != LocalProfileID {
		t.Fatalf("List() after remove = %+v, want just the permanent Local profile", list)
	}

	// Reload to confirm the removal persisted.
	s2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if _, ok := s2.Get(added.ID); ok {
		t.Error("reloaded store should not have the removed profile")
	}
}

func TestRemoveLocalProfileRefused(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if err := s.Remove(LocalProfileID); err == nil {
		t.Fatal("Remove(LocalProfileID): want error, got nil")
	}
	if list := s.List(); len(list) != 1 {
		t.Fatalf("the local profile must still be present, got %+v", list)
	}
}

func TestRemoveUnknownIDErrors(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if err := s.Remove("nonexistent"); err == nil {
		t.Fatal("Remove() with an unknown id: want error, got nil")
	}
}

func TestUpdatePersistsFieldChangesAndRoundTrips(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	added, err := s.Add(Profile{Name: "prod", Type: TypeRemoteSSH, Enabled: true, Host: "h1", User: "u1", Port: 22})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	updated := added
	updated.Host = "h2"
	updated.Port = 2222
	updated.User = "u2"
	got, err := s.Update(updated)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got.Host != "h2" || got.Port != 2222 || got.User != "u2" {
		t.Errorf("Update() returned = %+v, want the updated fields", got)
	}

	// Reload to confirm the update persisted (the save round trip).
	s2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	p2, ok := s2.Get(added.ID)
	if !ok || p2.Host != "h2" || p2.Port != 2222 || p2.User != "u2" {
		t.Errorf("reloaded profile after Update() = %+v, ok=%v", p2, ok)
	}
}

func TestUpdateUnknownIDErrors(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if _, err := s.Update(Profile{ID: "nonexistent", Name: "x", Type: TypeRemoteSSH}); err == nil {
		t.Fatal("Update() with an unknown id: want error, got nil")
	}
}

func TestUpdateTypeImmutableRejected(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	added, err := s.Add(Profile{Name: "prod", Type: TypeRemoteSSH, Enabled: true, Host: "h", User: "u", Port: 22})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	changed := added
	changed.Type = TypeLocal
	if _, err := s.Update(changed); err == nil {
		t.Fatal("Update() changing Type: want error, got nil")
	}

	// The profile must be unchanged after the rejected update.
	p, ok := s.Get(added.ID)
	if !ok || p.Type != TypeRemoteSSH {
		t.Errorf("profile after rejected type change = %+v, ok=%v, want Type unchanged", p, ok)
	}
}

// TestSaveMkdirFailureReturnsError drives save()'s MkdirAll error branch
// directly: a store loaded normally, then repointed (s.path — same package,
// so the unexported field is reachable) at a path whose parent directory
// component is actually a REGULAR FILE, which can never be mkdir'd into. Any
// mutation that calls save() must surface that failure rather than silently
// swallowing it.
func TestSaveMkdirFailureReturnsError(t *testing.T) {
	s, err := LoadFrom(testPath(t))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed blocking file: %v", err)
	}
	s.path = filepath.Join(blocker, "sub", "profiles.yaml")

	if _, err := s.Add(Profile{Name: "r1", Type: TypeRemoteSSH, Enabled: true, Host: "h", User: "u", Port: 22}); err == nil {
		t.Fatal("Add() should surface save()'s MkdirAll failure, got nil error")
	}
}

// TestSaveRenameFailureReturnsError drives save()'s os.Rename error branch:
// once the target path is a DIRECTORY rather than a regular file, renaming
// the freshly written temp file over it is never allowed, so a mutation
// must surface that failure too.
func TestSaveRenameFailureReturnsError(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path) // seeds and persists the Local profile at path
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove seeded file: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("replace it with a directory: %v", err)
	}

	if _, err := s.Add(Profile{Name: "r1", Type: TypeRemoteSSH, Enabled: true, Host: "h", User: "u", Port: 22}); err == nil {
		t.Fatal("Add() should surface save()'s Rename failure when the target path is a directory, got nil error")
	}
}

func TestDefaultPathFallsBackToHomeConfigWithoutXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := defaultPath()
	want := filepath.Join(home, ".config", "sandbar", "profiles.yaml")
	if got != want {
		t.Errorf("defaultPath() = %q, want %q", got, want)
	}
}

func TestSecondLocalRejected(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	_, err = s.Add(Profile{Name: "local2", Type: TypeLocal, Enabled: true})
	if err == nil {
		t.Fatal("Add() with a second Local profile: want error, got nil")
	}
}
