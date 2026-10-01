package manage

import (
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"
	"testing"
)

func TestResolveCloneToken(t *testing.T) {
	store, err := secrets.LoadFrom(t.TempDir() + "/secrets.json")
	if err != nil {
		t.Fatal(err)
	}
	scope := registry.Scope{Provider: "remote-ssh", RemoteTarget: "remote"}
	all := map[string]map[string]string{
		"":                      {"GH_TOKEN": "github", "GITLAB_TOKEN": "unsafe-global"},
		"gitlab.com":            {"GITLAB_TOKEN": "host"},
		"gitlab.com/group":      {"GITLAB_TOKEN": "group"},
		"gitlab.com/group/sub":  {"GITLAB_TOKEN": "subgroup"},
		"git.example.test/team": {"GITLAB_TOKEN": "self-hosted"},
	}
	if err := store.SetAll("web", scope, all); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, url, forge, explicit, want string
		scope                            registry.Scope
	}{
		{"github", "https://github.com/org/repo", "", "", "github", scope},
		{"explicit", "https://github.com/org/repo", "", "entered", "entered", scope},
		{"gitlab-subgroup", "https://gitlab.com/group/sub/repo", "", "", "subgroup", scope},
		{"gitlab-group", "https://gitlab.com/group/other/repo", "", "", "group", scope},
		{"gitlab-host", "https://gitlab.com/group-other/repo", "", "", "host", scope},
		{"self-hosted", "https://git.example.test/team/repo", "gitlab", "", "self-hosted", scope},
		{"other-group", "https://git.example.test/team-other/repo", "gitlab", "", "", scope},
		{"other-host", "https://other.test/team/repo", "gitlab", "", "", scope},
		{"other-connection", "https://github.com/org/repo", "", "", "", registry.LocalScope},
		{"unknown-forge", "https://example.test/org/repo", "", "", "", scope},
		{"no-project", "", "", "", "", scope},
		{"no-parent", "https://gitlab.com/repo", "", "", "", scope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := vm.CreateConfig{Name: "web", CloneURL: tc.url, CloneForge: tc.forge, CloneToken: tc.explicit}
			if got := ResolveCloneToken(store, cfg, tc.scope).CloneToken; got != tc.want {
				t.Fatal("wrong token selected")
			}
		})
	}
	if SavedCloneToken(nil, vm.CreateConfig{}, scope) != "" {
		t.Fatal("nil store returned token")
	}
}
