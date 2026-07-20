package lima

// sshhost.go is the SSH implementation of the host-access seam (Host =
// Runner + HostFiles). It drives the SAME limactl the local execRunner drives —
// it reuses Client's argv-building wholesale — and changes only HOW those args
// are executed (over `ssh <host> …` instead of a local fork) and WHERE the Lima
// instance files are read (over SSH off the remote host instead of the local
// filesystem). A remote-Lima provider (internal/provider) is the local Lima
// provider configured with one of these instead of an execRunner + LocalFiles.
//
// Two things are load-bearing and easy to lose across the transport swap:
//
//   - STDOUT/STDERR SEPARATION and cmd.WaitDelay reaping, both exactly as the
//     local execRunner does them (runner.go). Over SSH the orphan chain is one
//     generation deeper — our ssh child forks a remote limactl which forks the
//     guest ssh — which is PRECISELY the multi-generation orphan WaitDelay exists
//     to reap; a cancel must still tear the whole chain down. See Stream/StreamOut.
//   - SHELL QUOTING of every remote token. `ssh host a b c` joins the tokens with
//     spaces and re-parses them through the remote login shell, so any token with
//     a space or metacharacter (the provision script, an `edit --set` expression,
//     the guest tmux attach expression) must be shell-quoted or the remote shell
//     word-splits it. The local execRunner needs none of this — execve passes argv
//     verbatim — so this quoting is the one genuinely new hazard here.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SSHConfig is the connection identity for a remote Lima host. It is secret-free:
// IdentityPath is a PATH to a private key file, never key material (the same
// contract provider.TargetConfig keeps so the target can be persisted in the
// registry).
type SSHConfig struct {
	Host string // required
	User string // "" lets ssh use its own default (ssh_config / local user)
	Port int    // <=0 or 22 omits -p / -P entirely
	// IdentityPath is a private-key FILE path, or "" to fall back to the ambient
	// ssh agent / ssh_config. Never key material.
	IdentityPath string
	// RemoteLimaHome is LIMA_HOME on the remote host. "" defaults to defaultRemoteLimaHome,
	// a path RELATIVE to the remote login home (see LimaHome) — Lima's own default.
	RemoteLimaHome string
}

// defaultRemoteLimaHome is where a remote host keeps its Lima state when
// SSHConfig.RemoteLimaHome is unset/empty. It is RELATIVE (no leading / or ~)
// on purpose:
// the remote login home is unknown from here without a round trip, and a relative
// path handed to `ssh host cat .lima/…` resolves against the remote $HOME, which
// is exactly `~/.lima` — Lima's default — without us having to learn the remote
// home first.
const defaultRemoteLimaHome = ".lima"

// SSHHost is the SSH host-access implementation: a Runner AND a HostFiles (i.e. a
// Host), plus the two-stage copyAcrossHop and the ssh-wrapped interactive attach.
type SSHHost struct {
	cfg SSHConfig
	// newCmd is the injectable seam that turns a fully-built ssh/scp argv into an
	// *exec.Cmd. Production is exec.CommandContext; a unit test swaps it to RECORD
	// the argv (the assertion target) and return a stand-in command, so the exact
	// ssh argv is proven without a real ssh binary or remote host (AGENTS.md: no
	// test may require a real limactl — this extends that to ssh).
	newCmd func(ctx context.Context, argv []string) *exec.Cmd

	// userOnce/user cache the remote login user: it never changes for the process,
	// so HostUser (called every board refresh) resolves it over ssh only once.
	userOnce sync.Once
	user     string

	// statBSD remembers that the remote's `stat` is the BSD flavor (macOS), so
	// after the first `stat -c` rejection every later Stat/DiskAllocBytes goes
	// straight to the BSD `-f` form instead of paying a doomed GNU probe first.
	statBSD atomic.Bool

	// controlDir, when non-empty, holds the OpenSSH ControlMaster unix-domain
	// sockets for this process's ssh connections (see muxFlags). It is resolved
	// once at construction (NewSSHHost) and left EMPTY when it could not be
	// determined or created — connection multiplexing is a pure optimization,
	// never a hard requirement for reaching the remote host, so a failure here
	// must silently fall back to the pre-multiplexing argv shape rather than
	// failing construction or any later command.
	controlDir string
}

// Compile-time proof the SSH host satisfies the whole seam and the copy hook.
var (
	_ Host         = (*SSHHost)(nil)
	_ remoteCopier = (*SSHHost)(nil)
)

