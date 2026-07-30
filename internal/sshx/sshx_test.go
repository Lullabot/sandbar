package sshx

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// These tests are the executable specification of the ssh/scp argv every sand
// backend runs. They are PURE — no test spawns a real ssh or needs a remote host,
// exactly as no test may spawn a real limactl (AGENTS.md) — because argv
// construction is the whole subject: the flags below are what decide whether a
// connection is established at all, whether it hangs forever on a dead channel,
// and whether it prompts on a terminal a TUI owns.
//
// They live here rather than in a backend's package because both backends build
// their argvs from this one place; a flag lost here is lost for the Lima hop and
// for the Proxmox guest transport at the same time.

var testCfg = Config{Host: "example.com", User: "dev"}

func hasToken(argv []string, tok string) bool { return slices.Contains(argv, tok) }

// hasOptPair reports whether argv contains the contiguous `-o <opt>` pair, so a
// stray "-o" or a lookalike value elsewhere cannot pass an assertion.
func hasOptPair(argv []string, opt string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-o" && argv[i+1] == opt {
			return true
		}
	}
	return false
}

// TestControlDirAndMuxFlags proves New resolves a per-user control dir (0o700)
// for OpenSSH connection multiplexing, and that both the ssh and the scp argv
// thread ControlMaster=auto / ControlPath=<dir>/%C / ControlPersist=600 in before
// the target — the fix for a user with an SSH-agent prompt (1Password etc.) being
// re-prompted on every 5s board refresh, per-VM file read, heartbeat restart, and
// the final batched refresh at quit: every one of those commands now shares one
// already-authenticated master connection instead of paying a fresh handshake.
func TestControlDirAndMuxFlags(t *testing.T) {
	c := New(testCfg)
	if c.controlDir == "" {
		t.Fatalf("New left controlDir empty in a normal environment")
	}
	info, err := os.Stat(c.controlDir)
	if err != nil {
		t.Fatalf("controlDir was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("controlDir perm = %o, want 0700", perm)
	}

	wantControlPath := "ControlPath=" + filepath.Join(c.controlDir, "%C")
	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"Argv", c.Argv(false)},
		{"SCPArgv", c.SCPArgv(false, "/local", "remote:/path")},
	} {
		for _, val := range []string{"ControlMaster=auto", wantControlPath, "ControlPersist=600"} {
			if !hasOptPair(tc.argv, val) {
				t.Fatalf("%s = %v: want %q preceded by its own -o", tc.name, tc.argv, val)
			}
		}
	}
}

// TestKeepalivesAlwaysThreaded pins the liveness options onto EVERY ssh and scp
// this package builds, whatever the connection's shape.
//
// The regression it guards is not a wrong argv, it is a hang: a provisioning run
// streams one ssh session for a whole playbook, and a task that goes minutes
// without writing to the channel (a repo clone, an apt install) is
// indistinguishable from a session a firewall silently reaped — OpenSSH's own
// defaults notice neither for two hours. Dropping these options anywhere, for
// any connection, turns a reported failure back into an unbounded wait with no
// output to explain it.
func TestKeepalivesAlwaysThreaded(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"plain", testCfg},
		{"port and identity", Config{Host: "h", User: "u", Port: 2222, IdentityPath: "/k"}},
		{"ephemeral guest", Config{Host: "10.0.0.9", User: "dev", EphemeralHostKeys: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New(tc.cfg)
			for _, cmd := range []struct {
				name string
				argv []string
			}{
				{"Argv", c.Argv(false)},
				{"Argv tty", c.Argv(true)},
				{"Argv unmuxed", c.ArgvCtx(WithoutMux(context.Background()), false)},
				{"SCPArgv", c.SCPArgv(true, "/local", "remote:/path")},
			} {
				for _, val := range []string{"ServerAliveInterval=15", "ServerAliveCountMax=8"} {
					if !hasOptPair(cmd.argv, val) {
						t.Fatalf("%s = %v: want %q preceded by its own -o", cmd.name, cmd.argv, val)
					}
				}
			}
		})
	}
}

