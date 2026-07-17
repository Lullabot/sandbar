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

	"github.com/lullabot/sandbar/internal/vm"
)

// ErrUnsupported is returned by a Provenancer whose backend has no durable
// place to stash a marker (e.g. a provider with no VM-tag/label facility). A
// consumer that gets this back should degrade to "provenance unknown" rather
// than treating it as an I/O failure.
var ErrUnsupported = errors.New("provider does not support provenance")

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