// NewSSHHost builds an SSH host-access implementation for the given connection.
func NewSSHHost(cfg SSHConfig) *SSHHost {
	if cfg.RemoteLimaHome == "" {
		cfg.RemoteLimaHome = defaultRemoteLimaHome
	}
	h := &SSHHost{cfg: cfg, newCmd: func(ctx context.Context, argv []string) *exec.Cmd {
		return exec.CommandContext(ctx, argv[0], argv[1:]...)
	}}

	// Resolve a per-user control-socket directory for OpenSSH connection
	// multiplexing (see muxFlags). Best-effort: os.UserCacheDir or the MkdirAll
	// can fail (a read-only/unset HOME, a sandboxed environment), and that must
	// never make constructing an SSHHost — or any command it later runs — fail.
	// It just means every command pays a fresh ssh handshake, exactly as before
	// this feature existed.
	if cacheDir, err := os.UserCacheDir(); err == nil {
		dir := filepath.Join(cacheDir, "sandbar", "ssh")
		if err := os.MkdirAll(dir, 0o700); err == nil {
			h.controlDir = dir
		}
	}
	return h
}

// --- ssh/scp argv construction --------------------------------------------------

// shellSafe matches a token that needs no shell quoting: it survives the remote
// shell's word-splitting and expansion untouched. Anything else is single-quoted.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellQuote quotes s for the REMOTE shell that ssh re-parses the joined command
// through. A safe token is returned verbatim (so `ssh host limactl list --format
// json` reads cleanly); anything with a space or metacharacter is single-quoted,
// with embedded single quotes handled by the standard '\” splice. The empty
// string becomes ” rather than vanishing.
func shellQuote(s string) string {
	if s != "" && shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// target is the ssh/scp destination: user@host, or host when no user is set.
func (h *SSHHost) target() string {
	if h.cfg.User != "" {
		return h.cfg.User + "@" + h.cfg.Host
	}
	return h.cfg.Host
}

// muxFlags returns the OpenSSH connection-multiplexing flags shared by every
// ssh and scp argv, or nil when controlDir could not be resolved (a pure
// optimization, never a hard requirement — see NewSSHHost).
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
func (h *SSHHost) muxFlags() []string {
	if h.controlDir == "" {
		return nil
	}
	return []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(h.controlDir, "%C"),
		"-o", "ControlPersist=600",
	}
}

// sshBase is the ssh argv prefix up to and INCLUDING the target: `ssh [-t] [-p
// port] [-i identity] [mux flags] target`. tty adds -t for the interactive
// attach. Port is omitted at the default (<=0 or 22) and identity when unset,
// and the multiplexing flags are omitted when controlDir could not be
// resolved, so the common case is the bare `ssh target …` the tests pin.
func (h *SSHHost) sshBase(tty bool) []string {
	a := []string{"ssh"}
	if tty {
		a = append(a, "-t")
	}
	if h.cfg.Port > 0 && h.cfg.Port != 22 {
		a = append(a, "-p", strconv.Itoa(h.cfg.Port))
	}
	if h.cfg.IdentityPath != "" {
		a = append(a, "-i", h.cfg.IdentityPath)
	}
	a = append(a, h.muxFlags()...)
	return append(a, h.target())
}

// sshCommand builds the full ssh argv to run remoteArgv on the remote host, with
// each remote token shell-quoted so ssh's space-join + remote reshell reconstruct
// the identical argv the local execRunner would have passed via execve.
func (h *SSHHost) sshCommand(tty bool, remoteArgv ...string) []string {
	argv := h.sshBase(tty)
	for _, a := range remoteArgv {
		argv = append(argv, shellQuote(a))
	}
	return argv
}

// scpCommand builds an scp argv. Note scp's port flag is -P (capital), NOT ssh's
// -p — getting this wrong silently ignores a non-default port. The same
// multiplexing flags as sshBase are threaded in before the endpoints so an scp
// transfer benefits from (and can itself become) the shared master connection.
func (h *SSHHost) scpCommand(recursive bool, from, to string) []string {
	a := []string{"scp"}
	if recursive {
		a = append(a, "-r")
	}
	if h.cfg.Port > 0 && h.cfg.Port != 22 {
		a = append(a, "-P", strconv.Itoa(h.cfg.Port))
	}
	if h.cfg.IdentityPath != "" {
		a = append(a, "-i", h.cfg.IdentityPath)
	}
	a = append(a, h.muxFlags()...)
	return append(a, from, to)
}

// --- Runner: run limactl over ssh -----------------------------------------------

