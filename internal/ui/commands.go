package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
)

// Message types flowing through Update. Every blocking lima/provision call is
// wrapped in a tea.Cmd that returns one of these — Update itself never blocks.
type (
	// vmsLoadedMsg carries the result of a List refresh.
	vmsLoadedMsg struct {
		vms []vm.VM
		err error
		// The host's own capacity, sampled in the same command for the same reason as
		// the per-VM stats: hostMemBytes reads /proc/meminfo and hostDiskFree statfs's
		// the Lima volume, and the header called BOTH on every render. Zero means "not
		// sampled" — a test that hands the model a vmsLoadedMsg by hand — and the
		// header falls back to probing directly (see hostCapacityText).
		hostMem      int64
		hostDiskFree int64
	}
	// actionDoneMsg reports a lifecycle action (start/stop/restart/delete). name
	// is the affected instance, so the model can update the managed registry.
	// warn carries a non-fatal problem (currently: a failed ApplySecrets) that
	// must NOT be treated as a failure — err staying nil is what tells the
	// handler in model.go the action itself succeeded.
	actionDoneMsg struct {
		action string
		name   string
		err    error
		warn   string
	}
	// provisionOutputMsg is one chunk of streamed output from ONE job. It used to
	// be a bare string — which is precisely why only one job could ever exist: a
	// chunk that does not say which run it came from can only be appended to a
	// single global buffer. The jobKey (VM + kind) is what lets N jobs stream at
	// once, each into its own log — including a VM's build and a copy against that
	// same VM, which are two runs and must not share a buffer.
	provisionOutputMsg struct {
		job   jobKey
		chunk string
	}
	// provisionDoneMsg signals that ONE job's goroutine finished. Keyed for the
	// same reason.
	provisionDoneMsg struct {
		job jobKey
		err error
	}
)

// listCmd loads the VM list off the Update goroutine, and measures each VM's
// real disk consumption here in the command — a blocking per-VM stat that must
// NOT run in Update, so an unresponsive mount (stale NFS, sleeping USB, autofs)
// can't stall the Bubble Tea event loop. A non-positive result leaves DiskUsed
// empty so the cell renders blank.
//
// The tile's up/last-used times are sampled here for THE SAME REASON, and they were
// not: the tile computed them inside View, so every frame ran up to three os.Stat
// calls per tile against the Lima instance dir. A building board re-renders ~10x a
// second for its spinner, so a three-VM fleet was issuing ~90 stat syscalls per
// second on the Bubble Tea goroutine — and one stale mount would have stalled the
// whole UI, which is precisely the hazard the comment above already forbids.
func listCmd(cli *lima.Client) tea.Cmd {
	return func() tea.Msg {
		vms, err := cli.List()
		if err == nil {
			for i := range vms {
				if n := diskUsedBytes(vms[i].Dir); n > 0 {
					vms[i].DiskUsed = strconv.FormatInt(n, 10)
				}
				if vms[i].Status == limaRunning {
					if t, ok := upSince(vms[i].Dir); ok {
						vms[i].UpSince = t
					}
				} else if t, ok := lastUsed(vms[i].Dir); ok {
					vms[i].LastUsed = t
				}
			}
		}
		return vmsLoadedMsg{vms: vms, err: err, hostMem: hostMemBytesFn(), hostDiskFree: hostDiskFreeFn()}
	}
}

// startCmd boots a stopped VM and then writes its host-stored secrets into
// the guest. A secrets failure is reported as a warning, not a failure: a VM
// that is up without its secrets is more useful than one reported as
// failed-to-start. If Start itself fails, ApplySecrets is never attempted.
//
// Note: a VM started outside sand (a bare `limactl start`) does not get
// freshly applied secrets — it sources whatever secrets.env was last written
// by a previous sand-initiated start (or none, if there never was one).
func startCmd(cli *lima.Client, name, user string, scopes map[string]map[string]string) tea.Cmd {
	return func() tea.Msg {
		if err := cli.Start(name); err != nil {
			return actionDoneMsg{action: "start", name: name, err: err}
		}
		warn := ""
		if err := provision.ApplySecrets(context.Background(), cli, name, user, scopes, io.Discard); err != nil {
			warn = "secrets not applied: " + err.Error()
		}
		return actionDoneMsg{action: "start", name: name, warn: warn}
	}
}

