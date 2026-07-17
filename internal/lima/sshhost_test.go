package lima

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// These tests are the executable specification of the SSH host-access
// implementation. They assert on the ARGV the ssh command-runner builds (and the
// stdin/stdout wiring) over a FAKE exec seam — no test spawns a real ssh or needs
// a remote host, exactly as no test may spawn a real limactl (AGENTS.md). The
// genuine loopback end-to-end is the limae2e-gated remote e2e test
// (internal/provider/remote_e2e_test.go).

// recordingExec is the fake newCmd seam: it records every ssh/scp argv the
// SSHHost builds (the assertion target) and returns a stand-in *exec.Cmd — by
// default a no-op — so the SSH argv is proven without a real ssh binary. A test
// can install a stub to simulate remote behaviour (emit canned stdout, echo
// stdin, block holding a lock, fail with a missing-file message).
type recordingExec struct {
	calls [][]string
	stub  func(ctx context.Context, argv []string) *exec.Cmd
}

func (r *recordingExec) newCmd(ctx context.Context, argv []string) *exec.Cmd {
	r.calls = append(r.calls, append([]string(nil), argv...))
	if r.stub != nil {
		return r.stub(ctx, argv)
	}
	return exec.CommandContext(ctx, "true")
}

// hostWith builds an SSHHost over the recording seam for cfg.
func hostWith(cfg SSHConfig, rec *recordingExec) *SSHHost {
	h := NewSSHHost(cfg)
	h.newCmd = rec.newCmd
	return h
}

// sh returns a stand-in command running a shell snippet, for stubs that must
// simulate the remote side (canned output, stdin echo, a failure).
func sh(ctx context.Context, script string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", script)
}

func hasToken(argv []string, tok string) bool { return slices.Contains(argv, tok) }

// anyContains reports whether any argv token contains sub — for matching a token
// that is a full path or a quoted shell script rather than a bare word.
func anyContains(argv []string, sub string) bool {
	for _, a := range argv {
		if strings.Contains(a, sub) {
			return true
		}
	}
	return false
}

var testCfg = SSHConfig{Host: "example.com", User: "dev"}

// muxFlags returns the ssh/scp connection-multiplexing flags a pinned test
// should expect from h — the exact three -o tokens sshBase/scpCommand thread
// in when h.controlDir was resolved at construction, or nil when it was not
// (the graceful-degradation path). Reading h.controlDir directly (this file is
// in package lima) means no pinned test hardcodes a real cache-dir path; each
// only pins ssh's own argv SHAPE around whatever NewSSHHost actually resolved.
func muxFlags(h *SSHHost) []string {
	if h.controlDir == "" {
		return nil
	}
	return []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(h.controlDir, "%C"),
		"-o", "ControlPersist=600",
	}
}

// sshArgv builds the expected `ssh <preTarget...> <mux-flags> <target>
// <tail...>` argv for h, factoring the multiplexing-flag splice (always
// immediately before the target) so no pinned test hand-repeats it.
func sshArgv(h *SSHHost, preTarget []string, target string, tail ...string) []string {
	argv := []string{"ssh"}
	argv = append(argv, preTarget...)
	argv = append(argv, muxFlags(h)...)
	argv = append(argv, target)
	argv = append(argv, tail...)
	return argv
}

