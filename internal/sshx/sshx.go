// Package sshx builds the ssh and scp argvs every sand backend runs, and runs
// none of them. It holds what must be spelled identically for every ssh: the
// shell quoting of each remote token, identity selection, the throwaway-guest
// host-key posture, the unconditional keepalives, ControlMaster sharing and its
// WithoutMux opt-out, and the SAND_SSH_DEBUG transport log. Each backend keeps
// its own executor — a few lines around an argv, using WaitDelay and
// SuccessDespiteHeldPipes — because a re-derived argv is a connection that hangs
// or prompts, while an executor is not where those bugs live.
//
// Two backends use it: internal/lima.SSHHost (limactl on a remote Lima host)
// and the Proxmox provider (direct ssh into a guest).
//
// SHELL QUOTING of every remote token is the one hazard ssh adds over a local
// exec. `ssh host a b c` joins the tokens with spaces and re-parses them through
// the remote login shell, so any token with a space or metacharacter (the
// provision script, an `edit --set` expression, the guest tmux attach
// expression) must be shell-quoted or the remote shell word-splits it.
package sshx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Config is the identity of one ssh destination. It is secret-free:
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

// Conn builds the ssh and scp argvs for one destination. It holds no network
// connection; the name is for the destination it describes.
type Conn struct {
	cfg Config

	// controlDir, when non-empty, holds the OpenSSH ControlMaster unix-domain
	// sockets for this process's ssh connections (see muxFlags). It is resolved
	// once at construction (New) and left EMPTY when it could not be
	// determined or created — connection multiplexing is a pure optimization,
	// never a hard requirement for reaching the remote host, so a failure here
	// must silently fall back to the pre-multiplexing argv shape rather than
	// failing construction or any later command.
	controlDir string

	// debugLogPath, when non-empty, is the file ssh appends its own verbose
	// protocol log to (see debugFlags). It is opt-in via SAND_SSH_DEBUG and
	// resolved once at construction, for the same reason controlDir is: a
	// failure to resolve it must degrade to "no transport log", never to a
	// failed command.
	debugLogPath string
}

// New builds the argv source for one ssh destination.
func New(cfg Config) *Conn {
	c := &Conn{cfg: cfg}

	// Resolve a per-user control-socket directory for OpenSSH connection
	// multiplexing (see muxFlags). Best-effort: os.UserCacheDir or the MkdirAll
	// can fail (a read-only/unset HOME, a sandboxed environment), and that must
	// never make constructing a Conn — or any command it later runs — fail.
	// It just means every command pays a fresh ssh handshake, exactly as before
	// this feature existed.
	if cacheDir, err := os.UserCacheDir(); err == nil {
		dir := filepath.Join(cacheDir, "sandbar", "ssh")
		if err := os.MkdirAll(dir, 0o700); err == nil {
			c.controlDir = dir
		}
	}
	c.debugLogPath = resolveDebugLog(cfg)
	return c
}

// resolveDebugLog returns the file ssh should append its verbose log to for this
// connection, or "" when the host did not ask for one.
//
// SAND_SSH_DEBUG is an ABSOLUTE directory path to write the logs into, or any
// other non-empty value except "0" (e.g. "1") to use a default directory beside
// the control sockets. Only an absolute path is read as a destination: a
// relative one would resolve against whatever working directory the process
// happens to have, which for a TUI launched from anywhere is not a place a user
// can predict — so it is treated as a plain "on" instead.
//
// The log is per TARGET, not per command: a single provisioning run
// makes many ssh calls to one guest and — with multiplexing on — some of them
// are mux clients of a master started by an earlier one, so the only view that
// explains a transport failure is all of them interleaved in one file, in order.
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

// DebugLogPath is the transport log this connection is writing, or "" when
// SAND_SSH_DEBUG did not ask for one. A caller that reports an ssh-level failure
// uses it to point the user at the evidence (see the Proxmox provider's
// transport-error annotation), which is the whole reason the path is resolved
// once and remembered rather than recomputed at the failure site.
func (c *Conn) DebugLogPath() string { return c.debugLogPath }

