---
id: 12
group: "verification"
dependencies: [6, 10, 11]
status: "completed"
created: 2026-07-12
model: "opus"
effort: "xhigh"
complexity_score: 9
complexity_notes: "The plan's final gate. It must drive the real application against real Lima VMs and produce evidence, not reasoning. It is also the task most likely to be rationalized away — 'the tests pass' is exactly the claim that has already been wrong twice in this subsystem."
skills:
  - go
  - verification
---
# Verification and demo: drive the real app, capture the evidence

## Objective

Execute the plan's **Self Validation** section in full, against the real application and real Lima VMs
on this host. Produce **evidence** — captured output and screenshots — for every step.

**Nothing in this task may be marked satisfied by reasoning about the code.** The plan's bar is that
the end state is *demoable to a team*, and the reason that bar exists is that this subsystem has now
shipped broken **twice** past a green test suite.

## Skills Required

`go`, `verification` — driving a TUI in a real terminal, capturing screens, and reading output honestly.

## Acceptance Criteria

Each item requires **captured evidence** (command output, or a screen capture of the real TUI). Paste it.

- [ ] **1. Race detector.** `go test -race ./...` — full output captured. **Any race is a blocker, not
      a flake.**
- [ ] **2. The real-Lima e2e suite** (`LIMA_E2E=1 go test -tags limae2e -timeout 45m -run TestE2E ./...`)
      runs against actual VMs — output captured. It must cover the heartbeat parse, the heartbeat
      terminating cleanly when its VM is stopped underneath it, `last used` after a real stop, two VMs
      provisioning concurrently, **the secret edited and saved on a running VM and read back from inside
      the guest without a restart**, and **a deliberately failed provision rendering `Failed`, not a green
      "Running"**. Create the demo VMs with **modest explicit CPU and memory** — the host has 15GiB and the
      base default is 8GiB, so two concurrent builds at the default **will not fit**.
- [ ] **3. Drive the real application**, with a capture at each step:
      - **a.** Launch `sand`. One tile per **managed** VM, coloured by state; **no** architecture line,
        **no** base-image line, **no** managed badge (all three are uniform across the fleet).
        `claude-base` has **no tile**. The header reports the hidden count. **`f` does nothing** — the
        toggle is gone.
      - **b.** Press `n`, create a VM. A new tile appears with a **building** badge and an in-place
        progress bar showing the Ansible role and task count — and **the full-screen Ansible log does not
        take over the terminal**.
      - **c.** **While that VM is still building**, arrow to another tile and start a different VM. The
        board stayed responsive to **every** keypress and both jobs progress independently.
      - **d.** Watch a running VM's cpu and mem gauges for at least one refresh interval — **the numbers
        move**. Generate load inside the guest; **the cpu gauge responds**.
      - **e.** A stopped VM shows **no** cpu/mem gauge — **absent, not zeroed** — and shows a `last used`
        line with a plausible value.
      - **f.** Stop a VM whose tile is **focused**. The tile does **not** move; the focus ring is still on
        that same VM; **the footer updates** (Stop disappears, Start appears). Press `x` on the
        now-stopped VM — **nothing happens**.
      - **g.** **Break a provision on purpose** (e.g. an unreachable clone URL). The tile settles into a
        red **Failed** state and **stays** there — never falling back to a reassuring green "Running", and
        still visible after a refresh tick. Then, **without re-running the build**, reopen that run's log
        from the tile and read the Ansible task that actually failed. Navigate away and back — the log is
        still reachable.
      - **h.** On a VM that is **RUNNING and stays running**, open the secrets editor, type a secret, save
        with `ctrl+s`. **Without restarting the VM**, shell in and read the value back from the guest's
        environment. **This is the exact step that fails on the pre-fix code**, so it is the one that
        proves the fix. Then paste a CRLF-terminated buffer and confirm no value carries a trailing `\r`.
      - **i.** Press `/`, type a fragment of one VM's name — the board narrows, and typed keys do **not**
        fire verbs. `esc` returns the full board with focus **still on the same VM**. With a filter active,
        press `X` — it stops **every** managed VM, not only the visible ones.
      - **j.** Resize from wide down to exactly **80×24** and back. The board reflows to a single tile
        column and stays usable, coloured, and navigable at every size — **no "terminal too small" state at
        any point**.
      - **k.** At 80×24, with more VMs than fit on screen, arrow past the bottom of the viewport. The grid
        **scrolls** and the focus ring **stays visible** rather than getting trapped at the edge.
