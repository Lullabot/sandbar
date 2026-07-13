---
id: 6
group: "validation"
dependencies: [3, 4]
status: "completed"
created: 2026-07-13
model: "sonnet"
effort: "high"
complexity_score: 8
complexity_notes: "Verification gate (risk floor: sonnet+high minimum). Must drive PTY-requiring interactive attachment headlessly, and must assert the destroy-unattached asymmetry that, if wrong, silently eats user work."
skills:
  - go
  - tmux
---
# Real-VM validation: persistence, grouped sessions, and the heartbeat

## Objective

Prove — against a real VM, with assertions rather than eyeballs — that the
feature does what the plan promises: the attach works, work **survives detach**,
a second terminal gets an independent window without clamping, the grouped
session cleans itself up **without taking `main` with it**, and the board's live
gauges keep updating underneath. Land the durable parts as `limae2e`-tagged Go
tests, which the plan names as the correct home for this coverage.

This is a verification gate. A subagent's report is not evidence; captured
command output and exit codes are.

## Skills Required

- `go` — `limae2e` build-tagged tests (see `internal/ui/lima_e2e_test.go`, gated
  on `LIMA_E2E=1`).
- `tmux` — driving a PTY-requiring attach headlessly, and reading `tmux
  list-sessions` output to assert grouping.

## Acceptance Criteria

Each criterion below maps to a numbered step of the plan's **Self Validation**
section. Record the actual command and its actual output for each.

- [ ] **Attach (criteria 1-2).** Under a real PTY, the merged `S`/`sand shell`
      path attaches to guest tmux: a status bar is visible in captured pane
      output, and the guest reports a session named `main`.
- [ ] **Persistence — the headline claim (criterion 3).** Start a marker process
      in the session (`sh -c 'sleep 600 # sand-marker'`). Detach. Then confirm
      `limactl shell <vm> pgrep -af 'sleep 600'` **still lists it**. Re-attach and
      confirm the window is still there.
- [ ] **Grouped-session independence (criterion 4).** With one client attached,
      attach a second via `sand shell <vm>`. Confirm `limactl shell <vm> tmux
      list-sessions` shows **exactly two** sessions, that they share a window set
      (grouped), and that switching the second client's current window does not
      move the first.
- [ ] **Cleanup does not eat the user's work (criterion 4/5) — the asymmetric
      risk.** Detach the second client. Assert `tmux list-sessions` shows the
      grouped `sand-*` session is **GONE** and that **`main` is still present and
      still holds the marker process**. This must be an explicit assertion on
      `list-sessions` output, never a glance. Reversing `destroy-unattached`
      destroys user work with no error message.
- [ ] **Host-tmux fast path (criterion 5).** From inside a host tmux session,
      launch the TUI, focus a running VM's tile, press `S`. Confirm a new **host**
      window opens with the guest session in it and the TUI is **still running and
      responsive** in its original window (not suspended, not corrupted).
- [ ] **Heartbeat survives an attach (criterion 8).** With a tmux session attached
      to a running VM, confirm the VM's tile CPU/memory gauges still update over
      ~30s, and keep updating after detach. The guest tmux server must not disturb
      the heartbeat's own long-lived `limactl shell` stream.
- [ ] **The workdir fix (criterion 6).** From a host directory that does not exist
      in the guest (e.g. `mkdir -p /tmp/nope-$$ && cd`), run `sand shell <vm>` and
      confirm the first line of output is the shell/tmux — **no `bash: cd:` error**.
- [ ] **Error paths (criteria 7, 9).** `sand shell <stopped-vm>` gives the clear
      "not running" message, not a raw `limactl` error; `sand shell <bogus>` fails
      cleanly; `sand bogus` prints a usage string listing `shell`. In the TUI, `S`
      is absent from the footer for a stopped VM and for a mid-build VM, and
      pressing it does nothing.
- [ ] **Build gates (criterion 10).** `go build ./cmd/sand`, `gofmt -l .` (empty),
      `go vet ./...`, `go test ./...` all pass, and `go test ./internal/ui`
      **without `-update`** leaves the board goldens unchanged.
- [ ] The durable checks are committed as `limae2e`-tagged tests (persistence,
      grouped-session count, and the `destroy-unattached` asymmetry are the three
      worth automating). Tests without the tag must **not** require a real
      `limactl` (AGENTS.md, hard rule).
