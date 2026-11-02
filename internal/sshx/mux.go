package sshx

import "context"

// muxOptOutKey carries WithoutMux's opt-out on a context.
type muxOptOutKey struct{}

// WithoutMux marks a context's command as one that must NOT share (or become)
// the connection-multiplexing master, and it exists because sharing is only a
// good trade for SHORT commands.
//
// Every ssh sand runs to a given target reuses one master (see
// internal/lima.SSHHost's muxFlags), and
// with ControlMaster=auto the master is whichever connection got there first —
// which may be a command sand considers disposable and kills on a cancelled
// context. When a master dies, EVERY session multiplexed through it dies in the
// same instant. Field evidence: 28 master deaths in one session's transport log,
// each taking down a burst of five or six sessions at once — every VM's gauges
// and sweep, plus whatever provisioning was in flight.
//
// The long-lived probes (internal/ui's heartbeat and checkout sweep) hold one
// connection each for their whole life, so multiplexing saves them nothing: they
// pay one handshake either way. They are also the most likely candidates to
// BECOME the master (they connect early and last longest) and the biggest losers
// when one dies. Opting them out is therefore all upside — it costs one TCP
// connection per running VM, which is what they already hold, and it decouples
// their fate from every other command's.
//
// ControlPath=none as well as ControlMaster=no: without it, `ssh` still consults
// (and can be blocked by) an existing socket. This does NOT stop the short
// commands — refresh reads, file copies, provisioning steps, the interactive
// attach — from multiplexing among themselves, which is where the saved
// handshakes (and the un-repeated agent prompts) actually matter.
func WithoutMux(ctx context.Context) context.Context {
	return context.WithValue(ctx, muxOptOutKey{}, true)
}

// MuxSuppressed reports whether ctx carries WithoutMux's opt-out. Whatever builds
// an ssh argv from a context consults it: internal/lima.SSHHost for both
// limactl over the hop and the Proxmox provider's direct guest ssh.
func MuxSuppressed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	optedOut, _ := ctx.Value(muxOptOutKey{}).(bool)
	return optedOut
}