- [ ] **4. `NO_COLOR`.** Run `sand` with `NO_COLOR` set — every status remains distinguishable by **glyph
      and text label alone**.
- [ ] **5. Idle gating.** Leave `sand` open and idle on the board: it draws **no measurable CPU** and holds
      **no heartbeat connections**. Measure it (`top`/`ps`, and check for the `limactl shell` processes).
- [ ] **6. The metaphor grep.** `git diff main...HEAD` and grep for **harbour, harbor, slip, boat, pier,
      moored, deck, cargo** across identifiers, comments, and user-visible strings. **Zero hits.**
- [ ] **Cleanup gate** (per `POST_EXECUTION.md`): no dead code, no tech debt, no backwards-compatibility
      shims left behind. Confirm `list.go`, `defaultKeys`, `viewHelp`, `m.managedOnly`, `m.status`,
      `m.running`, and `secretsChrome` are all **gone**.
- [ ] All 12 tasks have `status: "completed"` in their frontmatter.

## Technical Requirements

- Driving the TUI: run `sand` in a real pty. `tmux` is the pragmatic tool — `tmux new-session -d`,
  send keys, `tmux capture-pane -p` (and `-e` to keep colour) for the capture. Resize with
  `tmux resize-window -x 80 -y 24`. This gives real, reproducible captures rather than screenshots of a
  terminal you cannot script.
- The test host: **16 cores, 15GiB RAM**, limactl **2.1.3**. The base VM's default allocation is 8 CPUs /
  8GiB — **two concurrent builds at the default will not fit in RAM**. Create demo VMs with modest explicit
  CPU/memory; the create form takes both.
- Clean up every VM this task creates.

## Input Dependencies

- Task 06 — the secrets fix and its e2e (step 3h and self-validation step 2 depend on it).
- Task 10 — the real-Lima e2e suite.
- Task 11 — the docs (the cleanup gate checks them too).

## Output Artifacts

- A verification report with the captured evidence for every step above.
- A list of anything that **failed** — reported honestly, not smoothed over.

## Implementation Notes

<details>
<summary>Guidance</summary>

**Read the anti-rationalization table before you start.**

| You catch yourself thinking… | The binding rule |
| --- | --- |
| "The tests pass, so the demo will work." | That claim has been wrong **twice** in this subsystem. The suite is not the evidence; the running application is. Drive it. |
| "I can see from the code that the tile renders Failed." | Reading the code is not running it. Break a provision for real and look at the tile. |
| "The gauges probably move." | "Probably" is a red flag. Watch them for a refresh interval, generate load, and capture the change. |
| "Step 3h is basically covered by the e2e test." | It is the **one step that fails on the pre-fix code**. Do it by hand, in the real editor, on a real running VM, and read the value back from inside the guest. |
| "The NO_COLOR / idle / grep steps are formalities." | They are three of the plan's success criteria. Run them. |

Per the verification gate: **identify** the command or signal that proves each claim, **run** it fresh
now, **read** the full output and exit code, **verify** it matches the claim, and only then state the
result. Never rely on an earlier run or on another task's report.

**If something fails, say so and stop.** Do not fix-and-hide, and do not soften an acceptance criterion
until it passes. A failure here is exactly the signal this task exists to produce, and the plan's whole
premise is that the last two releases shipped because nobody produced it. Report the failure, with its
evidence, and halt for direction.

Note for the orchestrator, **not** for this agent to act on: the plan's success criteria also call for
Renovate PRs **#22, #23, and #24** (bubbletea/bubbles/lipgloss v2 bumps) to be **closed as subsumed** by
task 01's migration. That is an outward-facing action on a real GitHub repository — **do not close them**.
Surface it in the completion report so the user can decide.
</details>
