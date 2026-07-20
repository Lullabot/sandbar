// Package provider's provenance submodule: Provenancer is the backend-agnostic seam
// that persists and reads back provenance markers on instances, making ownership
// decisions source-of-truth at the provider level rather than the registry. A marker
// is a small JSON file at <LimaHome>/<name>/sandbar.json (for Lima backends) or a VM
// tag/label (for cloud/Proxmox backends). Implementations read and write markers through
// their own HostFiles handle (local filesystem for local Lima, SSH for remote Lima over
// SSH), so the transport is provider-specific but the interface is uniform.
//
// The Provenance payload mirrors the registry-relevant subset of registry.Entry (Base,
// Config) plus observability fields (SandbarVersion, CreatedAt) and a SchemaVersion
// for forward compatibility. It is a standalone type so a future backend can serialize
// it directly into a VM tag without importing internal/registry (which itself imports
// internal/provider — see internal/registry/adopt.go for how the import cycle is broken).
package provider

import (
	"context"
	"errors"
	"time"

	"github.com/lullabot/sandbar/internal/version"
	"github.com/lullabot/sandbar/internal/vm"
)

// ErrUnsupported is returned by a Provenancer whose backend has no durable
// place to stash a marker (e.g. a provider with no VM-tag/label facility). A
// consumer that gets this back should degrade to "provenance unknown" rather
// than treating it as an I/O failure.
var ErrUnsupported = errors.New("provider does not support provenance")

// MarkerSchemaVersion is the schema version this build writes into every new
// marker. It is the single source of truth for the marker shape; manage
// references it (see manage.RecordSuccess), and registry.adoptSchemaVersion is
// a package-local mirror kept in step for the import-cycle reason documented
// there.
//
// v2 added the Provisioning field (in-flight/"building" markers). It is a
// purely additive change: a v1 marker has no `provisioning` key, so it decodes
// with Provisioning=false, i.e. "ready" — exactly what every v1 marker was.
//
// v3 added Progress, so an in-flight marker carries HOW FAR ALONG the build is
// and not merely that it is running. Additive in the same way: an older marker
// has no `progress` key and decodes with the zero BuildProgress, which renders
// as an empty bar — exactly what an observer showed for a v2 marker anyway.
const MarkerSchemaVersion = 3

// Provenance is the marker payload a provider attaches to an instance it
// created, mirroring the provenance-relevant subset of registry.Entry (Base,
// Config) plus observability fields (SandbarVersion, CreatedAt) and a
// SchemaVersion so the marker format can evolve. It is a standalone data
// mirror — this package does NOT import internal/registry to produce it, so
// a future Provenancer implementation can serialize this directly into a VM
// tag/label without pulling in the registry package.
type Provenance struct {
	// SchemaVersion identifies the shape of this marker, so a future reader can
	// detect and migrate an older payload rather than misparsing it.
	SchemaVersion int `json:"schema"`
	// Base is the base image name the instance was cloned from. Load-bearing:
	// recreate-gating depends on it.
	Base string `json:"base"`
	// Config is the create-time configuration, mirroring registry.Entry.Config.
	Config vm.CreateConfig `json:"config"`
	// SandbarVersion is the sandbar build that created the instance (see
	// internal/version), recorded for observability.
	SandbarVersion string `json:"sandbar_version"`
	// CreatedAt is the marker's creation time, RFC3339-formatted.
	CreatedAt string `json:"created_at"`
	// Provisioning is true while the instance is still being built — the marker
	// was written EARLY, at clone time, before the (long) finalize/provision
	// step, so that OTHER controllers of the same host see the in-flight VM as a
	// managed, building tile rather than not at all. It is flipped to false
	// ("ready") when the build succeeds (manage.RecordSuccess). omitempty +
	// the bool zero value keep older (v1) markers, which lack the key,
	// decoding as ready.
	Provisioning bool `json:"provisioning,omitempty"`
	// Progress is how far the in-flight build has got, republished by the
	// BUILDING controller at role boundaries. It is meaningful only while
	// Provisioning is true, and is the ONLY channel by which build progress
	// reaches another controller: the progress bar on the building controller's
	// own tile is parsed from the provisioner's streamed stdout (internal/ui's
	// ansibleParser), a byte stream that exists solely in the process running
	// the build. Without this field an observer can know a VM is Building and
	// nothing more, so its bar sits at zero for the whole build.
	//
	// omitzero (not omitempty — encoding/json omits an empty STRUCT only for the
	// former) keeps ready markers free of a `"progress":{}` key. The struct is a
	// VALUE, not a pointer, so Provenance stays comparable with == , which its
	// tests and callers rely on.
	Progress BuildProgress `json:"progress,omitzero"`
}

// BuildProgress is a coarse position within an in-flight build: which role is
// running, and how many of the run's tasks are done. It deliberately mirrors
// only the fields a remote tile can render (a bar and a role name) rather than
// the builder's full parsed state — Task and Step stay local, since they change
// per task and would make every republish a wire write.
type BuildProgress struct {
	// Role is the Ansible role currently running, e.g. "claude-code".
	Role string `json:"role,omitempty"`
	// Index is how many tasks of the current run have started, and Total how
	// many it declared. A bar is drawn only when both are positive — see
	// ui.ansibleProgress.Fraction, whose guard this mirrors — so a marker that
	// carries a role but no counts renders a name and an empty bar rather than a
	// misleading full one.
	Index int `json:"index,omitempty"`
	Total int `json:"total,omitempty"`
}

// NewProvenance builds a marker payload from a create config. It stamps the
// current MarkerSchemaVersion, sandbar version, and time, and strips secrets
// that must never reach the on-disk marker (exactly as the registry entry does
// with CloneToken). provisioning=true produces an in-flight/"building" marker
// written at clone time; false produces a "ready" marker written on success.
func NewProvenance(cfg vm.CreateConfig, provisioning bool) Provenance {
	marked := cfg
	marked.CloneToken = ""
	return Provenance{
		SchemaVersion:  MarkerSchemaVersion,
		Base:           cfg.BaseName,
		Config:         marked,
		SandbarVersion: version.String(""),
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		Provisioning:   provisioning,
	}
}

// Provenancer is the seam a Provider backend implements (or inherits) to
// persist and read back Provenance markers on the instances it manages. It is
// deliberately small and provider-agnostic: today's local/remote Lima
// backends can satisfy it by writing a sidecar file into the instance
// directory, and a future Proxmox/cloud backend can satisfy the same
// interface with VM tags/labels, with no redesign.
type Provenancer interface {
	// Provenance returns a marker for every listed instance that carries one.
	// Instances with no marker are simply absent from the map — this is the
	// primary entry point for the board, which needs provenance for a whole
	// fleet in one call.
	Provenance(ctx context.Context) (map[string]Provenance, error)
	// ProvenanceOf returns the marker for one instance. ok is false when the
	// instance carries no marker (i.e. "not managed"), which is distinct from
	// a non-nil error (an I/O failure reading/parsing the marker). Serves CLI
	// paths that target a single VM.
	ProvenanceOf(ctx context.Context, name string) (p Provenance, ok bool, err error)
	// MarkManaged writes (or overwrites) the marker for name.
	MarkManaged(ctx context.Context, name string, p Provenance) error
	// Unmark clears any marker for name.
	Unmark(ctx context.Context, name string) error
}
