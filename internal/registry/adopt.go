package registry

// adopt.go is a one-time, idempotent migration: it stamps provider-level
// provenance markers (see internal/provider/provenance.go) onto instances
// this registry already recorded as managed, so a controller upgrading to
// marker-based ownership does not make an already-managed VM lose its tile
// the moment the registry is demoted to a cache — see manage.RecreateBase and
// manage.RecordSuccess's "legacy, remove after one release" comments for the
// fallback this migration exists alongside (and eventually retires).
//
// This file deliberately does NOT import internal/provider: that package
// already imports internal/registry (for Scope — see provider/select.go and
// provider/fleet.go), so the reverse import would cycle. AdoptProvenance and
// AdoptProvenancer are therefore a package-local structural mirror of
// provider.Provenance / provider.Provenancer's ProvenanceOf+MarkManaged pair
// — exactly the same problem, and the same fix, internal/lima's own
// provenance file describes for the lima<->provider boundary. The real
// adapter that bridges a live provider.Provenancer to this interface lives in
// internal/manage (which already imports both packages) — see
// manage.AdoptOnce and its provenancerAdapter.
import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/lullabot/sandbar/internal/version"
	"github.com/lullabot/sandbar/internal/vm"
)

// adoptSchemaVersion is the marker schema Adopt writes. It must stay in step
// with provider.Provenance.SchemaVersion / manage's provenanceSchemaVersion
// (today both 1) — duplicated here, not imported, for the reason in this
// file's package comment.
// adoptSchemaVersion mirrors provider.MarkerSchemaVersion (kept in step for the
// import-cycle reason in this file's package comment). Adoption always writes a
// READY marker (a VM already in the registry finished building long ago), so it
// never sets the v2 Provisioning field — it just stamps the current version.
const adoptSchemaVersion = 2

// AdoptProvenance is the marker payload Adopt writes, mirroring the
// provenance-relevant fields of provider.Provenance without this package
// importing internal/provider (see this file's package comment).
type AdoptProvenance struct {
	SchemaVersion  int
	Base           string
	Config         vm.CreateConfig
	SandbarVersion string
	CreatedAt      string
}

// AdoptProvenancer is the minimal read/write seam Adopt needs: a structural
// subset of provider.Provenancer's ProvenanceOf and MarkManaged, expressed in
// this package's own AdoptProvenance. A real provider.Provenancer is bridged
// to this interface by internal/manage's provenancerAdapter.
type AdoptProvenancer interface {
	// ProvenanceOf returns the marker for name. ok is false when name carries
	// no marker yet, mirroring provider.Provenancer.ProvenanceOf.
	ProvenanceOf(ctx context.Context, name string) (p AdoptProvenance, ok bool, err error)
	// MarkManaged writes (or overwrites) the marker for name.
	MarkManaged(ctx context.Context, name string, p AdoptProvenance) error
}

// ManagedEntry is the exported, provider-agnostic view of one registry entry
// that Adopt needs to build a marker from, without leaking the package's
// private entry type.
type ManagedEntry struct {
	Name   string
	Base   string
	Config vm.CreateConfig
}

// ManagedInScope returns every entry the registry records as managed under
// scope, name-sorted so a caller that logs adoptions (Adopt) gets
// deterministic, diffable output across runs.
func (r *Registry) ManagedInScope(scope Scope) []ManagedEntry {
	out := make([]ManagedEntry, 0, len(r.vms))
	for k, e := range r.vms {
		if k.scope != scope {
			continue
		}
		out = append(out, ManagedEntry{Name: k.name, Base: e.Base, Config: e.Config})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Adopt is the one-time idempotent migration described in this file's
// package comment. For every entry scope's registry already records as
// managed that is (a) present in live (typically a fresh provider.List(),
// keyed by instance name) and (b) carries NO existing provenance marker, it
// writes one via prov.MarkManaged, sourced entirely from the registry
// entry's own Base/Config — never inferred. It NEVER overwrites an existing
// marker (the ProvenanceOf ⇒ skip check below is what makes repeated calls a
// no-op) and never claims a VM the registry itself did not already record:
// this must stay conservative, since a wrong adoption would claim a VM as
// sand-managed that never was.
//
// prov may be nil (a backend with no Provenancer, or one not yet resolved),
// in which case Adopt is a no-op — there is nowhere to write a marker.
//
// entries is a DETACHED snapshot of the registry's managed set for the scope
// (see ManagedInScope), read by the caller on its own goroutine. Adopt takes
// the snapshot rather than *Registry deliberately: it then touches no shared
// registry state, so a caller can run its (potentially slow, ssh-backed)
// ProvenanceOf/MarkManaged loop on a background goroutine without racing a
// concurrent registry mutation on another goroutine (e.g. the TUI's Update
// loop calling Reconcile/AddScoped).
//
// A ProvenanceOf read error is treated the same as "no marker found"
// (ok=false, per the algorithm this mirrors) rather than aborting the pass —
// a transient read hiccup on one instance must not block adoption for the
// rest. A MarkManaged failure IS surfaced: it is logged and that one instance
// is skipped, but Adopt keeps going and returns the last error it saw so a
// caller can report that something failed.
//
// Every adoption is logged (name + base) so the migration is auditable.
func Adopt(ctx context.Context, entries []ManagedEntry, live map[string]bool, prov AdoptProvenancer) ([]string, error) {
	if prov == nil {
		return nil, nil
	}
	var adopted []string
	var lastErr error
	for _, e := range entries {
		if !live[e.Name] {
			continue // gone; don't resurrect
		}
		if _, ok, _ := prov.ProvenanceOf(ctx, e.Name); ok {
			continue // already marked — idempotent
		}
		pv := AdoptProvenance{
			SchemaVersion:  adoptSchemaVersion,
			Base:           e.Base,
			Config:         e.Config,
			SandbarVersion: version.String(""),
			CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		}
		if err := prov.MarkManaged(ctx, e.Name, pv); err != nil {
			log.Printf("sand: provenance adoption failed to mark %s managed: %v", e.Name, err)
			lastErr = err
			continue
		}
		log.Printf("sand: adopted %s (base=%s) into provenance", e.Name, e.Base)
		adopted = append(adopted, e.Name)
	}
	return adopted, lastErr
}
