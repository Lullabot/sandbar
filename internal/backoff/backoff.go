// Package backoff holds the one doubling-retry schedule every poller in sandbar
// uses — the guest heartbeat and checkout sweep, the Proxmox client's per-host
// gate, and the Proxmox guest-agent lookup — so they cannot drift into
// different rules for the first retry, the cap, or a zero base.
package backoff

import "time"

// Doubling is the wait after the nth consecutive failure: base doubled once per
// failure after the first, capped at max. n <= 1 returns base unchanged, so a
// one-off failure is retried as promptly as the caller's base allows, and a
// base <= 0 disables the wait entirely.
func Doubling(base time.Duration, n int, max time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	wait := base
	for i := 1; i < n && wait < max; i++ {
		wait *= 2
	}
	if wait > max {
		wait = max
	}
	return wait
}