// TestSSHControlDirAndMuxFlags proves NewSSHHost resolves a per-user control
// dir (0o700) for OpenSSH connection multiplexing, and that both sshBase and
// scpCommand thread ControlMaster=auto / ControlPath=<dir>/%C /
// ControlPersist=600 in before the target — the fix for a user with an
// SSH-agent prompt (1Password etc.) being re-prompted on every 5s board
// refresh, per-VM file read, heartbeat restart, and the final batched refresh
// at quit: every one of those commands now shares one already-authenticated
// master connection instead of paying a fresh handshake.
func TestSSHControlDirAndMuxFlags(t *testing.T) {
	h := NewSSHHost(testCfg)
	if h.controlDir == "" {
		t.Fatalf("NewSSHHost left controlDir empty in a normal environment")
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
		{"sshBase", h.sshBase(false)},
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

// TestSSHNoControlDirOmitsMuxFlags proves the graceful-degradation path: when
// controlDir could not be resolved (simulated here by constructing the struct
// directly, standing in for os.UserCacheDir/MkdirAll failing in NewSSHHost),
// sshBase/scpCommand argv is EXACTLY the pre-multiplexing shape — connection
// multiplexing is a pure optimization and must NEVER become a hard
// requirement for reaching the remote host.
func TestSSHNoControlDirOmitsMuxFlags(t *testing.T) {
	h := &SSHHost{cfg: testCfg, newCmd: func(ctx context.Context, argv []string) *exec.Cmd {
		return exec.CommandContext(ctx, argv[0], argv[1:]...)
	}}
	// controlDir left at its zero value "" — the failure path.
	want := []string{"ssh", "dev@example.com"}
	if got := h.sshBase(false); !slices.Equal(got, want) {
		t.Fatalf("sshBase with empty controlDir = %v, want %v", got, want)
	}
	wantScp := []string{"scp", "/local", "remote:/path"}
	if got := h.scpCommand(false, "/local", "remote:/path"); !slices.Equal(got, wantScp) {
		t.Fatalf("scpCommand with empty controlDir = %v, want %v", got, wantScp)
	}
}

// TestSSHRunnerArgv pins the exact ssh argv the runner builds for representative
// limactl operations: List, a Shell exec, and a stdout-only exec. This is the
// core proof that the remote runner drives the SAME limactl the local one does,
// just wrapped in `ssh user@host …` — no limactl argv is rebuilt here (it comes
// straight from Client), only the transport prefix is added.
func TestSSHRunnerArgv(t *testing.T) {
	cases := []struct {
		name string
		call func(*Client)
		tail []string
	}{
		{"list", func(c *Client) { _, _ = c.List() },
			[]string{"LIMA_HOME=.lima", "limactl", "list", "--format", "json"}},
		{"shell-exec", func(c *Client) { _ = c.Shell(context.Background(), "web", nil, io.Discard, "ls", "-la") },
			[]string{"LIMA_HOME=.lima", "limactl", "shell", "web", "ls", "-la"}},
		{"shell-stream-out", func(c *Client) { _ = c.ShellStreamOut(context.Background(), "web", nil, io.Discard, "tar", "-c") },
			[]string{"LIMA_HOME=.lima", "limactl", "shell", "web", "tar", "-c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingExec{}
			h := hostWith(testCfg, rec)
			c := New(h)
			tc.call(c)
			if len(rec.calls) != 1 {
				t.Fatalf("got %d calls, want 1: %v", len(rec.calls), rec.calls)
			}
			want := sshArgv(h, nil, "dev@example.com", tc.tail...)
			if got := rec.calls[0]; !reflect.DeepEqual(got, want) {
				t.Fatalf("argv = %v\nwant %v", got, want)
			}
		})
	}
}

// TestSSHPortAndIdentityThreading proves the port and identity flags are threaded
// onto the ssh argv when set — and OMITTED when unset (or the default port 22), so
// the common case is the bare `ssh user@host …` the test above pins.
func TestSSHPortAndIdentityThreading(t *testing.T) {
	t.Run("port and identity set", func(t *testing.T) {
		rec := &recordingExec{}
		h := hostWith(SSHConfig{Host: "h", User: "u", Port: 2222, IdentityPath: "/k"}, rec)
		c := New(h)
		_, _ = c.List()
		want := sshArgv(h, []string{"-p", "2222", "-i", "/k"}, "u@h", "LIMA_HOME=.lima", "limactl", "list", "--format", "json")
		if got := rec.calls[0]; !reflect.DeepEqual(got, want) {
			t.Fatalf("argv = %v\nwant %v", got, want)
		}
	})
	t.Run("default port 22 omits -p", func(t *testing.T) {
		rec := &recordingExec{}
		c := New(hostWith(SSHConfig{Host: "h", User: "u", Port: 22}, rec))
		_, _ = c.List()
		if got := rec.calls[0]; hasToken(got, "-p") {
			t.Fatalf("port 22 should omit -p, got %v", got)
		}
	})
	t.Run("no user omits user@", func(t *testing.T) {
		rec := &recordingExec{}
		h := hostWith(SSHConfig{Host: "h"}, rec)
		c := New(h)
		_, _ = c.List()
		want := sshArgv(h, nil, "h", "LIMA_HOME=.lima", "limactl", "list", "--format", "json")
		if got := rec.calls[0]; !reflect.DeepEqual(got, want) {
			t.Fatalf("argv = %v\nwant %v", got, want)
		}
	})
}

// TestSSHStdinReachesRemoteLimactl is the secret-hygiene proof for the hop: the
// provision vars must arrive over STDIN, never argv, so a finalize token never
// lands in the remote process listing. The stub echoes stdin (cat), so seeing the
// vars come back out proves stdin was piped to the remote limactl; and the vars
// must NOT appear anywhere in the recorded argv.
func TestSSHStdinReachesRemoteLimactl(t *testing.T) {
	const secret = "finalize_token: super-secret-value\n"
	rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		return sh(ctx, "cat") // echo stdin to stdout so we can observe it arrived
	}}
	c := New(hostWith(testCfg, rec))

	var out bytes.Buffer
	// Mirror provision.runProvision: vars over stdin, a constant script on argv.
	if err := c.Shell(context.Background(), "web", strings.NewReader(secret), &out,
		"sudo", "bash", "-c", "echo provisioning"); err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if out.String() != secret {
		t.Fatalf("stdin did not reach the remote command: out = %q, want %q", out.String(), secret)
	}
	for _, a := range rec.calls[0] {
		if strings.Contains(a, "super-secret-value") {
			t.Fatalf("the secret leaked onto argv: %v", rec.calls[0])
		}
	}
}

