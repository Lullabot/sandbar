package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"
)

func TestResetReusesSavedCloneToken(t *testing.T) {
	for _, forge := range []string{"github", "gitlab", "self-hosted"} {
		for _, choice := range []string{"saved", "explicit", "absent"} {
			t.Run(forge+"/"+choice, func(t *testing.T) {
				isolateCreateState(t)
				cfg := recordedVM()
				key, dir := "GH_TOKEN", ""
				if forge != "github" {
					cfg.CloneURL, cfg.CloneForge = "https://gitlab.com/group/sub/repo", "gitlab"
					key, dir = "GITLAB_TOKEN", "gitlab.com/group"
					if forge == "self-hosted" {
						cfg.CloneURL, dir = "https://git.example.test/group/sub/repo", "git.example.test/group"
					}
				}
				store, err := secrets.Load()
				if err != nil {
					t.Fatal(err)
				}
				all := map[string]map[string]string{}
				want := ""
				if choice != "absent" {
					all[dir] = map[string]string{key: "saved"}
					want = "saved"
				}
				if err := store.SetAll(cfg.Name, registry.LocalScope, all); err != nil {
					t.Fatal(err)
				}
				if choice == "explicit" {
					cfg.CloneToken, want = "entered", "entered"
				}
				reg := registry.NewEmpty()
				p := &stubResetter{}
				if err := doReset(context.Background(), reg, p, cfg, registry.LocalScope, provision.ResetOptions{}, io.Discard); err != nil {
					t.Fatal(err)
				}
				if !p.called || p.cfg.CloneToken != want {
					t.Fatal("reset did not receive expected token")
				}
				rec, _ := reg.ConfigInScope(cfg.Name, registry.LocalScope)
				if rec.CloneToken != "" {
					t.Fatal("token entered registry")
				}
				settleSecretsIn(context.Background(), &providerfake.Provider{}, store, registry.LocalScope, cfg, io.Discard)
				target := dir
				if forge != "github" && choice == "explicit" {
					target, _ = provision.OrgRelDir(cfg.CloneURL)
				}
				if got := store.GetAll(cfg.Name, registry.LocalScope)[target][key]; got != want {
					t.Fatal("success saved wrong token")
				}
				if forge != "github" && choice == "saved" {
					parent, _ := provision.OrgRelDir(cfg.CloneURL)
					if store.GetAll(cfg.Name, registry.LocalScope)[parent][key] != "" {
						t.Fatal("inherited token was copied into a narrower scope")
					}
				}
			})
		}
	}
}

func TestRecreateReusesSavedGitLabToken(t *testing.T) {
	isolateCreateState(t)
	cfg := recordedVM()
	cfg.CloneURL, cfg.CloneForge = "https://git.example.test/group/sub/repo", "gitlab"
	reg, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(cfg); err != nil {
		t.Fatal(err)
	}
	store, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAll(cfg.Name, registry.LocalScope, map[string]map[string]string{"git.example.test/group": {"GITLAB_TOKEN": "saved"}}); err != nil {
		t.Fatal(err)
	}
	called := false
	p := &providerfake.Provider{
		ListFunc: func() ([]vm.VM, error) { return []vm.VM{{Name: cfg.Name}}, nil },
		RecreateFunc: func(_ context.Context, c vm.CreateConfig, _ provision.CreateOptions, _ io.Writer) error {
			called = true
			if c.CloneToken != "saved" || c.CloneForge != "gitlab" {
				t.Error("recreate did not receive saved GitLab token")
			}
			return nil
		},
	}
	if err := runCreateWithBinding(createAgentArgs(cfg.Name, "--recreate"), bindCreateFake(p)); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("provider not called")
	}
	store, err = secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	if store.GetAll(cfg.Name, registry.LocalScope)["git.example.test/group/sub"]["GITLAB_TOKEN"] != "" {
		t.Fatal("recreate narrowed token scope")
	}
}

func TestResetRefusesUnreadableSecretsBeforeProvisioning(t *testing.T) {
	isolateCreateState(t)
	store, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	const token = "secret-that-must-not-be-reported"
	if err := store.Set("web", registry.LocalScope, map[string]string{"GH_TOKEN": token}); err != nil {
		t.Fatal(err)
	}
	// A type error can quote a JSON value; the reset error must not expose it.
	data := `{"version":3,"scopes":"` + token + `"}`
	if err := os.WriteFile(filepath.Join(os.Getenv("XDG_DATA_HOME"), "sandbar", "secrets.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	p := &stubResetter{}
	err = doReset(context.Background(), registry.NewEmpty(), p, recordedVM(), registry.LocalScope, provision.ResetOptions{}, io.Discard)
	if err == nil || p.called {
		t.Fatal("unreadable credentials did not stop reset before provisioning")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("error exposed credentials")
	}
}
