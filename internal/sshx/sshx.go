// Package sshx builds the OpenSSH command lines sand uses to reach a machine it
// is not running on. It is BACKEND-AGNOSTIC on purpose: the remote-Lima hop
// (`ssh <host> limactl …`, internal/lima) and the Proxmox provider's direct
// guest transport (`ssh <vm-ip> bash -c …`, internal/provider) both build their
// argvs here, so the flags that decide whether an ssh connects at all — the
// quoting of remote tokens, the identity selection, the host-key posture, the
// keepalives, the ControlMaster socket sharing — are spelled ONCE rather than
// re-derived per backend.
//
// This package used to live in internal/lima, which is where the plumbing grew
// because the Lima backend was written first. Nothing here has anything to do
// with Lima; the name it lived under was an accident of history, and a second
// backend importing "lima" to build an ssh to a Proxmox VM read as a mistake
// every time.
//
// Two things are load-bearing and easy to lose:
//
//   - SHELL QUOTING of every remote token. `ssh host a b c` joins the tokens
//     with spaces and re-parses them through the REMOTE login shell, so any
//     token with a space or metacharacter (a provisioning script, an `edit
//     --set` expression, the guest tmux attach expression) must be shell-quoted
//     or the remote shell word-splits it. A local exec needs none of this —
//     execve passes argv verbatim — so this is the one genuinely new hazard an
//     ssh transport introduces. Quote is the whole answer, and Argv applies it
//     to every remote token so no caller has to remember.
//   - cmd.WaitDelay REAPING on the executor side. An ssh client forks children
//     (a ControlPersist master; over the Lima hop, a remote limactl that forks
//     the guest ssh) that inherit the stdout/stderr pipes os/exec created, so a
//     cancelled command's Wait can block forever on an orphan. See WaitDelay
//     and SuccessDespiteHeldPipes, which every executor built on these argvs
//     must use.
//
// The package deliberately does NOT run anything. Each caller owns its own
// executor — internal/lima's SSHHost wants separated stdout/stderr and a
// LIMA_HOME prefix, the Proxmox provider wants its own transport-error
// annotation — and an executor is a handful of lines around an argv. What
// cannot be re-derived safely is the argv itself, which is what lives here.
package sshx

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Config is the connection identity for one ssh target. It is secret-free:
// IdentityPath is a PATH to a private key file, never key material (the same
// contract provider.TargetConfig keeps so the target can be persisted in the
// registry).
type Config struct {
	Host string // required
	User string // "" lets ssh use its own default (ssh_config / local user)
	Port int    // <=0 or 22 omits -p / -P entirely
	// IdentityPath is a private-key FILE path, or "" to fall back to the ambient
	// ssh agent / ssh_config. Never key material.
	IdentityPath string
	// IdentitiesOnly, when true (and IdentityPath is set), adds `-o
	// IdentitiesOnly=yes` so ssh offers ONLY IdentityPath and never the keys the
	// ssh agent or ssh_config would otherwise volunteer.
	//
	// It is for a backend whose guest trusts exactly one key — the one sand had
	// cloud-init install from identity_path (the Proxmox provider) — where every
	// other key ssh could offer is guaranteed to be refused. Two failures follow
	// from offering them anyway. A locked or unusable agent key turns a single
	// refusal into a run of them, and the guest's sshd disconnects at MaxAuthTries
	// (6 by default) with "Too many authentication failures" instead of a clean
	// "Permission denied" — a far more confusing failure for the same underlying
	// cause. Worse, an agent holding six or more keys can exhaust MaxAuthTries
	// BEFORE ssh ever gets to offer the right one, so a correctly-provisioned
	// guest and a perfectly good unlocked key still fail to connect.
	//
	// The remote-Lima hop deliberately leaves this FALSE: that host is a machine
	// the user configured and authenticates to on their own terms, so an agent key
	// or an ssh_config IdentityFile is a legitimate way in, and restricting the
	// offer to IdentityPath would break connections that work today.
	IdentitiesOnly bool
	// EphemeralHostKeys, when true, adds `-o StrictHostKeyChecking=no -o
	// UserKnownHostsFile=/dev/null -o LogLevel=ERROR` to every ssh and scp argv.
	// It is for a backend whose "host" is a freshly-created VM reached at a
	// recycled IP (the Proxmox provider): that guest's host key is never in
	// known_hosts, and a rebuilt VM presents a DIFFERENT key on an IP the last one
	// used, so the OpenSSH default (StrictHostKeyChecking=ask) opens /dev/tty to
	// prompt — hanging a TUI that owns the terminal, and then failing the
	// provisioning ssh with "Host key verification failed" — while a strict-yes
	// setup would hard-fail the moment an IP is reused. /dev/null keeps those
	// throwaway keys out of the user's real known_hosts, and LogLevel=ERROR mutes
	// the "Permanently added …" warning that would otherwise bleed into the
	// message log.
	//
	// The remote-Lima hop deliberately leaves this FALSE: that host is a
	// persistent machine the user configured, where trust-on-first-use host-key
	// pinning is the right default and a changed key genuinely warrants a stop.
	EphemeralHostKeys bool
}