// TestSSHOutputSeparatesStdoutAndStderr proves Output keeps the remote limactl's
// stdout and stderr apart — the property that lets Client.List parse JSON without
// a logrus stderr line corrupting it, and that lets the clone/delete race sentinel
// still match the remote stderr folded into the error.
func TestSSHOutputSeparatesStdoutAndStderr(t *testing.T) {
	rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		return sh(ctx, `printf '{"name":"web"}'; printf 'time=... level=info msg=noise\n' >&2; exit 0`)
	}}
	h := hostWith(testCfg, rec)
	out, err := h.Output(context.Background(), "list", "--format", "json")
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if got := string(out); got != `{"name":"web"}` {
		t.Fatalf("stdout = %q; stderr must never leak into it", got)
	}
}

// TestSSHListRaceSentinelStillFires: the remote limactl fails the SAME way local
// limactl does while an instance is mid-clone (lima#5236). Its stderr is folded
// into the error by Output, so ErrListRacedInstanceDir must still recognise it —
// the workaround is transport-independent by construction.
func TestSSHListRaceSentinelStillFires(t *testing.T) {
	rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		return sh(ctx, `echo "fatal: unable to load instance web: open lima.yaml: no such file" >&2; exit 1`)
	}}
	c := New(hostWith(testCfg, rec))
	_, err := c.List()
	if !errors.Is(err, ErrListRacedInstanceDir) {
		t.Fatalf("List error = %v, want ErrListRacedInstanceDir over the ssh hop", err)
	}
}

// TestSSHStreamMergesStderr / TestSSHStreamReapsOrphan mirror the execRunner
// contract for the remote runner: Stream merges stderr for live display, and a
// cancel REAPS the whole ssh->limactl->guest-ssh orphan chain via WaitDelay.
func TestSSHStreamMergesStderr(t *testing.T) {
	rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		return sh(ctx, `printf out; printf err >&2`)
	}}
	h := hostWith(testCfg, rec)
	var out bytes.Buffer
	if err := h.Stream(context.Background(), nil, &out, "start", "web"); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Fatalf("Stream out = %q, want both stdout and stderr merged", got)
	}
}

func TestSSHStreamReapsOrphan(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*SSHHost, context.Context, io.Writer) error
	}{
		{"Stream", func(h *SSHHost, ctx context.Context, out io.Writer) error {
			return h.Stream(ctx, nil, out, "shell", "web")
		}},
		{"StreamOut", func(h *SSHHost, ctx context.Context, out io.Writer) error {
			return h.StreamOut(ctx, nil, out, "shell", "web")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The stub stands in for the whole ssh chain: a killed parent leaving an
			// orphaned grandchild holding the inherited pipe — exactly the shape
			// WaitDelay exists to reap.
			rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
				return sh(ctx, orphanScript)
			}}
			h := hostWith(testCfg, rec)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan error, 1)
			go func() { done <- tc.run(h, ctx, io.Discard) }()
			time.Sleep(300 * time.Millisecond)
			cancel()

			select {
			case <-done:
			case <-time.After(waitDelay + 5*time.Second):
				t.Fatalf("%s did not return within %v of cancel: WaitDelay is not reaping the orphaned ssh chain", tc.name, waitDelay)
			}
		})
	}
}

// TestSSHAttachArgvPreservesGuestExpr is DoD item 7: the remote attach argv is
// `ssh -t <host> limactl shell --workdir H NAME bash -c <expr>`, with the guest
// tmux expression byte-for-byte identical to the local form and --workdir before
// the instance name. Reversing/altering either is silently fatal (see attach.go).
func TestSSHAttachArgvPreservesGuestExpr(t *testing.T) {
	h := NewSSHHost(SSHConfig{Host: "example.com", User: "dev"})
	got := h.AttachArgv("web", "/home/debian.guest", "")

	// ssh -t <mux flags> <target> prefix.
	prefix := sshArgv(h, []string{"-t"}, "dev@example.com")
	if len(got) < len(prefix) || !slices.Equal(got[:len(prefix)], prefix) {
		t.Fatalf("remote attach must start `%v`, got %v", prefix, got)
	}

	// --workdir must PRECEDE the instance name (limactl forwards a trailing
	// --workdir to the guest bash, which dies — see attach.go).
	flag := slices.Index(got, "--workdir")
	name := slices.Index(got, "web")
	if flag < 0 || name < 0 {
		t.Fatalf("attach argv missing --workdir or the instance name: %v", got)
	}
	if flag > name {
		t.Fatalf("--workdir (argv[%d]) comes AFTER the instance name (argv[%d]): %v", flag, name, got)
	}

	// The guest expression survives BYTE-FOR-BYTE (only shell-quoted for the remote
	// shell), and destroy-unattached still never touches `main`.
	last := got[len(got)-1]
	if !strings.Contains(last, guestAttachExpr("")) {
		t.Fatalf("the guest tmux expression was not preserved byte-for-byte in the remote attach argv.\nlast argv element:\n\t%s\nwant it to contain:\n\t%s", last, guestAttachExpr(""))
	}

	// The remote argv's tail (after the `ssh -t <mux flags> target` prefix) must
	// be exactly the local attach argv, shell-quoted element by element — proof
	// the ONLY change is the transport prefix, and nothing about the
	// limactl/guest command drifted.
	local := AttachArgv("web", "/home/debian.guest", "")
	wantTail := make([]string, len(local))
	for i, a := range local {
		wantTail[i] = shellQuote(a)
	}
	if tail := got[len(prefix):]; !slices.Equal(tail, wantTail) {
		t.Fatalf("remote attach tail = %v\nwant the local attach argv quoted: %v", tail, wantTail)
	}
}

