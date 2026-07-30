// Package guestsh builds the commands that run INSIDE a sand VM, independent of
// how a backend reaches it. It is the ONLY place in sand that knows tmux exists.
//
// Every entrypoint into a guest shell — the TUI's `S` verb and `sand shell`, on
// every backend — builds its command from AttachArgv, and none constructs a tmux
// command of its own. A Lima backend wraps these argv elements in `limactl shell
// <name> …`; the Proxmox backend wraps the SAME elements in `ssh -t <ip> …`. That
// split is the whole reason this package is not part of internal/lima: a second
// hand-rolled copy of the expression below is the most destructive drift possible
// in this codebase, because a copy that drifts into setting destroy-unattached on
// `main` silently destroys the user's long-running work on detach.
package guestsh

import "regexp"

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
//
// **colortermEnv (` -e COLORTERM=<value>`, or "") is spliced into BOTH new-session
// commands** so the session — and any window later opened in it — carries the host
// terminal's COLORTERM. It MUST be `-e`, not a plain `export COLORTERM=…;` before
// the expression: tmux gives a new pane the SERVER's environment, captured when the
// server first started, and COLORTERM is not in tmux's default update-environment
// list — so an exported value is silently dropped for every attach after the one
// that happened to start the server. `-e` sets the variable on the session directly
// and survives an already-running server. Verified on a real VM (tmux 3.5a): with a
// server already up, a plain export lands as unset; `-e` lands as truecolor.
//
// What `-e` sets is the SESSION environment, which fixes the value each pane sees at
// the moment it is created. So the COLORTERM that reaches the common case — Claude
// Code running in `main` — is the one carried by whichever client first created
// `main` (the else branch). A later client attaching over the grouped branch shares
// main's ALREADY-RUNNING panes and cannot re-colour them; its own `-e` only reaches
// NEW windows it opens inside its grouped session. Two clients of different colour
// capability therefore do NOT each re-skin main's existing panes — the first client
// wins those — they only diverge on windows opened after attaching.
func guestAttachExpr(colortermEnv string) string {
	return `if tmux has-session -t =main 2>/dev/null; then s=sand-$$; tmux new-session` + colortermEnv + ` -t =main -s "$s" \; set-option -t "$s" destroy-unattached on; else tmux new-session` + colortermEnv + ` -s main; fi`
}

// AttachArgv returns the IN-GUEST half of the attach command — `bash -c <expr>`,
// three argv elements — without any host-side wrapper around it. The caller
// prefixes whatever gets it to the guest: `limactl shell <name>` for a Lima
// backend, `ssh -t <ip>` for one that reaches the guest over plain ssh.
//
// It is three elements, not one, because a host-side wrapper SHELL-ESCAPES each
// element it forwards (limactl does; ssh's re-shelling of a quoted token amounts
// to the same): passing the whole expression as one element gets it quoted into a
// single word and the guest reports `command not found`. `bash -c` is what gives
// the expression a shell to parse it.
//
// colorterm is the host process's COLORTERM (callers pass os.Getenv("COLORTERM"))
// and is set on the guest tmux session — via `tmux new-session -e` (see
// guestAttachExpr) — so Claude Code's UI keeps its 24-bit color. This is the OTHER
// half of a two-sided handshake: the guest sshd carries `AcceptEnv COLORTERM`
// (roles/base), but neither `limactl shell` nor a bare ssh forwards host
// environment, so without this the variable never leaves the host and the guest
// silently falls back to 256-color. Passing the value in (rather than reading the
// environment here) keeps this pure. An unsafe or empty value is dropped (see
// ColortermSafe), so a terminal that sets no COLORTERM — Terminal.app, say — is
// reported honestly as such rather than being told truecolor.
//
// It is pure: no globals, no I/O, no exec. That is what lets the command sand
// builds be unit-tested without a real limactl or a real ssh, which AGENTS.md
// requires. The caller execs it with a real TTY attached (tmux refuses to run
// without one).
//
// There is no workdir counterpart on purpose: `limactl shell` injects a `cd
// <host-cwd>` that the Lima wrapper's --workdir exists to override, while sshd
// starts the login shell in the guest user's own $HOME already — so the
// correction belongs to the wrapper that needs it, not here.
func AttachArgv(colorterm string) []string {
	return []string{"bash", "-c", guestAttachExpr(colortermFlag(colorterm))}
}

// colortermValue is the full set of shell-safe COLORTERM strings. Every real
// value a terminal sets — truecolor, 24bit, 1, gnome-terminal, rxvt — is letters,
// digits, dash, or underscore; nothing here can carry a shell metacharacter.
var colortermValue = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ColortermSafe reports whether a host COLORTERM value can be forwarded into a
// guest shell expression verbatim.
//
// The value is baked into a shell expression that a host-side wrapper escapes as
// a single argv element and the guest's `bash -c` then parses, so it MUST be
// shell safe. Rather than quote-and-escape arbitrary host input, an unrecognised
// value is dropped: a missing COLORTERM already means "no truecolor claim", so
// refusing to forward a malformed one degrades to exactly the honest default.
//
// It is exported for the callers that forward COLORTERM some way OTHER than
// AttachArgv's `tmux -e` — a one-shot interactive command that exports it into a
// plain login shell — so the "validate or drop" rule has one implementation.
func ColortermSafe(colorterm string) bool { return colortermValue.MatchString(colorterm) }

// colortermFlag returns the ` -e COLORTERM=<v>` fragment spliced into the guest's
// `tmux new-session` commands, or "" when there is nothing safe to forward. The
// leading space keeps the flag a separate argv word to tmux from the
// `new-session` before it.
func colortermFlag(colorterm string) string {
	if !ColortermSafe(colorterm) {
		return ""
	}
	return " -e COLORTERM=" + colorterm
}
