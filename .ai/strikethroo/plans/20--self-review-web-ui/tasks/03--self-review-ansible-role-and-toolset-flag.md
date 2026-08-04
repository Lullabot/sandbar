---
id: 3
group: "provisioning"
dependencies: [2]
status: "pending"
created: 2026-08-04
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Spans Ansible and Go, and must hook the tool-set stamp correctly or a base image silently clones with the wrong contents."
skills:
  - ansible
  - go
---
# self-review Ansible role and --with-review tool-set flag

## Objective

Install the review web app into the base image from a new `roles/self-review`,
gated by a `toolset_review` selection that `sand create --with-review` sets and
that participates in the existing base-image version stamp.

## Skills Required

- **ansible** — a new role plus its `site.yml` gate, following the codex role's
  opt-in pattern.
- **go** — the `--with-review` flag and its four-line plumbing through
  `CreateConfig`.

## Acceptance Criteria

- [ ] `sand create --help` lists `--with-review` and describes it as opt-in.
- [ ] `go test ./internal/vm/... -run Toolset -v` passes, and a new test asserts
      that enabling review changes `CreateConfig.ToolsetKey()` (e.g. `"claude+ddev+go+java"`
      → `"claude+ddev+go+java+review"`), so the stamp invalidates.
- [ ] `go test ./internal/provision/... -race` passes, including a test that
      `toolset_review` is emitted as an extra-var carrying the config's value.
- [ ] `ansible-lint roles/self-review` reports no errors.
- [ ] `go build ./...` succeeds and `go test ./... -race` passes.
- [ ] With `toolset_review: false`, a provisioning run does not execute the role
      — verified by running the playbook in check mode (or the molecule/base
      scenario) and confirming zero `self-review` tasks in the recap.
- [ ] With `toolset_review: true`, the role installs the webapp to the guest and
      `test -f <install_dir>/dist/index.html && test -f <install_dir>/server/index.mjs`
      both succeed in the guest.
- [ ] Re-running the role is idempotent: a second run reports `changed=0` for
      the role's tasks.
- [ ] `TestGuestSyncCopiesOnlyThePlaybook` still passes, and a locally present
      `roles/self-review/files/webapp/node_modules` or `dist` does not end up in
      the binary's embedded playbook.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- The tool name is `review`; it is **opt-in** (defaults to false), exactly like
  `codex`.
- Node 24 is already present from `roles/base` (`base_nodejs_major_version`) —
  do not install a second Node.
- The role runs in the `base` phase only (it is heavy and identity-free), which
  means gating on `provision_phase | default('full') != 'finalize'` like the
  codex and dev-tools roles.
- The install location must be a fixed, predictable guest path that task 5 can
  hard-code.

## Input Dependencies

From tasks 1 and 2: a `roles/self-review/files/webapp/` tree whose `npm ci` and
`npm run build` both succeed.

## Output Artifacts

- `roles/self-review/{defaults,tasks}/main.yml`
- The `self-review` role entry in `site.yml`
- `WithReview` on `vm.CreateConfig`, its `ToolPtrs()` entry, the
  `--with-review` flag, and the `toolset_review` extra-var
- The guest install path, documented for task 5 to consume

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Read the codex role first.** `roles/codex/` plus its `site.yml` block is the
exact template for an opt-in tool-set role. Copy its shape, including the
comment style explaining that this one is opt-in unlike its opt-out siblings.

**Go plumbing — four edits, no more.** The tool-set machinery is deliberately
centralized, so adding a tool is mechanical:

1. `internal/vm/vm.go` — add `WithReview bool` next to `WithCodex`, and add
   `"review": &c.WithReview` to `ToolPtrs()`. Do **not** set it in
   `DefaultCreateConfig()`; its zero value (false) *is* the default, and the
   existing comment about `WithCodex` explains why. `ToolsetKey()` and
   `ApplyToolset()` pick it up automatically from `ToolPtrs()`.
2. `cmd/sand/create.go` — add
   `fs.BoolVar(&cfg.WithReview, "with-review", cfg.WithReview, "Install the self-review web UI in the base image")`
   beside `--with-codex`.
3. `internal/provision/vars.go` — add `varItem{"toolset_review", cfg.WithReview}`
   beside the `toolset_codex` entry.
4. `site.yml` — add the role with the opt-in gate:

```yaml
    # The self-review web UI is part of the configurable base tool-set (sand
    # create --with-review). Like codex it is opt-IN: toolset_review defaults
    # to false, so an unconfigured run does not build it.
    - role: self-review
      when:
        - toolset_review | default(false) | bool
        - provision_phase | default('full') != 'finalize'
```

Also check whether the TUI's create form enumerates tools (see
`internal/ui/form.go` and the `--with-*` handling around it) — if it renders a
toggle per tool from `ToolPtrs()`, review appears for free; if the list is
hand-maintained, add it there too so the form and the CLI agree.

**Role tasks.** In `roles/self-review/tasks/main.yml`:

1. Create the install directory (default it in `defaults/main.yml`, e.g.
   `selfreview_install_dir: /opt/sandbar/self-review`), owned by the login user.
2. Synchronize/copy `files/webapp/` into it — **excluding** `node_modules` and
   `dist`, so a developer's local build never ships.
3. Run `npm ci` in the install directory.
4. Run `npm run build`.
5. Prune build-only dependencies afterwards (`npm prune --omit=dev`) to keep the
   image smaller — but verify the server still starts, since it needs its
   runtime dependencies (`@self-review/core` and its transitive deps) to remain.

Make steps 3 and 4 idempotent: guard them with a `creates:`-style check or
compare a checksum of `package-lock.json` against a stamp file in the install
directory, so a re-run does not reinstall. The acceptance criterion requires
`changed=0` on a second pass.

**The embed hazard.** `playbook_embed.go` embeds `all:roles`, and
`internal/provision`'s in-guest rsync mirrors that list — `TestGuestSyncCopiesOnlyThePlaybook`
fails if they drift. Adding a role with a `files/` subtree is fine, but a
`node_modules` or `dist` directory left in the working tree by a local build
would be embedded into a locally built binary and rsynced to the guest. Confirm
both are git-ignored (task 1 added the `node_modules` entry; add `dist` too) and
that the role's copy step excludes them regardless.

**Verification.** For the "role does not run when disabled" criterion, prefer
the cheapest honest signal: run the playbook with `--check` and
`-e toolset_review=false` against the molecule base scenario or a real guest and
show the recap. Do not claim it from reading the `when:` clause alone.

</details>
