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

// ErrNoInstance is returned by MarkManaged when the instance it was asked to
// mark does not exist yet, so there is nowhere legitimate to put the marker.
// Callers that may legitimately run BEFORE an instance exists — notably build
// progress republishing, which starts at the first phase banner and so races
// ahead of the clone — should treat it as "not yet" and skip quietly, NOT as a
// failure worth reporting. Every other caller runs after the clone and should
// treat it as the real error it is.
var ErrNoInstance = errors.New("instance does not exist")

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

// AbandonedBuildAfter is how long an IN-FLIGHT marker may sit un-rewritten
// before sand treats it as the leftover of a build that is no longer running
// anywhere, rather than as a build still in progress.
//
// THE CUTOFF IS MEASURED AGAINST CreatedAt, WHICH IS A HEARTBEAT ON AN
// IN-FLIGHT MARKER, not a birthday. NewProvenance stamps CreatedAt with the
// current time on every call, and the building controller calls it again at
// every Ansible role boundary to republish Progress (see ui.publishProgressCmd)
// — so an in-flight marker's CreatedAt is "when this build last reported in".
// A ready marker's CreatedAt is a true creation time; the two readings differ,
// and only the in-flight one is consulted here.
//
// Two hours is deliberately far longer than the widest real gap between
// republishes. Republishing is throttled to role boundaries (a per-task write
// would be an ssh round trip per Ansible task), and the widest of those gaps is
// the tail of a build — a cold dev-tools role, or a project role cloning a large
// repository — which is minutes, not hours. Erring long is the cheap direction:
// healing a marker early costs an observer a tile that reads Running for the
// rest of a build that is about to overwrite the marker anyway (RecordSuccess
// has the last word), and a marker-only "Building" gates nothing — every
// destructive verb gates on the LOCAL job registry (ui.vmBuilding), never on
// this. Erring short is therefore not dangerous, only noisy; erring long merely
// delays a repair no one is waiting on by the clock.
const AbandonedBuildAfter = 2 * time.Hour

// BuildAbandoned reports whether p is an in-flight marker left behind by a build
// that has stopped reporting in — the state that used to pin a perfectly healthy
// VM's tile to "Building" forever.
//
// It exists because NOTHING ever cleared such a marker. Provisioning is set at
// clone time (or, on the Proxmox backend, at the first role boundary) and
// cleared only by manage.RecordSuccess when the build job completes in the
// controller that started it. A controller that never reaches that handler — its
// TUI quit or killed mid-build, its ssh transport dropped, its final marker
// write failed — leaves the flag set on a VM that is finished and working. Every
// controller then reads that marker on every refresh and renders "Building", and
// because remoteProvisioning is checked before the VM's real status, the tile
// cannot even fall through to Stopped.
//
// An UNPARSEABLE (or absent) CreatedAt counts as abandoned. That is the
// deliberate choice: a marker whose timestamp cannot be read cannot be dated, so
// the alternative is to leave it in-flight forever — and there is no manual
// escape by design (sand ships no clear-the-marker verb, because a state the
// tool can repair itself is a state the user should never have to know about).
// Healing is the recoverable direction; a permanently wedged tile is not.
//
// The caller must ALSO establish that it is not itself building this VM before
// acting on a true result: this function can only see the marker, and the
// controller running the build is exactly the one whose own long-running role
// could outlast the cutoff. ui's caller gates on the local job registry.
func (p Provenance) BuildAbandoned(now time.Time) bool {
	if !p.Provisioning {
		return false
	}
	t, err := time.Parse(time.RFC3339, p.CreatedAt)
	if err != nil {
		return true
	}
	return now.Sub(t) > AbandonedBuildAfter
}

// Ready returns p as a COMPLETED marker: the in-flight flag cleared and the
// build position dropped, with every other field — Base, Config, SandbarVersion,
// CreatedAt — carried through untouched.
//
// Preserving the rest is the whole point. The obvious repair, rebuilding the
// marker with NewProvenance, would substitute the healing controller's own idea
// of the config for the one the build actually used, and restamp CreatedAt to
// now; the VM would go on reading as managed while quietly losing the record of
// what it was built from, which recreate-gating depends on. This is a two-field
// edit of the marker that is already there, and a value receiver makes that
// literal — the caller's copy is untouched.
func (p Provenance) Ready() Provenance {
	p.Provisioning = false
	p.Progress = BuildProgress{}
	return p
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
