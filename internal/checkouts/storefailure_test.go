package checkouts

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lullabot/sandbar/internal/registry"
)

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

func seededRegistry(t *testing.T) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "checkout-registry.json")
	r, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Set(registry.LocalScope, "keep", VMCheckouts{Checkouts: []Checkout{sampleCheckout("/home/u/a")}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, data
}

// TestUnreadableRegistryIsNeverSavedOver: the file exists but cannot be read
// while its directory stays writable. A write must refuse rather than replace
// every VM's cached checkouts with this process's empty copy.
func TestUnreadableRegistryIsNeverSavedOver(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod 000 does not stop root from reading")
	}
	path, saved := seededRegistry(t)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	r, err := LoadFrom(path)
	if err == nil {
		t.Fatal("LoadFrom of an unreadable file: want an error")
	}
	if err := r.Set(registry.LocalScope, "other", VMCheckouts{}); err == nil {
		t.Fatal("Set over an unreadable file: want an error, got nil")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, path, saved)
}

// TestNewerRegistryAtLoadIsNeverSavedOver is the load-time newer-schema case.
func TestNewerRegistryAtLoadIsNeverSavedOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout-registry.json")
	newer := []byte(`{"version":99,"vms":[],"future":true}`)
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := LoadFrom(path)
	if err == nil {
		t.Fatal("LoadFrom of a newer file: want an error")
	}
	if err := r.Set(registry.LocalScope, "other", VMCheckouts{}); err == nil {
		t.Fatal("Set over a newer file: want an error, got nil")
	}
	assertFileBytes(t, path, newer)
}

// TestUndecodableRegistryKeepsItsBytes: a file that goes bad under a running
// process must be preserved (as .corrupt), not silently overwritten.
func TestUndecodableRegistryKeepsItsBytes(t *testing.T) {
	_, saved := seededRegistry(t)
	truncated := saved[:len(saved)/2]

	t.Run("at load", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "checkout-registry.json")
		if err := os.WriteFile(path, truncated, 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := LoadFrom(path)
		if err == nil {
			t.Fatal("want an error for a truncated file")
		}
		if err := r.Set(registry.LocalScope, "n", VMCheckouts{}); err != nil {
			t.Fatalf("Set after quarantine: %v", err)
		}
		assertFileBytes(t, path+".corrupt", truncated)
	})

	t.Run("under a running process", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "checkout-registry.json")
		r, err := LoadFrom(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, truncated, 0o600); err != nil {
			t.Fatal(err)
		}
		// Either refusing or quarantining first is acceptable; losing the
		// bytes is not.
		_ = r.Set(registry.LocalScope, "n", VMCheckouts{})
		if got, err := os.ReadFile(path); err == nil && bytes.Equal(got, truncated) {
			return
		}
		assertFileBytes(t, path+".corrupt", truncated)
	})
}

// TestRefusalHoldsWhenTheLockCannotBeTaken: an unserialized write must still
// honour the re-read error.
func TestRefusalHoldsWhenTheLockCannotBeTaken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout-registry.json")
	r, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	newer := []byte(`{"version":99,"vms":[],"future":true}`)
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
	if err := r.Set(registry.LocalScope, "other", VMCheckouts{}); err == nil {
		t.Fatal("Set with the lock held elsewhere over a newer file: want an error")
	}
	assertFileBytes(t, path, newer)
}
