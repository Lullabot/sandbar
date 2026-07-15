package provision

import "github.com/lullabot/sandbar/internal/lima"

// hostFiles is the host-access seam every read/stat/write of Lima instance state
// in this package goes through: the base image's lima.yaml (baseoverlay.go), the
// playbook-version stamp and its lock under LIMA_HOME/_sand (baseversion.go,
// baselock.go), and the partial-instance cleanup (cleanup.go). It defaults to the
// local filesystem, so behaviour is unchanged; the seam exists so a remote-Lima
// provider (plan 15 task 5) can point every one of those touches at the host
// where limactl actually runs — where, for remote Lima, the base image and its
// stamp live.
var hostFiles lima.HostFiles = lima.LocalFiles()

// SetHostFiles points this package's host-access seam at hf. It is how a
// remote-Lima provider (plan 15 task 5) redirects every base-image file touch —
// the version stamp, the base lock, the partial-instance cleanup, the base
// overlay read — at the host where limactl actually runs, so the base and its
// _sand state are read and written on the REMOTE host under its LIMA_HOME rather
// than on the laptop.
//
// It is a process-global swap because these touches go through package-level
// helpers (baseversion.go, baselock.go, cleanup.go, baseoverlay.go) with no
// per-Provisioner seam, and because a single `sand` process resolves and runs
// exactly ONE provider (see provider.Resolve): the remote provider's constructor
// calls this once, the default local path never does, and nothing switches
// providers mid-process. Passing lima.LocalFiles() restores the default.
func SetHostFiles(hf lima.HostFiles) { hostFiles = hf }
