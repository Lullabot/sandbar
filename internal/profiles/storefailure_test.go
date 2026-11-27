package profiles

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

const savedProfilesYAML = "version: 1\nprofiles:\n  - id: local\n    name: Local\n    type: local\n    enabled: true\n  - id: r1\n    name: r1\n    type: remote-ssh\n    enabled: true\n    host: h\n    user: u\n    port: 22\n"

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("original file is gone: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file was rewritten:\n got %s\nwant %s", got, want)
	}
}

func newRemote(name string) Profile {
	return Profile{Name: name, Type: TypeRemoteSSH, Enabled: true, Host: "h2", User: "u", Port: 22}
}

// TestUnreadableProfilesAreNeverSavedOver: the load falls back to an in-memory
// Local profile; a write must not persist that over the unreadable file.
func TestUnreadableProfilesAreNeverSavedOver(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod 000 does not stop root from reading")
	}
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(savedProfilesYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	s, err := LoadFrom(path)
	if err == nil {
		t.Fatal("LoadFrom of an unreadable file: want an error")
	}
	if _, err := s.Add(newRemote("r2")); err == nil {
		t.Fatal("Add over an unreadable file: want an error, got nil")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, path, []byte(savedProfilesYAML))
}

// TestNewerProfilesAtLoadAreNeverSavedOver is the load-time newer-schema case.
func TestNewerProfilesAtLoadAreNeverSavedOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	newer := []byte("version: 99\nprofiles:\n  - id: local\n    name: Local\n    type: local\n    enabled: true\n")
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadFrom(path)
	if err == nil {
		t.Fatal("LoadFrom of a newer file: want an error")
	}
	if _, err := s.Add(newRemote("r2")); err == nil {
		t.Fatal("Add over a newer file: want an error, got nil")
	}
	assertFileBytes(t, path, newer)
}

// TestUndecodableProfilesKeepTheirBytes covers a truncated file at load
// (quarantined) and one that goes bad under a running process (refused).
func TestUndecodableProfilesKeepTheirBytes(t *testing.T) {
	truncated := []byte("version: 1\nprofiles:\n  - id: local\n    name: [Loc")

	t.Run("at load", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "profiles.yaml")
		if err := os.WriteFile(path, truncated, 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := LoadFrom(path)
		if err == nil {
			t.Fatal("want an error for a truncated file")
		}
		if _, err := s.Add(newRemote("r2")); err != nil {
			t.Fatalf("Add after quarantine: %v", err)
		}
		assertFileBytes(t, path+".corrupt", truncated)
	})

	t.Run("under a running process", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "profiles.yaml")
		s, err := LoadFrom(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, truncated, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Add(newRemote("r2")); err == nil {
			t.Fatal("Add over a file that went bad: want an error, got nil")
		}
		assertFileBytes(t, path, truncated)
	})
}

// TestRefusalHoldsWhenTheLockCannotBeTaken: an unserialized write must still
// honour the re-read error.
func TestRefusalHoldsWhenTheLockCannotBeTaken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	newer := []byte("version: 99\nprofiles: []\n")
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(newRemote("r2")); err == nil {
		t.Fatal("Add with the lock held elsewhere over a newer file: want an error")
	}
	assertFileBytes(t, path, newer)
}
