package provision

import "github.com/lullabot/sandbar/internal/lima"

// hostaccess.go used to hold a process-global host-access seam (a package var
// named hostFiles, swapped by SetHostFiles) that every base-image touch in this
// package read through — the base's lima.yaml (baseoverlay.go), the
// playbook-version stamp and its lock under LIMA_HOME/_sand (baseversion.go,
// baselock.go), and the partial-instance cleanup (cleanup.go). That design
// assumed a single sand process resolves and runs exactly one provider, which
// no longer holds.
//
// The host-access handle now lives on *Provisioner itself (the HostFiles
// field below), set once by whichever provider constructs it: lima.LocalFiles()
// for local Lima (the default — nil defaults to it, so behaviour for every
// existing caller is unchanged), or a remote provider's SSHHost, so every
// base-image touch lands on the host where limactl actually runs (see
// provider.NewRemoteLima). Two Provisioner instances in the same process each
// carry their OWN handle, with nothing shared between them — the property that
// makes running a local and a remote provisioning operation in the same
// process safe.
//
// Callers reach the handle through the hostFiles() method below. A
// Provisioner method calls p.hostFiles() directly; a free function elsewhere
// in this package (baseversion.go, baselock.go, cleanup.go, baseoverlay.go)
// takes it as an explicit hf lima.HostFiles parameter, threaded down from the
// Provisioner method that owns the call.

// hostFiles returns the host-access handle this Provisioner's base-image
// operations (overlay read, version stamp, base lock, partial-instance
// cleanup) read and write through. A nil HostFiles field — the zero value,
// carried by every caller that constructs a Provisioner without setting it —
// defaults to the local filesystem, so local-Lima behaviour is unchanged.
func (p *Provisioner) hostFiles() lima.HostFiles {
	if p.HostFiles != nil {
		return p.HostFiles
	}
	return lima.LocalFiles()
}
