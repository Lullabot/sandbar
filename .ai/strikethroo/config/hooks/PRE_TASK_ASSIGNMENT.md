# PRE_TASK_ASSIGNMENT Hook

## Agent Selection and Task Assignment

Every task runs in its own subagent. The task frontmatter pre-computes two
provider-specific model choices under `models` and one provider-neutral
`effort`. Read those fields plus `skills`, identify the current harness, and
apply only the matching provider configuration.

[IMPORTANT] Analyze the task's `skills` so the executing agent engages any
relevant global or project harness skills.

## Read and validate the tier

1. Read `models.anthropic`, `models.openai`, `effort`, and `skills` from the
   task frontmatter.
2. If a provider model or `effort` is missing or unrecognized, derive it from
   `.ai/strikethroo/config/shared/model-effort-rubric.md`. When genuinely
   unsure, use `sonnet` / `gpt-5.6-terra` + `medium`.
3. For a legacy task with scalar `model: haiku|sonnet|opus`, use that value in
   Claude Code. In Codex, map `haiku` to `gpt-5.6-luna`, `sonnet` to
   `gpt-5.6-terra` at low/medium effort or `gpt-5.6-sol` at high/xhigh effort,
   and `opus` to `gpt-6-astra`. Do not pass the scalar Anthropic ID to an
   OpenAI model override.

## Claude Code / Anthropic dispatch

Claude Code fixes effort in worker-agent frontmatter, so select the worker
whose tier equals the task's `effort`:

| Task `effort` | Worker subagent | Default model (overridden per task) |
| --- | --- | --- |
| `low` | `st-worker-low` | `haiku` |
| `medium` | `st-worker-medium` | `sonnet` |
| `high` | `st-worker-high` | `sonnet` |
| `xhigh` | `st-worker-xhigh` | `opus` |

Dispatch with the Task tool, selecting that worker and passing
`models.anthropic` as the per-call model override. If the exact effort worker
is unavailable, round up to the next available tier, never down.

## Codex / OpenAI dispatch

Codex accepts both model and reasoning-effort overrides when spawning a
subagent. Dispatch a general task-execution subagent with:

- model: `models.openai`
- reasoning effort: the task's `effort`

Do not look for or select the Claude-specific `st-worker-*` agents in Codex;
their frontmatter exists to work around Claude Code's lack of a per-call effort
override. If the requested OpenAI model is not available in the current
harness, choose the advertised model in the same capability/cost tier, preserve
the task's effort when supported, and record the fallback in the execution
record. Never substitute an Anthropic model ID.

## Domain-specialized agents (optional override)

If the harness provides a subagent whose expertise strongly matches the task's
`skills`, it may replace the general worker. Preserve the provider-specific
model and effort overrides whenever the harness permits them. If a specialized
agent fixes either value, use it only when the domain match is worth losing
precise tier control and record that tradeoff.

## Matching criteria

When choosing between candidate agents, weigh:

1. Correct provider model and effort tier.
2. Primary skill match from the task's `skills` array.
3. Domain expertise for named frameworks or libraries.
4. Resource efficiency; do not over-provision.

## Fallback

If the harness has no spawnable or matching subagent, execute with its
general-purpose agent. Apply the provider model and effort overrides if the
harness supports them; otherwise use the session defaults and note the fallback
in the execution record.