// Output runs `ssh … limactl args…`, capturing stdout and stderr SEPARATELY —
// exactly as execRunner.Output does — so a logrus line on limactl's stderr cannot
// corrupt the JSON on its stdout, and so Client.List's clone/delete race sentinel
// (ErrListRacedInstanceDir), which matches the remote limactl's stderr folded into
// the error here, still fires over the hop.
func (h *SSHHost) Output(ctx context.Context, args ...string) ([]byte, error) {
	argv := h.sshCommand(false, append([]string{"limactl"}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd := h.newCmd(ctx, argv)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
	}
	return stdout.Bytes(), err
}

// Stream runs `ssh … limactl args…`, piping stdin to the REMOTE limactl (so the
// provision vars keep arriving over stdin, never argv — a finalize token must
// never land in the remote process listing) and merging stdout+stderr into out
// for live display. cmd.WaitDelay reaps the ssh→limactl→guest-ssh orphan chain on
// a cancelled ctx, one generation deeper than the local case but the same hazard.
func (h *SSHHost) Stream(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	argv := h.sshCommand(false, append([]string{"limactl"}, args...)...)
	cmd := h.newCmd(ctx, argv)
	cmd.Stdin = stdin
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = waitDelay // a cancel must REAP the whole ssh->limactl->guest chain
	return cmd.Run()
}

// StreamOut runs `ssh … limactl args…`, streaming stdout ONLY to out and keeping
// stderr separate (folded into the error) so a `tar -czf -` payload on stdout is
// not corrupted by limactl's `cd` warning — exactly as execRunner.StreamOut does,
// with the same stdin passthrough and WaitDelay reaping.
func (h *SSHHost) StreamOut(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	argv := h.sshCommand(false, append([]string{"limactl"}, args...)...)
	cmd := h.newCmd(ctx, argv)
	cmd.Stdin = stdin
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
	}
	return err
}

// runRemote runs an arbitrary remote command (NOT prefixed with limactl — used by
// the HostFiles methods for cat / stat / mkdir / rm), capturing stdout and stderr
// separately. It mirrors Output's stdout/stderr discipline.
func (h *SSHHost) runRemote(ctx context.Context, stdin io.Reader, remoteArgv ...string) ([]byte, []byte, error) {
	argv := h.sshCommand(false, remoteArgv...)
	var stdout, stderr bytes.Buffer
	cmd := h.newCmd(ctx, argv)
	cmd.Stdin = stdin
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// --- HostFiles: read Lima instance state over ssh -------------------------------

// notExistMarker is the substring cat/stat/test print when a path is absent. We
// map it to fs.ErrNotExist so callers can tell "absent" from "present but
// unreadable" (a connection failure, a permission error) exactly as they do
// against the local filesystem — the HostFiles contract Stat/ReadFile promise.
const notExistMarker = "No such file or directory"

// asNotExist turns a remote failure whose stderr names a missing path into
// fs.ErrNotExist, and otherwise wraps the stderr for diagnostics.
func asNotExist(op, path string, stderr []byte, err error) error {
	msg := strings.TrimSpace(string(stderr))
	if strings.Contains(msg, notExistMarker) {
		return &fs.PathError{Op: op, Path: path, Err: fs.ErrNotExist}
	}
	if msg != "" {
		return fmt.Errorf("%s %s: %w: %s", op, path, err, msg)
	}
	return fmt.Errorf("%s %s: %w", op, path, err)
}

// ReadFile reads the file at path off the remote host via `ssh host cat <path>`. A
// missing file reports an error satisfying fs.ErrNotExist, as os.ReadFile does.
func (h *SSHHost) ReadFile(path string) ([]byte, error) {
	out, errb, err := h.runRemote(context.Background(), nil, "cat", path)
	if err != nil {
		return nil, asNotExist("read", path, errb, err)
	}
	return out, nil
}

// remoteFileInfo is the minimal fs.FileInfo the HostFiles callers actually use
// (existence, size, mtime, is-dir). It is parsed from one `stat -c` line.
type remoteFileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
}

func (fi remoteFileInfo) Name() string       { return fi.name }
func (fi remoteFileInfo) Size() int64        { return fi.size }
func (fi remoteFileInfo) Mode() fs.FileMode  { return fi.mode }
func (fi remoteFileInfo) ModTime() time.Time { return fi.modTime }
func (fi remoteFileInfo) IsDir() bool        { return fi.isDir }
func (fi remoteFileInfo) Sys() any           { return nil }

// statFormatGNU / statFormatBSD ask for size|epoch-mtime|type on one line.
// GNU coreutils stat (Linux) uses `-c` with %s (size) %Y (mtime) %F (type text
// "directory"/"regular file"); BSD stat (macOS — Lima's primary host platform)
// uses `-f` with %z (size) %m (mtime) %HT (type text "Directory"/"Regular File").
// The two are mutually incompatible, so Stat tries GNU then falls back to BSD.
const (
	statFormatGNU = "%s|%Y|%F"
	statFormatBSD = "%z|%m|%HT"
)