// TestDebugLogOptIn proves the transport log is opt-in, lands in a file rather
// than on stderr, and is per-target.
//
// The -E is the load-bearing half. Every guest-command caller in the Proxmox
// provider merges ssh's stderr into the stream carrying the guest's own output,
// which is also the stream the TUI's progress parser reads TASK banners from —
// so a -vv that logged to stderr would corrupt the display of the very run being
// diagnosed, and the tool would be unusable for the failures it exists to catch.
func TestDebugLogOptIn(t *testing.T) {
	t.Run("off by default", func(t *testing.T) {
		t.Setenv("SAND_SSH_DEBUG", "")
		c := New(testCfg)
		if got := c.DebugLogPath(); got != "" {
			t.Fatalf("DebugLogPath = %q with SAND_SSH_DEBUG unset, want empty", got)
		}
		if argv := c.Argv(false); hasToken(argv, "-vv") {
			t.Fatalf("Argv = %v, want no -vv when the debug log is off", argv)
		}
	})

	t.Run("explicit directory", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("SAND_SSH_DEBUG", dir)
		c := New(Config{Host: "10.0.0.9", User: "dev"})

		want := filepath.Join(dir, "dev@10.0.0.9.log")
		if got := c.DebugLogPath(); got != want {
			t.Fatalf("DebugLogPath = %q, want %q", got, want)
		}
		argv := c.Argv(false)
		idx := slices.Index(argv, "-E")
		if idx < 0 || idx+1 >= len(argv) || argv[idx+1] != want {
			t.Fatalf("Argv = %v, want `-E %s`", argv, want)
		}
		if !hasToken(argv, "-vv") {
			t.Fatalf("Argv = %v, want -vv alongside the log file", argv)
		}
		// scp has no -E, so a debug log must never be requested for it: its only
		// outlet would be the payload stream.
		if scp := c.SCPArgv(false, "/local", "remote:/path"); hasToken(scp, "-E") || hasToken(scp, "-vv") {
			t.Fatalf("SCPArgv = %v, want no debug flags (scp has no -E)", scp)
		}
	})

	t.Run("a relative value is just an on switch", func(t *testing.T) {
		// A relative path would resolve against whatever working directory the
		// process happens to have — not a place a user launching a TUI can
		// predict — so it must be read as a plain "on" against the default
		// directory, never as a destination.
		t.Setenv("SAND_SSH_DEBUG", "logs")
		c := New(Config{Host: "10.0.0.9", User: "dev"})
		got := c.DebugLogPath()
		if got == "" {
			t.Fatalf("DebugLogPath = %q, want the default directory", got)
		}
		if filepath.Dir(got) == "logs" {
			t.Fatalf("DebugLogPath = %q resolved a relative value as a destination", got)
		}
	})

	t.Run("0 is off", func(t *testing.T) {
		t.Setenv("SAND_SSH_DEBUG", "0")
		if got := New(testCfg).DebugLogPath(); got != "" {
			t.Fatalf("DebugLogPath = %q with SAND_SSH_DEBUG=0, want empty", got)
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
		c := New(Config{Host: "fd00::1", User: "dev"})
		got := c.DebugLogPath()
		if filepath.Dir(got) != dir {
			t.Fatalf("DebugLogPath = %q, want it inside %q", got, dir)
		}
		if strings.ContainsAny(filepath.Base(got), ":/") {
			t.Fatalf("log filename %q kept a separator-unsafe character", filepath.Base(got))
		}
	})
}

