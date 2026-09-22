---
id: [TASK-ID]
group: "user-authentication"
dependencies: []  # List of task IDs, e.g., [2, 3]
status: "[STATUS]"  # pending | in-progress | completed | needs-clarification
created: [YYYY-MM-DD]
models: # Provider-specific model IDs; dispatchers use only their current provider
  anthropic: "[ANTHROPIC-MODEL]" # haiku | sonnet | opus
  openai: "[OPENAI-MODEL]" # gpt-5.6-luna | gpt-5.6-terra | gpt-5.6-sol | gpt-6-astra
effort: "[EFFORT]"  # low | medium | high | xhigh — shared reasoning tier
skills: # Technical skills required for this task
  - [SKILL-1]
  - [SKILL-2]
---
# [TASK-TITLE]

## Objective
[Clear statement of what this task accomplishes]

## Skills Required
[Reference to the skills listed in frontmatter - these should align with the technical work needed]

## Acceptance Criteria
- [ ] Criterion 1
- [ ] Criterion 2
- [ ] Criterion 3

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
[Specific technical details, APIs, libraries, etc. - use this to infer appropriate skills]

## Input Dependencies
[What artifacts/code from other tasks are needed]

## Output Artifacts
[What this task produces for other tasks to consume]

## Implementation Notes
[Any helpful context or suggestions, including skill-specific guidance]
