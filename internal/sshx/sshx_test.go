package sshx

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// These tests are the executable specification of the ssh/scp argv every
// backend runs. They assert on the argv alone; nothing here spawns ssh.

var testCfg = Config{Host: "example.com", User: "dev"}

func hasToken(argv []string, tok string) bool { return slices.Contains(argv, tok) }

// TestSSHControlDirAndMuxFlags proves New resolves a per-user control
// dir (0o700) for OpenSSH connection multiplexing, and that both sshBase and
// scpCommand thread ControlMaster=auto / ControlPath=<dir>/%C /
// ControlPersist=600 in before the target — the fix for a user with an
// SSH-agent prompt (1Password etc.) being re-prompted on every 5s board
// refresh, per-VM file read, heartbeat restart, and the final batched refresh
// at quit: every one of those commands now shares one already-authenticated
// master connection instead of paying a fresh handshake.
func TestSSHControlDirAndMuxFlags(t *testing.T) {
	h := New(testCfg)
	if h.controlDir == "" {
		t.Fatalf("New left controlDir empty in a normal environment")
	}
	info, err := os.Stat(h.controlDir)
	if err != nil {
		t.Fatalf("controlDir was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("controlDir perm = %o, want 0700", perm)
	}

	wantControlPath := "ControlPath=" + filepath.Join(h.controlDir, "%C")
	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"sshBase", h.sshBase(false, true)},
		{"scpCommand", h.scpCommand(false, "/local", "remote:/path")},
	} {
		argv := tc.argv
		for _, val := range []string{"ControlMaster=auto", wantControlPath, "ControlPersist=600"} {
			idx := slices.Index(argv, val)
			if idx <= 0 || argv[idx-1] != "-o" {
				t.Fatalf("%s = %v: want %q preceded by its own -o", tc.name, argv, val)
			}
		}
	}
}

// TestSSHKeepalivesAlwaysThreaded pins the liveness options onto EVERY ssh and
// scp this package builds, whatever the connection's shape.
//
// The regression it guards is not a wrong argv, it is a hang: a provisioning run
// streams one ssh session for a whole playbook, and a task that goes minutes
// without writing to the channel (a repo clone, an apt install) is
// indistinguishable from a session a firewall silently reaped — OpenSSH's own
// defaults notice neither for two hours. Dropping these options anywhere, for
// any connection, turns a reported failure back into an unbounded wait with no
// output to explain it.
func TestSSHKeepalivesAlwaysThreaded(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"plain", testCfg},
		{"port and identity", Config{Host: "h", User: "u", Port: 2222, IdentityPath: "/k"}},
		{"ephemeral guest", Config{Host: "10.0.0.9", User: "dev", EphemeralHostKeys: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := New(tc.cfg)
			for _, cmd := range []struct {
				name string
				argv []string
			}{
				{"sshBase", h.sshBase(false, true)},
				{"sshBase tty", h.sshBase(true, true)},
				{"scpCommand", h.scpCommand(true, "/local", "remote:/path")},
			} {
				for _, val := range []string{"ServerAliveInterval=15", "ServerAliveCountMax=8"} {
					idx := slices.Index(cmd.argv, val)
					if idx <= 0 || cmd.argv[idx-1] != "-o" {
						t.Fatalf("%s = %v: want %q preceded by its own -o", cmd.name, cmd.argv, val)
					}
				}
			}
		})
	}
}