// statField runs `stat` for a format over ssh, trying the GNU `-c` form and
// falling back to the BSD `-f` form on anything but a missing-path error — a
// remote macOS host (Lima's primary platform) rejects `-c` with "illegal option",
// which is NOT "absent", and mis-reading it as absent is what made cleanup skip a
// half-written instance and the disk gauge render "?". Once the BSD form is needed
// it is remembered (statBSD), so every later Stat/DiskAllocBytes on a BSD remote
// goes straight to `-f` instead of paying a doomed GNU probe first.
func (h *SSHHost) statField(path, gnuFmt, bsdFmt string) (out, errb []byte, err error) {
	if h.statBSD.Load() {
		return h.runRemote(context.Background(), nil, "stat", "-f", bsdFmt, path)
	}
	out, errb, err = h.runRemote(context.Background(), nil, "stat", "-c", gnuFmt, path)
	if err != nil && !strings.Contains(string(errb), notExistMarker) {
		if out, errb, err = h.runRemote(context.Background(), nil, "stat", "-f", bsdFmt, path); err == nil {
			h.statBSD.Store(true)
		}
	}
	return out, errb, err
}

// Stat stats path over `ssh host stat`. A missing path reports fs.ErrNotExist.
func (h *SSHHost) Stat(path string) (fs.FileInfo, error) {
	out, errb, err := h.statField(path, statFormatGNU, statFormatBSD)
	if err != nil {
		return nil, asNotExist("stat", path, errb, err)
	}
	fields := strings.SplitN(strings.TrimSpace(string(out)), "|", 3)
	if len(fields) != 3 {
		return nil, fmt.Errorf("stat %s: unexpected output %q", path, string(out))
	}
	size, _ := strconv.ParseInt(fields[0], 10, 64)
	epoch, _ := strconv.ParseInt(fields[1], 10, 64)
	// GNU %F prints "directory"; BSD %HT prints "Directory".
	isDir := strings.Contains(strings.ToLower(fields[2]), "directory")
	mode := fs.FileMode(0)
	if isDir {
		mode |= fs.ModeDir
	}
	return remoteFileInfo{
		name:    filepath.Base(path),
		size:    size,
		mode:    mode,
		modTime: time.Unix(epoch, 0),
		isDir:   isDir,
	}, nil
}

// WriteFile writes data to path on the remote host, mirroring the local
// WriteFile: create the parent dir with dirPerm, then the file with filePerm. It
// all runs in ONE remote sh — mkdir, dir chmod (best-effort, like os.MkdirAll it
// only matters for a dir this call creates), content write, file chmod — a single
// round trip. The data travels over stdin (never argv), so file contents never
// land in a remote process listing. Plain `cat`+`chmod` rather than `install`,
// which is not guaranteed present on a minimal remote.
func (h *SSHHost) WriteFile(path string, data []byte, dirPerm, filePerm fs.FileMode) error {
	script := `set -e; mkdir -p -- "$1"; chmod "$4" -- "$1" 2>/dev/null || true; cat > "$2"; chmod "$3" -- "$2"`
	remote := []string{"sh", "-c", script, "sand",
		filepath.Dir(path), path,
		fmt.Sprintf("%o", filePerm.Perm()), fmt.Sprintf("%o", dirPerm.Perm())}
	_, errb, err := h.runRemote(context.Background(), bytes.NewReader(data), remote...)
	if err != nil {
		return asNotExist("write", path, errb, err)
	}
	return nil
}

// MkdirAll creates path and any missing parents on the remote host.
func (h *SSHHost) MkdirAll(path string, perm fs.FileMode) error {
	script := `set -e; mkdir -p -- "$1"; chmod "$2" -- "$1"`
	_, errb, err := h.runRemote(context.Background(), nil, "sh", "-c", script, "sand", path, fmt.Sprintf("%o", perm.Perm()))
	if err != nil {
		return asNotExist("mkdir", path, errb, err)
	}
	return nil
}

// RemoveAll removes path and any children on the remote host. Like os.RemoveAll, a
// non-existent path is not an error (`rm -rf` is silent about it).
func (h *SSHHost) RemoveAll(path string) error {
	_, errb, err := h.runRemote(context.Background(), nil, "rm", "-rf", "--", path)
	if err != nil {
		return asNotExist("remove", path, errb, err)
	}
	return nil
}

// DiskAllocBytes returns the ALLOCATED on-disk size (512-byte blocks × 512) of the
// remote file at path — a qcow2's sparse size — or -1 when it cannot be measured,
// the same contract as the local probe. `%b` is the allocated block count on both
// GNU (`-c`) and BSD (`-f`) stat; only the flag differs, so fall back to the BSD
// flag for a macOS remote — otherwise the tile's disk gauge renders "?".
func (h *SSHHost) DiskAllocBytes(path string) int64 {
	// %b is the allocated block count on both GNU (`-c`) and BSD (`-f`) stat;
	// statField picks the working flavor (and caches it).
	out, _, err := h.statField(path, "%b", "%b")
	if err != nil {
		return -1
	}
	blocks, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return -1
	}
	return blocks * 512
}