// TestNoControlDirOmitsMuxFlags proves the graceful-degradation path: when
// controlDir could not be resolved (simulated here by constructing the struct
// directly, standing in for os.UserCacheDir/MkdirAll failing in New), neither
// argv carries multiplexing flags — connection multiplexing is a pure
// optimization and must NEVER become a hard requirement for reaching the far
// side.
//
// The keepalive options stay, and that asymmetry is the point: multiplexing is
// an optimization that can be dropped, while a connection with no liveness check
// can hang forever instead of failing (see keepaliveFlags). One degrades, the
// other does not.
func TestNoControlDirOmitsMuxFlags(t *testing.T) {
	c := &Conn{cfg: testCfg} // controlDir and debugLogPath at their zero values
	want := append(append([]string{"ssh"}, keepaliveFlags()...), "dev@example.com")
	if got := c.Argv(false); !slices.Equal(got, want) {
		t.Fatalf("Argv with empty controlDir = %v, want %v", got, want)
	}
	wantScp := append(append([]string{"scp"}, keepaliveFlags()...), "/local", "remote:/path")
	if got := c.SCPArgv(false, "/local", "remote:/path"); !slices.Equal(got, wantScp) {
		t.Fatalf("SCPArgv with empty controlDir = %v, want %v", got, wantScp)
	}
	for _, tok := range []string{"-vv", "-E"} {
		if hasToken(c.Argv(false), tok) {
			t.Fatalf("Argv carried %q with no SAND_SSH_DEBUG set: %v", tok, c.Argv(false))
		}
	}
}

// TestArgvShape pins the whole prefix, in order, for the shapes callers rely on:
// the bare common case, a tty attach, and a connection with a non-default port
// and an identity file. It is the proof that a remote command's tokens come
// AFTER the target and that nothing is inserted between them.
func TestArgvShape(t *testing.T) {
	t.Run("port and identity threaded before the target", func(t *testing.T) {
		c := &Conn{cfg: Config{Host: "h", User: "u", Port: 2222, IdentityPath: "/k"}}
		want := []string{"ssh", "-p", "2222", "-i", "/k",
			"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=8",
			"u@h", "limactl", "list"}
		if got := c.Argv(false, "limactl", "list"); !slices.Equal(got, want) {
			t.Fatalf("Argv = %v\nwant %v", got, want)
		}
	})
	t.Run("tty adds -t first", func(t *testing.T) {
		c := &Conn{cfg: testCfg}
		got := c.Argv(true, "true")
		if len(got) < 2 || got[0] != "ssh" || got[1] != "-t" {
			t.Fatalf("Argv(tty) = %v, want it to start `ssh -t`", got)
		}
	})
	t.Run("default port 22 omits -p", func(t *testing.T) {
		c := New(Config{Host: "h", User: "u", Port: 22})
		if got := c.Argv(false); hasToken(got, "-p") {
			t.Fatalf("port 22 should omit -p, got %v", got)
		}
		if got := c.SCPArgv(false, "/a", "h:/a"); hasToken(got, "-P") {
			t.Fatalf("port 22 should omit -P, got %v", got)
		}
	})
	t.Run("no user omits user@", func(t *testing.T) {
		c := New(Config{Host: "h"})
		if got := c.Target(); got != "h" {
			t.Fatalf("Target = %q, want %q", got, "h")
		}
	})
	t.Run("scp uses capital -P for the port", func(t *testing.T) {
		// scp's port flag is -P, not ssh's -p; getting it wrong silently ignores a
		// non-default port and connects to 22 instead.
		c := &Conn{cfg: Config{Host: "h", User: "u", Port: 2222}}
		want := []string{"scp", "-r", "-P", "2222",
			"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=8",
			"/local", "u@h:/remote"}
		if got := c.SCPArgv(true, "/local", "u@h:/remote"); !slices.Equal(got, want) {
			t.Fatalf("SCPArgv = %v\nwant %v", got, want)
		}
	})
}