// stopCmd shuts a running VM down.
func stopCmd(cli *lima.Client, name string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{action: "stop", name: name, err: cli.Stop(name)}
	}
}

// restartCmd stops then starts a VM, surfacing the first failure, and applies
// secrets after a successful start — same warn-not-fail semantics as startCmd.
// This is not redundant with startCmd: restartCmd drives cli.Stop/cli.Start
// directly rather than re-dispatching startCmd, so it would otherwise skip
// the apply step entirely.
func restartCmd(cli *lima.Client, name, user string, scopes map[string]map[string]string) tea.Cmd {
	return func() tea.Msg {
		if err := cli.Stop(name); err != nil {
			return actionDoneMsg{action: "restart", name: name, err: err}
		}
		if err := cli.Start(name); err != nil {
			return actionDoneMsg{action: "restart", name: name, err: err}
		}
		warn := ""
		if err := provision.ApplySecrets(context.Background(), cli, name, user, scopes, io.Discard); err != nil {
			warn = "secrets not applied: " + err.Error()
		}
		return actionDoneMsg{action: "restart", name: name, warn: warn}
	}
}

// applySecretsCmd writes name's stored secrets into the guest without
// starting or stopping anything. It backs two seams:
//
//   - The create/reset follow-up: createVM and Reset each end with their own
//     StartStreaming (so the VM is already up by the time provisionDoneMsg
//     fires), and by then a create-form GH_TOKEN has just landed in the store —
//     so this pushes it in rather than waiting for the VM's *next* start.
//   - The secrets editor's save path (updateSecrets, secrets.go), gated on the
//     VM being Running.
//
// Unlike startCmd/restartCmd — where a failed apply is a non-fatal warning
// because the VM itself already started/stopped successfully — this command's
// entire job IS the apply, so its failure is reported as a real error
// (actionDoneMsg.err), not swallowed into a warning next to a false "ok".
func applySecretsCmd(cli *lima.Client, name, user string, scopes map[string]map[string]string) tea.Cmd {
	return func() tea.Msg {
		err := provision.ApplySecrets(context.Background(), cli, name, user, scopes, io.Discard)
		return actionDoneMsg{action: "apply secrets", name: name, err: err}
	}
}

// secretsFor returns the guest user and the VM's full scope map (global plus
// any directory-scoped secrets), defaulting the user to the host username
// when the VM has no recorded config (mirroring openResetForm's fallback in
// detail.go).
func (m model) secretsFor(name string) (user string, scopes map[string]map[string]string) {
	user = vm.HostUser()
	if cfg, ok := m.reg.Config(name); ok && cfg.User != "" {
		user = cfg.User
	}
	return user, m.sec.GetAll(name)
}

// deleteCmd force-removes a VM.
func deleteCmd(cli *lima.Client, name string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{action: "delete", name: name, err: cli.Delete(name, true)}
	}
}

// stopAllCmd stops each named VM in turn, accumulating failures. Stopping is
// sequential rather than concurrent: limactl stop is I/O-heavy, lima.Client
// gives no concurrency guarantees, and a serial loop yields a deterministic
// error report. VMs that stop successfully stay stopped even if a later one
// fails.
//
// name carries a count rather than a single VM name (there is no single VM
// here): model.go's actionDoneMsg handler builds its status label as
// `action + " " + name`, so passing a descriptive count keeps that label
// readable ("stop all (3 VMs) ok") instead of leaving a trailing space.
func stopAllCmd(cli *lima.Client, names []string) tea.Cmd {
	return func() tea.Msg {
		var failed []string
		for _, n := range names {
			if err := cli.Stop(n); err != nil {
				failed = append(failed, n)
			}
		}
		var err error
		if len(failed) > 0 {
			err = fmt.Errorf("could not stop: %s", strings.Join(failed, ", "))
		}
		return actionDoneMsg{action: "stop all", name: fmt.Sprintf("(%d VMs)", len(names)), err: err}
	}
}

