// Package keepawake asks the workstation's operating system not to idle-sleep
// while a long sandbar operation (a VM create or reset) is running.
//
// The reason is the SSH connection, not the operation's CPU. A reset of a
// remote VM rides one multiplexed SSH master connection for many minutes. If
// the laptop idle-sleeps part way through, the network drops, the master dies,
// and the next SSH session has to authenticate again — which fails outright
// when the SSH agent (1Password, for one) locked itself along with the screen.
// The reset then stops between steps, possibly after the old VM was deleted.
// Keeping the machine out of IDLE sleep keeps the connection alive.
//
// Only system idle sleep is prevented. The display may still sleep and the
// screen may still lock — locking is a security control the user chose, and
// nothing here should weaken it. A lid close or an explicit "sleep now" still
// sleeps; this is best effort against the idle timer, nothing more.
//
// Each backend talks to the OS directly rather than running a helper process
// (caffeinate, systemd-inhibit): a child process can outlive or predecease the
// operation it guards, and its absence from a minimal install would surface as
// a confusing exec error. Release builds are CGO_ENABLED=0, so the macOS
// backend loads IOKit at run time through purego instead of cgo.
//
// A failure to hold the machine awake is never a failure of the operation. Hold
// returns a no-op release alongside the error, so a caller can log it (or not)
// and carry on exactly as it would have before this package existed.
package keepawake

import "sync"

// acquire is the platform backend: take one OS-level "stay awake" assertion,
// returning the function that drops it. It is a variable so tests can drive the
// reference counting below on any OS without touching real power management.
var acquire = acquireOS

var (
	mu        sync.Mutex
	holders   int
	releaseOS func() error
)

// Hold asks the OS to keep the system out of idle sleep until the returned
// release func is called. reason is shown by OS tools (`pmset -g assertions`
// on macOS, `systemd-inhibit --list` on Linux).
//
// Holds nest: concurrent holders — the TUI may run several builds at once —
// share one OS assertion, taken by the first Hold and dropped when the last
// holder releases. The OS therefore shows the reason given by whichever Hold
// took the assertion, not every holder's.
//
// Hold never fails the caller. If the OS assertion cannot be taken (an
// unsupported platform, no logind in a container, a missing framework) it
// returns a no-op release together with the error, for the caller to log or
// ignore. A failure is not remembered: the next Hold tries again, so one bad
// moment (a slow D-Bus at startup) does not disable the feature for the life
// of a long-running TUI.
//
// release is idempotent and safe to call from any goroutine; only its first
// call counts, so a deferred release after an explicit one cannot drop another
// holder's share.
func Hold(reason string) (release func(), err error) {
	mu.Lock()
	defer mu.Unlock()
	// The backend is called under the lock on purpose: a second Hold arriving
	// while the first is still talking to the OS must wait and share that
	// assertion rather than race to take a second one. The backends bound
	// their own latency (D-Bus has a short timeout), so the wait is short.
	if holders == 0 {
		rel, err := acquire(reason)
		if err != nil {
			return func() {}, err
		}
		releaseOS = rel
	}
	holders++
	var once sync.Once
	return func() { once.Do(drop) }, nil
}

// drop gives up one holder's share, releasing the OS assertion with the last.
func drop() {
	mu.Lock()
	defer mu.Unlock()
	holders--
	if holders > 0 {
		return
	}
	rel := releaseOS
	releaseOS = nil
	if rel != nil {
		// Nothing useful can be done with a failure to release: the
		// operation is over, and every backend's assertion dies with the
		// process anyway (IOKit assertions and the logind fd are both
		// owned by this process; the Windows state by a thread that exits).
		_ = rel()
	}
}