// Conn is one ssh connection identity plus the process-wide resources every argv
// built from it shares: the ControlMaster socket directory and the opt-in
// transport log. Both are resolved once, at New, because both are best-effort and
// neither may be allowed to fail a command later.
//
// It is safe for concurrent use: nothing here mutates after construction.
type Conn struct {
	cfg Config

	// controlDir, when non-empty, holds the OpenSSH ControlMaster unix-domain
	// sockets for this process's ssh connections (see muxFlags). It is resolved
	// once at construction (New) and left EMPTY when it could not be determined
	// or created — connection multiplexing is a pure optimization, never a hard
	// requirement for reaching the far side, so a failure here must silently fall
	// back to the pre-multiplexing argv shape rather than failing construction or
	// any later command.
	controlDir string

	// debugLogPath, when non-empty, is the file ssh appends its own verbose
	// protocol log to (see debugFlags). It is opt-in via SAND_SSH_DEBUG and
	// resolved once at construction, for the same reason controlDir is: a failure
	// to resolve it must degrade to "no transport log", never to a failed command.
	debugLogPath string
}

// New builds a connection identity for cfg, resolving the shared control-socket
// directory and the opt-in transport log.
func New(cfg Config) *Conn {
	c := &Conn{cfg: cfg}

	// Resolve a per-user control-socket directory for OpenSSH connection
	// multiplexing (see muxFlags). Best-effort: os.UserCacheDir or the MkdirAll
	// can fail (a read-only/unset HOME, a sandboxed environment), and that must
	// never make constructing a Conn — or any command it later builds — fail. It
	// just means every command pays a fresh ssh handshake, exactly as before this
	// feature existed.
	if cacheDir, err := os.UserCacheDir(); err == nil {
		dir := filepath.Join(cacheDir, "sandbar", "ssh")
		if err := os.MkdirAll(dir, 0o700); err == nil {
			c.controlDir = dir
		}
	}
	c.debugLogPath = resolveDebugLog(cfg)
	return c
}

// Config returns the connection identity this Conn was built for.
func (c *Conn) Config() Config { return c.cfg }

// User is the configured login user, or "" when ssh should pick its own default.
// It is the fallback a caller uses when it cannot learn the far side's real login
// user over the wire.
func (c *Conn) User() string { return c.cfg.User }

// Target is the ssh/scp destination: user@host, or host when no user is set.
func (c *Conn) Target() string {
	if c.cfg.User != "" {
		return c.cfg.User + "@" + c.cfg.Host
	}
	return c.cfg.Host
}

// DebugLogPath is the transport log this connection is writing, or "" when
// SAND_SSH_DEBUG did not ask for one. A caller that reports an ssh-level failure
// uses it to point the user at the evidence (see the Proxmox provider's
// transport-error annotation), which is the whole reason the path is resolved
// once and remembered rather than recomputed at the failure site.
func (c *Conn) DebugLogPath() string { return c.debugLogPath }

// --- argv construction ----------------------------------------------------------

