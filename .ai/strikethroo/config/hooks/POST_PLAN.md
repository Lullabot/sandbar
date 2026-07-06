# POST_PLAN Hook

Ensure the plan includes a _Self Validation_ section describing the steps the LLM will take to validate that the plan was completed successfully.

Also, answer the question _Does this plan need to update the documentation, or the AGENTS.md_.

## Re-validate task model/effort (refinement only)

If tasks already exist for this plan (this is a refinement, not a fresh plan),
re-check each task's `model` and `effort` frontmatter against
`.ai/strikethroo/config/shared/model-effort-rubric.md`. When the refinement has
changed a task's scope, risk, or complexity, update its tier to match — raising
it where the work grew harder or higher-risk, lowering it where scope was
trimmed. Leave unchanged tasks alone. When no tasks exist yet, skip this step;
tiers are assigned at task generation by `POST_TASK_GENERATION_ALL.md`.