// TestEphemeralHostKeys pins the throwaway-guest host-key contract
// (Config.EphemeralHostKeys): when set, EVERY ssh and scp argv must carry
// `-o StrictHostKeyChecking=no`, `-o UserKnownHostsFile=/dev/null`, and
// `-o LogLevel=ERROR` — the flags that stop the Proxmox provider's first
// provisioning ssh from blocking on OpenSSH's interactive host-key prompt (a
// hang the TUI cannot answer) and from hard-failing when a rebuilt VM reuses a
// prior VM's IP with a new key. When UNSET (the remote-Lima default), none of
// those options may appear — that hop pins host keys on first use.
func TestEphemeralHostKeys(t *testing.T) {
	opts := []string{"StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null", "LogLevel=ERROR"}

	t.Run("set: ssh and scp carry every option", func(t *testing.T) {
		c := New(Config{Host: "10.0.0.9", User: "dev", EphemeralHostKeys: true})
		for _, argv := range [][]string{
			c.Argv(false, "true"),
			c.SCPArgv(false, "/tmp/a", "dev@10.0.0.9:/tmp/a"),
		} {
			for _, opt := range opts {
				if !hasOptPair(argv, opt) {
					t.Errorf("argv %v missing `-o %s`", argv, opt)
				}
			}
		}
	})

	t.Run("unset: neither ssh nor scp mentions host-key options", func(t *testing.T) {
		c := New(Config{Host: "10.0.0.9", User: "dev"})
		for _, argv := range [][]string{
			c.Argv(false, "true"),
			c.SCPArgv(false, "/tmp/a", "dev@10.0.0.9:/tmp/a"),
		} {
			for _, opt := range opts {
				if hasToken(argv, opt) {
					t.Errorf("argv %v must not weaken host-key checking for a persistent host (found %q)", argv, opt)
				}
			}
		}
	})
}

