package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lullabot/sandbar/internal/registry"
)

const savedSecretsJSON = `{"version":3,"scopes":[{"provider":"lima","vms":{"web":{"":{"GH_TOKEN":"keep-me"}}}}]}`

// holdLock takes the store's advisory lock from a second descriptor so the
// code under test has to give up waiting and proceed unserialized.
func holdLock(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
}

func assertUntouched(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("original file is gone: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file was rewritten:\n got %s\nwant %s", got, want)
	}
}

// TestUnreadableFileIsNeverSavedOver: the file exists but cannot be read
// (the directory stays writable, so a rewrite WOULD succeed). Every write
// must refuse and leave the bytes alone.
func TestUnreadableFileIsNeverSavedOver(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod 000 does not stop root from reading")
	}
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte(savedSecretsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	s, err := LoadFrom(path)
	if err == nil {
		t.Fatal("LoadFrom of an unreadable file: want an error")
	}
	if err := s.Set("other", registry.LocalScope, map[string]string{"K": "v"}); err == nil {
		t.Fatal("Set over an unreadable file: want an error, got nil")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	assertUntouched(t, path, []byte(savedSecretsJSON))
}

// TestUndecodableFileIsQuarantinedWithItsBytes: a truncated file is moved to
// .corrupt with its original content, at load and when it goes bad under a
// running process.
func TestUndecodableFileIsQuarantinedWithItsBytes(t *testing.T) {
	truncated := []byte(savedSecretsJSON[:len(savedSecretsJSON)/2])

	t.Run("at load", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "secrets.json")
		if err := os.WriteFile(path, truncated, 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := LoadFrom(path)
		if err == nil {
			t.Fatal("want an error for a truncated file")
		}
		if err := s.Set("web", registry.LocalScope, map[string]string{"K": "v"}); err != nil {
			t.Fatalf("Set after quarantine: %v", err)
		}
		assertUntouched(t, path+".corrupt", truncated)
	})

	t.Run("under a running process", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "secrets.json")
		s, err := LoadFrom(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, truncated, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.Set("web", registry.LocalScope, map[string]string{"K": "v"}); err != nil {
			t.Fatalf("Set after quarantine: %v", err)
		}
		assertUntouched(t, path+".corrupt", truncated)
	})
}

// TestRefusalHoldsWhenTheLockCannotBeTaken: statelock gives up after its wait
// budget and the write proceeds unserialized. That path must still re-read
// and honour a read error.
func TestRefusalHoldsWhenTheLockCannotBeTaken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	newer := []byte(`{"version":99,"scopes":[],"future":true}`)
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := LoadFrom(path)
	holdLock(t, path)
	if err := s.Set("other", registry.LocalScope, map[string]string{"K": "v"}); err == nil {
		t.Fatal("Set with the lock held elsewhere over a newer file: want an error")
	}
	assertUntouched(t, path, newer)
}