// TestSSHDebugLogOptIn proves the transport log is opt-in, lands in a file
// rather than on stderr, and is per-target.
//
// The -E is the load-bearing half. Every guest-command caller in the Proxmox
// provider merges ssh's stderr into the stream carrying the guest's own output,
// which is also the stream the TUI's progress parser reads TASK banners from —
// so a -vv that logged to stderr would corrupt the display of the very run being
// diagnosed, and the tool would be unusable for the failures it exists to catch.
func TestSSHDebugLogOptIn(t *testing.T) {
	t.Run("off by default", func(t *testing.T) {
		t.Setenv("SAND_SSH_DEBUG", "")
		h := New(testCfg)
		if got := h.DebugLogPath(); got != "" {
			t.Fatalf("DebugLogPath = %q with SAND_SSH_DEBUG unset, want empty", got)
		}
		if argv := h.sshBase(false, true); hasToken(argv, "-vv") {
			t.Fatalf("sshBase = %v, want no -vv when the debug log is off", argv)
		}
	})

	t.Run("explicit directory", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("SAND_SSH_DEBUG", dir)
		h := New(Config{Host: "10.0.0.9", User: "dev"})

		want := filepath.Join(dir, "dev@10.0.0.9.log")
		if got := h.DebugLogPath(); got != want {
			t.Fatalf("DebugLogPath = %q, want %q", got, want)
		}
		argv := h.sshBase(false, true)
		idx := slices.Index(argv, "-E")
		if idx < 0 || idx+1 >= len(argv) || argv[idx+1] != want {
			t.Fatalf("sshBase = %v, want `-E %s`", argv, want)
		}
		if !hasToken(argv, "-vv") {
			t.Fatalf("sshBase = %v, want -vv alongside the log file", argv)
		}
		// scp has no -E, so a debug log must never be requested for it: its only
		// outlet would be the payload stream.
		if scp := h.scpCommand(false, "/local", "remote:/path"); hasToken(scp, "-E") || hasToken(scp, "-vv") {
			t.Fatalf("scpCommand = %v, want no debug flags (scp has no -E)", scp)
		}
	})

	t.Run("one file per target", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("SAND_SSH_DEBUG", dir)
		a := New(Config{Host: "10.0.0.9", User: "dev"})
		b := New(Config{Host: "10.0.0.10", User: "dev"})
		if a.DebugLogPath() == b.DebugLogPath() {
			t.Fatalf("two targets shared one log file: %q", a.DebugLogPath())
		}
		// A second connection to the SAME target must append to the same file:
		// with multiplexing on, a mux client's failure is only explicable next to
		// the master's log lines, in order.
		if again := New(Config{Host: "10.0.0.9", User: "dev"}); again.DebugLogPath() != a.DebugLogPath() {
			t.Fatalf("same target got two log files: %q and %q", a.DebugLogPath(), again.DebugLogPath())
		}
	})

	t.Run("IPv6 literal stays a safe filename", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("SAND_SSH_DEBUG", dir)
		h := New(Config{Host: "fd00::1", User: "dev"})
		got := h.DebugLogPath()
		if filepath.Dir(got) != dir {
			t.Fatalf("DebugLogPath = %q, want it inside %q", got, dir)
		}
		if strings.ContainsAny(filepath.Base(got), ":/") {
			t.Fatalf("log filename %q kept a separator-unsafe character", filepath.Base(got))
		}
	})
}

// TestSSHNoControlDirOmitsMuxFlags proves the graceful-degradation path: when
// controlDir could not be resolved (simulated here by constructing the struct
// directly, standing in for os.UserCacheDir/MkdirAll failing in New),
// sshBase/scpCommand argv carries no multiplexing flags — connection
// multiplexing is a pure optimization and must NEVER become a hard
// requirement for reaching the remote host.
//
// The keepalive options stay, and that asymmetry is the point: multiplexing is
// an optimization that can be dropped, while a connection with no liveness check
// can hang forever instead of failing (see keepaliveFlags). One degrades, the
// other does not.
func TestSSHNoControlDirOmitsMuxFlags(t *testing.T) {
	h := &Conn{cfg: testCfg}
	// controlDir left at its zero value "" — the failure path.
	want := append(append([]string{"ssh"}, keepaliveFlags()...), "dev@example.com")
	if got := h.sshBase(false, true); !slices.Equal(got, want) {
		t.Fatalf("sshBase with empty controlDir = %v, want %v", got, want)
	}
	wantScp := append(append([]string{"scp"}, keepaliveFlags()...), "/local", "remote:/path")
	if got := h.scpCommand(false, "/local", "remote:/path"); !slices.Equal(got, wantScp) {
		t.Fatalf("scpCommand with empty controlDir = %v, want %v", got, wantScp)
	}
	for _, tok := range []string{"-vv", "-E"} {
		if hasToken(h.sshBase(false, true), tok) {
			t.Fatalf("sshBase carried %q with no SAND_SSH_DEBUG set: %v", tok, h.sshBase(false, true))
		}
	}
}

