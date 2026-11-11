package provider

import (
	"context"

	"github.com/lullabot/sandbar/internal/pve"
)

// pollKey carries AsPoll's mark on a context.
type pollKey struct{}

// AsPoll marks ctx as the board's background polling — the guest heartbeat
// and the checkout sweep, which retry on their own schedule for as long as the
// board is open — rather than something the user asked for. A backend may then
// refuse work it already knows will fail (the Proxmox provider's guest-agent
// backoff) instead of paying for it on every retry. Keying the decision on the
// caller's context, not on which helper a call path happens to use, means a
// user's shell, paste, copy or port forward is never refused by a backoff the
// polling ran up, whichever route it takes to the guest.
func AsPoll(ctx context.Context) context.Context {
	return context.WithValue(ctx, pollKey{}, true)
}

// isPoll reports whether ctx (or a context derived from it) was marked AsPoll.
func isPoll(ctx context.Context) bool {
	v, _ := ctx.Value(pollKey{}).(bool)
	return v
}

// IsDegraded reports whether err means a backend's API is not answering — a
// proxy/gateway 5xx, a transport failure, or the client backing off after
// one — as opposed to answering with an error. The board shows such a member
// as degraded and retrying, not as broken: nothing about its configuration is
// wrong, and the client is already pacing its retries. It exists so the UI
// can ask without importing a backend's client package.
func IsDegraded(err error) bool { return pve.IsUnavailable(err) }
