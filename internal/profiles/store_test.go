package profiles

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// TestLoadFromReadErrorSeedsUsableLocalStore is finding 6's regression test:
// a read failure that is NOT "file does not exist" (e.g. a permission error)
// must not degrade to an empty, unseeded store — that locks the user out of
// even purely-local VMs (runTUI's "no enabled connection profiles" exit,
// `sand create` failing). Using a directory at path (rather than chmod,
// which behaves inconsistently when tests run as root) forces os.ReadFile to
// fail with something other than fs.ErrNotExist, portably. The fix must
// return the store seeded with a usable, ENABLED Local profile alongside the
// error — and must NOT persist over or quarantine the unreadable path, since
// (unlike a corrupt file) there is no evidence its content is actually bad.
func TestLoadFromReadErrorSeedsUsableLocalStore(t *testing.T) {
	path := testPath(t)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("Mkdir(%s) error = %v", path, err)
	}

	s, err := LoadFrom(path)
	if err == nil {
		t.Fatal("LoadFrom() on an unreadable path: want error, got nil")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("LoadFrom() error = %v, want a read error distinct from fs.ErrNotExist", err)
	}

	list := s.List()
	if len(list) != 1 || list[0].ID != LocalProfileID || !list[0].Enabled {
		t.Fatalf("LoadFrom() on read error returned store %+v, want a single enabled seeded Local profile", list)
	}

	// Must not have quarantined the path (it may be intact, just unreadable).
	if _, statErr := os.Stat(path + ".corrupt"); !os.IsNotExist(statErr) {
		t.Errorf("LoadFrom() must not quarantine an unreadable (not known-corrupt) path")
	}
	// Must not have persisted over the unreadable path either — it is still
	// the directory we created, not a freshly-written seeded file.
	fi, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("Stat(%s) error = %v", path, statErr)
	}
	if !fi.IsDir() {
		t.Error("LoadFrom() must not persist a seeded store over an unreadable path")
	}
}

// TestAddUnknownTypeRejected is part of finding 3's regression coverage: the
// store must refuse to persist a profile whose Type is neither "local" nor
// "remote-ssh" — a hand-edited typo like "remote_ssh" must be caught here,
// not silently treated as local.
func TestAddUnknownTypeRejected(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	_, err = s.Add(Profile{Name: "weird", Type: Type("remote_ssh"), Enabled: true, Host: "h", User: "u", Port: 22})
	if err == nil {
		t.Fatal("Add() with an unknown Type: want error, got nil")
	}
	if !strings.Contains(err.Error(), "remote_ssh") {
		t.Errorf("Add() unknown-type error = %q, want it to name the bad type %q", err.Error(), "remote_ssh")
	}
}

