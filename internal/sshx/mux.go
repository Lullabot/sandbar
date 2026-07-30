package sshx

import (
	"context"
	"path/filepath"
)

// muxFlags returns the OpenSSH connection-multiplexing flags shared by every ssh
// and scp argv, or nil when controlDir could not be resolved (a pure
// optimization, never a hard requirement — see New).
//
//   - ControlMaster=auto: the first connection to a target becomes the master;
//     every later one to the SAME target (the 5s board refresh, a per-VM file
//     read, a heartbeat restart, an interactive attach, an scp transfer, even
//     the final refresh batched alongside tea.Quit) reuses its already
//     -authenticated channel instead of paying a fresh handshake. Without this,
//     a user whose ssh agent needs a per-connection unlock (a 1Password /
//     SSH-agent prompt) gets re-prompted on EVERY one of those — on startup
//     preflight, every refresh tick, and even at quit.
//   - ControlPath=<controlDir>/%C: %C is ssh's OWN hash of local host + remote
//     host + port + user, so the unix-domain socket path stays short (there is
//     a hard AF_UNIX path-length limit) and unique per target without us
//     computing anything.
//   - ControlPersist=600: keeps the master alive for 600s after the LAST client
//     disconnects, so a quick quit+relaunch (or the heartbeat's own periodic
//     reconnect) finds the same still-authenticated master instead of
//     re-prompting. The master exits on its own once idle that long — nothing
//     is left running indefinitely.
func (c *Conn) muxFlags() []string {
	if c.controlDir == "" {
		return nil
	}
	return []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(c.controlDir, "%C"),
		"-o", "ControlPersist=600",
	}
}

// muxOptOutKey carries WithoutMux's opt-out on a context.
type muxOptOutKey struct{}

// WithoutMux marks a context's command as one that must NOT share (or become)
// the connection-multiplexing master, and it exists because sharing is only a
// good trade for SHORT commands.
//
// Every ssh sand runs to a given target reuses one master (see muxFlags), and
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

// MuxSuppressed reports whether ctx carries WithoutMux's opt-out. ArgvCtx
// consults it for every backend, so a caller that marks its context once gets an
// unshared connection whether it reaches the guest over the Lima hop or over the
// Proxmox provider's direct ssh.
func MuxSuppressed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	optedOut, _ := ctx.Value(muxOptOutKey{}).(bool)
	return optedOut
}