// LimaHome is the Lima home on the REMOTE host: the configured RemoteLimaHome, or
// defaultRemoteLimaHome (a path relative to the remote login home). Both Lima's
// own per-instance state and sand's state ABOUT an instance (the base version
// stamp and its lock, under _sand/) live beneath it — now on the remote host.
func (h *SSHHost) LimaHome() string { return h.cfg.RemoteLimaHome }

// StagePlaybook copies the local playbook fileset to a stable path under the
// remote LimaHome and returns that path so `limactl start` on the remote host can
// bind-mount it as /mnt/playbook. See lima.HostFiles.StagePlaybook.
//
// The returned path is ABSOLUTE: a Lima mount `location` is resolved on the remote
// host and a relative one would be ambiguous, but RemoteLimaHome may be relative
// to $HOME (its Lima default is the relative ".lima"), so the _sand directory is
// created and its absolute path resolved in one round trip before the copy.
//
// The staged copy is refreshed each build (the prior one is removed first) and
// deliberately NOT cleaned up afterward: the base overlay's mount points at it and
// a clone's finalize re-mounts it, so it must outlive the build that created it —
// the remote analogue of the local checkout the mount points at for local Lima.
func (h *SSHHost) StagePlaybook(ctx context.Context, localDir string) (string, error) {
	sandDir := strings.TrimSuffix(h.cfg.RemoteLimaHome, "/") + "/_sand"
	// One round trip: create _sand, drop any prior staged playbook (scp into an
	// existing dir would nest the source under it), and echo _sand's ABSOLUTE path.
	q := shellQuote(sandDir)
	out, errb, err := h.runRemote(ctx, nil, "sh", "-c",
		fmt.Sprintf("mkdir -p %s && rm -rf %s/playbook && cd %s && pwd", q, q, q))
	if err != nil {
		return "", fmt.Errorf("prepare remote playbook dir: %w: %s", err, strings.TrimSpace(string(errb)))
	}
	dst := strings.TrimSpace(string(out)) + "/playbook"
	if err := h.scp(ctx, nil, true, localDir, h.target()+":"+dst); err != nil {
		return "", fmt.Errorf("stage playbook to remote host: %w", err)
	}
	return dst, nil
}

// HostUser returns the remote host's login user — `ssh host id -un` — which is
// the user Lima creates the guest account for (Lima names the guest user after
// whoever runs limactl) and therefore the account `limactl shell` logs into. It
// falls back to the configured SSH user, then "" when neither can be determined.
// A new VM's user must default to THIS, not the laptop's user, or the playbook
// provisions a different account than the shell lands in — leaving the guest
// login user without its ~/.tmux.conf, git identity, or secrets.
func (h *SSHHost) HostUser() string {
	// Resolved once and cached: the remote login user is constant for the process,
	// but HostUser is called on every board refresh (listCmd) — an ssh round trip
	// each time otherwise.
	h.userOnce.Do(func() {
		out, _, err := h.runRemote(context.Background(), nil, "id", "-un")
		if u := strings.TrimSpace(string(out)); err == nil && u != "" {
			h.user = u
		} else {
			h.user = h.cfg.User
		}
	})
	return h.user
}

// HostResources samples the remote host's CPU count, total memory (bytes),
// free disk (bytes) AND total disk (bytes) in ONE ssh round trip, for the board
// header's denominators and (diskTotalBytes) the host-capacity-warning feature's
// free% arithmetic (internal/ui/hostwarn.go — a percentage needs both halves,
// and sampling them apart would be a second remote round trip for a number
// that `df` already prints in the same line as the free one). It is
// best-effort — any field the remote shell cannot produce comes back 0 —
// because a wrong or missing host total must never break the header or a
// refresh.
//
// The script is portable across the two platforms Lima runs on: nproc /
// /proc/meminfo on Linux, sysctl on macOS; df -Pk for free/total KiB on both,
// falling back to $HOME when the Lima store dir does not exist yet.
func (h *SSHHost) HostResources() (cpus int, memBytes, diskFreeBytes, diskTotalBytes int64) {
	// RemoteLimaHome is never "" at runtime — NewSSHHost defaults it — so use it
	// directly, as LimaHome and StagePlaybook do.
	limaHome := h.cfg.RemoteLimaHome
	script := `c=$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 0)
if [ -r /proc/meminfo ]; then m=$(awk '/^MemTotal:/{print $2*1024}' /proc/meminfo); else m=$(sysctl -n hw.memsize 2>/dev/null || echo 0); fi
d=$(df -Pk ` + shellQuote(limaHome) + ` 2>/dev/null | awk 'NR==2{print $4*1024}')
t=$(df -Pk ` + shellQuote(limaHome) + ` 2>/dev/null | awk 'NR==2{print $2*1024}')
if [ -z "$d" ]; then
  d=$(df -Pk "$HOME" 2>/dev/null | awk 'NR==2{print $4*1024}')
  t=$(df -Pk "$HOME" 2>/dev/null | awk 'NR==2{print $2*1024}')
fi
echo "${c:-0} ${m:-0} ${d:-0} ${t:-0}"`
	out, _, err := h.runRemote(context.Background(), nil, "sh", "-c", script)
	if err != nil {
		return 0, 0, 0, 0
	}
	fields := strings.Fields(string(out))
	if len(fields) != 4 {
		return 0, 0, 0, 0
	}
	cpus, _ = strconv.Atoi(fields[0])
	memBytes, _ = strconv.ParseInt(fields[1], 10, 64)
	diskFreeBytes, _ = strconv.ParseInt(fields[2], 10, 64)
	diskTotalBytes, _ = strconv.ParseInt(fields[3], 10, 64)
	return cpus, memBytes, diskFreeBytes, diskTotalBytes
}