// TestUpdateUnknownTypeRejected mirrors TestAddUnknownTypeRejected for
// Update: a profile that reached the store with an unrecognised Type via
// LoadFrom (which must not lock out the rest of the file — see
// TestLoadFromToleratesUnknownTypeEntry below) must still be rejected by
// validate() when the user tries to Update it (e.g. editing it in the TUI)
// without fixing the Type.
func TestUpdateUnknownTypeRejected(t *testing.T) {
	path := testPath(t)
	yamlContent := `version: 1
profiles:
  - id: local
    name: local
    type: local
    enabled: true
  - id: weird1
    name: weird
    type: remote_ssh
    enabled: true
    host: example.com
    user: dev
    port: 22
`
	if err := os.WriteFile(path, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	p, ok := s.Get("weird1")
	if !ok {
		t.Fatal("LoadFrom() dropped the unknown-type profile")
	}

	p.Host = "example2.com" // an edit that leaves the bad Type untouched
	if _, err := s.Update(p); err == nil {
		t.Fatal("Update() of a profile with an unknown Type: want error, got nil")
	}
}

// TestLoadFromToleratesUnknownTypeEntry confirms the other half of finding
// 3: LoadFrom itself must NOT hard-fail the whole file just because one
// entry has an unrecognised Type — a bad entry must not lock the user out of
// the other (good) profiles in the file. The unknown-type entry loads as-is;
// it is fleet-build (provider.BuildFleet) and the store's write path
// (Add/Update) that must surface/reject it, not LoadFrom.
func TestLoadFromToleratesUnknownTypeEntry(t *testing.T) {
	path := testPath(t)
	yamlContent := `version: 1
profiles:
  - id: local
    name: local
    type: local
    enabled: true
  - id: weird1
    name: weird
    type: remote_ssh
    enabled: true
    host: example.com
    user: dev
    port: 22
`
	if err := os.WriteFile(path, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() on a file with an unknown-type entry: want no error, got %v", err)
	}
	if _, ok := s.Get("local"); !ok {
		t.Error("LoadFrom() must still load the good Local profile alongside the bad entry")
	}
	if _, ok := s.Get("weird1"); !ok {
		t.Error("LoadFrom() must still load the unknown-type entry itself (not silently drop it)")
	}
}

// TestAddRemoteSSHEmptyHostRejected is finding 9's regression test: the store
// must refuse an empty Host on a RemoteSSH profile rather than letting it
// through to fail later with a cryptic `ssh user@` error.
func TestAddRemoteSSHEmptyHostRejected(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	_, err = s.Add(Profile{Name: "bad", Type: TypeRemoteSSH, Enabled: true, Host: "", User: "u", Port: 22})
	if err == nil {
		t.Fatal("Add() with an empty Host: want error, got nil")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("Add() empty-host error = %q, want it to mention the missing host", err.Error())
	}
}

// TestLoadFromCanonicalizesPortForRemoteSSH is finding 8's regression test: a
// hand-edited profile with no `port:` must load with Port canonicalized to
// 22 (RemoteSSH's implicit SSH default, matching the retired
// resolveTargetConfig's defaultRemotePort), so its scope/remoteTarget agree
// with a profile that spells the port out explicitly — never "host:0".
func TestLoadFromCanonicalizesPortForRemoteSSH(t *testing.T) {
	path := testPath(t)
	yamlContent := `version: 1
profiles:
  - id: local
    name: local
    type: local
    enabled: true
  - id: noport
    name: noport
    type: remote-ssh
    enabled: true
    host: example.com
    user: dev
`
	if err := os.WriteFile(path, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	p, ok := s.Get("noport")
	if !ok {
		t.Fatal("LoadFrom() dropped the noport profile")
	}
	if p.Port != 22 {
		t.Fatalf("noport profile Port = %d, want canonicalized 22", p.Port)
	}

	explicit := Profile{Host: "example.com", User: "dev", Port: 22}
	if p.remoteTarget() != explicit.remoteTarget() {
		t.Fatalf("remoteTarget() = %q, want it to equal the explicit-port profile's %q", p.remoteTarget(), explicit.remoteTarget())
	}
}

// TestAddProxmoxNoHostRejected confirms a proxmox profile without a Host is
// rejected, mirroring the RemoteSSH empty-host guard.
func TestAddProxmoxNoHostRejected(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	_, err = s.Add(Profile{Name: "pve", Type: TypeProxmox, Enabled: true, Node: "pve1", Pool: "sandbar", TokenFile: "/tmp/token"})
	if err == nil {
		t.Fatal("Add() proxmox profile with no host: want error, got nil")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("Add() no-host proxmox error = %q, want it to mention the missing host", err.Error())
	}
}

// TestAddProxmoxNoTokenFileRejected confirms a proxmox profile without a
// TokenFile is rejected — profiles.yaml is secret-free, so a proxmox profile
// with nowhere to load its token from can never actually connect.
func TestAddProxmoxNoTokenFileRejected(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	_, err = s.Add(Profile{Name: "pve", Type: TypeProxmox, Enabled: true, Host: "pve.example.com", Node: "pve1", Pool: "sandbar"})
	if err == nil {
		t.Fatal("Add() proxmox profile with no token_file: want error, got nil")
	}
	if !strings.Contains(err.Error(), "token_file") {
		t.Errorf("Add() no-token_file proxmox error = %q, want it to mention token_file", err.Error())
	}
}

// TestAddProxmoxDuplicateTargetRejected confirms two enabled proxmox
// profiles resolving to the same host+node+pool are rejected, mirroring
// TestDuplicateTargetRejected for RemoteSSH.
func TestAddProxmoxDuplicateTargetRejected(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	base := Profile{Type: TypeProxmox, Enabled: true, Host: "pve.example.com", Node: "pve1", Pool: "sandbar", TokenFile: "/tmp/token"}

	first := base
	first.Name = "a"
	if _, err := s.Add(first); err != nil {
		t.Fatalf("first Add() error = %v", err)
	}

	second := base
	second.Name = "b"
	_, err = s.Add(second)
	if err == nil {
		t.Fatal("Add() with duplicate proxmox target: want error, got nil")
	}

	// A disabled duplicate target must be allowed (only enabled ones collide).
	third := base
	third.Name = "c"
	third.Enabled = false
	if _, err := s.Add(third); err != nil {
		t.Errorf("Add() with disabled duplicate proxmox target: want no error, got %v", err)
	}
}

// TestProxmoxRoundTripHasNoTokenValue writes a proxmox profile whose
// token_file points at a real (secret-bearing) file, reloads the store, and
// confirms every field survives while the persisted YAML never contains the
// token's actual value — only the path to it.
func TestProxmoxRoundTripHasNoTokenValue(t *testing.T) {
	path := testPath(t)
	tokenPath := filepath.Join(t.TempDir(), "pve-token")
	const secretValue = "super-secret-pve-api-token-value"
	if err := os.WriteFile(tokenPath, []byte(secretValue), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	pve := Profile{
		Name:         "pve",
		Type:         TypeProxmox,
		Enabled:      true,
		Host:         "pve.example.com",
		Node:         "pve1",
		Pool:         "sandbar",
		Storage:      "local-lvm",
		ImageStorage: "local",
		BaseImage:    "https://ex.test/img.qcow2",
		Bridge:       "vmbr0",
		TokenFile:    tokenPath,
		Insecure:     true,
		CAFile:       "/etc/ssl/pve-ca.pem",
	}
	added, err := s.Add(pve)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	s2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	got, ok := s2.Get(added.ID)
	if !ok {
		t.Fatal("reloaded store missing the added proxmox profile")
	}
	if got.Name != pve.Name || got.Host != pve.Host || got.Node != pve.Node || got.Pool != pve.Pool ||
		got.Storage != pve.Storage || got.ImageStorage != pve.ImageStorage || got.BaseImage != pve.BaseImage ||
		got.Bridge != pve.Bridge || got.TokenFile != pve.TokenFile || got.Insecure != pve.Insecure || got.CAFile != pve.CAFile {
		t.Errorf("reloaded proxmox profile = %+v, want fields to match %+v", got, pve)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if strings.Contains(string(raw), secretValue) {
		t.Fatalf("persisted profiles.yaml contains the secret token value: %s", raw)
	}
	if !strings.Contains(string(raw), tokenPath) {
		t.Errorf("persisted profiles.yaml should contain the token_file PATH %q, got: %s", tokenPath, raw)
	}
}

// TestDuplicateTargetCatchesMissingPortVsExplicit22 confirms finding 8's
// canonicalization actually closes the validation gap: a profile whose port
// is left unset must collide with an existing enabled profile on the SAME
// host:22, rather than looking like a distinct "host:0" target.
func TestDuplicateTargetCatchesMissingPortVsExplicit22(t *testing.T) {
	path := testPath(t)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if _, err := s.Add(Profile{Name: "a", Type: TypeRemoteSSH, Enabled: true, Host: "h", User: "u", Port: 22}); err != nil {
		t.Fatalf("first Add() error = %v", err)
	}

	_, err = s.Add(Profile{Name: "b", Type: TypeRemoteSSH, Enabled: true, Host: "h", User: "u"}) // Port omitted
	if err == nil {
		t.Fatal("Add() with host-without-port colliding with an existing host:22: want error, got nil")
	}
}
