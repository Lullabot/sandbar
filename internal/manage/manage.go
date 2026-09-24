// Package manage holds the managed-VM ownership bookkeeping shared by the
// interactive TUI (internal/ui) and the headless `sand create` subcommand
// (cmd/sand), so the two entrypoints cannot drift on how a VM becomes
// "managed", how the index is kept in sync with the live `limactl list`, or
// when a recreate is permitted.
//
// Ownership truth moved to provider.Provenancer markers (see
// internal/provider/provenance.go): a marker written on the instance itself
// is visible to every controller that later talks to it, unlike the local
// registry.Registry index, which only this machine's sand ever reads. Both
// RecreateBase and RecordSuccess accept an OPTIONAL trailing
// provider.Provenancer argument (Go variadic, not a required parameter) so
// the exported signatures every existing caller depends on keep compiling
// unchanged; a caller that omits it gets the pre-provenance, registry-only
// behavior. Every caller resolving a real provider should now pass one.
package manage

import (
	"context"
	"fmt"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// firstProvenancer returns the sole element of an optional trailing
// provider.Provenancer argument, or nil when the caller omitted it (or
// passed an explicit nil) — the shared helper behind RecreateBase and
// RecordSuccess's variadic "optional provenancer" parameter.
func firstProvenancer(prov []provider.Provenancer) provider.Provenancer {
	if len(prov) == 0 {
		return nil
	}
	return prov[0]
}

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
	return reg.ReconcileScoped(scope, present)
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
//
// Resolution order, when prov is supplied (see firstProvenancer): name's
// provenance marker is consulted FIRST via ProvenanceOf — the marker is
// authoritative, since it is what every controller touching the instance
// (not just this one's registry) can see. Only when prov is nil, the read
// itself errors, or the instance carries no marker does this fall back to
// reg.BaseInScope — LEGACY, remove after one release (see task 8's docs):
// that fallback exists solely so a VM recorded by a pre-provenance sand (or
// a controller that has not upgraded yet) does not spuriously lose its
// recreate-ability the moment this ships. Refused (ok=false) only when
// NEITHER a marker NOR a registry entry is found for name under scope —
// recreate must never run against an unmanaged/unknown VM.
func RecreateBase(reg *registry.Registry, name string, scope registry.Scope, prov ...provider.Provenancer) (base string, ok bool) {
	if p := firstProvenancer(prov); p != nil {
		if pv, found, err := p.ProvenanceOf(context.Background(), name); err == nil && found {
			if pv.Base != "" {
				return pv.Base, true
			}
			return vm.DefaultCreateConfig().BaseName, true
		}
	}

	// legacy, remove after one release: registry fallback for a marker-less
	// instance (see the doc comment above).
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
//
// reg.AddScoped always runs, keeping the registry warm as the one-release
// LEGACY fallback (see RecreateBase). When prov is supplied (see
// firstProvenancer), this ALSO writes the authoritative provenance marker via
// Provenancer.MarkManaged, carrying cfg's base name and configuration (secret
// stripped, exactly like the registry entry) — a VM with no marker is
// invisible to any OTHER controller that later looks at it, so a MarkManaged
// failure is returned rather than silently swallowed; the caller decides how
// loudly to report it (today, a printed warning — the VM itself is already
// up either way).
func RecordSuccess(reg *registry.Registry, cfg vm.CreateConfig, scope registry.Scope, prov ...provider.Provenancer) error {
	return RecordSuccessWithTemplate(reg, cfg, scope, "", prov...)
}

// RecordSuccessWithTemplate is RecordSuccess for a VM cloned from a golden
// template: it records templateSource as the entry's template provenance (see
// registry.AddScopedWithTemplate) so a later --recreate re-clones from that
// same template rather than falling back to the base image.
//
// The provenance-marker half is IDENTICAL to RecordSuccess's — a
// template-cloned VM is just as invisible to other controllers without a
// marker as any other, so the two paths must not diverge on it. That shared
// tail is why this is one function with a template argument rather than a
// parallel implementation; templateSource == "" is exactly RecordSuccess.
func RecordSuccessWithTemplate(reg *registry.Registry, cfg vm.CreateConfig, scope registry.Scope, templateSource string, prov ...provider.Provenancer) error {
	if templateSource != "" {
		if err := reg.AddScopedWithTemplate(cfg, scope, templateSource); err != nil {
			return err
		}
	} else if err := reg.AddScoped(cfg, scope); err != nil {
		return err
	}

	p := firstProvenancer(prov)
	if p == nil {
		return nil
	}

	// A READY marker (Provisioning=false): the build succeeded. This overwrites
	// any in-flight marker the provider wrote at clone time (see local.go
	// Create / provision OnCloned), flipping the VM from "building" to "ready"
	// for every controller that reads it.
	pv := provider.NewProvenance(cfg, false)
	if err := p.MarkManaged(context.Background(), cfg.Name, pv); err != nil {
		return fmt.Errorf("mark %s managed (provenance): %w", cfg.Name, err)
	}
	return nil
}