// shellSafe matches a token that needs no shell quoting: it survives the remote
// shell's word-splitting and expansion untouched. Anything else is single-quoted.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// Quote quotes s for the REMOTE shell that ssh re-parses the joined command
// through. A safe token is returned verbatim (so `ssh host limactl list --format
// json` reads cleanly); anything with a space or metacharacter is single-quoted,
// with embedded single quotes handled by the standard '\” splice. The empty
// string becomes ” rather than vanishing.
func Quote(s string) string {
	if s != "" && shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Config returns the destination this Conn was built for.
func (c *Conn) Config() Config { return c.cfg }

// Target is the ssh/scp destination: user@host, or host when no user is set.
func (c *Conn) Target() string {
	if c.cfg.User != "" {
		return c.cfg.User + "@" + c.cfg.Host
	}
	return c.cfg.Host
}

// muxFlags returns the OpenSSH connection-multiplexing flags shared by every
// ssh and scp argv, or nil when controlDir could not be resolved (a pure
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

// sshBase is the ssh argv prefix up to and INCLUDING the target: `ssh [-t] [-p
// port] [-i identity] [host-key flags] [keepalives] [debug log] [mux flags]
// [preTarget…] target`. tty adds -t for the interactive attach. Port is
// omitted at the default (<=0 or 22) and identity when unset, the
// multiplexing flags are omitted when controlDir could not be resolved, and
// the debug flags only appear when the host asked for a transport log — so
// the common case is the bare `ssh <keepalives> target …` the tests pin.
//
// mux false replaces the multiplexing flags with an explicit opt-out, for a
// long-lived command that must not share (or become) the master — see
// WithoutMux, which is how a caller asks for it.
//
// preTarget carries any further ssh OPTION flags that must land before the
// target — e.g. ForwardArgv's `-N -L …` — as opposed to a remote command,
// which every caller here appends (shell-quoted) after sshBase returns. It is
// empty for every caller except ForwardArgv.
func (c *Conn) sshBase(tty, mux bool, preTarget ...string) []string {
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
	a = append(a, preTarget...)
	return append(a, c.Target())
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

// debugFlags returns `-vv -E <path>` when a transport log was asked for (see
// resolveDebugLog), or nil.
//
// -E is what makes this safe to switch on during a real create: it sends ssh's
// verbose log to a FILE rather than stderr, and every caller here merges ssh's
// stderr into the same stream the guest's own output goes to. Without it, -vv
// would interleave protocol chatter with the playbook output the user is reading
// and with the TASK banners the TUI's progress parser matches on (internal/ui's
// ansible.go), corrupting the display in exactly the run being diagnosed.
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

// identityFlags returns the key-selection options — `-i <path>`, plus `-o
// IdentitiesOnly=yes` when the connection asked for it (see
// Config.IdentitiesOnly) — or nil when no identity is configured and ssh
// should fall back to the ambient agent / ssh_config. Shared by sshBase and
// scpCommand so ssh and scp authenticate identically; an scp that offered a
// different set of keys than the ssh beside it could fail a copy in the middle of
// a run that was otherwise connecting fine.
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
// are throwaway VMs on recycled IPs (see Config.EphemeralHostKeys), or nil
// when the connection is to a persistent host that should keep the OpenSSH
// trust-on-first-use default. Shared by sshBase and scpCommand so ssh and scp
// treat the guest's key identically.
func (c *Conn) ephemeralHostKeyFlags() []string {
	if !c.cfg.EphemeralHostKeys {
		return nil
	}
	var a []string
	for _, o := range EphemeralHostKeyOptions() {
		a = append(a, "-o", o.Key+"="+o.Value)
	}
	return a
}

// Option is one ssh configuration option, usable as `-o Key=Value` on a
// command line or as a `Key Value` line in an ssh_config file.
type Option struct{ Key, Value string }

// EphemeralHostKeyOptions is the throwaway-guest host-key posture (see
// Config.EphemeralHostKeys), in order. It is the single spelling of it:
// sandbar's own argv uses it as -o flags, and internal/sshconfig writes the
// same options into the workstation entry an editor connects with, so the
// two cannot disagree about how a guest's host key is treated. A fresh slice
// each call, so no caller can change it for the others.
func EphemeralHostKeyOptions() []Option {
	return []Option{
		{"StrictHostKeyChecking", "no"},
		{"UserKnownHostsFile", "/dev/null"},
		{"LogLevel", "ERROR"},
	}
}

// sshCommand builds the full ssh argv to run remoteArgv on the remote host, with
// each remote token shell-quoted so ssh's space-join + remote reshell reconstruct
// the identical argv the local execRunner would have passed via execve.
func (c *Conn) sshCommand(tty bool, remoteArgv ...string) []string {
	return c.sshCommandMux(true, tty, remoteArgv...)
}

// sshCommandMux is sshCommand with the multiplexing decision made explicitly:
// mux false builds the long-lived, unshared form (see WithoutMux). Every
// ctx-carrying entry point below routes through it so one context value decides
// the shape of the argv.
func (c *Conn) sshCommandMux(mux, tty bool, remoteArgv ...string) []string {
	argv := c.sshBase(tty, mux)
	for _, a := range remoteArgv {
		argv = append(argv, Quote(a))
	}
	return argv
}

// scpCommand builds an scp argv. Note scp's port flag is -P (capital), NOT ssh's
// -p — getting this wrong silently ignores a non-default port. The same
// keepalive and multiplexing flags as sshBase are threaded in before the
// endpoints so an scp transfer benefits from (and can itself become) the shared
// master connection, and so a stalled transfer of a multi-gigabyte project tree
// fails in bounded time rather than hanging (see keepaliveFlags).
func (c *Conn) scpCommand(recursive bool, from, to string) []string {
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

// SSHArgv builds the full ssh argv that runs remoteArgv on this connection's
// host, with every remote token shell-quoted (see the package doc: the quoting
// is the one hazard ssh adds over a local exec) and the port, identity, and
// connection-multiplexing flags threaded in. tty adds -t.
//
// It only builds the argv; the caller runs it. internal/lima.SSHHost prefixes
// `LIMA_HOME=… limactl` to the remote argv, and the Proxmox provider runs guest
// commands directly. Both build from here so the quoting, the identity flag,
// and the ControlMaster socket sharing are spelled once for every ssh sand runs.
func (c *Conn) SSHArgv(tty bool, remoteArgv ...string) []string {
	return c.sshCommand(tty, remoteArgv...)
}

// SSHArgvCtx is SSHArgv with the context's multiplexing opt-out honoured
// (WithoutMux): a long-lived command gets its own unshared connection instead of
// riding — or becoming — the master every other command depends on.
//
// It is separate from SSHArgv because not every caller has a context to consult
// at argv-build time: an interactive attach hands its argv to a terminal it does
// not run itself.
func (c *Conn) SSHArgvCtx(ctx context.Context, tty bool, remoteArgv ...string) []string {
	return c.sshCommandMux(!MuxSuppressed(ctx), tty, remoteArgv...)
}

// SCPArgv builds the scp argv for a transfer between from and to (either of
// which may be a `user@host:path` endpoint), with the same port/identity/
// multiplexing flags SSHArgv threads in — including scp's capital -P port flag,
// which is the detail scpCommand exists to get right in one place.
func (c *Conn) SCPArgv(recursive bool, from, to string) []string {
	return c.scpCommand(recursive, from, to)
}

// ForwardArgv builds the ssh argv for a long-running LOCAL port forward:
// `ssh <flags> -N -L <hostPort>:127.0.0.1:<guestPort> target`. -N requests no
// remote command at all — the connection exists purely to forward, and the
// caller (provider.Provider.ForwardArgv) execs this as a child it kills when
// the forward is no longer needed, so there is nothing for -N to run. -L's
// far end is 127.0.0.1 (the REMOTE host's own loopback, where Lima already
// landed the guest port — see the local Lima provider's ForwardArgv doc for
// why), never 0.0.0.0: binding wider would defeat the whole point, which is
// that a checkout under review never becomes reachable from anything but the
// workstation. It reuses sshBase's flag-building (port, identity, host-key
// posture, keepalives, debug log) via the preTarget parameter, so this hop
// authenticates identically to every other ssh this connection makes.
//
// Mux is deliberately OFF (the WithoutMux shape, spelled directly here rather
// than read off a context — a port forward has no ctx-carrying call site to
// read one from). A port forward is long-lived by construction, and
// ControlMaster=auto would let it become — or ride — the shared master:
// killing it (the normal way a caller tears a forward down) would then kill
// every OTHER session sharing that master, or an unrelated master death would
// silently take the forward down too. See WithoutMux for the same reasoning
// applied to the long-lived heartbeat and sweep probes.
// ExitOnForwardFailure=yes turns a failed LOCAL bind from a warning into a
// fatal error, and that is a correctness requirement here rather than a
// preference. The caller picks hostPort by binding and immediately releasing it
// (landreview.freePort), so anything else on the workstation can claim it in
// the gap. ssh's default is to print "bind: Address already in use" and carry
// on serving the connection with no forward attached — at which point the
// caller's readiness probe then talks to whatever unrelated service holds that
// port instead of to the review server. The probe now identifies the responder
// (it demands the review server's own JSON endpoint), so the collision surfaces
// as a readiness timeout rather than as a browser opened onto someone else's
// application — but a timeout that names the wrong problem is still the wrong
// error. Exiting non-zero makes the collision a reported failure at the moment
// it happens.
// The listen side is spelled 127.0.0.1 EXPLICITLY rather than left as a bare
// port, and that is what makes ExitOnForwardFailure actually bite. A bare
// `-L <port>:...` asks ssh to bind every loopback address getaddrinfo returns,
// and on a dual-stack host ssh treats the forward as established if ANY of them
// succeeds. Measured: with something already holding 127.0.0.1:<port>, ssh
// bound ::1, printed "bind [127.0.0.1]:<port>: Address already in use" as a
// mere warning, and kept running — while the caller's probe and browser, which
// both use 127.0.0.1, reached the OTHER service. Naming the address makes the
// bind singular, so a collision is unambiguously a failure, and drops the ::1
// listener nobody wanted.
func (c *Conn) ForwardArgv(hostPort, guestPort int) []string {
	return c.sshBase(false, false,
		"-o", "ExitOnForwardFailure=yes",
		"-N", "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", hostPort, guestPort))
}
