# Model & Effort Rubric — Right-Sizing Each Task

Strikethroo executes every task in its own subagent. Two independent knobs
control how much capability and reasoning each subagent gets, so cost tracks
task difficulty instead of paying a flat maximum for everything:

- **`models`** — the provider-specific model for the task. Store both
  `models.anthropic` and `models.openai`; the dispatcher uses the entry for its
  current harness.
- **`effort`** — the provider-neutral reasoning-depth tier. Both Claude Code
  and Codex use `low`, `medium`, `high`, and `xhigh`, but apply it differently:
  Claude Code selects an effort-specific `st-worker-*` agent, while Codex passes
  the effort directly when spawning the subagent.

These values are written into every task's frontmatter at task-generation time
(see `POST_TASK_GENERATION_ALL.md`) and consumed at dispatch time (see
`PRE_TASK_ASSIGNMENT.md`).

## Provider model tiers

Use this mapping as a maintained dispatch policy, not as a claim that similarly
positioned models from different providers are identical.

| Tier | Anthropic / Claude Code | OpenAI / Codex | Intended use |
| --- | --- | --- | --- |
| Economy | `haiku` | `gpt-5.6-luna` | Fast, inexpensive, mechanical work |
| Balanced | `sonnet` | `gpt-5.6-terra` | Default implementation work |
| Capable | `sonnet` | `gpt-5.6-sol` | Complex implementation and careful integration |
| Frontier | `opus` | `gpt-6-astra` | Hardest reasoning and highest-risk work |

The OpenAI choices follow the current model family's intended positions: Luna
for cost-sensitive workloads, Terra for balanced intelligence and cost, Sol for
complex professional work, and Astra for the hardest end-to-end work. Do not
replace these IDs from memory when they become unavailable; use the current
harness's advertised model list to choose the same tier and record the fallback.

## Assignment table

Pick the row that best matches the task, then apply the guardrails below.

| Task profile | `complexity_score` | `models.anthropic` | `models.openai` | `effort` |
| --- | --- | --- | --- | --- |
| Trivial / mechanical: docs, comments, config, renames, formatting, simple CRUD, boilerplate | 1–3 | `haiku` | `gpt-5.6-luna` | `low` |
| Standard implementation: one clear domain, straightforward logic, most tests | 4–6 | `sonnet` | `gpt-5.6-terra` | `medium` |
| Complex: multi-component, non-trivial logic, integration points, tricky edge cases | 7–8 | `sonnet` | `gpt-5.6-sol` | `high` |
| Very hard / high-risk: novel algorithms, architecture, concurrency, security- or data-migration-sensitive | 9–10 | `opus` | `gpt-6-astra` | `xhigh` |

Tasks often carry no `complexity_score` (it is only required when > 4). When it
is absent, judge the profile from the task's Objective, `skills`, and Technical
Requirements and pick the matching row.

## Guardrails (override the table)

1. **Default to the middle.** When a task is ambiguous or you cannot confidently
   place it, use `sonnet` / `gpt-5.6-terra` + `medium`. Never leave either
   provider model or `effort` unset.
2. **Cheapest that can succeed.** Bias toward the lowest tier that will
   plausibly complete the task correctly. Cost is a first-class constraint.
3. **Escalate effort before model.** If a task needs more careful thinking but
   not more raw capability, raise `effort` first. Reach for the frontier models
   only when the task genuinely needs stronger reasoning or synthesis.
4. **Do not pair an economy model with `high`/`xhigh`.** High effort on a
   low-capability model has poor returns. If a task truly needs high effort, it
   needs at least the balanced provider model.
5. **Risk floor — never cut corners on these.** Regardless of size, tasks that
   are security-sensitive, perform data migrations, touch auth, handle money,
   involve concurrency, or serve as a verification/quality gate get **at least
   `sonnet` / `gpt-5.6-sol` + `high`**. A cheap model rubber-stamping a gate
   defeats the gate.
6. **Use only the current provider's model.** Never pass an Anthropic model ID
   to an OpenAI harness or an OpenAI model ID to Claude Code.
7. **Make both knobs real.** In Claude Code, match `effort` to the
   `st-worker-*` agent and override it with `models.anthropic`. In Codex, pass
   `models.openai` and `effort` as explicit model and reasoning-effort spawn
   overrides.

## Legacy task compatibility

Older tasks may contain a scalar `model: haiku|sonnet|opus` instead of the
`models` map. Do not rewrite an archived task merely to migrate metadata. When
executing one, treat the scalar as its Anthropic choice and derive the OpenAI
choice as follows: `haiku` → `gpt-5.6-luna`; `sonnet` → `gpt-5.6-terra` for
low/medium effort or `gpt-5.6-sol` for high/xhigh effort; `opus` →
`gpt-6-astra`. When refining an active task, migrate it to the `models` map.
