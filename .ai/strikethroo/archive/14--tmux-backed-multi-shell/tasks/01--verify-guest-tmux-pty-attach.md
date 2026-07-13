---
id: 1
group: "guest-attach"
dependencies: []
status: "completed"
created: 2026-07-13
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Empirical probe against a real VM that decides the implementation shape of every later task; must be run under a real PTY, which an agent shell does not have by default."
skills:
  - lima
  - tmux
---
# Verify the `limactl shell` PTY assumption and settle the attach mechanism

## Objective

Answer, against a **real running sand VM**, the single highest-risk unknown in
plan 14: does `limactl shell <name> <command>` allocate a PTY, such that
`tmux new-session` attaches instead of dying with `open terminal failed: not a
terminal`? Record the verified answer, and the resulting decision (use `limactl
shell`, or fall back to `ssh -t` against Lima's per-instance `ssh.config`), in
the plan document so tasks 2–4 build on evidence rather than an assumption.

Nothing else in this plan may be implemented until this task completes.

## Skills Required

- `lima` — `limactl shell` flags (`--workdir`), per-instance `ssh.config` at
  `~/.lima/<name>/ssh.config`, instance lifecycle.
- `tmux` — session creation/attachment, and driving tmux headlessly so a probe
  can run from a non-interactive agent shell.

## Acceptance Criteria

- [x] A real sand VM is running and is the probe target. `limactl list` shows it
      `Running`.
- [x] The probe is executed **under a real PTY** (an agent's shell has none — see
      Implementation Notes for the two supported ways to get one) and its verdict
      is captured as literal terminal output, not inferred.
- [x] Running the equivalent of `limactl shell <vm> tmux new-session -A -s probe`
      under that PTY is observed to either (a) attach — a tmux status bar is
      visible in captured pane output — or (b) fail with `open terminal failed:
      not a terminal`. The captured output is pasted into the plan.
- [x] If (b), the `ssh -t -F ~/.lima/<vm>/ssh.config lima-<vm> …` fallback is
      probed the same way and shown to attach, so the plan has a known-good path.
      **N/A — result was (a), attach succeeded, so the fallback was not probed.**
- [x] `limactl shell --help` output confirming a `--workdir` flag exists is
      captured (it is depended on by task 4).
- [x] The **Change Log** section of
      `.ai/strikethroo/plans/14--tmux-backed-multi-shell/plan-14--tmux-backed-multi-shell.md`
      gains a dated entry stating: the verdict, the captured evidence, the chosen
      mechanism (`limactl shell` or `ssh -t`), and the name of the probe VM left
      running for tasks 4 and 6.
- [x] The probe leaves no orphaned guest tmux sessions: `limactl shell <vm> tmux
      kill-session -t probe` (and any grouped probes) run at the end, and
      `limactl shell <vm> tmux list-sessions` reports no `probe` session.

## Technical Requirements

- Host has `limactl` (`/usr/local/bin/limactl`) and `tmux` (`/usr/bin/tmux`).
- Two Lima instances exist and are **Stopped**: `claude-base` (sand's base image
  — do **not** start it; it is the clone source) and `zoom-drive-uploader` (a
  user VM — do not touch it).
- Therefore the probe needs its own VM. Prefer creating a throwaway one:
  `go run ./cmd/sand create --name sand-tmux-probe` (clones `claude-base`, then
  runs the Ansible `user` role — the guest ends up with tmux, `~/.tmux.conf`,
  `ssh_rc`, and linger already provisioned). Take the defaults; the host git
  identity is configured.
- The guest tmux config uses prefix `C-a` and lives at `~/.tmux.conf`
  (`roles/user/templates/tmux.conf.j2`).

## Input Dependencies

None. This is the first executable step of the plan.

## Output Artifacts

- A running probe VM (record its name) reused by tasks 4 and 6.
- A dated Change Log entry in the plan document recording the verdict, the
  evidence, and the chosen attach mechanism. **Tasks 2, 3 and 4 read this entry
  to know which command they are building.**

## Implementation Notes

<details>
<summary>How to probe a PTY-requiring command from an agent shell</summary>

Your Bash tool has **no controlling terminal**. Running `limactl shell <vm> tmux
new-session` directly will report `not a terminal` *even if limactl does pass
`-t` to ssh* — the failure would be your own shell's, not Lima's, and you would
draw exactly the wrong conclusion and send this whole plan down the `ssh -t`
fallback for no reason. **Do not run the probe bare.** Fabricate a PTY. Two ways,
either is acceptable:

**A. `script(1)`** — simplest:

```bash
script -qec "limactl shell sand-tmux-probe tmux new-session -A -s probe -d; \
             limactl shell sand-tmux-probe tmux ls" /dev/null
```

For the *interactive* attach question specifically, run the attach itself under
`script` with a short-lived guest session and capture what lands on the pty:

```bash
script -qec "limactl shell sand-tmux-probe tmux new-session -A -s probe \
             'sleep 3; exit'" /dev/null | cat -v | head -20
```

A working attach emits terminal escape sequences and a status line; a broken one
emits the single line `open terminal failed: not a terminal`.

**B. Host tmux (preferred — it is also the harness task 6 will want).** Use a
**private tmux socket** so you never touch the user's own tmux server, and
**never** run `tmux kill-server`:

```bash
S="-L sandprobe"                       # private socket; user's sessions untouched
tmux $S new-session -d -s probe -x 200 -y 50 \
  "limactl shell sand-tmux-probe tmux new-session -A -s probe"
sleep 5
tmux $S capture-pane -p -t probe       # <-- the evidence. Paste this into the plan.
tmux $S kill-session -t probe          # kill by NAME only
```

If the capture shows a shell prompt plus a tmux status bar (a green bar naming
window `0`), `limactl shell` **does** allocate a PTY and the plan's primary path
is confirmed. If it shows `open terminal failed: not a terminal`, the fallback is
in play.
</details>

<details>
<summary>The fallback, if `limactl shell` does not allocate a PTY</summary>

Lima writes a per-instance ssh config at `~/.lima/<name>/ssh.config`; the repo
already parses it (`internal/ui/transfer.go`). It defines a host entry named
`lima-<name>`. The fallback puts `-t` under our control:

```bash
ssh -t -F ~/.lima/sand-tmux-probe/ssh.config lima-sand-tmux-probe \
    'tmux new-session -A -s probe'
```

Probe it under the same PTY harness. If this works and `limactl shell` does not,
say so plainly in the plan Change Log — task 2's builder will then emit `ssh`
argv instead of `limactl` argv, and task 4's `--workdir` fix becomes an `ssh`
`cd` in the remote command instead of a `--workdir` flag.
</details>

<details>
<summary>What to write into the plan</summary>

Append to the `### Change Log` section at the end of the plan document, in the
same style as the existing entries:

```markdown
- **2026-07-13 — PTY question settled (task 01).** `limactl shell <vm> tmux
  new-session -A -s probe`, run under a real PTY (<how>), <attached cleanly /
  failed with `open terminal failed: not a terminal`>. Evidence:

  ```
  <pasted capture-pane output>
  ```

  **Decision: the attach command builder emits `<limactl … | ssh -t …>` argv.**
  `limactl shell --help` confirms `--workdir` exists. Probe VM `sand-tmux-probe`
  is left **running** for tasks 4 and 6; delete it when the plan completes.
```

Be honest about what you observed. If the result is ambiguous, say it is
ambiguous and probe again a different way rather than picking the answer that
makes later tasks easier. This entry is the evidence three downstream tasks
build on.
</details>
