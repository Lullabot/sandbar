package manage

import (
	"testing"

	"github.com/lullabot/sandbar/internal/keepawake"
)

// StayAwake keeps the workstation out of idle sleep until the returned func
// is called, for the length of a VM create or reset (see internal/keepawake
// for why that protects the SSH connection those depend on). A failure to
// hold it is ignored on purpose: it is common and harmless where it happens
// (no logind in a container, polkit refusing a session it cannot tie to a
// local seat — sandbar run over SSH on a server, whose sleep is not the
// workstation's), and a note about it on every build would be noise.
//
// It does nothing under `go test`. The CLI and TUI suites drive creates and
// resets through here with fake backends, and a real hold is host state: on
// a Linux runner with a system bus it would take a genuine logind inhibitor
// for every faked build. keepawake's own tests exercise Hold through its
// fake backend instead.
func StayAwake(why string) (release func()) {
	if testing.Testing() {
		return func() {}
	}
	release, _ = keepawake.Hold("sandbar: " + why)
	return release
}
