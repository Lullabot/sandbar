// Package manage holds the managed-VM registry bookkeeping shared by the
// interactive TUI (internal/ui) and the headless `sand create` subcommand
// (cmd/sand), so the two entrypoints cannot drift on how a VM becomes
// "managed", how the index is kept in sync with the live `limactl list`, or
// when a recreate is permitted.
package manage

import (
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// Reconcile drops managed entries whose VM is no longer present in live (the
// current `limactl list` result), so a VM deleted outside sand stops being
// flagged managed (and recreate-able). It returns the names it dropped, so a
// caller can prune those from its own per-VM state too (the TUI does this for
// its secrets store). Mirrors the TUI's vmsLoadedMsg handling in
// internal/ui/model.go.
func Reconcile(reg *registry.Registry, live []vm.VM) ([]string, error) {
	present := make(map[string]bool, len(live))
	for _, v := range live {
		present[v.Name] = true
	}
	return reg.Reconcile(present)
}

// RecreateBase reports whether name may be recreated — recreate clones from a
// Claude base image and would replace ANY instance it is pointed at, so it is
// only ever offered for VMs sand itself created — and, when it may, the base
// image to clone from: the VM's recorded base, or the default base name if
// none was recorded (e.g. a pre-snapshot index entry). Mirrors the TUI's
// Reset gate in internal/ui/detail.go.
func RecreateBase(reg *registry.Registry, name string) (base string, ok bool) {
	if !reg.IsManaged(name) {
		return "", false
	}
	if b := reg.Base(name); b != "" {
		return b, true
	}
	return vm.DefaultCreateConfig().BaseName, true
}

// RecordSuccess records cfg as a managed VM after a successful create/recreate
// so a later recreate reproduces it faithfully and the list/registry flags it
// as sand-managed. Mirrors the TUI's provisionDoneMsg handling in
// internal/ui/model.go.
func RecordSuccess(reg *registry.Registry, cfg vm.CreateConfig) error {
	return reg.Add(cfg)
}
