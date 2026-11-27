// Package manage holds the managed-VM ownership bookkeeping shared by the
// interactive TUI (internal/ui) and the headless `sandbar create` subcommand
// (cmd/sandbar), so the two entrypoints cannot drift on how a VM becomes
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
	"github.com/lullabot/sandbar/internal/provision"
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
	rec, ok, _ := Recorded(context.Background(), reg, name, scope, firstProvenancer(prov))
	return rec.Base, ok
}

// RecordedVM is what a rebuild of an existing managed VM starts from: the base
// it clones and the configuration it was last built with.
type RecordedVM struct {
	// Base is the image to clone from, resolved exactly as RecreateBase
	// resolves it (it never comes back empty for a managed VM).
	Base string
	// Config is the VM's recorded create configuration, with BaseName set to
	// Base. It never holds a CloneToken: neither the marker nor the registry
	// stores one. Zero when HasConfig is false.
	Config vm.CreateConfig
	// HasConfig reports that Config came from a record rather than being
	// absent. A VM can be managed with no usable config: an index entry from
	// before configs were recorded, or a marker adopted from one.
	HasConfig bool
}

// Recorded resolves name's base AND recorded configuration in one read, for
// every entrypoint that rebuilds an existing VM (`sandbar reset`, `sandbar
// create --recreate`, the TUI's reset form). ok is false exactly when
// RecreateBase refuses: neither a marker nor a registry entry claims name.
//
// The marker is consulted first for the config as well as the base, because it
// is the record every controller of the host can see. Reading the config from
// the local registry alone meant a VM managed only by its marker — created by
// another controller on a shared profile, or one whose index entry was lost —
// passed the reset gate and was then rebuilt from defaults: no project clone,
// default sizing, no agents, and RecordSuccess then wrote that loss back into
// the marker, making it permanent. The registry remains the fallback for a
// marker-less (legacy) instance, and for a marker whose config is empty.
//
// prov may be nil (registry only), which is also what makes this safe to call
// with no I/O.
//
// markerErr is a failed marker read. The result is still filled from the
// registry, as RecreateBase always has, but a caller about to destroy the VM
// should refuse on it: the registry may hold nothing, or something older than
// the marker it could not read, and a rebuild from that is the loss this
// function exists to prevent.
func Recorded(ctx context.Context, reg *registry.Registry, name string, scope registry.Scope, prov provider.Provenancer) (rec RecordedVM, ok bool, markerErr error) {
	managed := false
	if prov != nil {
		pv, found, err := prov.ProvenanceOf(ctx, name)
		markerErr = err
		if err == nil && found {
			managed = true
			rec.Base = pv.Base
			if pv.Config.Name != "" {
				rec.Config, rec.HasConfig = pv.Config, true
			}
		}
	}

	if !managed {
		// legacy, remove after one release: registry fallback for a
		// marker-less instance (see RecreateBase's doc comment).
		b, inRegistry := reg.BaseInScope(name, scope)
		if !inRegistry {
			return RecordedVM{}, false, markerErr
		}
		rec.Base = b
	}
	if !rec.HasConfig {
		if cfg, found := reg.ConfigInScope(name, scope); found && cfg.Name != "" {
			rec.Config, rec.HasConfig = cfg, true
		}
	}
	if rec.Base == "" {
		rec.Base = vm.DefaultCreateConfig().BaseName
	}
	if rec.HasConfig {
		rec.Config.Name = name
		rec.Config.BaseName = rec.Base
		rec.Config.CloneToken = ""
	}
	return rec, true, markerErr
}

// RefuseUnrecordedPreserve rejects a reset that asks to keep something from a
// VM with no recorded configuration. Without a record the rebuild would run
// from defaults: --preserve-project has no project to name and archives
// nothing, without saying so, and a whole-home or agents restore would land in
// a VM sized and configured unlike the one it came from, after the original
// was already deleted. Refusing here, before anything is staged or deleted,
// leaves the VM untouched. A plain reset still proceeds: it discards the
// guest's contents by definition, and refusing it would leave no entrypoint
// able to rebuild such a VM at all.
func RefuseUnrecordedPreserve(name string, hasConfig bool, opts provision.ResetOptions) error {
	if hasConfig {
		return nil
	}
	if !opts.PreserveAgents && !opts.PreserveProject && !opts.PreserveHome && len(opts.PreservePaths) == 0 {
		return nil
	}
	return fmt.Errorf("%q has no recorded configuration (neither its provenance marker nor this machine's managed index holds one), so a reset would rebuild it from defaults; refusing to preserve anything into that rebuild. Reset it without preserve options, or copy what you need out of the VM first", name)
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
	// Providers stamp the clone source while holding its clone lock and record
	// setup only after finalize succeeds. Do not replace that history with a
	// fresh guess from today's shared base or playbook here.
	recorded, ok, err := p.ProvenanceOf(context.Background(), cfg.Name)
	if err != nil {
		return fmt.Errorf("read %s provenance before success record: %w", cfg.Name, err)
	}
	if ok && !recorded.Provisioning {
		pv.BaseRevision = recorded.BaseRevision
		pv.SetupRevision = recorded.SetupRevision
	}
	if err := p.MarkManaged(context.Background(), cfg.Name, pv); err != nil {
		return fmt.Errorf("mark %s managed (provenance): %w", cfg.Name, err)
	}
	return nil
}
