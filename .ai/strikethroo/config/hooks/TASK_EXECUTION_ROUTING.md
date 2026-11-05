# TASK_EXECUTION_ROUTING Hook

Runs during task generation, after every task file for the plan exists and
before the execution blueprint is generated.

## Classification contract

- Execution profiles are defined in the `execution_routing` section of
  `.ai/strikethroo/config/config.yaml`. Their descriptions are the
  routing contract: match each newly generated task against them using the
  full task content already in your context — `skills`, `complexity_score`,
  objective, acceptance criteria, technical requirements, and integration
  surface. Do not reread task files to reconstruct information you already
  have.
- Classify every generated task exactly once, using only configured profile
  names. Never guess or invent a model, harness, or profile name.
- The task-to-profile mapping file is temporary. The selected profile name is
  durable task metadata, persisted by the routing helper as exactly one
  `execution_profile` field. Concrete execution targets are selected later at
  task dispatch and are never persisted during generation.
- Any routing failure aborts task generation before blueprint generation.
  Never continue with partially routed tasks.

## Project-specific guidance

This project does not use Strikethroo's native execution routing. Model and
effort are assigned per task by `POST_TASK_GENERATION_ALL.md` from
`config/shared/model-effort-rubric.md`, and `PRE_TASK_ASSIGNMENT.md` dispatches
on those fields through the `st-worker-*` agents. Running both would let a
routed profile's model override the rubric's choice at dispatch.

Keep routing off: leave `config/config.yaml` absent, or set
`execution_routing.enabled: false` in it (the file is local and gitignored, so
each developer sets this). If `route-task-execution.cjs profiles` reports
routing as enabled, stop and ask the user to disable it rather than
classifying tasks against the shipped example profiles.
