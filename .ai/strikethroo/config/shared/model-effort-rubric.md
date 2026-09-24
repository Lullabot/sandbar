# Model & Effort Rubric — Right-Sizing Each Task

Strikethroo executes every task in its own subagent. Two independent knobs
control capability and reasoning depth:

- **`models`** — provider-specific model for the task. Store both
  `models.anthropic` and `models.openai`; dispatch uses the current harness.
- **`effort`** — reasoning-depth tier. Claude Code selects a matching
  `st-worker-*` agent, while Codex passes effort as a spawn override.

This policy is current as of **September 24, 2026**. Recheck the providers'
official model catalogs before changing it. Select only active models and
never dispatch a deprecated or retired model, even when old task metadata names
one. **Never use Claude Fable or GPT-6 Astra**, regardless of their catalog
status or a task's legacy metadata.

## Provider model tiers

This is a dispatch policy, not a claim that providers' models are equivalent.

| Tier | Anthropic / Claude Code | OpenAI / Codex | Intended use |
| --- | --- | --- | --- |
| Economy | `claude-haiku-4-5` | `gpt-6-luna` | Fast, focused, mechanical work |
| Balanced | `claude-sonnet-5` | `gpt-6-sol` | Straightforward implementation and ordinary integration |
| Capable | `claude-opus-5-5` | `gpt-6-sol` | Complex implementation and careful integration |
| Highest allowed | `claude-opus-5-5` | `gpt-6-sol` | Hardest reasoning and highest-risk work |

The Anthropic catalog identifies Haiku 4.5 as fastest, Sonnet 5 as the speed
and intelligence balance, and Opus 5.5 for long-running coding and knowledge
work. The OpenAI catalog positions GPT-6 Luna for efficiency and GPT-6 Sol for
complex coding and agentic workflows. GPT-6 Astra and Claude Fable are
prohibited by project policy. Never invent a replacement ID from memory: check
the official catalogs and use the current allowed model in the same tier.

## Assignment table

Pick the row that best matches the task. When the task's difficulty is high,
upgrade the model first; use greater effort only after selecting a model with
enough capability.

| Task profile | `complexity_score` | `models.anthropic` | `models.openai` | `effort` |
| --- | --- | --- | --- | --- |
| Trivial / mechanical: docs, comments, config, renames, formatting, simple CRUD, boilerplate | 1–3 | `claude-haiku-4-5` | `gpt-6-luna` | `low` |
| Standard implementation: one clear domain, straightforward logic, most tests | 4–6 | `claude-sonnet-5` | `gpt-6-sol` | `medium` |
| Complex: multiple components, non-trivial logic, integration points, tricky edge cases | 7–8 | `claude-opus-5-5` | `gpt-6-sol` | `medium` or `high` |
| Very hard / high-risk: novel algorithms, architecture, concurrency, security- or data-migration-sensitive | 9–10 | `claude-opus-5-5` | `gpt-6-sol` | `high` or `xhigh` |

Tasks often omit `complexity_score` (it is required only above 4). Infer the
profile from the Objective, `skills`, and Technical Requirements.

## Guardrails (override the table)

1. **Default to the middle.** For an ambiguous task, use
   `claude-sonnet-5` / `gpt-6-sol` + `medium`. Never leave either provider
   model or `effort` unset.
2. **Use the least costly model that can succeed.** Cost matters, but never
   choose an underpowered model to save cost on difficult or risky work.
3. **Upgrade model before effort.** If a task is too demanding for its assigned
   model, move to a stronger allowed model before raising effort. Do not try to
   compensate for a cheap model's capability limits with more reasoning.
4. **Never use high effort on an economy model.** `claude-haiku-4-5` and
   `gpt-6-luna` may only be assigned `low` or `medium`. For work that needs
   `high` or `xhigh`, use at least `claude-opus-5-5` or `gpt-6-sol`.
5. **Risk floor.** Security-sensitive work, migrations, auth, money,
   concurrency, and verification or quality gates require at least
   `claude-opus-5-5` / `gpt-6-sol` + `high`. Never use a cheap model to
   rubber-stamp a gate.
6. **No prohibited or deprecated models.** Never use Fable or Astra. Never use
   a deprecated/retired model (including GPT-5.6 family IDs) in new frontmatter
   or dispatch. If legacy metadata names one, replace it using this rubric.
7. **Use only the current provider's model.** Never pass an Anthropic model ID
   to an OpenAI harness or an OpenAI model ID to Claude Code.
8. **Make both knobs real.** In Claude Code, select the worker matching
   `effort` and override its model with `models.anthropic`. In Codex, pass
   `models.openai` and `effort` as explicit model and reasoning-effort overrides.

## Legacy task compatibility

Older tasks may have scalar `model: haiku|sonnet|opus` instead of the `models`
map. Do not rewrite archived task files solely to migrate metadata. When
executing one, map the scalar to an allowed current Anthropic model:
`haiku` → `claude-haiku-4-5`, `sonnet` → `claude-sonnet-5`, and `opus` →
`claude-opus-5-5`. Derive the Codex model from effort and risk using the
assignment table; never map legacy metadata to GPT-5.6 or GPT-6 Astra. When
refining an active task, migrate it to the `models` map and current IDs.
