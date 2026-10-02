package ui

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

func TestResetFormReusesSavedCloneToken(t *testing.T) {
	for _, forge := range []string{"github", "gitlab", "self-hosted"} {
		for _, choice := range []string{"saved", "explicit", "absent", "failed"} {
			t.Run(forge+"/"+choice, func(t *testing.T) {
				m := newTestModel(t)
				cfg := resetVMConfig()
				key, dir := "GH_TOKEN", ""
				if forge != "github" {
					cfg.CloneURL, cfg.CloneForge = "https://gitlab.com/group/sub/repo", "gitlab"
					key, dir = "GITLAB_TOKEN", "gitlab.com/group"
					if forge == "self-hosted" {
						cfg.CloneURL, dir = "https://git.example.test/group/sub/repo", "git.example.test/group"
					}
				}
				want := ""
				if choice != "absent" {
					if err := m.sec.SetAll(cfg.Name, registry.LocalScope, map[string]map[string]string{dir: {key: "saved"}}); err != nil {
						t.Fatal(err)
					}
					want = "saved"
				}
				var received vm.CreateConfig
				m.members[0].prov = &providerfake.Provider{
					ResetFunc: func(_ context.Context, c vm.CreateConfig, _ provision.ResetOptions, _ io.Writer) error {
						received = c
						if choice == "failed" {
							return errors.New("reset failed")
						}
						return nil
					},
				}
				m.openResetForm(registry.LocalScope, cfg.Name, cfg)
				if choice != "absent" && m.inputs[fCloneToken].Placeholder != "*** saved — leave blank to keep it" {
					t.Fatal("saved token hint missing")
				}
				if choice == "explicit" || choice == "failed" {
					m.inputs[fCloneToken].SetValue("entered")
					want = "entered"
				}
				loop := newTeaLoop(t, m)
				loop.send(ctrlKey('s'))
				if loop.m.formErr != nil {
					t.Fatal(loop.m.formErr)
				}
				loop.pump("reset completion", func(m model) bool {
					job, ok := m.jobs.snapshot(provisionKey(registry.LocalScope, cfg.Name))
					return ok && !job.Running()
				})
				if received.CloneToken != want {
					t.Fatal("provider received wrong clone token")
				}
				if choice == "failed" {
					want = "saved"
				}
				target := dir
				if forge != "github" && choice == "explicit" {
					target, _ = provision.OrgRelDir(cfg.CloneURL)
				}
				if loop.m.sec.GetAll(cfg.Name, registry.LocalScope)[target][key] != want {
					t.Fatal("success saved wrong token")
				}
				rec, _ := loop.m.reg.ConfigInScope(cfg.Name, registry.LocalScope)
				if rec.CloneToken != "" {
					t.Fatal("registry retained token")
				}
			})
		}
	}
}