// TestSSHEphemeralHostKeys pins the throwaway-guest host-key contract
// (Config.EphemeralHostKeys): when set, EVERY ssh and scp argv must carry
// `-o StrictHostKeyChecking=no`, `-o UserKnownHostsFile=/dev/null`, and
// `-o LogLevel=ERROR` — the flags that stop the Proxmox provider's first
// provisioning ssh from blocking on OpenSSH's interactive host-key prompt (a
// hang the TUI cannot answer) and from hard-failing when a rebuilt VM reuses a
// prior VM's IP with a new key. When UNSET (the remote-Lima default), none of
// those options may appear — that hop pins host keys on first use.
func TestSSHEphemeralHostKeys(t *testing.T) {
	// hasOptPair reports whether argv contains the contiguous `-o <opt>` pair, so
	// a stray "-o" or a lookalike value elsewhere cannot pass the assertion.
	hasOptPair := func(argv []string, opt string) bool {
		for i := 0; i+1 < len(argv); i++ {
			if argv[i] == "-o" && argv[i+1] == opt {
				return true
			}
		}
		return false
	}
	opts := []string{"StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null", "LogLevel=ERROR"}

	t.Run("set: ssh and scp carry every option", func(t *testing.T) {
		h := New(Config{Host: "10.0.0.9", User: "dev", EphemeralHostKeys: true})
		ssh := h.SSHArgv(false, "true")
		scp := h.SCPArgv(false, "/tmp/a", "dev@10.0.0.9:/tmp/a")
		for _, argv := range [][]string{ssh, scp} {
			for _, opt := range opts {
				if !hasOptPair(argv, opt) {
					t.Errorf("argv %v missing `-o %s`", argv, opt)
				}
			}
		}
	})

	t.Run("unset: neither ssh nor scp mentions host-key options", func(t *testing.T) {
		h := New(Config{Host: "10.0.0.9", User: "dev"})
		ssh := h.SSHArgv(false, "true")
		scp := h.SCPArgv(false, "/tmp/a", "dev@10.0.0.9:/tmp/a")
		for _, argv := range [][]string{ssh, scp} {
			for _, opt := range opts {
				if hasToken(argv, opt) {
					t.Errorf("argv %v must not weaken host-key checking for a persistent host (found %q)", argv, opt)
				}
			}
		}
	})
}

// TestSSHIdentitiesOnly pins the single-key-offer contract
// (Config.IdentitiesOnly): when set, EVERY ssh and scp argv must carry `-o
// IdentitiesOnly=yes` alongside its `-i`, so ssh offers only the configured key
// and never the ones the agent or ssh_config would volunteer.
//
// The regression it guards is an authentication failure on a correctly-built
// guest. A Proxmox guest trusts exactly one key, so every extra offer is refused
// and counts against the guest sshd's MaxAuthTries (6): a locked agent key turns
// a clean "Permission denied" into a "Too many authentication failures"
// disconnect, and an agent holding six or more keys exhausts the budget before
// ssh ever offers the right one. When UNSET (the remote-Lima default) the option
// must be absent — that hop authenticates on the user's own terms, where an agent
// key or an ssh_config IdentityFile is a legitimate way in.
func TestSSHIdentitiesOnly(t *testing.T) {
	const opt = "IdentitiesOnly=yes"

	t.Run("set: ssh and scp offer only the configured key", func(t *testing.T) {
		h := New(Config{Host: "10.0.0.9", User: "dev", IdentityPath: "/k", IdentitiesOnly: true})
		for _, argv := range [][]string{
			h.SSHArgv(false, "true"),
			h.SCPArgv(false, "/tmp/a", "dev@10.0.0.9:/tmp/a"),
		} {
			idx := slices.Index(argv, opt)
			if idx <= 0 || argv[idx-1] != "-o" {
				t.Errorf("argv %v: want %q preceded by its own -o", argv, opt)
			}
			// The restriction is meaningless without the key it restricts ssh TO.
			if i := slices.Index(argv, "-i"); i < 0 || i+1 >= len(argv) || argv[i+1] != "/k" {
				t.Errorf("argv %v: want `-i /k` alongside %q", argv, opt)
			}
		}
	})

	t.Run("unset: neither ssh nor scp restricts the offer", func(t *testing.T) {
		h := New(Config{Host: "h", User: "u", IdentityPath: "/k"})
		for _, argv := range [][]string{
			h.SSHArgv(false, "true"),
			h.SCPArgv(false, "/tmp/a", "u@h:/tmp/a"),
		} {
			if hasToken(argv, opt) {
				t.Errorf("argv %v must not restrict key selection on a persistent host", argv)
			}
		}
	})

	t.Run("no identity: the option is not emitted on its own", func(t *testing.T) {
		// Without an -i there is nothing to restrict ssh to, so the option would
		// only narrow ssh to its built-in default key names — a behaviour change
		// with nothing to gain.
		h := New(Config{Host: "h", User: "u", IdentitiesOnly: true})
		for _, argv := range [][]string{
			h.SSHArgv(false, "true"),
			h.SCPArgv(false, "/tmp/a", "u@h:/tmp/a"),
		} {
			if hasToken(argv, opt) {
				t.Errorf("argv %v carries %q with no -i to restrict ssh to", argv, opt)
			}
		}
	})
}

