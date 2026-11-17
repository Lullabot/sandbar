package manage

import (
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// ConfigLookup is the managed-index surface RecordedCloneURL reads,
// satisfied by *registry.Registry.
type ConfigLookup interface {
	ConfigInScope(name string, scope registry.Scope) (vm.CreateConfig, bool)
}

// RecordedCloneURL is the project a VM was created for, read the way reset
// reads a VM's recorded config: the provenance marker first (it lives with
// the VM, so it is right even for a VM another controller created), then the
// managed index. marker returns the marker's config and whether one was
// found; the CLI reads it from the provider, while the board already holds
// its member's batched read and passes a map lookup. A nil marker or reg
// skips that source. "" when neither knows, which is also the honest answer
// for a VM created without a repository.
func RecordedCloneURL(marker func() (vm.CreateConfig, bool), reg ConfigLookup, scope registry.Scope, name string) string {
	if marker != nil {
		if cfg, ok := marker(); ok && cfg.CloneURL != "" {
			return cfg.CloneURL
		}
	}
	if reg != nil {
		if cfg, ok := reg.ConfigInScope(name, scope); ok {
			return cfg.CloneURL
		}
	}
	return ""
}
