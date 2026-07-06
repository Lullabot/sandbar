# PRE_TASK_ASSIGNMENT Hook

## Agent Selection and Task Assignment

Every task runs in its own subagent. Selecting that subagent has two parts:
choosing the **reasoning-effort tier** (which worker agent) and the **model**
(a per-call override). Both are pre-computed and stored in the task's
frontmatter at generation time — your job here is to read them and dispatch
accordingly.

- For each task in the current phase:
    - Read the task frontmatter to extract `model`, `effort`, and `skills`.
    - Select the executing subagent and model per the sections below.
    - Consider task-specific requirements from the task document.

[IMPORTANT] Analyze the set of task skills in order to engage any relevant harness skills as necessary (either global or project skills).

## Model and Effort Selection (primary)

The Task tool exposes a **per-call `model` override but no per-call `effort`
override** — effort is fixed by the frontmatter of the worker subagent that
runs the task. Strikethroo therefore ships one worker per effort tier:

| Task `effort` | Worker subagent | Default model (overridden per task) |
| --- | --- | --- |
| `low` | `st-worker-low` | `haiku` |
| `medium` | `st-worker-medium` | `sonnet` |
| `high` | `st-worker-high` | `sonnet` |
| `xhigh` | `st-worker-xhigh` | `opus` |

To dispatch a task:

1. **Read `effort` and `model`** from the task frontmatter. If either is
   missing or unrecognized, derive them now from
   `.ai/strikethroo/config/shared/model-effort-rubric.md` (default to
   `sonnet` + `medium` when genuinely unsure). Never dispatch without both.
2. **Pick the worker** whose tier equals the task's `effort` (table above). If
   that exact worker is unavailable in this harness, round **up** to the next
   available tier — never down.
3. **Dispatch with the Task tool**, setting the subagent type to the chosen
   worker and passing the task's `model` as the per-call `model` override. This
   pairing is what makes both knobs real: the worker fixes effort, the override
   fixes the model.

The `st-worker-*` agents are general-purpose task executors; they are the
default path. Only prefer a different agent when the rule below clearly applies.

## Domain-specialized agents (optional override)

If this harness provides a sub-agent whose expertise is a strong match for the
task's `skills` (beyond what a general worker offers), you may dispatch to it
instead. When you do:

- Still pass the task's `model` as the per-call override.
- Be aware the specialized agent's effort is whatever its own frontmatter
  declares, so this trades precise effort control for domain expertise. Only
  make that trade when the skill match is clearly worth it; otherwise use the
  effort-tier worker.

## Matching Criteria

When choosing between candidate agents, weigh:

1. **Effort tier match**: the task's `effort` maps to the `st-worker-*` tier.
2. **Primary skill match**: task technical requirements from the `skills` array.
3. **Domain expertise**: specific frameworks or libraries named in the task.
4. **Resource efficiency**: do not over-provision; respect the rubric's
   cost guardrails and the "cheapest that can succeed" bias.

## Fallback

If no `st-worker-*` agents and no matching specialized sub-agents are available
in this harness, execute the task with a general-purpose agent, still passing
the task's `model` as the per-call override. In that case effort falls back to
the session default; note this in the execution record so the outcome is
interpreted with that context.