// --- copy across the hop --------------------------------------------------------

// copyAcrossHop resolves a copy whose limactl runs on the remote host into the
// two-stage path the hop requires (local <-> remote host <-> guest), preserving
// the `--backend=scp` placement contract at the guest end. It is the ONE place the
// topology lives, reached from Client.Copy, so aptcache/ui-transfer/etc. inherit
// it unchanged.
//
//   - UPLOAD (local -> guest): scp the local source into a remote temp dir, then
//     `limactl copy` that staged copy into the guest. The staged path keeps the
//     source's basename so the guest-end scp nesting (`src` lands INSIDE `dst`) is
//     byte-identical to the local single-stage copy.
//   - DOWNLOAD (guest -> local): `limactl copy` the guest source into a remote
//     temp dir, then scp it back to the local destination.
//   - guest -> guest / anything with no host-local endpoint: no staging; run
//     `limactl copy` on the remote directly.
func (h *SSHHost) copyAcrossHop(ctx context.Context, out io.Writer, recursive bool, src, dst string) error {
	_, srcPath, srcGuest := splitGuestEndpoint(src)
	_, _, dstGuest := splitGuestEndpoint(dst)

	switch {
	case !srcGuest && dstGuest:
		return h.uploadAcrossHop(ctx, out, recursive, src, dst)
	case srcGuest && !dstGuest:
		return h.downloadAcrossHop(ctx, out, recursive, src, srcPath, dst)
	default:
		// Both endpoints already live on the remote host (guest<->guest), or
		// neither is a guest endpoint (a caller error we let limactl report):
		// either way there is nothing local to stage, so run limactl copy directly.
		return streamCopy(ctx, h, out, recursive, src, dst)
	}
}

// uploadAcrossHop stages localSrc to a remote temp dir and copies it from there
// into the guest, then removes the temp dir. localSrc is a host-local path;
// guestDst is a `<vm>:<path>` endpoint.
func (h *SSHHost) uploadAcrossHop(ctx context.Context, out io.Writer, recursive bool, localSrc, guestDst string) error {
	tmp, err := h.mktemp(ctx)
	if err != nil {
		return err
	}
	defer h.rmRemote(tmp)

	// scp the source INTO the temp dir; scp then names it tmp/<basename(localSrc)>.
	if err := h.scp(ctx, out, recursive, localSrc, h.target()+":"+tmp); err != nil {
		return fmt.Errorf("stage %s to remote host: %w", localSrc, err)
	}
	staged := tmp + "/" + filepath.Base(localSrc)
	// Same limactl copy argv as the local case, only the host endpoint is now the
	// staged remote path — so the guest-end placement is identical.
	return streamCopy(ctx, h, out, recursive, staged, guestDst)
}

// downloadAcrossHop copies the guest source into a remote temp dir and scps it
// back to the local destination, then removes the temp dir. guestSrc is a
// `<vm>:<path>` endpoint whose path portion is guestSrcPath; localDst is a
// host-local path.
func (h *SSHHost) downloadAcrossHop(ctx context.Context, out io.Writer, recursive bool, guestSrc, guestSrcPath, localDst string) error {
	tmp, err := h.mktemp(ctx)
	if err != nil {
		return err
	}
	defer h.rmRemote(tmp)

	// limactl copy the guest source into the remote temp dir; it lands at
	// tmp/<basename(guestSrcPath)> by the same scp-backend contract.
	if err := streamCopy(ctx, h, out, recursive, guestSrc, tmp); err != nil {
		return err
	}
	staged := tmp + "/" + filepath.Base(guestSrcPath)
	// scp the staged copy back to the local destination; scp nests it inside
	// localDst, matching the local single-stage placement.
	if err := h.scp(ctx, out, recursive, h.target()+":"+staged, localDst); err != nil {
		return fmt.Errorf("retrieve %s from remote host: %w", guestSrcPath, err)
	}
	return nil
}

