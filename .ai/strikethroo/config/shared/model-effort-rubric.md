# Model & Effort Rubric — Right-Sizing Each Task

Strikethroo executes every task in its own subagent. Two independent knobs
control how much capability and reasoning each subagent gets, so cost tracks
task difficulty instead of paying a flat maximum for everything:

- **`model`** — the capability/cost tier. Set **per task** via the Task tool's
  per-call `model` override. Higher tiers cost more per token.
- **`effort`** — the reasoning depth / token budget. There is **no per-call
  effort override**; effort is fixed by the frontmatter of the worker
  subagent that runs the task (`st-worker-low|medium|high|xhigh`). The
  dispatcher selects the worker whose effort matches the task.

Both values are written into each task's frontmatter at task-generation time
(see `POST_TASK_GENERATION_ALL.md`) and consumed at dispatch time (see
`PRE_TASK_ASSIGNMENT.md`).

## The tiers

**Model** (capability, cheapest → most expensive):

- `haiku` — fast and cheap. Mechanical, low-ambiguity work.
- `sonnet` — the default. Handles the large majority of implementation tasks.
- `opus` — most capable and most expensive. Reserve for genuinely hard
  reasoning: novel algorithms, architecture, subtle correctness.

**Effort** (reasoning depth): `low` → `medium` → `high` → `xhigh`.

## Assignment table

Pick the row that best matches the task, then apply the guardrails below.

| Task profile | `complexity_score` | `model` | `effort` | Worker |
| --- | --- | --- | --- | --- |
| Trivial / mechanical: docs, comments, config, renames, formatting, simple CRUD, boilerplate | 1–3 | `haiku` | `low` | `st-worker-low` |
| Standard implementation: one clear domain, straightforward logic, most tests | 4–6 | `sonnet` | `medium` | `st-worker-medium` |
| Complex: multi-component, non-trivial logic, integration points, tricky edge cases | 7–8 | `sonnet` | `high` | `st-worker-high` |
| Very hard / high-risk: novel algorithms, architecture, concurrency, security- or data-migration-sensitive | 9–10 | `opus` | `xhigh` | `st-worker-xhigh` |

Tasks often carry no `complexity_score` (it is only required when > 4). When it
is absent, judge the profile from the task's Objective, `skills`, and
Technical Requirements and pick the matching row.

## Guardrails (override the table)

1. **Default to the middle.** When a task is ambiguous or you cannot confidently
   place it, use `sonnet` + `medium`. Never leave `model`/`effort` unset.
2. **Cheapest that can succeed.** Bias toward the lowest tier that will
   plausibly complete the task correctly. Cost is a first-class constraint.
3. **Escalate effort before model.** If a task needs more *careful thinking* but
   not more raw capability, raise `effort` first (it is cheaper than jumping to
   `opus`). Reach for `opus` only when the task genuinely needs stronger
   reasoning or synthesis.
4. **Don't pair `haiku` with `high`/`xhigh`.** High effort on a low-capability
   model has poor returns. If a task truly needs high effort, it needs at least
   `sonnet`.
5. **Risk floor — never cut corners on these.** Regardless of size, tasks that
   are security-sensitive, perform data migrations, touch auth, handle money,
   involve concurrency, or serve as a verification/quality gate get **at least
   `sonnet` + `high`**. A cheap model rubber-stamping a gate defeats the gate.
6. **Match the worker to the effort.** The dispatcher must run the task in the
   `st-worker-*` whose tier equals the task's `effort`, and pass the task's
   `model` as the per-call override. If the exact effort worker is unavailable,
   round **up** to the next available tier, never down.
