package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/agentprefs"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

func isolateCreateState(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
}

func bindCreateFake(p provider.Provider) createBinder {
	return func(store *profiles.Store, name string) (provider.Provider, registry.Scope, profiles.Profile, error) {
		profile, err := resolveProfileName(store, name)
		return p, registry.LocalScope, profile, err
	}
}

func createAgentArgs(name string, extra ...string) []string {
	return append([]string{"--name", name, "--user", "tester", "--git-name", "Test User", "--git-email", "test@example.com", "--timezone", "Etc/UTC"}, extra...)
}

func TestCreateRemembersAgentFlagsAcrossInvocations(t *testing.T) {
	isolateCreateState(t)
	var received []vm.CreateConfig
	p := &providerfake.Provider{
		HostFilesFunc: lima.LocalFiles,
		CreateFunc: func(_ context.Context, cfg vm.CreateConfig, _ provision.CreateOptions, _ io.Writer) error {
			received = append(received, cfg)
			return nil
		},
	}
	bind := bindCreateFake(p)
	for _, args := range [][]string{
		createAgentArgs("first", "--with-claude=false", "--with-codex", "--with-pi"),
		createAgentArgs("second"),
		createAgentArgs("empty", "--with-claude=false", "--with-codex=false", "--with-opencode=false", "--with-pi=false"),
		createAgentArgs("empty-again"),
	} {
		if err := runCreateWithBinding(args, bind); err != nil {
			t.Fatal(err)
		}
	}
	for i, cfg := range received {
		want := agentprefs.Selection{}
		if i < 2 {
			want = agentprefs.Selection{Codex: true, Pi: true}
		}
		if got := agentprefs.FromConfig(cfg); got != want {
			t.Fatalf("create %d reached provider with %+v, want %+v", i, got, want)
		}
	}
	if len(received) != 4 {
		t.Fatalf("provider received %d creates, want 4", len(received))
	}
	if err := runCreateWithBinding(createAgentArgs("invalid", "--cpus=0", "--with-claude"), bind); err == nil {
		t.Fatal("invalid create accepted")
	}
	saved, found, err := agentprefs.Load()
	if err != nil || !found || saved != (agentprefs.Selection{}) {
		t.Fatalf("invalid create changed saved all-off choice: %+v, %v, %v", saved, found, err)
	}
}

type createMarkerProvider struct {
	*providerfake.Provider
	marker provider.Provenance
}

func (p *createMarkerProvider) Provenance(context.Context) (map[string]provider.Provenance, error) {
	return map[string]provider.Provenance{"existing": p.marker}, nil
}
func (p *createMarkerProvider) ProvenanceOf(context.Context, string) (provider.Provenance, bool, error) {
	return p.marker, true, nil
}
func (p *createMarkerProvider) MarkManaged(_ context.Context, _ string, marker provider.Provenance) error {
	p.marker = marker
	return nil
}
func (p *createMarkerProvider) Unmark(context.Context, string) error { return nil }

func TestRecreateReplaysRecordedAgentsBeforeExplicitOverrides(t *testing.T) {
	for _, source := range []string{"marker-only", "marker-over-cache", "legacy-cache"} {
		t.Run(source, func(t *testing.T) {
			isolateCreateState(t)
			remembered := agentprefs.Selection{Claude: true, OpenCode: true}
			if err := agentprefs.Save(remembered); err != nil {
				t.Fatal(err)
			}
			previous := vm.DefaultCreateConfig()
			previous.Name = "existing"
			previous.WithClaude = false
			previous.WithCodex = true
			previous.WithPi = true
			if source != "marker-only" {
				reg, err := registry.Load()
				if err != nil {
					t.Fatal(err)
				}
				cached := previous
				if source == "marker-over-cache" {
					remembered.Apply(&cached)
				}
				if err := reg.Add(cached); err != nil {
					t.Fatal(err)
				}
			}
			var received vm.CreateConfig
			called := false
			fake := &providerfake.Provider{
				HostFilesFunc: lima.LocalFiles,
				ListFunc:      func() ([]vm.VM, error) { return []vm.VM{{Name: "existing"}}, nil },
				RecreateFunc: func(_ context.Context, cfg vm.CreateConfig, _ provision.CreateOptions, _ io.Writer) error {
					received, called = cfg, true
					return nil
				},
			}
			var p provider.Provider = fake
			if source != "legacy-cache" {
				p = &createMarkerProvider{Provider: fake, marker: provider.NewProvenance(previous, false)}
			}
			if err := runCreateWithBinding(createAgentArgs("existing", "--recreate", "--with-pi=false"), bindCreateFake(p)); err != nil {
				t.Fatal(err)
			}
			if got := agentprefs.FromConfig(received); !called || got != (agentprefs.Selection{Codex: true}) {
				t.Fatalf("recreate called=%v, agents=%+v; want only recorded Codex", called, got)
			}
			if saved, _, err := agentprefs.Load(); err != nil || saved != remembered {
				t.Fatalf("recreate replaced last-create preferences: %+v, %v", saved, err)
			}
		})
	}
}

func TestRecreateFirstMigratesRecordedBaseBeforeProvisioning(t *testing.T) {
	isolateCreateState(t)
	stampDir := filepath.Join(os.Getenv("LIMA_HOME"), "_sand")
	if err := os.MkdirAll(stampDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stampDir, "custom-base.playbook-version"), []byte("v2:old:codex:template-gen2"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := vm.DefaultCreateConfig()
	previous.Name, previous.BaseName = "existing", "custom-base"
	previous.WithClaude, previous.WithCodex = false, true
	called := false
	p := &createMarkerProvider{
		marker: provider.NewProvenance(previous, false),
		Provider: &providerfake.Provider{
			HostFilesFunc: lima.LocalFiles,
			ListFunc:      func() ([]vm.VM, error) { return []vm.VM{{Name: "existing"}}, nil },
			RecreateFunc: func(_ context.Context, cfg vm.CreateConfig, _ provision.CreateOptions, _ io.Writer) error {
				called = true
				selection, found, err := agentprefs.Load()
				if err != nil || !found || selection != (agentprefs.Selection{Codex: true}) {
					t.Fatalf("preferences were not migrated before provisioning: %+v, %v, %v", selection, found, err)
				}
				if cfg.WithClaude || !cfg.WithCodex {
					t.Fatalf("recorded explicit opt-out lost: %+v", agentprefs.FromConfig(cfg))
				}
				return nil
			},
		},
	}
	if err := runCreateWithBinding(createAgentArgs("existing", "--recreate"), bindCreateFake(p)); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("recreate did not reach provider")
	}
}