// shellSafe matches a token that needs no shell quoting: it survives the remote
// shell's word-splitting and expansion untouched. Anything else is single-quoted.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// Quote quotes s for the REMOTE shell that ssh re-parses the joined command
// through. A safe token is returned verbatim (so `ssh host limactl list --format
// json` reads cleanly); anything with a space or metacharacter is single-quoted,
// with embedded single quotes handled by the standard '\” splice. The empty
// string becomes ” rather than vanishing.
//
// Argv applies it to every remote token already; it is exported for the caller
// that has to splice a path into a remote `sh -c` SCRIPT, where the quoting is
// inside the token rather than around it.
func Quote(s string) string {
	if s != "" && shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Argv builds the full ssh argv that runs remoteArgv on this connection's host,
// with every remote token shell-quoted (see the package doc: the quoting is the
// one genuinely new hazard the transport introduces) and the port, identity,
// host-key, keepalive, debug-log, and connection-multiplexing flags threaded in.
// tty adds -t, which the interactive attach needs for the nested tmux client.
//
// Prefer ArgvCtx wherever a context is in hand: it honours the multiplexing
// opt-out a long-lived caller marks its context with. Argv is for the caller with
// no context to consult — an attach that hands an argv to a terminal it does not
// run itself.
func (c *Conn) Argv(tty bool, remoteArgv ...string) []string {
	return c.argv(true, tty, remoteArgv...)
}

// ArgvCtx is Argv with the context's multiplexing opt-out honoured (see
// WithoutMux): a long-lived command gets its own unshared connection instead of
// riding — or becoming — the master every other command depends on.
func (c *Conn) ArgvCtx(ctx context.Context, tty bool, remoteArgv ...string) []string {
	return c.argv(!MuxSuppressed(ctx), tty, remoteArgv...)
}

// argv is Argv with the multiplexing decision made explicitly, so one context
// value decides the shape of every argv this package builds.
func (c *Conn) argv(mux, tty bool, remoteArgv ...string) []string {
	out := c.base(tty, mux)
	for _, a := range remoteArgv {
		out = append(out, Quote(a))
	}
	return out
}

// base is the ssh argv prefix up to and INCLUDING the target: `ssh [-t] [-p
// port] [-i identity] [host-key flags] [keepalives] [debug log] [mux flags]
// target`. Port is omitted at the default (<=0 or 22) and identity when unset,
// the multiplexing flags are omitted when controlDir could not be resolved, and
// the debug flags only appear when the connection asked for a transport log — so
// the common case is the bare `ssh <keepalives> target …` the tests pin.
//
// mux false replaces the multiplexing flags with an explicit opt-out, for a
// long-lived command that must not share (or become) the master — see
// WithoutMux, which is how a caller asks for it.
func (c *Conn) base(tty, mux bool) []string {
	a := []string{"ssh"}
	if tty {
		a = append(a, "-t")
	}
	if c.cfg.Port > 0 && c.cfg.Port != 22 {
		a = append(a, "-p", strconv.Itoa(c.cfg.Port))
	}
	a = append(a, c.identityFlags()...)
	a = append(a, c.ephemeralHostKeyFlags()...)
	a = append(a, keepaliveFlags()...)
	a = append(a, c.debugFlags()...)
	if mux {
		a = append(a, c.muxFlags()...)
	} else {
		a = append(a, "-o", "ControlMaster=no", "-o", "ControlPath=none")
	}
	return append(a, c.Target())
}

// SCPArgv builds an scp argv for a transfer between from and to (either of which
// may be a `user@host:path` endpoint), with the same identity, host-key,
// keepalive and multiplexing flags Argv threads in — so an scp transfer
// authenticates identically to the ssh beside it, benefits from (and can itself
// become) the shared master connection, and fails in bounded time rather than
// hanging on a stalled multi-gigabyte tree.
//
// Note scp's port flag is -P (capital), NOT ssh's -p — getting this wrong
// silently ignores a non-default port, which is the detail this function exists
// to get right in one place.
func (c *Conn) SCPArgv(recursive bool, from, to string) []string {
	a := []string{"scp"}
	if recursive {
		a = append(a, "-r")
	}
	if c.cfg.Port > 0 && c.cfg.Port != 22 {
		a = append(a, "-P", strconv.Itoa(c.cfg.Port))
	}
	a = append(a, c.identityFlags()...)
	a = append(a, c.ephemeralHostKeyFlags()...)
	a = append(a, keepaliveFlags()...)
	a = append(a, c.muxFlags()...)
	return append(a, from, to)
}

// keepaliveFlags returns the application-level keepalive options every ssh and
// scp this package builds carries.
//
// They are unconditional because the alternative is not "a slower failure", it is
// NO failure: a provisioning run streams one ssh session for a whole playbook,
// and single tasks in it (a repo clone, an apt install) go minutes without
// putting a byte on the channel. OpenSSH detects nothing in that window on its
// own — TCPKeepAlive is on by default but fires at the kernel's two-hour idle
// timer — so a session reaped by a stateful firewall or NAT on the path, or a
// guest that stops answering, leaves the client blocked forever with no output
// and no error. That is indistinguishable from a slow task, which makes it
// undiagnosable.
//
// 15s x 8 declares a dead channel in about two minutes and exits, turning an
// unbounded hang into a bounded, reported failure. The count is deliberately
// generous: a guest pinned by a heavy apt install can miss several replies
// without being gone, and a false positive here would kill a healthy build.
func keepaliveFlags() []string {
	return []string{
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=8",
	}
}

// identityFlags returns the key-selection options — `-i <path>`, plus `-o
// IdentitiesOnly=yes` when the connection asked for it (see
// Config.IdentitiesOnly) — or nil when no identity is configured and ssh should
// fall back to the ambient agent / ssh_config. Shared by base and SCPArgv so ssh
// and scp authenticate identically; an scp that offered a different set of keys
// than the ssh beside it could fail a copy in the middle of a run that was
// otherwise connecting fine.
//
// IdentitiesOnly is gated on IdentityPath being set on purpose: with no -i to
// restrict ssh TO, the option would only narrow ssh to its built-in default key
// names — a change in behaviour with nothing to gain, since the point is to offer
// the one key the far side is known to trust.
func (c *Conn) identityFlags() []string {
	if c.cfg.IdentityPath == "" {
		return nil
	}
	a := []string{"-i", c.cfg.IdentityPath}
	if c.cfg.IdentitiesOnly {
		a = append(a, "-o", "IdentitiesOnly=yes")
	}
	return a
}

// ephemeralHostKeyFlags returns the host-key options for a backend whose guests
// are throwaway VMs on recycled IPs (see Config.EphemeralHostKeys), or nil when
// the connection is to a persistent host that should keep the OpenSSH
// trust-on-first-use default. Shared by base and SCPArgv so ssh and scp treat the
// far side's key identically.
func (c *Conn) ephemeralHostKeyFlags() []string {
	if !c.cfg.EphemeralHostKeys {
		return nil
	}
	return []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
	}
}