// TestIdentitiesOnly pins the single-key-offer contract (Config.IdentitiesOnly):
// when set, EVERY ssh and scp argv must carry `-o IdentitiesOnly=yes` alongside
// its `-i`, so ssh offers only the configured key and never the ones the agent or
// ssh_config would volunteer.
//
// The regression it guards is an authentication failure on a correctly-built
// guest. A Proxmox guest trusts exactly one key, so every extra offer is refused
// and counts against the guest sshd's MaxAuthTries (6): a locked agent key turns
// a clean "Permission denied" into a "Too many authentication failures"
// disconnect, and an agent holding six or more keys exhausts the budget before
// ssh ever offers the right one. When UNSET (the remote-Lima default) the option
// must be absent — that hop authenticates on the user's own terms, where an agent
// key or an ssh_config IdentityFile is a legitimate way in.
func TestIdentitiesOnly(t *testing.T) {
	const opt = "IdentitiesOnly=yes"

	t.Run("set: ssh and scp offer only the configured key", func(t *testing.T) {
		c := New(Config{Host: "10.0.0.9", User: "dev", IdentityPath: "/k", IdentitiesOnly: true})
		for _, argv := range [][]string{
			c.Argv(false, "true"),
			c.SCPArgv(false, "/tmp/a", "dev@10.0.0.9:/tmp/a"),
		} {
			if !hasOptPair(argv, opt) {
				t.Errorf("argv %v: want %q preceded by its own -o", argv, opt)
			}
			// The restriction is meaningless without the key it restricts ssh TO.
			if i := slices.Index(argv, "-i"); i < 0 || i+1 >= len(argv) || argv[i+1] != "/k" {
				t.Errorf("argv %v: want `-i /k` alongside %q", argv, opt)
			}
		}
	})

	t.Run("unset: neither ssh nor scp restricts the offer", func(t *testing.T) {
		c := New(Config{Host: "h", User: "u", IdentityPath: "/k"})
		for _, argv := range [][]string{
			c.Argv(false, "true"),
			c.SCPArgv(false, "/tmp/a", "u@h:/tmp/a"),
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
		c := New(Config{Host: "h", User: "u", IdentitiesOnly: true})
		for _, argv := range [][]string{
			c.Argv(false, "true"),
			c.SCPArgv(false, "/tmp/a", "u@h:/tmp/a"),
		} {
			if hasToken(argv, opt) {
				t.Errorf("argv %v carries %q with no -i to restrict ssh to", argv, opt)
			}
		}
	})
}

// TestQuote covers the remote-shell quoting: safe tokens pass through, and a
// token with a space/metacharacter (the whole point over ssh) is single-quoted so
// the remote shell does not word-split it.
func TestQuote(t *testing.T) {
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

// TestArgvQuotesEveryRemoteToken proves the quoting is applied by Argv itself, so
// no caller can forget it — the failure it prevents is a remote shell
// word-splitting a script or an expression into command-not-found.
func TestArgvQuotesEveryRemoteToken(t *testing.T) {
	c := &Conn{cfg: testCfg}
	got := c.Argv(false, "bash", "-c", "echo hi; ls")
	want := []string{"ssh",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=8",
		"dev@example.com", "bash", "-c", "'echo hi; ls'"}
	if !slices.Equal(got, want) {
		t.Fatalf("Argv = %v\nwant %v", got, want)
	}
}

// TestWithoutMuxOptsOneCommandOutOfTheSharedMaster proves a long-lived caller
// gets its own connection.
//
// The failure it guards is a burst outage, not a slow command: with
// ControlMaster=auto the master is whichever connection got there first, and when
// it dies every session multiplexed through it dies in the same instant. The
// board's heartbeat and sweep connect early and last longest, so they are the
// likeliest masters and the costliest to lose.
func TestWithoutMuxOptsOneCommandOutOfTheSharedMaster(t *testing.T) {
	c := New(testCfg)
	if c.controlDir == "" {
		t.Skip("no control dir resolved in this environment; multiplexing is off entirely")
	}

	shared := c.ArgvCtx(context.Background(), false, "true")
	if !hasOptPair(shared, "ControlMaster=auto") {
		t.Fatalf("an ordinary command must still multiplex, got %v", shared)
	}

	alone := c.ArgvCtx(WithoutMux(context.Background()), false, "true")
	for _, val := range []string{"ControlMaster=no", "ControlPath=none"} {
		if !hasOptPair(alone, val) {
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

// TestMuxSuppressed covers the marker's own contract, including the nil context a
// caller can reach this with (MuxSuppressed is consulted on every ctx-carrying
// argv build, so it must not panic on one).
func TestMuxSuppressed(t *testing.T) {
	//nolint:staticcheck // a nil context is exactly what this asserts is tolerated
	if MuxSuppressed(nil) {
		t.Error("MuxSuppressed(nil) = true, want false")
	}
	if MuxSuppressed(context.Background()) {
		t.Error("MuxSuppressed(background) = true, want false")
	}
	if !MuxSuppressed(WithoutMux(context.Background())) {
		t.Error("MuxSuppressed(WithoutMux(...)) = false, want true")
	}
	// The marker survives a derived context: a caller marks it once and then
	// wraps it in a timeout or a cancel before handing it to the transport.
	ctx, cancel := context.WithCancel(WithoutMux(context.Background()))
	defer cancel()
	if !MuxSuppressed(ctx) {
		t.Error("the opt-out did not survive context.WithCancel")
	}
}

// TestConfigAccessors pins the two facts a caller reads back off a connection:
// the target it will dial, and the login user to fall back on when the far side
// cannot be asked.
func TestConfigAccessors(t *testing.T) {
	c := New(testCfg)
	if got := c.Target(); got != "dev@example.com" {
		t.Errorf("Target = %q, want dev@example.com", got)
	}
	if got := c.User(); got != "dev" {
		t.Errorf("User = %q, want dev", got)
	}
	if got := c.Config(); got != testCfg {
		t.Errorf("Config = %+v, want %+v", got, testCfg)
	}
}
