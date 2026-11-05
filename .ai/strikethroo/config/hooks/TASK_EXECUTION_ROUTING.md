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

Profiles live in each developer's local `config/config.yaml`, so match on
their descriptions rather than assuming particular names. For this Go and
Ansible repository:

- Route to the most capable profile any task touching secrets or tokens,
  the reset/preserve path (data that exists only in staged archives),
  registry or state-lock read-modify-write, base-image locking and other
  concurrency, the guest isolation boundary (writable mounts, clipboard,
  Landing, drupal.org publishing), or a verification gate. A wrong answer
  there loses data or leaks a credential silently.
- Treat MkDocs pages, `AGENTS.md`, and other prose-only edits as routine
  work.
- Route ordinary Go changes in `internal/` and Ansible role changes to the
  default implementation profile, including TUI work that only needs a
  golden-file update.
- When unsure between two profiles, choose the more capable one.