// --- transport log --------------------------------------------------------------

// resolveDebugLog returns the file ssh should append its verbose log to for this
// connection, or "" when the caller did not ask for one.
//
// SAND_SSH_DEBUG is an ABSOLUTE directory path to write the logs into, or any
// other non-empty value except "0" (e.g. "1") to use a default directory beside
// the control sockets. Only an absolute path is read as a destination: a relative
// one would resolve against whatever working directory the process happens to
// have, which for a TUI launched from anywhere is not a place a user can predict
// — so it is treated as a plain "on" instead.
//
// The log is per TARGET, not per command: a single provisioning run makes many
// ssh calls to one guest and — with multiplexing on — some of them are mux
// clients of a master started by an earlier one, so the only view that explains a
// transport failure is all of them interleaved in one file, in order.
//
// Best-effort throughout: an unresolvable cache dir or an uncreatable directory
// yields "", which just means no log. Diagnostics must never be able to fail a
// command.
func resolveDebugLog(cfg Config) string {
	v := os.Getenv("SAND_SSH_DEBUG")
	if v == "" || v == "0" {
		return ""
	}
	dir := v
	if !filepath.IsAbs(v) {
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(cacheDir, "sandbar", "ssh-debug")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	return filepath.Join(dir, debugLogName(cfg)+".log")
}

// debugLogName renders a connection identity as a single filename-safe token, so
// a target that is a bare IPv4 address, an IPv6 literal, or a user@host all
// produce a name that is readable and cannot escape the log directory.
func debugLogName(cfg Config) string {
	name := cfg.Host
	if cfg.User != "" {
		name = cfg.User + "@" + name
	}
	if cfg.Port > 0 && cfg.Port != 22 {
		name += "-" + strconv.Itoa(cfg.Port)
	}
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_', r == '@':
			return r
		}
		return '-'
	}, name)
	if safe == "" {
		return "ssh"
	}
	return safe
}

// debugFlags returns `-vv -E <path>` when a transport log was asked for (see
// resolveDebugLog), or nil.
//
// -E is what makes this safe to switch on during a real create: it sends ssh's
// verbose log to a FILE rather than stderr, and every executor built on these
// argvs merges ssh's stderr into the same stream the guest's own output goes to.
// Without it, -vv would interleave protocol chatter with the playbook output the
// user is reading and with the TASK banners the TUI's progress parser matches on
// (internal/ui's ansible.go), corrupting the display in exactly the run being
// diagnosed.
//
// ssh only: scp has no -E, so a debug scp would have nowhere to put its log but
// the payload stream, which for a `tar -czf -` transfer is the one thing that
// must not be written to.
func (c *Conn) debugFlags() []string {
	if c.debugLogPath == "" {
		return nil
	}
	return []string{"-vv", "-E", c.debugLogPath}
}