// mktemp creates a fresh remote temp directory for staging a copy and returns its
// absolute path. A unique dir per copy keeps concurrent transfers from colliding
// and makes cleanup a single rm -rf.
func (h *SSHHost) mktemp(ctx context.Context) (string, error) {
	out, errb, err := h.runRemote(ctx, nil, "mktemp", "-d", "-t", "sand-copy-XXXXXX")
	if err != nil {
		return "", fmt.Errorf("create remote staging dir: %w: %s", err, strings.TrimSpace(string(errb)))
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", fmt.Errorf("create remote staging dir: mktemp returned no path")
	}
	return dir, nil
}

// rmRemote best-effort removes a remote staging dir. Cleanup failure is not worth
// failing an otherwise-successful transfer over — but it is on a background
// context so a cancelled copy still tidies up.
func (h *SSHHost) rmRemote(path string) {
	_, _, _ = h.runRemote(context.Background(), nil, "rm", "-rf", "--", path)
}

// scp runs scp with the connection's port/identity, streaming its progress to out
// (through the same scpDebugFilter Copy uses, since scp -v is just as chatty).
func (h *SSHHost) scp(ctx context.Context, out io.Writer, recursive bool, from, to string) error {
	argv := h.scpCommand(recursive, from, to)
	cmd := h.newCmd(ctx, argv)
	cmd.WaitDelay = waitDelay
	if out == nil {
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("scp %s %s: %w", from, to, err)
		}
		return nil
	}
	f := &scpDebugFilter{w: out}
	cmd.Stdout = f
	cmd.Stderr = f
	err := cmd.Run()
	if ferr := f.Flush(); err == nil {
		err = ferr
	}
	if err != nil {
		return fmt.Errorf("scp %s %s: %w", from, to, err)
	}
	return nil
}

