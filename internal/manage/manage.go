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

// Reconcile drops managed entries owned by scope whose VM is no longer present
// in live (the current `limactl list` result for THAT SAME provider/target),
// so a VM deleted outside sand stops being flagged managed (and
// recreate-able). It returns the names it dropped, so a caller can prune those
// from its own per-VM state too (the TUI does this for its secrets store).
// Mirrors the TUI's vmsLoadedMsg handling in internal/ui/model.go.
//
// scope confines this to entries owned by one provider (registry.LocalScope
// for the local Lima provider sand uses by default) — live is a listing from
// that provider alone, so an entry another provider owns must never be
// pruned by it, and vice versa. See registry.Scope.
func Reconcile(reg *registry.Registry, live []vm.VM, scope registry.Scope) ([]string, error) {
	present := make(map[string]bool, len(live))
	for _, v := range live {
		present[v.Name] = true
	}
	// known is the caller's LAST-OBSERVED key set for this scope — the registry's
	// current in-memory view, captured BEFORE ReconcileScoped reloads the on-disk
	// index under the lock. ReconcileScoped prunes only known ∩ absent, so a VM a
	// concurrent process added after this snapshot (absent from known) is never
	// pruned even though it is equally absent from this caller's `present` live
	// list — the lost-update this plan closes. See registry.ReconcileScoped.
	known := reg.NamesInScope(scope)
	return reg.ReconcileScoped(scope, present, known)
}

// RecreateBase reports whether name may be recreated within scope — recreate
// clones from a Claude base image and would replace ANY instance it is
// pointed at, so it is only ever offered for VMs sand itself created, AND only
// to the same provider that created it (a name owned by a different
// provider's scope is refused exactly like an unmanaged one, since recreating
// it would mean routing the clone to the wrong backend) — and, when it may,
// the base image to clone from: the VM's recorded base, or the default base
// name if none was recorded (e.g. a pre-snapshot index entry). Mirrors the
// TUI's Reset gate in internal/ui/detail.go.
func RecreateBase(reg *registry.Registry, name string, scope registry.Scope) (base string, ok bool) {
	b, managed := reg.BaseInScope(name, scope)
	if !managed {
		return "", false
	}
	if b != "" {
		return b, true
	}
	return vm.DefaultCreateConfig().BaseName, true
}

// RecordSuccess records cfg as a managed VM owned by scope after a successful
// create/recreate so a later recreate reproduces it faithfully and the
// list/registry flags it as sand-managed under the right provider. Mirrors
// the TUI's provisionDoneMsg handling in internal/ui/model.go.
func RecordSuccess(reg *registry.Registry, cfg vm.CreateConfig, scope registry.Scope) error {
	return reg.AddScoped(cfg, scope)
}
