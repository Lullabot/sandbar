package registry

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
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

func seededIndex(t *testing.T) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	r, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Add(vm.CreateConfig{Name: "keep", BaseName: "sandbar-base"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, data
}

// TestUnreadableIndexIsNeverSavedOver: the index exists but cannot be read
// while its directory stays writable, so a rewrite would succeed and replace
// every recorded VM with this process's empty copy.
func TestUnreadableIndexIsNeverSavedOver(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod 000 does not stop root from reading")
	}
	path, saved := seededIndex(t)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	r, err := LoadFrom(path)
	if err == nil {
		t.Fatal("LoadFrom of an unreadable index: want an error")
	}
	if err := r.Add(vm.CreateConfig{Name: "other", BaseName: "sandbar-base"}); err == nil {
		t.Fatal("Add over an unreadable index: want an error, got nil")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, path, saved)
}

// TestNewerIndexAtLoadIsNeverSavedOver is the load-time half of the newer
// schema case: the process starts with an empty registry alongside the error.
func TestNewerIndexAtLoadIsNeverSavedOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	newer := []byte(`{"version":99,"vms":[{"name":"new","provider":"lima"}],"future":true}`)
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := LoadFrom(path)
	if err == nil {
		t.Fatal("LoadFrom of a newer index: want an error")
	}
	// LoadFrom hands back a pathless registry here, so Add may succeed in
	// memory; the invariant is that the file itself is untouched.
	_ = r.Add(vm.CreateConfig{Name: "other", BaseName: "sandbar-base"})
	assertFileBytes(t, path, newer)
}

// TestUndecodableIndexIsQuarantinedWithItsBytes covers a truncated file at
// load and one that goes bad under a running process.
func TestUndecodableIndexIsQuarantinedWithItsBytes(t *testing.T) {
	_, saved := seededIndex(t)
	truncated := saved[:len(saved)/2]

	t.Run("at load", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "managed-vms.json")
		if err := os.WriteFile(path, truncated, 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := LoadFrom(path)
		if err == nil {
			t.Fatal("want an error for a truncated index")
		}
		if err := r.Add(vm.CreateConfig{Name: "n", BaseName: "sandbar-base"}); err != nil {
			t.Fatalf("Add after quarantine: %v", err)
		}
		assertFileBytes(t, path+".corrupt", truncated)
	})

	t.Run("under a running process", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "managed-vms.json")
		r, err := LoadFrom(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, truncated, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := r.Add(vm.CreateConfig{Name: "n", BaseName: "sandbar-base"}); err != nil {
			t.Fatalf("Add after quarantine: %v", err)
		}
		assertFileBytes(t, path+".corrupt", truncated)
	})
}

// TestRefusalHoldsWhenTheLockCannotBeTaken: when statelock gives up and the
// write proceeds unserialized, it must still re-read and honour the error.
func TestRefusalHoldsWhenTheLockCannotBeTaken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
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
	if err := r.Add(vm.CreateConfig{Name: "other", BaseName: "sandbar-base"}); err == nil {
		t.Fatal("Add with the lock held elsewhere over a newer file: want an error")
	}
	assertFileBytes(t, path, newer)
}