// splitGuestEndpoint splits a copy endpoint into (instance, path, isGuest). A
// guest endpoint is GuestPath's `<instance>:<path>` form — a colon whose
// left side has no slash (a plain host path like /a/b or C-less unix paths never
// match). isGuest is false for a host-local path, so copyAcrossHop can tell which
// side needs staging.
func splitGuestEndpoint(s string) (instance, path string, isGuest bool) {
	i := strings.IndexByte(s, ':')
	if i <= 0 || strings.ContainsAny(s[:i], "/") {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// --- interactive attach across the hop ------------------------------------------

// AttachArgv wraps the local attach argv in `ssh -t <host> …`, keeping the guest
// tmux expression byte-for-byte identical (it reuses AttachArgv, so guestAttachExpr
// is never retyped) — the single most destructive thing to get wrong (see
// attach.go). Each element of the limactl argv is shell-quoted for the remote
// shell ssh re-parses the joined command through; -t allocates the PTY the nested
// tmux client needs (ssh -t -> limactl shell -> guest bash -> tmux). --workdir
// still precedes the instance name, because AttachArgv put it there and quoting
// preserves order. The caller execs the result against its real TTY.
func (h *SSHHost) AttachArgv(name, guestHome, colorterm string) []string {
	// The `ssh -t <target> <shell-quoted local argv>` construction IS sshCommand
	// with tty=true; reuse it rather than re-spelling the quoting loop the file's
	// header calls load-bearing.
	return h.sshCommand(true, AttachArgv(name, guestHome, colorterm)...)
}

// --- base-image lock over ssh ---------------------------------------------------

// remoteLockSentinel is printed by the remote flock holder the instant it acquires
// the lock, so TryLock can tell acquisition from contention without guessing.
const remoteLockSentinel = "SAND_LOCK_ACQUIRED"

// OpenLock returns a LockFile backed by a remote advisory flock. Unlike the local
// LockFile (which flocks an open fd for the process lifetime), each TryLock spawns
// a fresh `ssh host flock -n <path> …` holder: the remote flock is held for as
// long as that ssh command runs, and dies — releasing the lock — the moment the
// holder's ssh connection drops (Unlock/Close, or the create process crashing).
// That preserves the local lock's crucial property that a dead holder never wedges
// the base (baselock.go tolerates OpenLock failing entirely, so a remote that
// lacks flock degrades to unserialized rather than failing the build).
func (h *SSHHost) OpenLock(path string, perm fs.FileMode) (LockFile, error) {
	return &sshLock{h: h, path: path}, nil
}

// sshLock is a remote advisory lock. It holds at most one live ssh flock holder at
// a time (started by a successful TryLock, torn down by Unlock/Close).
type sshLock struct {
	h    *SSHHost
	path string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc
}

// TryLock attempts a NON-BLOCKING remote flock. It starts a holder that runs
// `flock -n <path> sh -c 'printf SENTINEL; cat'`: flock -n acquires the lock or
// exits 1 at once; on success the holder prints the sentinel and then blocks in
// `cat` reading our stdin pipe, holding the lock until we close that pipe (Unlock).
// Returns (true,nil) when the sentinel arrives, (false,nil) when the holder exits
// first (someone else holds it), (false,err) on any other failure — the exact
// contract the base serializer's poll loop expects.
func (l *sshLock) TryLock() (bool, error) {
	if l.cmd != nil {
		return true, nil // already held by this LockFile
	}
	ctx, cancel := context.WithCancel(context.Background())

	// flock -n <path> holds the lock for the lifetime of the command it runs; the
	// inner sh prints the sentinel then blocks in `cat` reading our stdin pipe, so
	// the lock lasts exactly until we close that pipe (Unlock/Close).
	remote := []string{"flock", "-n", l.path, "sh", "-c", fmt.Sprintf("printf '%s\\n'; exec cat", remoteLockSentinel)}

	argv := l.h.sshCommand(false, remote...)
	cmd := l.h.newCmd(ctx, argv)
	cmd.WaitDelay = waitDelay
	var errb bytes.Buffer
	cmd.Stderr = &errb

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return false, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return false, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return false, err
	}

	// Read stdout until the sentinel appears (acquired) or the holder exits (EOF —
	// the lock was held, flock -n bailed). A held lock is (false,nil): the caller
	// waits and retries.
	sc := bufio.NewScanner(stdout)
	acquired := false
	for sc.Scan() {
		if strings.Contains(sc.Text(), remoteLockSentinel) {
			acquired = true
			break
		}
	}
	if !acquired {
		// Holder produced no sentinel. Two reasons must be told apart: the lock was
		// HELD (flock -n exits 1 → retry, the (false,nil) contract), or the remote
		// has NO usable `flock` at all — a macOS or busybox Lima host, where the
		// shell reports command-not-found (exit 127). The latter must surface as an
		// ERROR so lockBase degrades to unserialized (baselock.go treats a TryLock
		// error as "continue without the lock"); returning (false,nil) there would
		// make the caller poll a lock nothing will ever hold, hanging the very first
		// `sand create` against such a host — the exact opposite of OpenLock's
		// documented promise to degrade rather than fail.
		_ = stdin.Close()
		cancel()
		werr := cmd.Wait()
		if remoteFlockUnavailable(werr, errb.String()) {
			return false, fmt.Errorf("remote flock unavailable (%v): %s", werr, strings.TrimSpace(errb.String()))
		}
		return false, nil
	}

	// Drain any further holder stdout so Unlock's Wait() can never deadlock on an
	// undrained StdoutPipe (the holder's quiescent `cat` emits nothing more, but a
	// live remote could log to stdout). The goroutine ends when the pipe closes on
	// Wait.
	go func() { _, _ = io.Copy(io.Discard, stdout) }()

	l.cmd, l.stdin, l.cancel = cmd, stdin, cancel
	return true, nil
}

// remoteFlockUnavailable reports whether a non-sentinel flock holder exit means
// the remote host has no usable `flock` binary — command-not-found surfaces as a
// "not found" diagnostic on stderr and an exit status of 127 — rather than the
// lock merely being held (flock -n exits 1 with no such diagnostic). See
// sshLock.TryLock: only the former must degrade to unserialized.
func remoteFlockUnavailable(waitErr error, stderr string) bool {
	if strings.Contains(stderr, "not found") {
		return true
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) && ee.ExitCode() == 127 {
		return true
	}
	return false
}

// Unlock releases the held lock by closing the holder's stdin (its `cat` EOFs, the
// remote flock exits, the lock releases) and reaping the ssh process. It is safe
// to call when nothing is held.
func (l *sshLock) Unlock() error {
	if l.cmd == nil {
		return nil
	}
	cmd, stdin, cancel := l.cmd, l.stdin, l.cancel
	l.cmd, l.stdin, l.cancel = nil, nil, nil
	_ = stdin.Close()
	// WaitDelay bounds the wait if the connection is wedged; cancel guarantees the
	// local ssh dies even then, which drops the connection and releases the remote
	// flock.
	err := cmd.Wait()
	cancel()
	return err
}

// Close releases any held lock (an unheld Close is a no-op).
func (l *sshLock) Close() error { return l.Unlock() }
