package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

type updatesProvider struct {
	*providerfake.Provider
	marker      provider.Provenance
	found       bool
	base, setup string
}

func TestUpdateMetadataLoadsDoNotWriteHostState(t *testing.T) {
	data := t.TempDir()
	config := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_CONFIG_HOME", config)
	index := filepath.Join(data, "sandbar", "managed-vms.json")
	if err := os.MkdirAll(filepath.Dir(index), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"version":3,"vms":[]}`)
	if err := os.WriteFile(index, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.LoadReadOnly(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(index); err != nil || !bytes.Equal(got, legacy) {
		t.Fatalf("read-only registry load rewrote file: %q, %v", got, err)
	}
	store, err := profiles.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(profiles.LocalProfileID); !ok {
		t.Fatal("missing in-memory local profile")
	}
	if _, err := os.Stat(filepath.Join(config, "sandbar", "profiles.yaml")); !os.IsNotExist(err) {
		t.Fatalf("read-only profiles load created file: %v", err)
	}
	if err := os.WriteFile(index, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.LoadReadOnly(); err == nil {
		t.Fatal("corrupt index should fail")
	}
	if _, err := os.Stat(index); err != nil {
		t.Fatalf("corrupt index was quarantined: %v", err)
	}
	profilePath := filepath.Join(config, "sandbar", "profiles.yaml")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, []byte("profiles: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := profiles.LoadReadOnly(); err == nil {
		t.Fatal("corrupt profiles should fail")
	}
	if _, err := os.Stat(profilePath); err != nil {
		t.Fatalf("corrupt profiles were quarantined: %v", err)
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	oldIndex := filepath.Join(data, "claude-code-ansible", "managed-vms.json")
	if err := os.MkdirAll(filepath.Dir(oldIndex), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldIndex, []byte(`{"version":3,"vms":[{"name":"dev","provider":"lima","base":"sandbar-base","config":{"name":"dev"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldReg, err := registry.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if !oldReg.IsManagedInScope("dev", registry.LocalScope) {
		t.Fatal("legacy index owner was not loaded")
	}
	if _, err := os.Stat(index); !os.IsNotExist(err) {
		t.Fatalf("legacy index was copied into new path: %v", err)
	}
	if _, err := os.Stat(oldIndex); err != nil {
		t.Fatalf("legacy index was removed: %v", err)
	}
}

func (p updatesProvider) ProvenanceOf(context.Context, string) (provider.Provenance, bool, error) {
	return p.marker, p.found, nil
}
func (p updatesProvider) Provenance(context.Context) (map[string]provider.Provenance, error) {
	return nil, nil
}
func (p updatesProvider) MarkManaged(context.Context, string, provider.Provenance) error {
	panic("read-only update check wrote marker")
}
func (p updatesProvider) Unmark(context.Context, string) error {
	panic("read-only update check removed marker")
}
func (p updatesProvider) DesiredBaseRevision(context.Context, vm.CreateConfig) string { return p.base }
func (p updatesProvider) DesiredSetupRevision(context.Context) string                 { return p.setup }

func TestDoUpdatesIndependentStatesAndReadOnly(t *testing.T) {
	marker := provider.NewProvenance(vm.CreateConfig{Name: "dev"}, false)
	marker.BaseRevision, marker.SetupRevision = "old-base", "new-setup"
	p := updatesProvider{Provider: &providerfake.Provider{GetFunc: func(name string) (vm.VM, error) {
		return vm.VM{Name: name}, nil
	}, StartFunc: func(string) error { panic("read-only update check started VM") },
	}, marker: marker, found: true, base: "new-base", setup: "new-setup"}
	var out bytes.Buffer
	if err := doUpdates(context.Background(), p, "dev", "local", true, &out); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Name    string               `json:"name"`
		Profile string               `json:"profile"`
		Base    provider.UpdateState `json:"base"`
		Setup   provider.UpdateState `json:"setup"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "dev" || got.Profile != "local" || got.Base != provider.UpdateAvailable || got.Setup != provider.UpdateCurrent {
		t.Fatalf("JSON = %+v", got)
	}
	p.found = false
	out.Reset()
	if err := doUpdates(context.Background(), p, "dev", "work team's host", false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "base version unknown") || !strings.Contains(out.String(), "setup version unknown") || !strings.Contains(out.String(), "sand reset dev --profile 'work team'\\''s host' --preserve-home") {
		t.Fatalf("legacy VM output = %q", out.String())
	}
}