// TestSSHAttachArgvThreadsPortIdentity: the attach argv threads -p/-i just like
// the runner does, after the -t.
func TestSSHAttachArgvThreadsPortIdentity(t *testing.T) {
	h := NewSSHHost(SSHConfig{Host: "h", User: "u", Port: 2222, IdentityPath: "/k"})
	got := h.AttachArgv("web", "", "")
	wantPrefix := sshArgv(h, []string{"-t", "-p", "2222", "-i", "/k"}, "u@h", "limactl", "shell", "web")
	if !slices.Equal(got[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("attach prefix = %v\nwant %v", got[:len(wantPrefix)], wantPrefix)
	}
}

// TestSSHTwoStageUpload proves a host->guest copy is resolved as: stage the LOCAL
// source to a remote temp via scp, then run `limactl copy --backend=scp` ON THE
// REMOTE host from that temp into the guest, preserving the source basename so the
// guest-end placement matches the local single-stage copy. The topology lives in
// ONE place (copyAcrossHop), reached through Client.Copy, so every Copy caller
// inherits it.
func TestSSHTwoStageUpload(t *testing.T) {
	const tmp = "/tmp/sand-copy-abc"
	rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		if hasToken(argv, "mktemp") {
			return sh(ctx, "printf %s "+tmp) // emit the staging dir path
		}
		return exec.CommandContext(ctx, "true")
	}}
	h := hostWith(testCfg, rec)
	c := New(h)

	if err := c.Copy(context.Background(), io.Discard, false, "/host/file.txt", GuestPath("web", "/guest/dir")); err != nil {
		t.Fatalf("Copy upload: %v", err)
	}

	// 1) mktemp, 2) scp local -> remote temp, 3) limactl copy on remote, 4) rm -rf.
	scpCall := findCall(t, rec.calls, "scp")
	wantScp := []string{"scp", "dev@example.com:" + tmp}
	if scpCall[0] != "scp" || !hasToken(scpCall, "/host/file.txt") || !hasToken(scpCall, wantScp[1]) {
		t.Fatalf("stage-to-remote scp = %v, want it to scp /host/file.txt to %q", scpCall, wantScp[1])
	}

	copyCall := findLimactlCopy(t, rec.calls)
	// The limactl copy must run over ssh, pin --backend=scp, and take the STAGED
	// remote path (temp/basename) as its host endpoint, and web:/guest/dir as guest.
	wantCopy := sshArgv(h, nil, "dev@example.com", "LIMA_HOME=.lima", "limactl", "copy", "-v", "--backend=scp", tmp+"/file.txt", "web:/guest/dir")
	if !reflect.DeepEqual(copyCall, wantCopy) {
		t.Fatalf("remote limactl copy = %v\nwant %v", copyCall, wantCopy)
	}

	if findCall(t, rec.calls, "rm") == nil {
		t.Fatalf("remote temp dir was never cleaned up: %v", rec.calls)
	}
}

// TestSSHTwoStageDownload proves the reverse: `limactl copy` runs on the remote
// into a temp, then scp brings it back to the local destination.
func TestSSHTwoStageDownload(t *testing.T) {
	const tmp = "/tmp/sand-copy-xyz"
	rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		if hasToken(argv, "mktemp") {
			return sh(ctx, "printf %s "+tmp)
		}
		return exec.CommandContext(ctx, "true")
	}}
	h := hostWith(testCfg, rec)
	c := New(h)

	if err := c.Copy(context.Background(), io.Discard, true, GuestPath("web", "/guest/src"), "/host/dst"); err != nil {
		t.Fatalf("Copy download: %v", err)
	}

	copyCall := findLimactlCopy(t, rec.calls)
	wantCopy := sshArgv(h, nil, "dev@example.com", "LIMA_HOME=.lima", "limactl", "copy", "-v", "--backend=scp", "-r", "web:/guest/src", tmp)
	if !reflect.DeepEqual(copyCall, wantCopy) {
		t.Fatalf("remote limactl copy (download) = %v\nwant %v", copyCall, wantCopy)
	}

	scpCall := findCall(t, rec.calls, "scp")
	if !hasToken(scpCall, "dev@example.com:"+tmp+"/src") || !hasToken(scpCall, "/host/dst") {
		t.Fatalf("retrieve scp = %v, want it to scp %q back to /host/dst", scpCall, tmp+"/src")
	}
}

