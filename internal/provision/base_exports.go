package provision

import (
	"context"
	"io"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
)

// base_exports.go opens exactly three of this package's base-image internals to
// a NON-Lima provider — the Proxmox provider (internal/provider), which builds
// its base as a PVE TEMPLATE rather than a Lima instance and so cannot go
// through the Lima-coupled Provisioner.
//
// It must nonetheless stamp and read the base's playbook version through the
// SAME machinery the Lima flow uses, at the SAME host path and in the SAME
// on-disk format. If it reproduced the path or the format, a base built by one
// provider would be judged stale by the other's read, and the shared staleness
// contract (a base rebuilds when the playbook fileset changes) would silently
// diverge the day either side edited its copy. These wrappers delegate to the
// unexported functions that own that path/format, so there is one definition,
// not two.

// ReadBaseVersion returns the stamped playbook version recorded for baseName
// under hf, or "" when no readable stamp exists (a base built before stamping,
// or an unreadable stamp) — which a caller treats as stale, exactly as baseStale
// does. See readBaseVersion.
func ReadBaseVersion(hf lima.HostFiles, baseName string) string {
	return readBaseVersionFn(hf, baseName)
}

// WriteBaseVersion records the playbook version a freshly built base was made
// from, together with builtAt (the moment its packages were last known current
// — the clock the 30-day apt-refresh age is measured against), at the same host
// path the Lima flow stamps. A write failure is the caller's to treat as
// non-fatal, the same posture buildBase takes. See writeBaseVersion.
func WriteBaseVersion(hf lima.HostFiles, baseName, version string, builtAt time.Time) error {
	return writeBaseVersionFn(hf, baseName, version, builtAt)
}

// LockBase takes the exclusive base-image advisory lock for baseName under hf,
// blocking (honouring ctx) until it is free, and returns the function that
// releases it. It is the exported form of lockBase, letting a non-Lima provider
// serialize base preparation across separate sand processes exactly as the Lima
// flow does. See lockBase for the full contract — notably that a failure to LOCK
// is NOT a failure to BUILD: it reports the reason to out, returns a no-op
// release, and lets the caller proceed unserialized rather than turn a
// concurrency guard into an outage.
func LockBase(ctx context.Context, hf lima.HostFiles, baseName string, out io.Writer) (release func(), err error) {
	return lockBase(ctx, hf, baseName, out)
}
