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