// findCall returns the first recorded argv whose first token (ssh) is followed by
// a remote command starting with tok, OR whose first token IS tok (scp). Returns
// nil when absent.
func findCall(t *testing.T, calls [][]string, tok string) []string {
	t.Helper()
	for _, c := range calls {
		if len(c) == 0 {
			continue
		}
		if c[0] == tok { // scp
			return c
		}
		if hasToken(c, tok) {
			return c
		}
	}
	return nil
}

// findLimactlCopy returns the recorded `ssh … limactl copy …` argv.
func findLimactlCopy(t *testing.T, calls [][]string) []string {
	t.Helper()
	for _, c := range calls {
		if hasToken(c, "limactl") && hasToken(c, "copy") {
			return c
		}
	}
	t.Fatalf("no `ssh … limactl copy …` call was recorded: %v", calls)
	return nil
}

// TestSplitGuestEndpoint pins the endpoint classification the two-stage copy
// depends on: a `<vm>:<path>` guest endpoint vs a host-local path.
func TestSplitGuestEndpoint(t *testing.T) {
	cases := []struct {
		in       string
		instance string
		path     string
		isGuest  bool
	}{
		{"web:/home/u/dir", "web", "/home/u/dir", true},
		{"/host/local/path", "", "", false},
		{"relative/path", "", "", false},
		{":/leading-colon", "", "", false},
	}
	for _, tc := range cases {
		inst, p, g := splitGuestEndpoint(tc.in)
		if inst != tc.instance || p != tc.path || g != tc.isGuest {
			t.Errorf("splitGuestEndpoint(%q) = (%q,%q,%v), want (%q,%q,%v)", tc.in, inst, p, g, tc.instance, tc.path, tc.isGuest)
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
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- HostFiles over ssh ---------------------------------------------------------

// TestSSHReadFile proves ReadFile cats the remote path and maps a missing file to
// fs.ErrNotExist (so callers tell "absent" from "unreadable", the HostFiles
// contract), while GuestHome/GuestUser read the REMOTE instance files through it.
func TestSSHReadFile(t *testing.T) {
	t.Run("reads remote content", func(t *testing.T) {
		rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
			return sh(ctx, "printf 'hello'")
		}}
		h := hostWith(testCfg, rec)
		b, err := h.ReadFile("/remote/.lima/web/cloud-config.yaml")
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(b) != "hello" {
			t.Fatalf("ReadFile = %q, want %q", b, "hello")
		}
		want := sshArgv(h, nil, "dev@example.com", "cat", "/remote/.lima/web/cloud-config.yaml")
		if !reflect.DeepEqual(rec.calls[0], want) {
			t.Fatalf("ReadFile argv = %v, want %v", rec.calls[0], want)
		}
	})
	t.Run("missing file is fs.ErrNotExist", func(t *testing.T) {
		rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
			return sh(ctx, `echo "cat: x: No such file or directory" >&2; exit 1`)
		}}
		h := hostWith(testCfg, rec)
		_, err := h.ReadFile("/remote/missing")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("ReadFile(missing) err = %v, want fs.ErrNotExist", err)
		}
	})
	t.Run("connection failure is not fs.ErrNotExist", func(t *testing.T) {
		rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
			return sh(ctx, `echo "ssh: connect: Connection refused" >&2; exit 255`)
		}}
		h := hostWith(testCfg, rec)
		_, err := h.ReadFile("/remote/x")
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("a connection failure must be a real error, not fs.ErrNotExist: %v", err)
		}
	})
}

// TestSSHGuestIdentityOverSSH proves GuestHomeVia/GuestUserVia resolve the guest
// home/user off the REMOTE host's instance files (via the SSH HostFiles) — what
// the remote provider's AttachArgv/GuestHome depend on.
func TestSSHGuestIdentityOverSSH(t *testing.T) {
	rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		switch {
		case anyContains(argv, "ssh.config"):
			return sh(ctx, `printf 'Host lima-web\n  User andrew.guest\n'`)
		case anyContains(argv, "cloud-config.yaml"):
			return sh(ctx, `printf '#cloud-config\nusers:\n  - name: "andrew.guest"\n    homedir: "/home/andrew.guest"\n'`)
		}
		return exec.CommandContext(ctx, "true")
	}}
	h := hostWith(testCfg, rec)
	if got := GuestUserVia(h, "/remote/.lima/web"); got != "andrew.guest" {
		t.Fatalf("GuestUserVia = %q, want andrew.guest", got)
	}
	if got := GuestHomeVia(h, "/remote/.lima/web"); got != "/home/andrew.guest" {
		t.Fatalf("GuestHomeVia = %q, want /home/andrew.guest", got)
	}
}