// TestSSHForwardArgv proves ForwardArgv builds `ssh <flags> -o
// ExitOnForwardFailure=yes -N -L 127.0.0.1:<hostPort>:127.0.0.1:<guestPort>
// target`: -N because the caller execs this as a long-running child purely to
// forward the port, never to run a remote command, and -L binds the far end to
// the REMOTE host's OWN loopback (127.0.0.1) so the guest port stays
// unreachable from anything else on that host's network.
//
// BOTH halves of the -L spec are explicit. The listen address is named rather
// than left implicit so ssh performs exactly ONE bind — a bare port asks it to
// bind every loopback address, and a dual-stack host where ::1 succeeds and
// 127.0.0.1 collides leaves ssh running with ExitOnForwardFailure never firing
// while the caller's IPv4 probe reaches an unrelated service. Port and identity
// thread through exactly as every other ssh argv this package builds.
func TestSSHForwardArgv(t *testing.T) {
	h := New(Config{Host: "h", User: "u", Port: 2222, IdentityPath: "/k"})
	got := h.ForwardArgv(8080, 3000)

	wantPrefix := []string{"ssh", "-p", "2222", "-i", "/k"}
	if len(got) < len(wantPrefix) || !slices.Equal(got[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("ForwardArgv prefix = %v\nwant %v", got, wantPrefix)
	}
	wantTail := []string{"-o", "ExitOnForwardFailure=yes", "-N", "-L", "127.0.0.1:8080:127.0.0.1:3000", "u@h"}
	if len(got) < len(wantTail) || !slices.Equal(got[len(got)-len(wantTail):], wantTail) {
		t.Fatalf("ForwardArgv tail = %v\nwant %v", got, wantTail)
	}
}

// TestSSHForwardArgvDoesNotMultiplex proves a port forward never shares (or
// becomes) the ControlMaster: it is long-lived by construction, and killing it
// — the normal way a caller tears a forward down — must not take every other
// multiplexed session down with it (see WithoutMux, which documents the same
// hazard for the long-lived heartbeat and sweep probes).
func TestSSHForwardArgvDoesNotMultiplex(t *testing.T) {
	h := New(testCfg)
	if h.controlDir == "" {
		t.Skip("no control dir resolved in this environment; multiplexing is off entirely")
	}
	got := h.ForwardArgv(8080, 3000)
	if slices.Contains(got, "ControlMaster=auto") {
		t.Fatalf("ForwardArgv must not multiplex: %v", got)
	}
	for _, val := range []string{"ControlMaster=no", "ControlPath=none"} {
		if !slices.Contains(got, val) {
			t.Fatalf("ForwardArgv = %v: want explicit %q", got, val)
		}
	}
}

// TestShellQuote covers the remote-shell quoting: safe tokens pass through, and a
// token with a space/metacharacter (the whole point over ssh) is single-quoted so
// the remote shell does not word-split it.
func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"limactl":       "limactl",
		"--format":      "--format",
		"web:/home/u":   "web:/home/u",
		"a b":           "'a b'",
		"":              "''",
		"{{.Status}}":   "'{{.Status}}'",
		"it's":          `'it'\''s'`,
		"/home/x.guest": "/home/x.guest",
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %q, want %q", in, got, want)
		}
	}
}

// A context marked WithoutMux must produce an argv that neither joins nor
// becomes the shared control master, while an ordinary context keeps
// multiplexing — the whole point being that ONE dying master must not be able to
// take every long-lived session down with it (see WithoutMux).
func TestWithoutMuxOptsOneCommandOutOfTheSharedMaster(t *testing.T) {
	h := New(testCfg)
	if h.controlDir == "" {
		t.Skip("no control dir resolved in this environment; multiplexing is off entirely")
	}

	shared := h.SSHArgvCtx(context.Background(), false, "true")
	if slices.Index(shared, "ControlMaster=auto") <= 0 {
		t.Fatalf("an ordinary command must still multiplex, got %v", shared)
	}

	alone := h.SSHArgvCtx(WithoutMux(context.Background()), false, "true")
	for _, val := range []string{"ControlMaster=no", "ControlPath=none"} {
		idx := slices.Index(alone, val)
		if idx <= 0 || alone[idx-1] != "-o" {
			t.Fatalf("WithoutMux argv = %v: want %q preceded by its own -o", alone, val)
		}
	}
	if slices.Contains(alone, "ControlMaster=auto") {
		t.Fatalf("WithoutMux argv must not also ask to multiplex: %v", alone)
	}
	// ControlPath=none as well as ControlMaster=no: without it ssh still
	// consults (and can block on) an existing socket.
	if i := slices.IndexFunc(alone, func(s string) bool {
		return strings.HasPrefix(s, "ControlPath=") && s != "ControlPath=none"
	}); i >= 0 {
		t.Fatalf("WithoutMux argv must not name a control socket: %v", alone)
	}
	// The keepalives are not negotiable, whichever shape the argv takes.
	for _, val := range []string{"ServerAliveInterval=15", "ServerAliveCountMax=8"} {
		if !slices.Contains(alone, val) {
			t.Fatalf("WithoutMux argv dropped %q: %v", val, alone)
		}
	}
}