// shellCmd opens a shell into name's persistent guest tmux session (built
// from lima.AttachArgv — the one seam that knows tmux exists on the guest
// side; this function constructs no guest tmux command of its own) rooted at
// guestHome (lima.GuestHome), so no shell greets the user with `bash: cd: …
// No such file or directory`.
//
// It branches on whether the TUI is ITSELF already running inside host tmux:
//
//   - $TMUX unset (the common case — a plain terminal, an IDE pane, an SSH
//     session): this is the only branch that suspends anything. A tmux
//     CLIENT needs a real TTY, which only tea.ExecProcess can hand it — it
//     releases the terminal, blocks until the child exits, then restores the
//     TUI. The TUI resumes on detach (C-a d) or exit either way.
//   - $TMUX set: instead of suspending, open a new HOST window with `tmux
//     new-window` and let it run in the background. This is an ORDINARY
//     tea.Cmd, deliberately NOT tea.ExecProcess: `tmux new-window` returns in
//     milliseconds, and wrapping a command that fast in tea.ExecProcess would
//     tear down the alt-screen and release input to suspend the TUI for
//     essentially no time at all — defeating the entire point of the branch,
//     which is to keep the live board and its in-flight job progress bars on
//     screen next to the new shell. The new window re-enters through `sand
//     shell <name>` rather than reaching into lima directly, so the fast path
//     and a user typing the command by hand are the exact same code.
func shellCmd(name, guestHome string) tea.Cmd {
	if hostInTmux() {
		return hostTmuxWindowCmd(name)
	}

	argv := lima.AttachArgv(name, guestHome)
	c := exec.Command(argv[0], argv[1:]...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return actionDoneMsg{action: "shell", name: name, err: err}
	})
}

// hostInTmux reports whether the TUI is itself running inside a host tmux
// session, which is what decides between shellCmd's two branches. It is one
// function rather than two reads of the environment because the `S` verb's log
// line has to describe the branch that actually fired: two independent checks
// would let the copy drift into telling the user the board stayed live while the
// TUI was in fact suspending.
func hostInTmux() bool { return os.Getenv("TMUX") != "" }

// runHostTmuxNewWindow runs `tmux new-window shellCommand` on the host.
// Indirected through a package-level var — the same seam header.go's
// hostMemBytesFn/hostDiskFreeFn use — so a test can substitute a fake
// without ever driving a real tmux server: this package must not require one
// in a unit test, the same reason no test here may require a real limactl.
var runHostTmuxNewWindow = func(shellCommand string) error {
	return exec.Command("tmux", "new-window", shellCommand).Run()
}

// hostTmuxWindowCmd is shellCmd's fast-path branch: an ordinary (non-
// suspending) tea.Cmd that opens name's shell in a new HOST tmux window. See
// shellCmd's doc comment for why this must never be wrapped in
// tea.ExecProcess.
func hostTmuxWindowCmd(name string) tea.Cmd {
	return func() tea.Msg {
		self, err := os.Executable()
		if err != nil {
			self = "sand" // last resort: PATH lookup: os.Executable rarely fails
		}
		err = runHostTmuxNewWindow(hostTmuxShellCommand(self, name))
		return actionDoneMsg{action: "shell", name: name, err: err}
	}
}

// hostTmuxShellCommand builds the single shell-command string `tmux
// new-window` runs in its new window. tmux hands that string to $SHELL -c,
// so self (the running binary's own resolved path — NOT the bare word
// "sand", which may not be on PATH for an absolute invocation or a `go run`)
// and name are each quoted as one POSIX shell word rather than joined with a
// bare space: a resolved binary path is not guaranteed to be space-free.
func hostTmuxShellCommand(self, name string) string {
	return shQuote(self) + " shell " + shQuote(name)
}

// shQuote quotes s as a single POSIX shell word.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