// TestSSHStat covers Stat's parse and its missing-path fs.ErrNotExist mapping.
func TestSSHStat(t *testing.T) {
	rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		return sh(ctx, "printf '4096|1700000000|directory'")
	}}
	h := hostWith(testCfg, rec)
	fi, err := h.Stat("/remote/.lima/web")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !fi.IsDir() || fi.Size() != 4096 || fi.Name() != "web" {
		t.Fatalf("Stat = {dir=%v size=%d name=%q}, want {true 4096 web}", fi.IsDir(), fi.Size(), fi.Name())
	}

	recMissing := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		return sh(ctx, `echo "stat: cannot statx 'x': No such file or directory" >&2; exit 1`)
	}}
	if _, err := hostWith(testCfg, recMissing).Stat("/x"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat(missing) err = %v, want fs.ErrNotExist", err)
	}

	// A macOS remote runs BSD stat, which rejects GNU's `-c` with "illegal
	// option" (NOT a missing-path error); Stat must retry the BSD `-f` form and
	// parse its "Directory" type text, not mis-report the path as absent.
	recBSD := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		if hasToken(argv, "-c") {
			return sh(ctx, `echo "stat: illegal option -- c" >&2; exit 1`)
		}
		return sh(ctx, "printf '8192|1700000000|Directory'")
	}}
	fiBSD, err := hostWith(testCfg, recBSD).Stat("/remote/.lima/web")
	if err != nil {
		t.Fatalf("Stat (BSD fallback): %v", err)
	}
	if !fiBSD.IsDir() || fiBSD.Size() != 8192 {
		t.Fatalf("Stat BSD fallback = {dir=%v size=%d}, want {true 8192}", fiBSD.IsDir(), fiBSD.Size())
	}
}

// TestSSHFileMutations covers WriteFile / MkdirAll / RemoveAll argv and the stdin
// path of WriteFile (content over stdin, never argv).
func TestSSHFileMutations(t *testing.T) {
	t.Run("WriteFile keeps content off argv", func(t *testing.T) {
		// WriteFile pipes the file content over stdin (cat), never argv — the same
		// secret-hygiene the provision-vars path relies on for the version stamp.
		rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
			return sh(ctx, "cat >/dev/null")
		}}
		h := hostWith(testCfg, rec)
		if err := h.WriteFile("/remote/.lima/_sand/base.playbook-version", []byte("v2:hash:none"), 0o755, 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		for _, a := range rec.calls[0] {
			if strings.Contains(a, "v2:hash:none") {
				t.Fatalf("WriteFile put the file content on argv: %v", rec.calls[0])
			}
		}
	})
	t.Run("MkdirAll", func(t *testing.T) {
		rec := &recordingExec{}
		if err := hostWith(testCfg, rec).MkdirAll("/remote/.lima/_sand", 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if !anyContains(rec.calls[0], "mkdir") || !hasToken(rec.calls[0], "/remote/.lima/_sand") {
			t.Fatalf("MkdirAll argv = %v", rec.calls[0])
		}
	})
	t.Run("RemoveAll", func(t *testing.T) {
		rec := &recordingExec{}
		h := hostWith(testCfg, rec)
		if err := h.RemoveAll("/remote/.lima/web"); err != nil {
			t.Fatalf("RemoveAll: %v", err)
		}
		want := sshArgv(h, nil, "dev@example.com", "rm", "-rf", "--", "/remote/.lima/web")
		if !reflect.DeepEqual(rec.calls[0], want) {
			t.Fatalf("RemoveAll argv = %v, want %v", rec.calls[0], want)
		}
	})
}

// TestSSHDiskAllocBytes covers the sparse-size probe and its -1 failure contract.
func TestSSHDiskAllocBytes(t *testing.T) {
	rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		return sh(ctx, "printf '10'") // 10 blocks * 512 = 5120
	}}
	if got := hostWith(testCfg, rec).DiskAllocBytes("/remote/.lima/web/disk"); got != 5120 {
		t.Fatalf("DiskAllocBytes = %d, want 5120", got)
	}
	recFail := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		return sh(ctx, "exit 1")
	}}
	if got := hostWith(testCfg, recFail).DiskAllocBytes("/x"); got != -1 {
		t.Fatalf("DiskAllocBytes(unmeasurable) = %d, want -1", got)
	}

	// A macOS remote (BSD stat) rejects `-c %b`; the probe must retry `-f %b`.
	recBSD := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		if hasToken(argv, "-c") {
			return sh(ctx, "exit 1")
		}
		return sh(ctx, "printf '20'") // 20 blocks * 512 = 10240
	}}
	if got := hostWith(testCfg, recBSD).DiskAllocBytes("/remote/.lima/web/disk"); got != 10240 {
		t.Fatalf("DiskAllocBytes (BSD fallback) = %d, want 10240", got)
	}
}

