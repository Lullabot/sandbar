package lima

// This file is the ONLY place in sand that knows tmux exists. Both entrypoints
// into a guest shell — the TUI's `S` verb and `sand shell` — build their command
// from AttachArgv and neither constructs a tmux command of its own, for the same
// reason the two create paths both go through provision/registry: two hand-rolled
// copies of this drift, and the ways this one can drift are ugly (see below).

// guestAttachExpr is the shell expression that runs IN THE GUEST to attach a
// caller to the VM's persistent tmux session. Read it slowly; every token in it
// is load-bearing.
//
//	if tmux has-session -t =main 2>/dev/null; then
//	  s=sand-$$
//	  tmux new-session -t =main -s "$s" \; set-option -t "$s" destroy-unattached on
//	else
//	  tmux new-session -s main
//	fi
//
// **The branch is decided in the guest**, not on the host, because the host cannot
// see the guest's tmux server without a round trip that would race anyway.
//
// **The first client creates `main`**, the canonical session that holds the user's
// work. tmux finds the shipped ~/.tmux.conf itself (C-a prefix, mouse, 50k
// scrollback, splits bound to -c "#{pane_current_path}"), so no -f is passed.
//
// **Every later client gets its OWN session GROUPED against `main`** (`new-session
// -t`), not a second client on `main` itself. Two clients attached to the SAME tmux
// session are mirrored: they follow each other's window switches and the display is
// clamped to the smallest attached client, which makes a second terminal useless for
// looking at a second window — the entire point of this feature. A grouped session
// shares main's window set (same windows, same running processes) while tracking its
// own current window, and is not size-clamped.
//
// **destroy-unattached is set on the GROUPED session, on itself, and NEVER on
// `main`.** This is the single most dangerous line in the package and the two
// failure modes are wildly asymmetric:
//
//   - Omitted from the grouped session: every second attach leaks an orphan session.
//     Untidy.
//   - Set on `main`: the user's long-running work is DESTROYED the moment they
//     detach or close their terminal — silently, with no error and nothing in any
//     log. Surviving detach is the whole reason this feature exists.
//
// So the target is spelled out (`set-option -t "$s"`) rather than relying on
// set-option's implicit "current session", and TestAttachArgvDestroyUnattachedOnGroupedSessionOnly
// asserts both directions. Verified on a real VM: after this expression runs, tmux
// reports the option on the grouped session and NOT on `main`, and the grouped
// session evaporates on detach while `main` and its processes live on.
//
// **The grouped session's name is chosen in the guest** ($$, the attaching shell's
// PID) so two concurrent `sand shell` invocations cannot collide on it. A
// host-computed counter would race; a PID is allocated by the same kernel that owns
// the tmux server it is naming a session in.
//
// **`=main` is an exact-match target.** A bare `-t main` would also match a session
// whose name merely starts with "main", so a user's own `maintenance` session could
// capture the attach.
//
// Deliberately NOT `tmux new-session -A -s main` in the else branch: -A would make a
// racing second attach (both clients checking has-session before either created
// `main`) succeed as a MIRRORED client on `main`, quietly reintroducing the clamped,
// window-locked behaviour this whole design exists to avoid. The unguarded form
// fails that microsecond-wide race loudly and retryably instead, which is the better
// trade. Do not "fix" this by adding -A.
const guestAttachExpr = `if tmux has-session -t =main 2>/dev/null; then s=sand-$$; tmux new-session -t =main -s "$s" \; set-option -t "$s" destroy-unattached on; else tmux new-session -s main; fi`

// AttachArgv returns the full argv that attaches a caller to instance name's
// persistent guest tmux session (see guestAttachExpr for the tmux semantics).
// guestHome is the guest login user's home directory — Lima puts it at
// /home/<user>.guest, NOT /home/<user>, so it cannot be reconstructed from a
// username and is always passed in; internal/ui.guestHome reads it from Lima's
// generated cloud-config.yaml.
//
// It is pure: no globals, no I/O, no exec. That is what lets the command this
// package builds be unit-tested without a real limactl, which AGENTS.md requires.
// The caller execs it with a real TTY attached (tmux refuses to run without one).
//
// Three argv details, each learned against a real VM (plan 14, task 01) and each
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
//     `command not found`. bash -c is what gives the expression a shell to parse it.
//
// When guestHome is empty (Lima's cloud-config could not be read) the flag is
// omitted entirely rather than passed empty: `--workdir ""` would point limactl at
// nowhere. The cost of omitting it is only the cosmetic `bash: cd: … No such file or
// directory` this flag exists to suppress.
func AttachArgv(name, guestHome string) []string {
	// "limactl" is the same binary NewExecRunner shells out to; the interactive
	// attach deliberately bypasses Runner (which captures output) because a tmux
	// client needs the real terminal, not a pipe.
	argv := []string{"limactl", "shell"}
	if guestHome != "" {
		argv = append(argv, "--workdir", guestHome)
	}
	return append(argv, name, "bash", "-c", guestAttachExpr)
}
