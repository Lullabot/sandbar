package landgh

import (
	"context"
	"time"
)

// availabilityTimeout bounds the `gh auth status` probe. Without it a gh that
// hangs — a wedged credential helper, an unreachable GHES host — leaves the
// Landing pane on "checking host gh…" forever with no way to move it along.
// A failed probe degrades to the gh-free browser path, which is a working
// fallback, so timing out is strictly better than waiting indefinitely.
const availabilityTimeout = 10 * time.Second

// Availability is the result of the host-gh probe: not just whether the
// one-key draft-create path is usable, but WHY it is not when it is not.
//
// The distinction is the whole point of this type. The probe has two
// independent failure modes with two completely different fixes — gh missing
// from PATH (install it) versus gh present but holding no credential this
// process can see (authenticate it) — and collapsing them into one boolean
// produced a "gh: unavailable" message that told a user with gh very much
// installed nothing they could act on.
//
// The authentication case is worth spelling out, because it is the one people
// hit without understanding why: gh is invoked DIRECTLY, argv-only, never
// through a shell (see the Runner contract in landgh.go). A credential that
// exists only as a shell alias or wrapper function — the shape the 1Password
// shell plugin and similar credential injectors use — is therefore invisible
// here, even though the very same `gh` command works when typed at an
// interactive prompt. That is not a false negative: every later `gh api` call
// this package makes would fail the same way, so the probe is telling the
// truth about this invocation style. The fix is to make the credential visible
// to a plain exec — a `gh auth login`, or GH_TOKEN exported into the
// environment `sand` itself was launched from.
type Availability struct {
	// Installed is whether a gh binary resolved on PATH.
	Installed bool

	// Authenticated is whether `gh auth status` then exited zero. It is only
	// meaningful when Installed is true.
	Authenticated bool
}

// OK reports whether host gh is usable for the one-key draft-create action.
func (a Availability) OK() bool { return a.Installed && a.Authenticated }

// Reason is a short human-readable explanation of a not-OK Availability,
// phrased as the state it found (never as an instruction), for a caller to
// embed in whatever sentence it is building. It returns "" when OK.
func (a Availability) Reason() string {
	switch {
	case !a.Installed:
		return "not installed"
	case !a.Authenticated:
		return "not authenticated"
	default:
		return ""
	}
}

// Availability probes host gh: present on PATH, and authenticated (`gh auth
// status` succeeds). It never touches a VM or the guest — this is a purely
// workstation-local check. Callers use it to decide whether to offer the
// one-key draft-create action or fall back to the gh-free browser URL helpers
// (CompareURL/PRURL/OpenInBrowser), and to explain which of the two it is.
func (c *Client) Availability(ctx context.Context) Availability {
	if _, err := c.lookPath("gh"); err != nil {
		return Availability{}
	}
	ctx, cancel := context.WithTimeout(ctx, availabilityTimeout)
	defer cancel()
	// `gh auth status` writes its human output to stderr, so the exit code is
	// the entire signal here; Output discards stderr and returns a non-nil
	// error for any non-zero exit, which is exactly the bit needed.
	if _, err := c.run.Output(ctx, "auth", "status"); err != nil {
		return Availability{Installed: true}
	}
	return Availability{Installed: true, Authenticated: true}
}

// Available is the boolean shorthand for Availability().OK(), for callers that
// only branch on usable/not and have no message to explain.
func (c *Client) Available(ctx context.Context) bool {
	return c.Availability(ctx).OK()
}
