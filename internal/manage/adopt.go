package manage

// adopt.go bridges a real provider.Provenancer to registry.Adopt's
// package-local AdoptProvenancer seam (see internal/registry/adopt.go's
// package comment for why registry cannot import internal/provider directly)
// and gates the one-time adoption migration (registry.Adopt) to run AT MOST
// ONCE PER PROCESS PER TARGET. The create path (cmd/sand/create.go) and the
// TUI's first board load (internal/ui/model.go's vmsLoadedMsg handler) are
// both natural triggers for this — whichever runs first for a given scope
// wins, and Adopt itself is idempotent besides, so calling it twice for the
// same scope would be harmless but wasteful: re-scanning the whole registry
// on every 5s refresh for a migration that only ever does real work once is
// exactly the kind of "not cheap on the no-op path" this guard exists to
// avoid.
import (
	"context"
	"log"
	"sync"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// provenancerAdapter narrows a provider.Provenancer down to
// registry.AdoptProvenancer by translating provider.Provenance <->
// registry.AdoptProvenance field-by-field — the two packages cannot share one
// type without an import cycle (see registry/adopt.go's package comment).
type provenancerAdapter struct{ p provider.Provenancer }

func (a provenancerAdapter) ProvenanceOf(ctx context.Context, name string) (registry.AdoptProvenance, bool, error) {
	pv, ok, err := a.p.ProvenanceOf(ctx, name)
	if err != nil || !ok {
		return registry.AdoptProvenance{}, ok, err
	}
	return registry.AdoptProvenance{
		SchemaVersion:  pv.SchemaVersion,
		Base:           pv.Base,
		Config:         pv.Config,
		SandbarVersion: pv.SandbarVersion,
		CreatedAt:      pv.CreatedAt,
	}, true, nil
}

func (a provenancerAdapter) MarkManaged(ctx context.Context, name string, p registry.AdoptProvenance) error {
	return a.p.MarkManaged(ctx, name, provider.Provenance{
		SchemaVersion:  p.SchemaVersion,
		Base:           p.Base,
		Config:         p.Config,
		SandbarVersion: p.SandbarVersion,
		CreatedAt:      p.CreatedAt,
	})
}

// adoptedMu guards adoptedTargets, the per-target "has adoption already run
// in this process" flag set. A package-level map (not per-model/per-Registry
// state) because it must be shared by every caller of AdoptOnce regardless of
// which entrypoint (headless create, or the TUI) triggers it first, and the
// TUI's own refreshes run on background tea.Cmd goroutines.
var (
	adoptedMu      sync.Mutex
	adoptedTargets = map[registry.Scope]bool{}
)

// AdoptOnce runs registry.Adopt for scope's currently-live VMs at most once
// per process per target (keyed by scope — see adoptedTargets above), so an
// upgrading controller stamps provenance markers onto VMs the registry
// already claimed but that predate provenance, without re-scanning the whole
// registry on every refresh tick. Safe to call from both the create path and
// the TUI's first board load: whichever runs first for a given scope wins,
// and every later call for that same scope is a cheap map lookup that
// returns immediately.
//
// prov is the resolved provider's Provenancer — nil for a backend that does
// not implement one (a future non-marker-capable backend, or a test double),
// in which case AdoptOnce is a no-op, since there is nowhere to write a
// marker. reg may not be nil.
// entries is a detached snapshot of the registry's managed set for scope,
// read by the CALLER on its own goroutine (registry.ManagedInScope) — never
// read here, so AdoptOnce holds no *Registry and its ssh-backed loop is safe
// to run on a background goroutine (e.g. a Bubble Tea command) alongside a
// concurrent registry mutation on the Update goroutine. See registry.Adopt.
func AdoptOnce(ctx context.Context, entries []registry.ManagedEntry, live []vm.VM, scope registry.Scope, prov provider.Provenancer) {
	if prov == nil {
		return
	}

	adoptedMu.Lock()
	if adoptedTargets[scope] {
		adoptedMu.Unlock()
		return
	}
	adoptedTargets[scope] = true
	adoptedMu.Unlock()

	liveNames := make(map[string]bool, len(live))
	for _, v := range live {
		liveNames[v.Name] = true
	}
	// registry.Adopt already logs every individual adoption and marking
	// failure (see its doc comment); the returned error is a summary a
	// caller cannot otherwise observe, so it is worth one more log line here.
	if _, err := registry.Adopt(ctx, entries, liveNames, provenancerAdapter{prov}); err != nil {
		log.Printf("sand: provenance adoption for %s had errors: %v", scope.Provider, err)
	}
}
