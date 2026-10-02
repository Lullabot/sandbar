package manage

import (
	"strings"

	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

type cloneTokenStore interface {
	GetAll(string, registry.Scope) map[string]map[string]string
}

// SavedCloneToken returns only credentials applicable to the recorded project.
// GitHub uses its global token; GitLab uses the most specific matching parent
// scope, never a global token or credentials for another host or group.
func SavedCloneToken(store cloneTokenStore, cfg vm.CreateConfig, scope registry.Scope) string {
	if store == nil || cfg.CloneURL == "" {
		return ""
	}
	all := store.GetAll(cfg.Name, scope)
	key := vm.CloneTokenKey(cfg.CloneURL, cfg.CloneForge)
	if key == "GH_TOKEN" {
		return all[""][key]
	}
	if key != "GITLAB_TOKEN" {
		return ""
	}
	parent, ok := provision.OrgRelDir(cfg.CloneURL)
	if !ok {
		return ""
	}
	token, best := "", 0
	for dir, pairs := range all {
		if dir != "" && len(dir) > best && pairs[key] != "" && (parent == dir || strings.HasPrefix(parent, dir+"/")) {
			token, best = pairs[key], len(dir)
		}
	}
	return token
}

// ResolveCloneToken leaves explicit credentials intact. Callers keep the
// original config for post-success secret saving so an inherited GitLab token
// is not copied into a new, more specific scope.
func ResolveCloneToken(store cloneTokenStore, cfg vm.CreateConfig, scope registry.Scope) vm.CreateConfig {
	if cfg.CloneToken == "" {
		cfg.CloneToken = SavedCloneToken(store, cfg, scope)
	}
	return cfg
}