- [ ] The probe VM created by task 1 is deleted at the end (`limactl delete -f
      sand-tmux-probe`), and `claude-base` / `zoom-drive-uploader` are left exactly
      as they were found (Stopped, untouched).

## Technical Requirements

- Reuse the running probe VM from task 1 rather than creating another.
- `limae2e` conventions: `//go:build limae2e`, skip unless `LIMA_E2E=1`; run with
  `LIMA_E2E=1 go test -tags limae2e -timeout 45m ./internal/ui/`.
- Host tmux is at `/usr/bin/tmux`.

## Input Dependencies

- Task 3 (`sand shell`) and task 4 (the TUI `S` verb) — both must be merged and
  building. Task 1's probe VM.

## Output Artifacts

- `limae2e`-tagged tests covering attach, persistence, and grouped-session
  lifecycle.
- A written validation record — command, output, verdict per criterion — for the
  orchestrator's POST_EXECUTION gate.

## Implementation Notes

<details>
<summary>Driving an interactive attach from an agent shell (you have no TTY)</summary>

Your Bash tool has **no controlling terminal**, so a bare `sand shell <vm>` will
fail with `not a terminal` for reasons that have nothing to do with the code
under test. Fabricate a PTY with host tmux, which doubles as the harness for the
multi-client assertions.

**Use a private tmux socket (`-L`) — the user has their own tmux sessions running
on the default socket. NEVER run `tmux kill-server`. Kill only by session name.**

```bash
T="tmux -L sandval"                                    # private socket

# client 1: attach, leave a marker running
$T new-session -d -s c1 -x 200 -y 50 "sand shell $VM"
sleep 5
$T send-keys -t c1 "sleep 600 # sand-marker" Enter
$T capture-pane -p -t c1 | tail -3                     # evidence: status bar present

# client 2: a SECOND, independent attach
$T new-session -d -s c2 -x 120 -y 30 "sand shell $VM"
sleep 5
limactl shell $VM tmux list-sessions                   # EXPECT exactly 2: main + sand-<pid>

# independence: move client 2's window, client 1 must not follow
$T send-keys -t c2 C-a c                               # new window in the grouped session
$T capture-pane -p -t c1 | tail -1                     # client 1 still on its own window

# cleanup semantics — the asymmetric risk, asserted:
$T kill-session -t c2                                  # detach client 2 (kill by NAME)
sleep 2
limactl shell $VM tmux list-sessions                   # EXPECT: sand-* GONE, main PRESENT
limactl shell $VM pgrep -af 'sleep 600'                # EXPECT: marker STILL RUNNING

# persistence across full detach:
$T kill-session -t c1
sleep 2
limactl shell $VM pgrep -af 'sleep 600'                # EXPECT: STILL RUNNING. This is the plan.
```

Every `EXPECT` above is an assertion. If any of them comes out the other way —
especially the last two — **stop and report it**. A `main` session that vanishes
on detach means `destroy-unattached` landed on the wrong session, which is the
single worst bug this plan can ship: it silently converts "your work survives a
closed laptop" into "your work dies when you look away".
</details>

<details>
<summary>Heartbeat and the TUI paths</summary>

For the heartbeat check (criterion 8), run the TUI itself inside the private-socket
tmux, attach a guest session in a second window, and `capture-pane` the TUI window
twice ~30s apart: the focused VM's CPU/memory gauges must differ (they are live
samples). The concern is that the guest tmux **server** could fight the
heartbeat's own long-lived `limactl shell` stream for the SSH connection budget
or trip its cooldown/retry logic. The design says they should not collide by
construction — this step is where that is *observed* rather than assumed.

For the host-tmux fast path (criterion 5), run the TUI inside a window on the
private socket (so `$TMUX` is set for it), `send-keys` the arrow keys to focus a
running VM's tile and then `S`, and assert with `tmux -L sandval list-windows`
that a **new window appeared** and that `capture-pane` on the TUI's original
window still shows a live, uncorrupted board.
</details>

<details>
<summary>Testing philosophy for the tests you commit</summary>

Write a few tests, mostly integration. Meaningful tests verify custom business
logic, critical paths, and edge cases specific to this application — test *your*
code, not the framework or library. **Do** cover: the persistence guarantee, the
grouped-session count, and the `destroy-unattached` asymmetry — these are the
plan's core claims and each has a real failure mode. **Do not** write tests for
tmux's own behavior, Lima's own behavior, or trivial wiring. Three focused
`limae2e` tests is the right order of magnitude; a comprehensive suite is
gold-plating.
</details>
