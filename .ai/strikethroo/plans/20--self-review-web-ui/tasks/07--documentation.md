---
id: 7
group: "documentation"
dependencies: [5, 6]
status: "pending"
created: 2026-08-04
model: "sonnet"
effort: "medium"
skills:
  - technical-writing
  - mkdocs
---
# Document browser-based review

## Objective

Document the review feature for both audiences: the mkdocs site and `README.md`
for humans, and `AGENTS.md` for assistants working in this repository.

## Skills Required

- **technical-writing** — accurate, concise prose matching the existing voice.
- **mkdocs** — adding a page and wiring it into `mkdocs.yml` navigation.

## Acceptance Criteria

- [ ] The CLI reference documents `sand land NAME PATH --review` alongside
      `--pr` and `--web`, including that it requires no pushed branch.
- [ ] The tool-set documentation documents `sand create --with-review`, that it
      is opt-in, and that toggling it rebuilds the base image.
- [ ] A new "Reviewing changes in a browser" page covers the end-to-end workflow
      (open the review, comment, Finish Review, feed the written `review.xml`
      back to the agent), the TUI Landing-pane key, and the per-backend
      reachability model.
- [ ] The new page is reachable from `mkdocs.yml` navigation, and
      `mkdocs build --strict` exits 0 with no broken-link or nav warnings.
- [ ] `README.md`'s feature overview mentions browser-based review.
- [ ] `AGENTS.md` records: the `Provider.ForwardArgv` seam and its
      nil-means-already-reachable contract; where the web app source lives and
      that it is built in the guest by the `self-review` role; and the rule that
      the three `@self-review/*` packages must always be bumped together.
- [ ] Every command shown in the docs is one that actually exists — verified by
      running each `sand ... --help` and comparing.
- [ ] No documentation claims support for the v1 non-goals (expand-context,
      image previews, walkthrough guide, `--resume-from`).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Match the existing documentation voice and structure; read neighbouring pages
  under `docs/` before writing.
- The per-backend reachability explanation must be accurate: local Lima needs no
  forwarder because Lima auto-forwards guest localhost to host localhost on the
  same port; remote Lima and Proxmox use an `ssh -L` child.
- Note that the guest server binds loopback only, so reviewed code is not
  exposed on the VM's network.

## Input Dependencies

Tasks 5 and 6 — the CLI flag, its help text, and the TUI key must be final
before they are documented.

## Output Artifacts

- Updated CLI reference and tool-set pages under `docs/`
- A new "Reviewing changes in a browser" page plus its `mkdocs.yml` nav entry
- Updated `README.md` and `AGENTS.md`

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Find the right pages first.** `mkdocs.yml` lists the site structure; the CLI
reference and the connection-profile pages under `docs/using-sand/` are the
models for tone and depth. Read at least two neighbouring pages before writing
so the new page does not read as a bolt-on.

**Do not document from the plan — document from the code.** Run the real
commands and quote their actual output:

```sh
go run ./cmd/sand land --help
go run ./cmd/sand create --help | grep -A1 with-review
```

If help text and prose disagree, the help text is the truth; fix the prose (or
raise the discrepancy rather than papering over it).

**The workflow page should answer, in order:** what problem this solves (review
agent-written code without pushing it anywhere), how to get a VM that has it
(`sand create --with-review`), how to open a review (`sand land NAME PATH
--review`, or the Landing-pane key), what happens when you finish (the server
writes `review.xml` into that checkout in the guest and exits, and the command
prints the path), and how to feed it back to the agent. Mention that multiple
checkouts can be reviewed at once, each on its own port — the work order called
this out explicitly.

**Reachability section.** Keep it short and correct: the server binds
`127.0.0.1` inside the guest; on local Lima it is already on your machine's
loopback because Lima forwards guest localhost ports to host localhost on the
same port; on remote Lima and Proxmox `sand` starts an `ssh -L` child for the
duration of the review. The reviewed code never leaves your workstation's
loopback.

**AGENTS.md.** That file is dense, structured reference for assistants. Add the
three facts listed in the acceptance criteria near the material they belong with
(the provider seam next to the other `Provider` notes; the role and the npm
lockstep rule near the provisioning/tool-set material). Keep entries terse and
factual — match the surrounding density rather than writing prose.

**Verification.** Run `mkdocs build --strict` and paste the result. If mkdocs is
not installed locally, say so and verify the nav entry and links by inspection,
reporting exactly what was and was not checked.

</details>
