---
id: 9
group: "documentation"
dependencies: [5, 7, 8]
status: "pending"
created: 2026-08-28
model: "sonnet"
effort: "medium"
skills:
  - technical-writing
  - markdown
complexity_score: 5
complexity_notes: "Mechanical in form, but it carries the security reasoning the design depends on, and one section is deliberately withheld pending an unrun validation step."
---
# Documentation: security model, publishing guide, and the agent-facing record

## Objective

Document publication for humans and for future agents: the split of authority
and its honest limits, how a change set gets from a guest checkout to a merge
request on both surfaces, where the host PAT lives, why drupal.org work needs
no guest secret, and — in `AGENTS.md` — why no drupal.org credential belongs in
a VM, so later work does not reintroduce one.

## Skills Required

`technical-writing` for the security reasoning and the contributor-facing
guide; `markdown` for the docs site's conventions.

## Acceptance Criteria

- [ ] `docs/reference/security-model.md` documents the
      agent-decides-content / host-decides-destination split, why no drupal.org
      credential enters a VM (a contributor may hold push access to modules on
      tens of thousands of sites, so the only safe credential in an
      agent-controlled VM is none), **and the limits stated honestly**: the
      host still holds a powerful credential and publishes agent-authored
      content with it, and drupal.org's edge allowlist is **not** a containment
      boundary.
- [ ] A new user-facing guide under `docs/using-sand/` covers publishing from
      both surfaces, what the confirmation shows, and the three things a
      contributor would otherwise learn the hard way: that a publish **replays
      each local commit** rather than squashing, so local history is what
      appears on the fork; that a replay failing partway leaves the earlier
      commits public and is recovered by **re-running the publish**, not by
      repairing the fork; and that commits published this way are **not
      GPG-signed**. Written for someone who knows Drupal and not sandbar.
- [ ] That guide records that publication is a **human-initiated action** under
      drupal.org's PAT policy, and notes that the Drupal Association has not
      been formally consulted.
- [ ] `docs/using-sand/secrets.md` states plainly that drupal.org work needs no
      guest secret at all, and why — a notable exception to that page's whole
      premise.
- [ ] `docs/reference/files-and-state.md` documents the host-side PAT file at
      `${XDG_CONFIG_HOME:-~/.config}/sandbar/drupalorg.token`, its required
      mode, that its path is a **convention rather than a configured value**
      and why, and that it is deliberately not part of the guest secrets store.
- [ ] `AGENTS.md` describes the publication mechanism and the reason no
      drupal.org credential belongs in a guest.
- [ ] `docs/using-sand/cli-reference.md` covers `sand publish` and its flags;
      `docs/using-sand/tui.md` covers the Landing pane's publish row.
- [ ] The "why can't I just push?" escape-hatch write-up is **deliberately not
      written**, and its absence is recorded — with the reason — in the
      follow-up note below rather than left as a silent omission.
- [ ] A follow-up record captures the pre-existing behaviour that GitHub
      worktrees inherit the main clone's token (git resolves `includeIf
      "gitdir:…"` against `$GIT_DIR`, which for a linked worktree is
      `<main-clone>/.git/worktrees/<name>`), deliberately left unchanged here.
- [ ] `mkdocs` navigation includes the new page, and any docs build/lint the
      repository runs in CI passes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Match the existing docs' voice and structure; read
  `docs/reference/security-model.md` and `docs/using-sand/secrets.md` before
  writing so the new material reads as part of them rather than bolted on.
- Do not restate the plan. These pages are for a Drupal contributor and a
  future agent, not for a reader of the blueprint.

## Input Dependencies

- Tasks 5, 7, and 8: the behaviour being documented must exist and be settled,
  including flag names and the pane's row label.

## Output Artifacts

- Edits to `docs/reference/security-model.md`,
  `docs/reference/files-and-state.md`, `docs/using-sand/secrets.md`,
  `docs/using-sand/cli-reference.md`, `docs/using-sand/tui.md`, and
  `AGENTS.md`.
- A new publishing guide under `docs/using-sand/`.
- A follow-up record covering the withheld escape-hatch section and the
  worktree token-inheritance behaviour.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

The plan's "Documentation" section is the contract and lists each page
explicitly. Two items need care.

**The escape hatch is deliberately withheld.** The plan requires an honest
answer to "why can't I just push?", including how to set up a fine-grained
token bounded to one issue fork — and then gates it:

> Before this is written up as safe, the negative control must pass: a token
> bounded to one fork must be shown to **fail** when pushing to a canonical
> `project/<module>`. That test has not been run, and the documentation must
> not ship until it does.

That control requires a real drupal.org account, a hand-made fine-grained
token, and a push attempt against a canonical project — none of which this
implementation can run. So **do not write the escape-hatch instructions.** What
the guide may and should say, because it is established fact rather than an
unverified safety claim, is *why* publication is host-side: a fine-grained
token bounded to one issue fork exists and works, but GitLab exposes no API to
create one, so it costs a manual web-UI trip per issue — the exact friction the
work order asked to remove. Stop there, and record the withheld section as a
follow-up naming the negative control as its precondition.

**The worktree follow-up** is a pre-existing sandbar behaviour uncovered during
this plan's investigation and deliberately left unchanged: every worktree of a
repo silently inherits the main clone's token today, and no per-worktree token
is expressible through `includeIf "gitdir:…"`. The plan says it "should be
filed rather than lost". File it where this repository files follow-ups — a
docs note or an issue, matching existing practice; do not invent a new
mechanism for it.

Both follow-ups belong somewhere a future contributor will actually meet them,
not only in the archived plan.

**Test philosophy.** No tests are warranted for documentation. State that
rather than inventing one — but do run whatever docs build or link check CI
runs, since a broken nav entry is the failure mode this task actually has.

</details>

### Per-task completion gate (required by the plan)

This task is not complete until:

1. `/code-review --fix` has run against this task's changes and every finding
   was applied or consciously rejected.
2. `/simplify` has then run, and its findings applied or consciously rejected.
3. The repository's tests were **re-run** afterwards and pass — both commands
   modify the working tree.
