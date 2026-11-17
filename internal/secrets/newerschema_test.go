package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/registry"
)

// TestWriteRefusesNewerSchemaFile is the upgrade-under-an-old-binary case: a
// store written by a newer sandbar is refused at load, which leaves this
// process holding an EMPTY store. A write must then refuse too, not replace
// every saved secret with that empty copy.
func TestWriteRefusesNewerSchemaFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	newer := []byte(`{"version":99,"scopes":[{"provider":"lima","vms":{"web":{"":{"GH_TOKEN":"keep-me"}}}}],"future":true}`)
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadFrom(path)
	if err == nil {
		t.Fatal("LoadFrom of a newer-schema file: want an error")
	}
	if err := s.Set("other", registry.LocalScope, map[string]string{"K": "v"}); err == nil {
		t.Fatal("Set over a newer-schema file: want an error, got nil")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newer) {
		t.Fatalf("newer-schema file was rewritten:\n got %s\nwant %s", got, newer)
	}
}

// TestWriteAfterQuarantineStillSaves pins the case the refusal must NOT cover:
// a corrupt file is moved aside by the re-read, so saving loses nothing.
func TestWriteAfterQuarantineStillSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("web", registry.LocalScope, map[string]string{"K": "v"}); err != nil {
		t.Fatalf("Set after a corrupt file was quarantined: %v", err)
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatalf("corrupt file not kept aside: %v", err)
	}
	re, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if re.Get("web", registry.LocalScope)["K"] != "v" {
		t.Fatal("write after quarantine was not saved")
	}
}