// TestSSHLimaHome pins the remote LIMA_HOME resolution: the configured value, or
// the relative default that resolves against the remote login home.
func TestSSHLimaHome(t *testing.T) {
	if got := NewSSHHost(SSHConfig{Host: "h", RemoteLimaHome: "/srv/lima"}).LimaHome(); got != "/srv/lima" {
		t.Fatalf("LimaHome(configured) = %q, want /srv/lima", got)
	}
	if got := NewSSHHost(SSHConfig{Host: "h"}).LimaHome(); got != defaultRemoteLimaHome {
		t.Fatalf("LimaHome(default) = %q, want %q", got, defaultRemoteLimaHome)
	}
}

// TestSSHLimaHomeExportedToRemoteLimactl proves the fix for the latent bug where
// RemoteLimaHome was honored for HostFiles reads but never reached the remote
// `limactl` process itself: the remote limactl argv sshCommand builds must carry
// a `LIMA_HOME=<value>` assignment on the REMOTE command (a plain token in the
// shell-quoted remote argv, positioned immediately before "limactl" — never an
// ssh client env/SetEnv), using the SAME value LimaHome() returns for reads, so
// discovery (`limactl list`) and sand's own file reads resolve the same instance
// directory. It also pins the chosen default-case behavior: LIMA_HOME is set
// ALWAYS, even when RemoteLimaHome is unconfigured (the remote default), since
// that is always what sand intends.
func TestSSHLimaHomeExportedToRemoteLimactl(t *testing.T) {
	t.Run("configured RemoteLimaHome reaches the remote limactl", func(t *testing.T) {
		rec := &recordingExec{}
		h := hostWith(SSHConfig{Host: "h", RemoteLimaHome: "/srv/lima"}, rec)
		if _, err := h.Output(context.Background(), "list", "--format", "json"); err != nil {
			t.Fatalf("Output: %v", err)
		}
		argv := rec.calls[0]
		wantEnv, wantBin := "LIMA_HOME=/srv/lima", "limactl"
		envIdx := slices.Index(argv, wantEnv)
		binIdx := slices.Index(argv, wantBin)
		if envIdx < 0 || binIdx < 0 || binIdx != envIdx+1 {
			t.Fatalf("argv = %v, want %q immediately followed by %q", argv, wantEnv, wantBin)
		}
	})

	t.Run("default RemoteLimaHome is still always exported", func(t *testing.T) {
		rec := &recordingExec{}
		h := hostWith(SSHConfig{Host: "h"}, rec)
		if _, err := h.Output(context.Background(), "list", "--format", "json"); err != nil {
			t.Fatalf("Output: %v", err)
		}
		argv := rec.calls[0]
		wantEnv := "LIMA_HOME=" + defaultRemoteLimaHome
		envIdx := slices.Index(argv, wantEnv)
		binIdx := slices.Index(argv, "limactl")
		if envIdx < 0 || binIdx < 0 || binIdx != envIdx+1 {
			t.Fatalf("argv = %v, want %q immediately followed by \"limactl\"", argv, wantEnv)
		}
	})

	t.Run("no local env leaks across the hop", func(t *testing.T) {
		// Set a distinctive local LIMA_HOME/XDG_* to prove they never cross: only the
		// single resolved remote LIMA_HOME token may appear in the remote argv.
		t.Setenv("LIMA_HOME", "/should/never/leak")
		t.Setenv("XDG_CONFIG_HOME", "/should/never/leak/xdg")
		rec := &recordingExec{}
		h := hostWith(SSHConfig{Host: "h", RemoteLimaHome: "/srv/lima"}, rec)
		if _, err := h.Output(context.Background(), "list", "--format", "json"); err != nil {
			t.Fatalf("Output: %v", err)
		}
		argv := rec.calls[0]
		if anyContains(argv, "should/never/leak") {
			t.Fatalf("local env leaked into the remote argv: %v", argv)
		}
		envTokens := 0
		for _, a := range argv {
			if strings.HasPrefix(a, "LIMA_HOME=") || strings.HasPrefix(a, "XDG_") {
				envTokens++
			}
		}
		if envTokens != 1 {
			t.Fatalf("argv = %v, want exactly one env-assignment token (LIMA_HOME only)", argv)
		}
	})
}

// TestSSHRemoteLock covers the remote flock LockFile: a holder that prints the
// sentinel is ACQUIRED (and released on Unlock), and a holder that exits without
// it (flock -n lost) is CONTENDED — (false,nil), the retry signal the base
// serializer's poll loop needs.
func TestSSHHostUser(t *testing.T) {
	// The remote login user comes from `id -un` — the account Lima names the guest
	// after, which a new VM's user must default to (not the laptop's user).
	rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		if anyContains(argv, "id") {
			return sh(ctx, "printf debian")
		}
		return exec.CommandContext(ctx, "true")
	}}
	if got := hostWith(testCfg, rec).HostUser(); got != "debian" {
		t.Fatalf("HostUser = %q, want debian", got)
	}
	// Falls back to the configured SSH user when `id -un` cannot be run.
	recFail := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		return sh(ctx, "exit 1")
	}}
	if got := hostWith(testCfg, recFail).HostUser(); got != testCfg.User {
		t.Fatalf("HostUser fallback = %q, want %q", got, testCfg.User)
	}
}

