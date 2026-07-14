---
id: 4
group: "docs-content"
dependencies: [1]
status: "completed"
created: 2026-07-14
model: "sonnet"
effort: "medium"
skills:
  - technical-writing
  - go
---
# CLI reference page

## Objective

Write `docs/using-sand/cli-reference.md`: every `sand` command and every `sand create` flag, with the default value each one actually has according to `cmd/sand/create.go` and `internal/vm/vm.go` — not according to the existing docs, which document only 6 of the 15 flags and get some of them wrong.

## Skills Required

`technical-writing` for the reference tables; `go` to read the flag definitions and their defaults directly out of the source.

## Acceptance Criteria

- [ ] `docs/using-sand/cli-reference.md` documents all four entry points: `sand` (no args → TUI), `sand create`, `sand shell NAME`, and `sand version` / `sand --version`.
- [ ] Every flag registered in `cmd/sand/create.go` appears in the flag table with its type, its real default, and a one-line description. No flag is omitted.
- [ ] `--git-name` / `--git-email` are documented as **falling back to the host's `git config`** (they are not required — `README-sand.md` says they are, and it is wrong), including what happens when neither the flag nor host git config provides a value.
- [ ] Verified by diff: run `go build -o /tmp/sand ./cmd/sand && /tmp/sand create --help`, and confirm flag-by-flag that the page matches its output. Paste that `--help` output into your completion report alongside the page's flag table.
- [ ] `uvx --with-requirements docs/requirements.txt mkdocs build --strict` exits 0 with no `WARNING`.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Sources of truth: `cmd/sand/main.go` (command dispatch), `cmd/sand/create.go` (flag registration and defaults), `internal/vm/vm.go` (`DefaultCreateConfig`), `internal/provision/vars.go` (values `sand` forces regardless of flags).
- Where a flag's registered default and the value actually sent to the playbook differ, document what the user gets.

## Input Dependencies

Task 1: scaffold, nav, the `using-sand/cli-reference.md` stub.

## Output Artifacts

`docs/using-sand/cli-reference.md`.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Do not trust the current READMEs for any of this.** Read `cmd/sand/create.go` and transcribe. The audit found these flags registered — treat the list as a starting point to verify, not as copy:

| Flag | Default (verify!) |
|---|---|
| `--name` | `claude` |
| `--base-name` | `claude-base` |
| `--hostname` | derived |
| `--user` | the **host username** (not `claude`) |
| `--git-name` | host `git config user.name` |
| `--git-email` | host `git config user.email` |
| `--cpus` | `2` |
| `--memory` | `8GiB` |
| `--disk` | `100GiB` |
| `--locale` | `en_US.UTF-8` (not `en_CA.UTF-8`) |
| `--domain` | `lan` |
| `--docker-proxy-host` | — |
| `--clone-url` | — |
| `--clone-token` | — |
| `--recreate` | `false` |
| `--rebuild` | `false` |

Things worth calling out in prose rather than burying in the table:

- **There is deliberately no `--ref` flag.** `cmd/sand/create.go` says so in a comment. If a reader goes looking for one, the docs should tell them it does not exist and why, rather than leaving them to search.
- `--git-name` / `--git-email` fall back to the host's `git config`, and error out only if neither the flag nor host git config yields a value. State the error condition.
- `--clone-token` is a credential: note that it is streamed into guest tmpfs and never placed on a command line.
- `--rebuild` vs `--recreate` — these are easy to confuse. One rebuilds the shared base image; the other recreates this VM. Read the code and say precisely which is which and when you would want each.
- Disk sizing: the base image is built at a 20 GiB floor and clones are grown to `--disk`. A `--disk` smaller than the floor is not a thing you can ask for. Confirm the actual behaviour in the code and document it.
- `samba_enabled` is forced **off** for every `sand` run (`internal/provision/vars.go`), regardless of what the Ansible role's own default says. If any flag or variable appears to offer samba, say plainly that it does not apply to the `sand` path.

**Format.** One `##` section per command. Under `sand create`, a single markdown table of all flags (`Flag | Default | Description`), then prose for the subtleties above. Add a short "Examples" section with two or three real invocations — a minimal one, one with a repo clone, one with non-default resources.

**Verification is the whole point of this task.** The `--help` diff in the acceptance criteria is not a formality: the existing docs are wrong precisely because nobody re-checked them against the binary. Build the binary, run `--help`, compare line by line.
</details>
