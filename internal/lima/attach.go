package lima

import "github.com/lullabot/sandbar/internal/guestsh"

// This file is the LIMA wrapper around the guest shell commands: it prefixes
// `limactl shell` (and its --workdir) onto the in-guest argv internal/guestsh
// builds. The tmux expression itself — and the COLORTERM handshake that rides
// with it — lives there, not here, because a backend that reaches the guest over
// plain ssh needs the identical expression with a different wrapper, and two
// hand-rolled copies of it drift in ways that silently destroy a user's work.
// See internal/guestsh.

// AttachArgv returns the full argv that attaches a caller to instance name's
// persistent guest tmux session (see guestsh.AttachArgv for the tmux semantics).
// guestHome is the guest login user's home directory — Lima puts it at
// /home/<user>.guest, NOT /home/<user>, so it cannot be reconstructed from a
// username and is always passed in; GuestHome reads it from Lima's generated
// cloud-config.yaml.
//
// colorterm is the host process's COLORTERM (callers pass os.Getenv("COLORTERM")),
// forwarded to the guest tmux session because `limactl shell` carries NO host
// environment without --preserve-env. See guestsh.AttachArgv.
//
// It is pure: no globals, no I/O, no exec. That is what lets the command this
// package builds be unit-tested without a real limactl, which AGENTS.md requires.
// The caller execs it with a real TTY attached (tmux refuses to run without one).
//
// Three argv details, each learned against a real VM and each
// silently fatal if a future edit "tidies" it:
//
//   - `--workdir` comes BEFORE the instance name. After it, limactl stops treating
//     it as its own flag and forwards it to the guest's login bash, which then both
//     ignores the workdir (reintroducing the `cd` papercut the flag exists to fix)
//     and chokes on the rest of the line.
//   - No `--` separator is emitted before the guest command. limactl tolerates one
//     (`limactl shell --workdir H NAME -- echo hi` prints `hi` and exits 0), so this
//     is a matter of not adding a token that buys nothing — not a hazard.
//   - The guest command is `bash -c <expr>`, three argv elements, because limactl
//     SHELL-ESCAPES each element it forwards: passing the whole expression as one
//     element gets it quoted into a single word and the guest reports
//     `command not found`. That shape is guestsh.AttachArgv's contract.
//
// When guestHome is empty (Lima's cloud-config could not be read) the flag is
// omitted entirely rather than passed empty: `--workdir ""` would point limactl at
// nowhere. The cost of omitting it is only the cosmetic `bash: cd: … No such file or
// directory` this flag exists to suppress.
func AttachArgv(name, guestHome, colorterm string) []string {
	// "limactl" is the same binary NewExecRunner shells out to; the interactive
	// attach deliberately bypasses Runner (which captures output) because a tmux
	// client needs the real terminal, not a pipe.
	argv := []string{"limactl", "shell"}
	if guestHome != "" {
		argv = append(argv, "--workdir", guestHome)
	}
	return append(append(argv, name), guestsh.AttachArgv(colorterm)...)
}

// RunArgv returns the full argv that runs one INTERACTIVE guest command with
// the caller's real TTY attached — the same `limactl shell` transport
// AttachArgv uses, but running expr in workdir instead of joining the guest's
// persistent tmux session. It is what the Landing pane's commit-and-push
// action needs: `git commit` opens the user's editor, which requires a
// terminal, so it cannot go through the captured-output Runner path.
//
// SAFETY: workdir is passed as its OWN argv element (`--workdir <dir>`), never
// spliced into expr. That matters because workdir is a checkout path
// DISCOVERED BY SWEEPING THE GUEST — the lowest-trust string in the system —
// and expr is parsed by the guest's `bash -c`. Callers must therefore keep
// expr a FIXED, literal command: anything it needs to know about the checkout
// it should compute for itself in the guest (the working directory is already
// the checkout), never receive by interpolation from the host.
func RunArgv(name, workdir, expr, colorterm string) []string {
	argv := []string{"limactl", "shell"}
	if workdir != "" {
		argv = append(argv, "--workdir", workdir)
	}
	// COLORTERM rides through the same validated-or-dropped path AttachArgv
	// uses, so an editor launched here reports the same colour capability the
	// guest's tmux session would.
	if guestsh.ColortermSafe(colorterm) {
		return append(argv, name, "bash", "-c", "export COLORTERM="+colorterm+"; "+expr)
	}
	return append(argv, name, "bash", "-c", expr)
}