func TestSSHHostResources(t *testing.T) {
	t.Run("parses cpus mem disk-free disk-total", func(t *testing.T) {
		rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
			// The one probe script echoes "cpus mem disk-free disk-total" (bytes) — the
			// host-capacity-warning feature needs the TOTAL alongside the free reading
			// already there, so a free% is computable without a second round trip.
			return sh(ctx, "printf '%s' '8 16777216000 500000000000 1000000000000'")
		}}
		h := hostWith(testCfg, rec)
		cpus, mem, disk, diskTotal := h.HostResources()
		if cpus != 8 || mem != 16777216000 || disk != 500000000000 || diskTotal != 1000000000000 {
			t.Fatalf("HostResources = (%d, %d, %d, %d), want (8, 16777216000, 500000000000, 1000000000000)", cpus, mem, disk, diskTotal)
		}
	})
	t.Run("degrades to zeros on failure", func(t *testing.T) {
		rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
			return sh(ctx, "exit 1") // probe failed — header must just drop the clause
		}}
		h := hostWith(testCfg, rec)
		if c, m, d, dt := h.HostResources(); c != 0 || m != 0 || d != 0 || dt != 0 {
			t.Fatalf("HostResources on failure = (%d,%d,%d,%d), want all zero", c, m, d, dt)
		}
	})
}

func TestSSHStagePlaybook(t *testing.T) {
	const abs = "/home/dev/.lima/_sand"
	rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		if anyContains(argv, "pwd") {
			return sh(ctx, "printf %s "+abs) // resolve _sand to its absolute path
		}
		return exec.CommandContext(ctx, "true") // scp succeeds
	}}
	h := hostWith(testCfg, rec)

	dst, err := h.StagePlaybook(context.Background(), "/local/playbook")
	if err != nil {
		t.Fatalf("StagePlaybook: %v", err)
	}
	// The mount location must be ABSOLUTE (a relative RemoteLimaHome would make an
	// ambiguous Lima mount `location`), and under the remote _sand dir.
	if want := abs + "/playbook"; dst != want {
		t.Fatalf("StagePlaybook returned %q, want the absolute staged path %q", dst, want)
	}
	// The local playbook is scp'd RECURSIVELY to that remote path so `limactl
	// start` on the remote host can bind-mount it as /mnt/playbook.
	scpCall := findCall(t, rec.calls, "scp")
	if !hasToken(scpCall, "-r") || !hasToken(scpCall, "/local/playbook") || !hasToken(scpCall, h.target()+":"+dst) {
		t.Fatalf("stage scp = %v, want it to scp -r /local/playbook to %s", scpCall, h.target()+":"+dst)
	}
}

func TestSSHRemoteLock(t *testing.T) {
	t.Run("acquired then released", func(t *testing.T) {
		rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
			// flock acquired: print the sentinel, then block reading stdin (the lock
			// is held until our stdin pipe closes on Unlock).
			return sh(ctx, "printf '"+remoteLockSentinel+"\\n'; exec cat")
		}}
		h := hostWith(testCfg, rec)
		lf, err := h.OpenLock("/remote/.lima/_sand/base.lock", 0o600)
		if err != nil {
			t.Fatalf("OpenLock: %v", err)
		}
		ok, err := lf.TryLock()
		if err != nil || !ok {
			t.Fatalf("TryLock = (%v,%v), want (true,nil)", ok, err)
		}
		if !hasToken(rec.calls[0], "flock") {
			t.Fatalf("lock holder did not run flock: %v", rec.calls[0])
		}
		if err := lf.Unlock(); err != nil {
			t.Fatalf("Unlock: %v", err)
		}
		if err := lf.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	t.Run("contended is (false,nil)", func(t *testing.T) {
		rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
			return sh(ctx, "exit 1") // flock -n failed: no sentinel, immediate exit
		}}
		h := hostWith(testCfg, rec)
		lf, _ := h.OpenLock("/remote/.lima/_sand/base.lock", 0o600)
		ok, err := lf.TryLock()
		if ok || err != nil {
			t.Fatalf("TryLock(contended) = (%v,%v), want (false,nil)", ok, err)
		}
	})
	t.Run("no flock on remote is an error, not silent contention", func(t *testing.T) {
		// A macOS/busybox Lima host ships no util-linux flock: the shell reports
		// command-not-found and exits 127, with no sentinel. TryLock MUST surface
		// that as an error so lockBase degrades to unserialized — returning
		// (false,nil) here would hang the first `sand create` polling a lock that
		// nothing will ever hold.
		rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
			return sh(ctx, "echo 'sh: flock: not found' >&2; exit 127")
		}}
		h := hostWith(testCfg, rec)
		lf, _ := h.OpenLock("/remote/.lima/_sand/base.lock", 0o600)
		ok, err := lf.TryLock()
		if ok || err == nil {
			t.Fatalf("TryLock(no flock) = (%v,%v), want (false, non-nil error)", ok, err)
		}
	})
}
